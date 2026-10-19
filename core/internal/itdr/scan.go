package itdr

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/login"
	"github.com/grantra-dev/grantra/core/internal/textutil"
)

// batchSize is how many sign-ins one transaction of a scan looks at.
const batchSize = 2000

// Retention is how long sign-ins are kept: over a year, so an account
// dormant for up to a year is still recognised as such.
const Retention = 400 * 24 * time.Hour

// Cursors of the access detections.
const (
	privilegeCursor = "privileges"
	adminCursor     = "platform-admins"
)

// ScanResult is what one scan did.
type ScanResult struct {
	// Read is how many new sign-ins came from Grantra Login.
	Read      int `json:"read"`
	Evaluated int `json:"evaluated"`
	Raised    int `json:"raised"`
	Escalated int `json:"escalated"`
	// Responses is how many automatic responses ran; ResponseErrors how
	// many of them failed, in part or in full.
	Responses      int `json:"responses"`
	ResponseErrors int `json:"response_errors"`
	Deleted        int `json:"deleted"`
}

// Scanner runs the detections and their automatic responses.
type Scanner struct {
	Pool     *pgxpool.Pool
	TenantID string
	Secrets  connector.SecretResolver
	// Now is the time the scan believes it is; tests set it.
	Now func() time.Time
}

func (sc Scanner) now() time.Time {
	if sc.Now != nil {
		return sc.Now()
	}
	return time.Now()
}

// Scan reads Grantra Login's new sign-ins, looks at every sign-in not yet
// looked at and at access that changed, raises alerts, emails the admins
// about new serious ones and runs automatic responses. A failure to read
// Grantra Login is returned, after the rest has run. While the tenant has
// threat detection off, it does nothing.
func (sc Scanner) Scan(ctx context.Context) (ScanResult, error) {
	var res ScanResult
	var (
		on       bool
		settings map[string]Setting
	)
	if err := db.InTenant(ctx, sc.Pool, sc.TenantID, func(tx pgx.Tx) (err error) {
		if on, err = On(ctx, tx); err != nil || !on {
			return err
		}
		settings, err = settingsByID(ctx, tx)
		return err
	}); err != nil || !on {
		return res, err
	}
	n, readErr := ReadGrantraLogin(ctx, sc.Pool, sc.TenantID, sc.Secrets)
	if errors.Is(readErr, login.ErrNotConnected) {
		readErr = nil
	}
	if readErr != nil {
		readErr = fmt.Errorf("read Grantra Login sign-ins: %w", readErr)
	}
	res.Read = n

	var all []raised
	for {
		var (
			done  bool
			found []raised
		)
		err := db.InTenant(ctx, sc.Pool, sc.TenantID, func(tx pgx.Tx) (err error) {
			var evaluated int
			found, evaluated, err = sc.evaluate(ctx, tx, settings)
			res.Evaluated += evaluated
			done = evaluated < batchSize
			if err != nil {
				return err
			}
			return notifyAdmins(ctx, tx, sc.TenantID, found)
		})
		if err != nil {
			return res, errors.Join(readErr, err)
		}
		all = append(all, found...)
		if done {
			break
		}
	}
	err := db.InTenant(ctx, sc.Pool, sc.TenantID, func(tx pgx.Tx) error {
		found, err := sc.checkAccess(ctx, tx, settings)
		if err != nil {
			return err
		}
		all = append(all, found...)
		if err := notifyAdmins(ctx, tx, sc.TenantID, found); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM signin_events WHERE occurred_at < $1`, sc.now().Add(-Retention))
		res.Deleted = int(tag.RowsAffected())
		return err
	})
	if err != nil {
		return res, errors.Join(readErr, err)
	}
	for _, r := range all {
		if r.created {
			res.Raised++
		} else {
			res.Escalated++
		}
	}
	res.Responses, res.ResponseErrors = sc.respond(ctx, all, settings)
	return res, readErr
}

// respond runs the automatic response of each alert's detection, once
// per alert and per kind of response: an alert that grows more severe
// after it was responded to is not responded to again.
func (sc Scanner) respond(ctx context.Context, alerts []raised, settings map[string]Setting) (ran, failed int) {
	if len(alerts) == 0 {
		return 0, 0
	}
	done := map[string]bool{}
	err := db.InTenant(ctx, sc.Pool, sc.TenantID, func(tx pgx.Tx) error {
		ids := make([]string, 0, len(alerts))
		for _, a := range alerts {
			ids = append(ids, a.id)
		}
		rows, err := tx.Query(ctx, `SELECT alert_id::text, action FROM threat_responses WHERE alert_id = ANY($1::uuid[])`, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, action string
			if err := rows.Scan(&id, &action); err != nil {
				return err
			}
			done[id+action] = true
		}
		return rows.Err()
	})
	if err != nil {
		return 0, len(alerts)
	}
	rs := Responder{Pool: sc.Pool, TenantID: sc.TenantID, Secrets: sc.Secrets}
	for _, a := range alerts {
		action := settings[a.detection].AutoResponse
		if a.identityID == "" || action == "" || action == None || done[a.id+action] {
			continue
		}
		done[a.id+action] = true
		ran++
		out, err := rs.Respond(ctx, Actor, a.identityID, ResponseInput{
			Action: action, AlertID: a.id, Reason: "Automatic response to: " + a.summary})
		if err != nil || out.Error != "" {
			failed++
		}
	}
	return ran, failed
}

// pending returns the ids of the sign-ins not looked at yet, oldest
// first, locking them so a scan started from the console at the same
// time skips them.
func pending(ctx context.Context, tx pgx.Tx) ([]int64, error) {
	rows, err := tx.Query(ctx,
		`SELECT id FROM signin_events WHERE NOT evaluated ORDER BY occurred_at, id LIMIT $1 FOR UPDATE SKIP LOCKED`, batchSize)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// evaluate looks at one batch of sign-ins.
func (sc Scanner) evaluate(ctx context.Context, tx pgx.Tx, settings map[string]Setting) ([]raised, int, error) {
	ids, err := pending(ctx, tx)
	if err != nil || len(ids) == 0 {
		return nil, 0, err
	}
	var findings []finding
	for _, detect := range []func(context.Context, pgx.Tx, []int64, map[string]Setting) ([]finding, error){
		detectSuccesses, detectFailures, detectSprays,
	} {
		f, err := detect(ctx, tx, ids, settings)
		if err != nil {
			return nil, 0, err
		}
		findings = append(findings, f...)
	}
	if _, err := tx.Exec(ctx, `UPDATE signin_events SET evaluated = true WHERE id = ANY($1)`, ids); err != nil {
		return nil, 0, err
	}
	out, err := raiseAll(ctx, tx, sc.TenantID, settings, findings)
	return out, len(ids), err
}

func secs(d time.Duration) float64 { return d.Seconds() }

// detectSuccesses looks at successful sign-ins: by leavers, after
// impossible travel, from a new country, on dormant accounts, and right
// after many failures.
func detectSuccesses(ctx context.Context, tx pgx.Tx, ids []int64, settings map[string]Setting) ([]finding, error) {
	travel, country, dormant := settings[ImpossibleTravel], settings[NewCountry], settings[DormantAccount]
	brute, fatigue := settings[BruteForce], settings[MFAFatigue]
	rows, err := tx.Query(ctx, `
		SELECT e.id, e.occurred_at, e.identity_id::text, i.display_name, i.status,
		       coalesce(host(e.ip), ''), coalesce(e.country, ''), e.latitude, e.longitude, e.source, coalesce(e.application, ''),
		       (i.status = 'terminated' AND e.occurred_at > coalesce((i.end_date + 1)::timestamptz, i.updated_at))
		         OR (i.status = 'suspended' AND e.occurred_at > i.updated_at),
		       prev.occurred_at,
		       geo.occurred_at, geo.latitude, geo.longitude, coalesce(host(geo.ip), ''), coalesce(geo.country, ''),
		       coalesce(hist.with_country, 0), coalesce(hist.here, false),
		       coalesce(f.failures, 0), coalesce(f.mfa, 0)
		FROM signin_events e JOIN identities i ON i.id = e.identity_id
		LEFT JOIN LATERAL (
			SELECT p.occurred_at FROM signin_events p
			WHERE p.identity_id = e.identity_id AND p.outcome = 'success' AND (p.occurred_at, p.id) < (e.occurred_at, e.id)
			ORDER BY p.occurred_at DESC, p.id DESC LIMIT 1) prev ON true
		LEFT JOIN LATERAL (
			SELECT p.occurred_at, p.latitude, p.longitude, p.ip, p.country FROM signin_events p
			WHERE p.identity_id = e.identity_id AND p.outcome = 'success' AND p.latitude IS NOT NULL
			  AND (p.occurred_at, p.id) < (e.occurred_at, e.id) AND p.occurred_at >= e.occurred_at - make_interval(secs => $2)
			ORDER BY p.occurred_at DESC, p.id DESC LIMIT 1) geo ON e.latitude IS NOT NULL
		LEFT JOIN LATERAL (
			SELECT count(*) AS with_country, bool_or(p.country = e.country) AS here FROM signin_events p
			WHERE p.identity_id = e.identity_id AND p.outcome = 'success' AND p.country IS NOT NULL
			  AND (p.occurred_at, p.id) < (e.occurred_at, e.id) AND p.occurred_at >= e.occurred_at - make_interval(secs => $3)) hist
			ON e.country IS NOT NULL
		LEFT JOIN LATERAL (
			SELECT count(*) FILTER (WHERE p.outcome = 'failure' AND p.occurred_at >= e.occurred_at - make_interval(secs => $4)) AS failures,
			       count(*) FILTER (WHERE p.outcome = 'mfa_failure' AND p.occurred_at >= e.occurred_at - make_interval(secs => $5)) AS mfa
			FROM signin_events p
			WHERE p.identity_id = e.identity_id AND p.outcome <> 'success'
			  AND p.occurred_at >= e.occurred_at - make_interval(secs => greatest($4, $5)) AND p.occurred_at <= e.occurred_at) f ON true
		WHERE e.id = ANY($1) AND e.outcome = 'success'
		ORDER BY e.occurred_at, e.id`,
		ids, secs(time.Duration(travel.param("window_hours"))*time.Hour),
		secs(time.Duration(country.param("lookback_days"))*24*time.Hour),
		secs(minutes(brute.param("window_minutes"))), secs(minutes(fatigue.param("window_minutes"))))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []finding
	for rows.Next() {
		var (
			id                         int64
			at                         time.Time
			identity, name, status     string
			ip, cc, source, app        string
			lat, lon                   *float64
			leaver                     bool
			prevAt, geoAt              *time.Time
			geoLat, geoLon             *float64
			geoIP, geoCC               string
			withCountry, failures, mfa int
			seenHere                   bool
		)
		if err := rows.Scan(&id, &at, &identity, &name, &status, &ip, &cc, &lat, &lon, &source, &app, &leaver,
			&prevAt, &geoAt, &geoLat, &geoLon, &geoIP, &geoCC, &withCountry, &seenHere, &failures, &mfa); err != nil {
			return nil, err
		}
		base := map[string]any{"event_id": id, "source": source, "ip": ip, "country": cc, "application": app, "at": at}
		evidence := func(extra map[string]any) map[string]any {
			m := map[string]any{}
			for k, v := range base {
				if v != "" {
					m[k] = v
				}
			}
			for k, v := range extra {
				m[k] = v
			}
			return m
		}
		add := func(detection, summary string, raised bool, extra map[string]any) {
			if !settings[detection].Enabled {
				return
			}
			out = append(out, finding{detection: detection, identityID: identity, subject: identity, summary: summary,
				raised: raised, evidence: evidence(extra), at: at})
		}

		if leaver {
			how := "they left the company"
			if status == "suspended" {
				how = "Grantra suspended them"
			}
			add(LeaverSignIn, fmt.Sprintf("%s signed in to %s although %s", name, where(source, app), how), false,
				map[string]any{"identity_status": status})
		}
		if geoAt != nil && lat != nil {
			km := distanceKm(*geoLat, *geoLon, *lat, *lon)
			hours := at.Sub(*geoAt).Hours()
			speed := math.Inf(1)
			if hours > 0 {
				speed = km / hours
			}
			if km >= float64(travel.param("min_distance_km")) && speed >= float64(travel.param("min_speed_kmh")) {
				add(ImpossibleTravel, fmt.Sprintf("%s signed in %s km from their previous sign-in, %s later", name,
					thousands(int(km)), span(at.Sub(*geoAt))), false, map[string]any{
					"distance_km": int(km), "speed_kmh": speedValue(speed), "previous_at": *geoAt,
					"previous_ip": geoIP, "previous_country": geoCC})
			}
		}
		if cc != "" && !seenHere && withCountry >= country.param("min_history") {
			add(NewCountry, fmt.Sprintf("%s signed in from %s, a country not seen for them in %d days", name, cc,
				country.param("lookback_days")), false, map[string]any{"known_sign_ins": withCountry})
		}
		if prevAt != nil && at.Sub(*prevAt) >= time.Duration(dormant.param("dormant_days"))*24*time.Hour {
			add(DormantAccount, fmt.Sprintf("%s signed in after %d days without signing in", name,
				int(at.Sub(*prevAt).Hours()/24)), false, map[string]any{"previous_at": *prevAt})
		}
		if failures >= brute.param("failures") {
			add(BruteForce, fmt.Sprintf("%s signed in right after %s in %d minutes", name,
				textutil.Plural(failures, "wrong password", "wrong passwords"), brute.param("window_minutes")), true,
				map[string]any{"failures": failures, "signed_in": true})
		}
		if mfa >= fatigue.param("failures") {
			add(MFAFatigue, fmt.Sprintf("%s signed in after %s in %d minutes", name,
				textutil.Plural(mfa, "refused second factor", "refused second factors"), fatigue.param("window_minutes")), true,
				map[string]any{"failures": mfa, "signed_in": true})
		}
	}
	return out, rows.Err()
}

// detectFailures looks for many wrong passwords, or second factors, for one
// person. Each person is looked at once per minute of sign-ins, not once
// per failure: a burst of thousands costs dozens of queries.
func detectFailures(ctx context.Context, tx pgx.Tx, ids []int64, settings map[string]Setting) ([]finding, error) {
	brute, fatigue := settings[BruteForce], settings[MFAFatigue]
	if !brute.Enabled && !fatigue.Enabled {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		WITH reps AS (
			SELECT DISTINCT ON (e.identity_id, e.outcome, date_trunc('minute', e.occurred_at))
			       e.id, e.identity_id, e.outcome, e.occurred_at, e.source, coalesce(e.application, '') AS application,
			       CASE e.outcome WHEN 'failure' THEN $2::float8 ELSE $3::float8 END AS window_secs
			FROM signin_events e
			WHERE e.id = ANY($1) AND e.outcome <> 'success' AND e.identity_id IS NOT NULL
			ORDER BY e.identity_id, e.outcome, date_trunc('minute', e.occurred_at), e.occurred_at DESC, e.id DESC)
		SELECT r.id, r.identity_id::text, i.display_name, r.outcome, r.occurred_at, r.source, r.application, c.n, c.ips
		FROM reps r JOIN identities i ON i.id = r.identity_id,
		LATERAL (
			SELECT count(*) AS n, (array_agg(DISTINCT host(p.ip)) FILTER (WHERE p.ip IS NOT NULL))[1:5] AS ips
			FROM signin_events p
			WHERE p.identity_id = r.identity_id AND p.outcome = r.outcome
			  AND p.occurred_at > r.occurred_at - make_interval(secs => r.window_secs) AND p.occurred_at <= r.occurred_at) c
		ORDER BY r.occurred_at, r.id`,
		ids, secs(minutes(brute.param("window_minutes"))), secs(minutes(fatigue.param("window_minutes"))))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []finding
	for rows.Next() {
		var (
			id                                   int64
			identity, name, outcome, source, app string
			at                                   time.Time
			n                                    int
			ips                                  []string
		)
		if err := rows.Scan(&id, &identity, &name, &outcome, &at, &source, &app, &n, &ips); err != nil {
			return nil, err
		}
		s, detection, what := brute, BruteForce, textutil.Plural(n, "wrong password", "wrong passwords")
		if outcome == MFAFailure {
			s, detection, what = fatigue, MFAFatigue, textutil.Plural(n, "refused or wrong second factor", "refused or wrong second factors")
		}
		if !s.Enabled || n < s.param("failures") {
			continue
		}
		out = append(out, finding{detection: detection, identityID: identity, subject: identity,
			summary:  fmt.Sprintf("%s for %s in %d minutes, on %s", what, name, s.param("window_minutes"), where(source, app)),
			evidence: map[string]any{"event_id": id, "failures": n, "ips": httpjson.NonNil(ips), "source": source, "at": at}, at: at})
	}
	return out, rows.Err()
}

// detectSprays looks for one address failing to sign in as many people.
func detectSprays(ctx context.Context, tx pgx.Tx, ids []int64, settings map[string]Setting) ([]finding, error) {
	s := settings[PasswordSpray]
	if !s.Enabled {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		WITH reps AS (
			SELECT DISTINCT ON (e.ip, date_trunc('minute', e.occurred_at)) e.id, e.ip, e.occurred_at, e.source
			FROM signin_events e
			WHERE e.id = ANY($1) AND e.outcome = 'failure' AND e.ip IS NOT NULL
			ORDER BY e.ip, date_trunc('minute', e.occurred_at), e.occurred_at DESC, e.id DESC)
		SELECT r.id, host(r.ip), r.occurred_at, r.source, c.n, c.users
		FROM reps r, LATERAL (
			SELECT count(DISTINCT coalesce(p.identity_id::text, lower(p.username))) AS n,
			       (array_agg(DISTINCT coalesce(p.username, '')) FILTER (WHERE p.username IS NOT NULL))[1:10] AS users
			FROM signin_events p
			WHERE p.ip = r.ip AND p.outcome = 'failure'
			  AND p.occurred_at > r.occurred_at - make_interval(secs => $2) AND p.occurred_at <= r.occurred_at) c
		WHERE c.n >= $3
		ORDER BY r.occurred_at, r.id`, ids, secs(minutes(s.param("window_minutes"))), s.param("users"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []finding
	for rows.Next() {
		var (
			id         int64
			ip, source string
			at         time.Time
			n          int
			users      []string
		)
		if err := rows.Scan(&id, &ip, &at, &source, &n, &users); err != nil {
			return nil, err
		}
		out = append(out, finding{detection: PasswordSpray, subject: "ip:" + ip,
			summary:  fmt.Sprintf("The address %s tried passwords for %d people in %d minutes", ip, n, s.param("window_minutes")),
			evidence: map[string]any{"event_id": id, "ip": ip, "people": n, "usernames": httpjson.NonNil(users), "source": source, "at": at}, at: at})
	}
	return out, rows.Err()
}

// checkAccess looks at access that appeared since the last scan: high and
// critical access Grantra did not give, and new Grantra admins.
func (sc Scanner) checkAccess(ctx context.Context, tx pgx.Tx, settings map[string]Setting) ([]raised, error) {
	var findings []finding

	// Access granted outside Grantra. The first scan only notes where
	// things stand: what was there before is for reviews to deal with.
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return nil, err
	}
	since, err := getCursor(ctx, tx, privilegeCursor)
	if err != nil {
		return nil, err
	}
	if since != "" && settings[OutsideGrantra].Enabled {
		from, err := time.Parse(time.RFC3339Nano, since)
		if err != nil {
			return nil, err
		}
		// An application's first read brings all its access at once; only
		// what appears in an application already read counts.
		rows, err := tx.Query(ctx, `
			SELECT a.id, coalesce(a.identity_id::text, ''), coalesce(i.display_name, ''), coalesce(a.username, a.native_id),
			       app.name, e.id, e.name, e.risk, ae.granted_at
			FROM account_entitlements ae
			JOIN accounts a ON a.id = ae.account_id
			JOIN entitlements e ON e.id = ae.entitlement_id
			JOIN applications app ON app.id = a.application_id
			LEFT JOIN identities i ON i.id = a.identity_id
			WHERE ae.granted_at > $1 AND ae.granted_at <= $2 AND e.risk IN ('high', 'critical')
			  AND a.status = 'active' AND app.connector <> $3
			  AND EXISTS (SELECT 1 FROM accounts o WHERE o.application_id = a.application_id
			              AND o.created_at < ae.granted_at - interval '10 minutes')
			  AND (a.identity_id IS NULL OR NOT `+catalog.HeldSQL("a.identity_id", "e.id")+`)
			ORDER BY ae.granted_at`, from, now, authz.BuiltinConnector)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var (
				account, identity, name, user, app, ent, entName, risk string
				at                                                     time.Time
			)
			if err := rows.Scan(&account, &identity, &name, &user, &app, &ent, &entName, &risk, &at); err != nil {
				rows.Close()
				return nil, err
			}
			subject, who := identity, name
			if identity == "" {
				subject, who = "account:"+account, fmt.Sprintf("The account %s, which belongs to nobody,", user)
			}
			findings = append(findings, finding{detection: OutsideGrantra, identityID: identity, subject: subject,
				summary: fmt.Sprintf("%s got %s (%s risk) in %s, and Grantra did not give it", who, entName, risk, app),
				raised:  risk == "critical",
				evidence: map[string]any{"account_id": account, "account": user, "application": app,
					"entitlement_id": ent, "entitlement": entName, "risk": risk, "at": at}, at: at})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if err := setCursor(ctx, tx, sc.TenantID, privilegeCursor, now.Format(time.RFC3339Nano)); err != nil {
		return nil, err
	}

	// New Grantra admins, from the audit log.
	last, err := getCursor(ctx, tx, adminCursor)
	if err != nil {
		return nil, err
	}
	var top int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM audit_events`).Scan(&top); err != nil {
		return nil, err
	}
	if last != "" && settings[NewPlatformAdmin].Enabled {
		from, err := strconv.ParseInt(last, 10, 64)
		if err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `
			SELECT ev.id, ev.actor, ev.target_id, coalesce(i.display_name, ''), ev.occurred_at
			FROM audit_events ev LEFT JOIN identities i ON i.id::text = ev.target_id
			WHERE ev.id > $1 AND ev.id <= $2 AND ev.action = 'platform_role.granted' AND ev.details->>'role' = $3
			ORDER BY ev.id`, from, top, authz.RoleAdmin)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var (
				id                  int64
				actor, target, name string
				at                  time.Time
			)
			if err := rows.Scan(&id, &actor, &target, &name, &at); err != nil {
				rows.Close()
				return nil, err
			}
			findings = append(findings, finding{detection: NewPlatformAdmin, identityID: target, subject: target,
				summary:  fmt.Sprintf("%s was made a Grantra admin", name),
				evidence: map[string]any{"audit_event_id": id, "granted_by": actor, "at": at}, at: at})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if err := setCursor(ctx, tx, sc.TenantID, adminCursor, strconv.FormatInt(top, 10)); err != nil {
		return nil, err
	}
	return raiseAll(ctx, tx, sc.TenantID, settings, findings)
}

// where names where a sign-in happened, such as "Grantra Login (grantra-console)".
func where(source, app string) string {
	name := source
	if source == SourceGrantraLogin {
		name = "Grantra Login"
	}
	if app != "" {
		return name + " (" + app + ")"
	}
	return name
}

// distanceKm is the great-circle distance between two points.
func distanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	const earth = 6371.0
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earth * math.Asin(math.Min(1, math.Sqrt(a)))
}

// speedValue is a speed for the evidence; JSON has no infinity.
func speedValue(kmh float64) any {
	if math.IsInf(kmh, 0) {
		return "instant"
	}
	return int(kmh)
}

// span says how long d is, roughly: "35 minutes", "3 hours".
func span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < 2*time.Hour:
		return textutil.Plural(int(d.Minutes()), "minute", "minutes")
	default:
		return textutil.Plural(int(d.Hours()), "hour", "hours")
	}
}

// thousands writes n with commas: 12,345.
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
