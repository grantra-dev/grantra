package reports

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/notify"
	"github.com/grantra-dev/grantra/core/internal/settings"
)

// ScheduleActor is who makes scheduled packs, in the audit log.
const ScheduleActor = "system:reports"

// maxRecipients is how many people a schedule sends to at most.
const maxRecipients = 50

// Recipient is someone a schedule sends to.
type Recipient struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Allowed is false once they no longer hold the admin or auditor role:
	// they get nothing until they do again.
	Allowed bool `json:"allowed"`
}

// Schedule makes an evidence pack of one report or framework for each
// period of Months months as it ends, counted from the first month of the
// fiscal year, and tells its recipients.
type Schedule struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Report        string      `json:"report,omitempty"`
	Framework     string      `json:"framework,omitempty"`
	ApplicationID string      `json:"application_id,omitempty"`
	Application   string      `json:"application,omitempty"`
	Months        int         `json:"months"`
	Recipients    []Recipient `json:"recipients"`
	Enabled       bool        `json:"enabled"`
	UpdatedBy     string      `json:"updated_by"`
	UpdatedAt     time.Time   `json:"updated_at"`
	// Next is the period of the next pack, made once it ends.
	NextFrom string    `json:"next_from"`
	NextTo   string    `json:"next_to"`
	Last     *Delivery `json:"last,omitempty"`
}

// Delivery is one pack a schedule made, or tried to.
type Delivery struct {
	ID         string    `json:"id"`
	ScheduleID string    `json:"schedule_id,omitempty"`
	Title      string    `json:"title"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	FileName   string    `json:"file_name,omitempty"`
	SHA256     string    `json:"sha256,omitempty"`
	Bytes      int       `json:"bytes"`
	Recipients int       `json:"recipients"`
	CreatedAt  time.Time `json:"created_at"`
}

const deliveryColumns = `d.id, coalesce(d.schedule_id::text, ''), d.title, to_char(d.period_from, 'YYYY-MM-DD'),
	to_char(d.period_to, 'YYYY-MM-DD'), d.status, coalesce(d.error, ''), coalesce(d.file_name, ''), coalesce(d.sha256, ''),
	coalesce(length(d.data), 0), d.recipients, d.created_at`

func scanDelivery(row pgx.CollectableRow) (Delivery, error) {
	var d Delivery
	err := row.Scan(&d.ID, &d.ScheduleID, &d.Title, &d.From, &d.To, &d.Status, &d.Error, &d.FileName, &d.SHA256,
		&d.Bytes, &d.Recipients, &d.CreatedAt)
	return d, err
}

// readers are the people who may get packs: who holds the admin or auditor
// role now.
func readers(ctx context.Context, tx pgx.Tx) ([]string, error) {
	return notify.Members(ctx, tx, authz.RoleAdmin, authz.RoleAuditor)
}

// nextPeriod is the period of months months that now falls in.
func nextPeriod(now time.Time, months, startMonth int) (from, to time.Time) {
	last, _ := LastPeriod(now, months, startMonth)
	from = last.AddDate(0, months, 0)
	return from, from.AddDate(0, months, -1)
}

// Schedules lists the schedules, each with its last pack and the period of
// its next one.
func Schedules(ctx context.Context, tx pgx.Tx, now time.Time) ([]Schedule, error) {
	return listSchedules(ctx, tx, now, "")
}

// GetSchedule returns the schedule id.
func GetSchedule(ctx context.Context, tx pgx.Tx, id string, now time.Time) (Schedule, error) {
	if !db.ValidUUID(id) {
		return Schedule{}, apperr.ErrNotFound
	}
	out, err := listSchedules(ctx, tx, now, id)
	if err == nil && len(out) == 0 {
		err = apperr.ErrNotFound
	}
	if err != nil {
		return Schedule{}, err
	}
	return out[0], nil
}

func listSchedules(ctx context.Context, tx pgx.Tx, now time.Time, id string) ([]Schedule, error) {
	start, err := settings.Get(ctx, tx, settings.FiscalYearStartMonth)
	if err != nil {
		return nil, err
	}
	allowed, err := readers(ctx, tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx,
		`SELECT s.id, s.name, coalesce(s.report, ''), coalesce(s.framework, ''), coalesce(s.application_id::text, ''),
		        coalesce(ap.name, ''), s.months, s.recipients::text[],
		        ARRAY(SELECT coalesce(i.display_name, '') FROM unnest(s.recipients) WITH ORDINALITY r (id, n)
		              LEFT JOIN identities i ON i.id = r.id ORDER BY r.n),
		        s.enabled, `+actor("s.updated_by")+`, s.updated_at
		 FROM report_schedules s LEFT JOIN applications ap ON ap.id = s.application_id
		 WHERE $1 = '' OR s.id = nullif($1, '')::uuid
		 ORDER BY lower(s.name), s.id`, id)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Schedule, error) {
		var (
			s          Schedule
			ids, names []string
		)
		err := row.Scan(&s.ID, &s.Name, &s.Report, &s.Framework, &s.ApplicationID, &s.Application, &s.Months, &ids, &names,
			&s.Enabled, &s.UpdatedBy, &s.UpdatedAt)
		s.Recipients = make([]Recipient, len(ids))
		for i := range ids {
			s.Recipients[i] = Recipient{ID: ids[i], Name: names[i], Allowed: slices.Contains(allowed, ids[i])}
		}
		from, to := nextPeriod(now, s.Months, start)
		s.NextFrom, s.NextTo = from.Format(time.DateOnly), to.Format(time.DateOnly)
		return s, err
	})
	if err != nil {
		return nil, err
	}
	// The last pack of each, in one query.
	rows, err = tx.Query(ctx, `SELECT DISTINCT ON (d.schedule_id) `+deliveryColumns+` FROM report_deliveries d
		 WHERE d.schedule_id IS NOT NULL AND ($1 = '' OR d.schedule_id = nullif($1, '')::uuid)
		 ORDER BY d.schedule_id, d.period_to DESC, d.created_at DESC`, id)
	if err != nil {
		return nil, err
	}
	last, err := pgx.CollectRows(rows, scanDelivery)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if j := slices.IndexFunc(last, func(d Delivery) bool { return d.ScheduleID == out[i].ID }); j >= 0 {
			out[i].Last = &last[j]
		}
	}
	if out == nil {
		out = []Schedule{}
	}
	return out, nil
}

// ScheduleInput is what an admin writes.
type ScheduleInput struct {
	Name          string   `json:"name"`
	Report        string   `json:"report"`
	Framework     string   `json:"framework"`
	ApplicationID string   `json:"application_id"`
	Months        int      `json:"months"`
	Recipients    []string `json:"recipients"`
	Enabled       bool     `json:"enabled"`
}

func (in *ScheduleInput) check(ctx context.Context, tx pgx.Tx) error {
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "" || len(in.Name) > 200:
		return apperr.Invalidf("name must be between 1 and 200 characters")
	case (in.Report == "") == (in.Framework == ""):
		return apperr.Invalidf("pick a report or a framework")
	case in.Months < 1 || in.Months > 12:
		return apperr.Invalidf("each pack covers between 1 and 12 months")
	}
	if in.Report != "" {
		d, ok := Lookup(in.Report)
		if !ok {
			return apperr.Invalidf("there is no report %q", in.Report)
		}
		if in.ApplicationID != "" && !d.Application {
			return apperr.Invalidf("%s cannot be narrowed to one application", d.Title)
		}
	} else {
		if in.ApplicationID != "" {
			return apperr.Invalidf("a framework's pack covers every application")
		}
		if _, err := GetFramework(ctx, tx, in.Framework); errors.Is(err, apperr.ErrNotFound) {
			return apperr.Invalidf("there is no framework %q", in.Framework)
		} else if err != nil {
			return err
		}
	}
	if in.ApplicationID != "" {
		var found bool
		if db.ValidUUID(in.ApplicationID) {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM applications WHERE id = $1)`, in.ApplicationID).Scan(&found); err != nil {
				return err
			}
		}
		if !found {
			return apperr.Invalidf("there is no such application")
		}
	}
	in.Recipients = slices.Compact(slices.Sorted(slices.Values(in.Recipients)))
	if len(in.Recipients) == 0 || len(in.Recipients) > maxRecipients {
		return apperr.Invalidf("send it to between 1 and %d people", maxRecipients)
	}
	allowed, err := readers(ctx, tx)
	if err != nil {
		return err
	}
	for _, r := range in.Recipients {
		if !slices.Contains(allowed, r) {
			return apperr.Invalidf("packs go only to admins and auditors: give them the auditor role first (an auditor from outside the company can be invited under External auditors)")
		}
	}
	return nil
}

// SaveSchedule writes the schedule id, or adds one when id is empty.
func SaveSchedule(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, in ScheduleInput, now time.Time) (Schedule, error) {
	if id != "" {
		if _, err := GetSchedule(ctx, tx, id, now); err != nil {
			return Schedule{}, err
		}
	}
	if err := in.check(ctx, tx); err != nil {
		return Schedule{}, err
	}
	err := tx.QueryRow(ctx,
		`INSERT INTO report_schedules (id, tenant_id, name, report, framework, application_id, months, recipients, enabled,
		                               created_by, updated_by)
		 VALUES (coalesce(nullif($1, '')::uuid, gen_random_uuid()), $2, $3, nullif($4, ''), nullif($5, ''), nullif($6, '')::uuid,
		         $7, $8::uuid[], $9, $10, $10)
		 ON CONFLICT (id) DO UPDATE
		 SET name = $3, report = nullif($4, ''), framework = nullif($5, ''), application_id = nullif($6, '')::uuid, months = $7,
		     recipients = $8::uuid[], enabled = $9, updated_by = $10, updated_at = now()
		 RETURNING id`, id, tenantID, in.Name, in.Report, in.Framework, in.ApplicationID, in.Months, in.Recipients,
		in.Enabled, actor).Scan(&id)
	if err != nil {
		return Schedule{}, err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "report_schedule.saved", "report_schedule", id, map[string]any{
		"name": in.Name, "report": in.Report, "framework": in.Framework, "application_id": in.ApplicationID,
		"months": in.Months, "recipients": in.Recipients, "enabled": in.Enabled,
	}); err != nil {
		return Schedule{}, err
	}
	return GetSchedule(ctx, tx, id, now)
}

// DeleteSchedule removes a schedule. The packs it made stay until they are
// too old.
func DeleteSchedule(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error {
	if !db.ValidUUID(id) {
		return apperr.ErrNotFound
	}
	name, err := db.QueryOne(ctx, tx, pgx.RowTo[string], `DELETE FROM report_schedules WHERE id = $1 RETURNING name`, id)
	if err != nil {
		return err
	}
	return db.Audit(ctx, tx, tenantID, actor, "report_schedule.deleted", "report_schedule", id, map[string]any{"name": name})
}

// Deliveries lists the packs schedules made, newest first: all of them, or
// those of one schedule.
func Deliveries(ctx context.Context, tx pgx.Tx, scheduleID string, limit int) ([]Delivery, error) {
	if scheduleID != "" && !db.ValidUUID(scheduleID) {
		return []Delivery{}, nil
	}
	rows, err := tx.Query(ctx, `SELECT `+deliveryColumns+` FROM report_deliveries d
		 WHERE $1 = '' OR d.schedule_id = nullif($1, '')::uuid
		 ORDER BY d.created_at DESC, d.id LIMIT $2`, scheduleID, limit)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, scanDelivery)
	if out == nil {
		out = []Delivery{}
	}
	return out, err
}

// DeliveryFile returns a pack a schedule made.
func DeliveryFile(ctx context.Context, tx pgx.Tx, id string) (d Delivery, data []byte, err error) {
	if !db.ValidUUID(id) {
		return d, nil, apperr.ErrNotFound
	}
	err = tx.QueryRow(ctx, `SELECT `+deliveryColumns+`, d.data FROM report_deliveries d WHERE d.id = $1 AND d.status = 'ready'`, id).
		Scan(&d.ID, &d.ScheduleID, &d.Title, &d.From, &d.To, &d.Status, &d.Error, &d.FileName, &d.SHA256, &d.Bytes,
			&d.Recipients, &d.CreatedAt, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, nil, apperr.ErrNotFound
	}
	return d, data, err
}

// Sent counts what one pass of SendScheduled did.
type Sent struct {
	Ready  int `json:"ready"`
	Failed int `json:"failed"`
	Pruned int `json:"pruned"`
}

// SendScheduled makes the packs whose period ended: for each enabled
// schedule, the last full period, once. A pack that cannot be made (a
// framework with no controls left, too many rows) is recorded as failed
// with why and not tried again; anything else is tried on the next pass.
// Packs older than the tenant keeps them are deleted.
func SendScheduled(ctx context.Context, pool *pgxpool.Pool, tenantID string, now time.Time) (Sent, error) {
	var sent Sent
	_, err := db.TryLocked(ctx, pool, "scheduled reports: "+tenantID, func(ctx context.Context) error {
		var due []Schedule
		err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `DELETE FROM report_deliveries
				WHERE created_at < now() - make_interval(days => `+settings.SQL(settings.DeliveryKeepDays)+`)`)
			if err != nil {
				return err
			}
			sent.Pruned = int(tag.RowsAffected())
			all, err := Schedules(ctx, tx, now)
			due = slices.DeleteFunc(all, func(s Schedule) bool { return !s.Enabled })
			return err
		})
		if err != nil {
			return err
		}
		var errs []error
		for _, s := range due {
			err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
				status, err := deliver(ctx, tx, tenantID, s, now)
				switch status {
				case "ready":
					sent.Ready++
				case "failed":
					sent.Failed++
				}
				return err
			})
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	})
	return sent, err
}

// deliver makes the pack of s for its last full period, unless it exists,
// and tells the recipients. It returns "ready", "failed" or "" when there
// was nothing to do.
func deliver(ctx context.Context, tx pgx.Tx, tenantID string, s Schedule, now time.Time) (string, error) {
	start, err := settings.Get(ctx, tx, settings.FiscalYearStartMonth)
	if err != nil {
		return "", err
	}
	from, to := LastPeriod(now, s.Months, start)
	var done bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM report_deliveries WHERE schedule_id = $1 AND period_from = $2 AND period_to = $3)`,
		s.ID, from, to).Scan(&done); err != nil || done {
		return "", err
	}
	p := Params{From: from, To: to, ApplicationID: s.ApplicationID}
	who := Who{Actor: ScheduleActor, Name: "Schedule: " + s.Name}
	kind, id := "report", s.Report
	var sum Summary
	if s.Framework != "" {
		kind, id = "report_framework", s.Framework
		var fw Framework
		if fw, err = GetFramework(ctx, tx, s.Framework); err == nil {
			sum, err = ForFramework(ctx, tx, fw, p, now, who)
		}
	} else {
		var res Result
		if res, err = Run(ctx, tx, s.Report, p, now); err == nil {
			sum = ForReport(res, who)
		}
	}
	title := s.Name
	if sum.Title != "" {
		title = sum.Title
	}
	var (
		data         []byte
		sha, problem string
	)
	if err == nil {
		data, sha, err = Pack(sum, tenantID)
	}
	var invalid *apperr.Invalid
	switch {
	case errors.As(err, &invalid):
		problem = invalid.Msg
	case errors.Is(err, apperr.ErrNotFound):
		problem = "the report, framework or application it names no longer exists"
	case err != nil:
		return "", err
	}
	var recipients []string
	for _, r := range s.Recipients {
		if r.Allowed {
			recipients = append(recipients, r.ID)
		}
	}
	status, fileName := "ready", FileName(id, p)+".zip"
	if problem != "" {
		status, fileName = "failed", ""
	}
	var deliveryID string
	err = tx.QueryRow(ctx,
		`INSERT INTO report_deliveries (tenant_id, schedule_id, title, period_from, period_to, status, error, file_name, data, sha256, recipients)
		 VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''), nullif($8, ''), $9, nullif($10, ''), $11) RETURNING id`,
		tenantID, s.ID, title, from, to, status, problem, fileName, data, sha, len(recipients)).Scan(&deliveryID)
	if err != nil {
		return "", err
	}
	if status == "ready" {
		err = Record(ctx, tx, tenantID, ScheduleActor,
			Download{Kind: kind, ID: id, Format: "zip", Params: p, SHA256: sha, Files: sum.Files(), Schedule: s.ID})
	} else {
		err = db.Audit(ctx, tx, tenantID, ScheduleActor, "report.failed", "report_schedule", s.ID,
			map[string]any{"error": problem, "from": from.Format(time.DateOnly), "to": to.Format(time.DateOnly)})
	}
	if err != nil {
		return "", err
	}
	msg := map[string]any{"title": title, "from": from.Format(time.DateOnly), "to": to.Format(time.DateOnly), "delivery_id": deliveryID}
	if problem != "" {
		msg["error"] = problem
	}
	return status, notify.Enqueue(ctx, tx, tenantID, notify.ReportReady, recipients, msg)
}
