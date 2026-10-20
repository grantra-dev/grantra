// Package settings holds how a tenant has Grantra set up: the time limits,
// reminders and thresholds that admins change in the console under
// Settings → General.
//
// Every setting is defined once, in this package, with its default and its
// limits; the tenant_settings table keeps only the values an admin changed.
// Code reads a value with Get (or SQL, inside a query) and never keeps its
// own constant for it.
//
// A change that makes Grantra less strict, such as a longer break-glass
// check-out or a longer console session, waits for a second admin when the
// tenant has one: the principle of four eyes. Making Grantra stricter takes
// effect at once. Every change is in the audit log.
package settings

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/notify"
	"github.com/grantra-dev/grantra/core/internal/textutil"
)

// Kinds of setting.
const (
	// Number is a whole number between Min and Max, in Unit.
	Number = "number"
	// Switch is on (1) or off (0).
	Switch = "switch"
)

// Which way a setting makes Grantra less strict.
const (
	Higher = "higher"
	Lower  = "lower"
)

// Definition is one setting.
type Definition struct {
	Key   string `json:"key"`
	Group string `json:"group"`
	// Module is the console module the setting belongs to, when it only
	// matters with that module on.
	Module  string `json:"module,omitempty"`
	Title   string `json:"title"`
	Help    string `json:"help"`
	Kind    string `json:"kind"`
	Unit    string `json:"unit,omitempty"`
	Default int    `json:"default"`
	Min     int    `json:"min"`
	Max     int    `json:"max"`
	// Looser says which way the setting makes Grantra less strict ("" when
	// neither does): such changes wait for a second admin.
	Looser string `json:"looser,omitempty"`
	// Public settings are shown to everyone, for the console to explain
	// what it shows (such as how long access goes unused).
	Public bool `json:"-"`
}

// loosens reports whether going from old to value makes Grantra less strict.
func (d Definition) loosens(old, value int) bool {
	return d.Looser == Higher && value > old || d.Looser == Lower && value < old
}

// clamp keeps v within the setting's limits, which may have moved since it
// was saved (an install may lower a maximum).
func (d Definition) clamp(v int) int { return min(max(v, d.Min), d.Max) }

// List returns every setting definition, in the order the console shows
// them.
func List() []Definition { return slices.Clone(definitions) }

// Lookup returns the setting with key.
func Lookup(key string) (Definition, bool) {
	i := slices.IndexFunc(definitions, func(d Definition) bool { return d.Key == key })
	if i < 0 {
		return Definition{}, false
	}
	return definitions[i], true
}

func mustLookup(key string) Definition {
	d, ok := Lookup(key)
	if !ok {
		panic("settings: unknown setting " + key)
	}
	return d
}

// Cap lowers the most a tenant may set key to, and its default with it.
// The install sets caps at start, before serving anything, from its own
// configuration (such as GRANTRA_SESSION_MAX), so no tenant goes past what
// the operator allows.
func Cap(key string, most int) {
	i := slices.IndexFunc(definitions, func(d Definition) bool { return d.Key == key })
	if i < 0 {
		panic("settings: unknown setting " + key)
	}
	d := &definitions[i]
	d.Max = max(min(d.Max, most), 1)
	d.Min = min(d.Min, d.Max)
	d.Default = min(d.Default, d.Max)
}

// Get returns the tenant's value of key.
func Get(ctx context.Context, tx pgx.Tx, key string) (int, error) {
	d := mustLookup(key)
	var v int
	err := tx.QueryRow(ctx, `SELECT value FROM tenant_settings WHERE key = $1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return d.Default, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read setting %s: %w", key, err)
	}
	return d.clamp(v), nil
}

// On returns whether the switch key is on for the tenant.
func On(ctx context.Context, tx pgx.Tx, key string) (bool, error) {
	v, err := Get(ctx, tx, key)
	return v == 1, err
}

// Values returns the tenant's value of each of keys, in one query.
func Values(ctx context.Context, tx pgx.Tx, keys ...string) (map[string]int, error) {
	out := make(map[string]int, len(keys))
	for _, k := range keys {
		out[k] = mustLookup(k).Default
	}
	rows, err := tx.Query(ctx, `SELECT key, value FROM tenant_settings WHERE key = ANY($1)`, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var v int
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = mustLookup(k).clamp(v)
	}
	return out, rows.Err()
}

// SQL is an expression for the tenant's value of key, to use inside a query
// that runs in the tenant's transaction: it saves a round trip where a value
// is read on every request or for many rows.
func SQL(key string) string {
	d := mustLookup(key)
	// Keys are constants of this package, never input, so quoting them in
	// the query is safe.
	return fmt.Sprintf(`coalesce((SELECT least(greatest(value, %d), %d) FROM tenant_settings WHERE key = '%s'), %d)`,
		d.Min, d.Max, d.Key, d.Default)
}

// Setting is one setting as the tenant has it.
type Setting struct {
	Definition
	Value     int        `json:"value"`
	UpdatedBy string     `json:"updated_by,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	// Pending is a change that waits for a second admin.
	Pending *Change `json:"pending,omitempty"`
}

// Change is a change that makes Grantra less strict, which a second admin
// approves or rejects. Whoever proposed it may withdraw it.
type Change struct {
	ID             string     `json:"id"`
	Key            string     `json:"key"`
	Title          string     `json:"title"`
	Unit           string     `json:"unit,omitempty"`
	Kind           string     `json:"kind"`
	OldValue       int        `json:"old_value"`
	Value          int        `json:"value"`
	Reason         string     `json:"reason,omitempty"`
	ProposedBy     string     `json:"proposed_by"`
	ProposedByName string     `json:"proposed_by_name,omitempty"`
	ProposedAt     time.Time  `json:"proposed_at"`
	Status         string     `json:"status"`
	DecidedBy      string     `json:"decided_by,omitempty"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
	Comment        string     `json:"comment,omitempty"`
}

// Statuses of a change.
const (
	Pending   = "pending"
	Approved  = "approved"
	Rejected  = "rejected"
	Withdrawn = "withdrawn"
)

const changeColumns = `c.id, c.key, c.old_value, c.value, coalesce(c.reason, ''), c.proposed_by, coalesce(i.display_name, ''),
	c.proposed_at, c.status, coalesce(c.decided_by, ''), c.decided_at, coalesce(c.comment, '')
	FROM tenant_setting_changes c
	LEFT JOIN identities i ON i.id = CASE WHEN c.proposed_by ~ '^identity:[0-9a-f-]{36}$' THEN substr(c.proposed_by, 10)::uuid END`

func scanChange(row pgx.CollectableRow) (Change, error) {
	var c Change
	err := row.Scan(&c.ID, &c.Key, &c.OldValue, &c.Value, &c.Reason, &c.ProposedBy, &c.ProposedByName,
		&c.ProposedAt, &c.Status, &c.DecidedBy, &c.DecidedAt, &c.Comment)
	if d, ok := Lookup(c.Key); ok {
		c.Title, c.Unit, c.Kind = d.Title, d.Unit, d.Kind
	}
	return c, err
}

// PendingChanges lists the changes waiting for a second admin, oldest first.
func PendingChanges(ctx context.Context, tx pgx.Tx) ([]Change, error) {
	rows, err := tx.Query(ctx, `SELECT `+changeColumns+` WHERE c.status = 'pending' ORDER BY c.proposed_at, c.id`)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, scanChange)
	// A setting removed in a newer version leaves nothing to decide.
	return slices.DeleteFunc(out, func(c Change) bool { return c.Title == "" }), err
}

// Load returns every setting as the tenant has it, with the changes that
// wait for a second admin.
func Load(ctx context.Context, tx pgx.Tx) ([]Setting, error) {
	rows, err := tx.Query(ctx, `SELECT key, value, updated_by, updated_at FROM tenant_settings`)
	if err != nil {
		return nil, err
	}
	type saved struct {
		value int
		by    string
		at    time.Time
	}
	stored := map[string]saved{}
	for rows.Next() {
		var k string
		var s saved
		if err := rows.Scan(&k, &s.value, &s.by, &s.at); err != nil {
			rows.Close()
			return nil, err
		}
		stored[k] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	pending, err := PendingChanges(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := make([]Setting, 0, len(definitions))
	for _, d := range definitions {
		s := Setting{Definition: d, Value: d.Default}
		if v, ok := stored[d.Key]; ok {
			s.Value, s.UpdatedBy, s.UpdatedAt = d.clamp(v.value), v.by, &v.at
		}
		if i := slices.IndexFunc(pending, func(c Change) bool { return c.Key == d.Key }); i >= 0 {
			s.Pending = &pending[i]
		}
		out = append(out, s)
	}
	return out, nil
}

// Public returns the values everyone may see, for the console to explain
// what it shows.
func Public(ctx context.Context, tx pgx.Tx) (map[string]int, error) {
	var keys []string
	for _, d := range definitions {
		if d.Public {
			keys = append(keys, d.Key)
		}
	}
	return Values(ctx, tx, keys...)
}

// find returns key as the tenant has it.
func find(ctx context.Context, tx pgx.Tx, key string) (Setting, error) {
	all, err := Load(ctx, tx)
	if err != nil {
		return Setting{}, err
	}
	i := slices.IndexFunc(all, func(s Setting) bool { return s.Key == key })
	if i < 0 {
		return Setting{}, apperr.ErrNotFound
	}
	return all[i], nil
}

// Save sets key to value as actor, an admin, with an optional reason. A
// change that makes Grantra less strict waits for a second admin when the
// tenant has another active one; then the setting comes back with Pending
// set and its value unchanged.
func Save(ctx context.Context, tx pgx.Tx, tenantID, actor, key string, value int, reason string) (Setting, error) {
	reason = strings.TrimSpace(reason)
	if len(reason) > 2000 {
		return Setting{}, apperr.Invalidf("reason must be at most 2000 characters")
	}
	// One change at a time per setting: another admin may be deciding the
	// pending one right now.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('tenant setting ' || $1))`, key); err != nil {
		return Setting{}, err
	}
	s, err := find(ctx, tx, key)
	if err != nil {
		return Setting{}, err
	}
	if s.Kind == Switch && value != 0 && value != 1 {
		return Setting{}, apperr.Invalidf("%s is a switch: its value is 1 (on) or 0 (off)", key)
	}
	if value < s.Min || value > s.Max {
		return Setting{}, apperr.Invalidf("%s must be between %d and %d", key, s.Min, s.Max)
	}
	if s.Pending != nil {
		return Setting{}, apperr.Conflictf("a change to this setting waits for a second admin: approve, reject or withdraw it first")
	}
	if value == s.Value {
		return s, nil
	}
	details := map[string]any{"reason": reason}
	if s.loosens(s.Value, value) {
		others, err := otherAdmins(ctx, tx, actor)
		if err != nil {
			return Setting{}, err
		}
		if len(others) > 0 {
			return propose(ctx, tx, tenantID, actor, s, value, reason, others)
		}
		// The audit log shows a less strict setting nobody else saw.
		details["no_second_admin"] = true
	}
	return s, apply(ctx, tx, tenantID, actor, &s, value, details)
}

// SecondAdmin reports whether a change by actor that makes Grantra less
// strict waits for another admin: whether the tenant has another active one.
func SecondAdmin(ctx context.Context, tx pgx.Tx, actor string) (bool, error) {
	others, err := otherAdmins(ctx, tx, actor)
	return len(others) > 0, err
}

// otherAdmins returns the active admins other than actor.
func otherAdmins(ctx context.Context, tx pgx.Tx, actor string) ([]string, error) {
	admins, err := notify.Admins(ctx, tx)
	return slices.DeleteFunc(admins, func(id string) bool { return db.IdentityActor(id) == actor }), err
}

func propose(ctx context.Context, tx pgx.Tx, tenantID, actor string, s Setting, value int, reason string, admins []string) (Setting, error) {
	var id string
	if err := tx.QueryRow(ctx,
		`INSERT INTO tenant_setting_changes (tenant_id, key, old_value, value, reason, proposed_by)
		 VALUES ($1, $2, $3, $4, nullif($5, ''), $6) RETURNING id`,
		tenantID, s.Key, s.Value, value, reason, actor).Scan(&id); err != nil {
		return Setting{}, err
	}
	c, err := change(ctx, tx, id)
	if err != nil {
		return Setting{}, err
	}
	s.Pending = &c
	if err := db.Audit(ctx, tx, tenantID, actor, "setting.change_proposed", "setting", s.Key, map[string]any{
		"change_id": id, "title": s.Title, "old_value": s.Value, "value": value, "reason": reason}); err != nil {
		return Setting{}, err
	}
	return s, notify.Enqueue(ctx, tx, tenantID, notify.SettingChange, admins, map[string]any{
		"title": s.Title, "old_value": describe(s.Definition, s.Value), "value": describe(s.Definition, value),
		"proposed_by": c.ProposedByName, "reason": reason})
}

// describe writes a value the way people read it: "60 minutes", "on".
func describe(d Definition, v int) string {
	if d.Kind == Switch {
		if v == 1 {
			return "on"
		}
		return "off"
	}
	if d.Unit == "percent" {
		return strconv.Itoa(v) + "%"
	}
	return textutil.Plural(v, strings.TrimSuffix(d.Unit, "s"), d.Unit)
}

// apply stores value, and audits the change with details.
func apply(ctx context.Context, tx pgx.Tx, tenantID, actor string, s *Setting, value int, details map[string]any) error {
	var err error
	if value == s.Default {
		// The default needs no row, so a later version's default applies.
		_, err = tx.Exec(ctx, `DELETE FROM tenant_settings WHERE key = $1`, s.Key)
		s.UpdatedBy, s.UpdatedAt = "", nil
	} else {
		var at time.Time
		err = tx.QueryRow(ctx,
			`INSERT INTO tenant_settings (tenant_id, key, value, updated_by) VALUES ($1, $2, $3, $4)
			 ON CONFLICT (tenant_id, key) DO UPDATE SET value = $3, updated_by = $4, updated_at = now()
			 RETURNING updated_at`, tenantID, s.Key, value, actor).Scan(&at)
		s.UpdatedBy, s.UpdatedAt = actor, &at
	}
	if err != nil {
		return err
	}
	details["title"], details["old_value"], details["value"] = s.Title, s.Value, value
	s.Value = value
	return db.Audit(ctx, tx, tenantID, actor, "setting.changed", "setting", s.Key, details)
}

func change(ctx context.Context, tx pgx.Tx, id string) (Change, error) {
	if !db.ValidUUID(id) {
		return Change{}, apperr.ErrNotFound
	}
	return db.QueryOne(ctx, tx, scanChange, `SELECT `+changeColumns+` WHERE c.id = $1`, id)
}

// Decide approves or rejects a pending change as actor, an admin. Nobody
// approves their own change; rejecting it withdraws it.
func Decide(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, approve bool, comment string) (Change, error) {
	comment = strings.TrimSpace(comment)
	if len(comment) > 2000 {
		return Change{}, apperr.Invalidf("comment must be at most 2000 characters")
	}
	c, err := change(ctx, tx, id)
	if err != nil {
		return Change{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('tenant setting ' || $1))`, c.Key); err != nil {
		return Change{}, err
	}
	// Read it again under the lock: someone may have decided it meanwhile.
	if c, err = change(ctx, tx, id); err != nil {
		return Change{}, err
	}
	if c.Status != Pending {
		return Change{}, apperr.Conflictf("this change was already %s", c.Status)
	}
	own := c.ProposedBy == actor
	if approve && own {
		return Change{}, apperr.Conflictf("a second admin approves this change: nobody approves their own")
	}
	var s Setting
	if approve {
		if s, err = find(ctx, tx, c.Key); err != nil {
			return Change{}, err
		}
		if c.Value < s.Min || c.Value > s.Max {
			return Change{}, apperr.Conflictf("%s must now be between %d and %d: reject this change and propose another", c.Key, s.Min, s.Max)
		}
	}
	status, action := Rejected, "setting.change_rejected"
	switch {
	case approve:
		status, action = Approved, "setting.change_approved"
	case own:
		status, action = Withdrawn, "setting.change_withdrawn"
	}
	var at time.Time
	if err := tx.QueryRow(ctx,
		`UPDATE tenant_setting_changes SET status = $2, decided_by = $3, decided_at = now(), comment = nullif($4, '')
		 WHERE id = $1 RETURNING decided_at`, id, status, actor, comment).Scan(&at); err != nil {
		return Change{}, err
	}
	c.Status, c.DecidedBy, c.DecidedAt, c.Comment = status, actor, &at, comment
	if err := db.Audit(ctx, tx, tenantID, actor, action, "setting", c.Key, map[string]any{
		"change_id": id, "title": c.Title, "old_value": c.OldValue, "value": c.Value, "comment": comment}); err != nil {
		return Change{}, err
	}
	if !approve {
		return c, nil
	}
	return c, apply(ctx, tx, tenantID, actor, &s, c.Value, map[string]any{
		"reason": c.Reason, "change_id": id, "proposed_by": c.ProposedBy})
}
