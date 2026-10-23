package reports

import (
	"strings"

	"github.com/grantra-dev/grantra/core/internal/db"
)

// Every table's query follows withParams, so it reads the period and the
// application from p: since (included) and until (excluded), and app (NULL
// for all). A query is a SELECT that joins p (FROM p CROSS JOIN ...), or
// more common table expressions starting with a comma.
const withParams = `WITH p AS (SELECT $1::timestamptz AS since, $2::timestamptz AS until, nullif($3, '')::uuid AS app) `

// The pieces below turn columns into the text the reports show. Times are
// UTC, written so spreadsheets read them as dates. Their own table aliases
// end in _, so they never hide a query's.

func ts(expr string) string {
	return `coalesce(to_char(` + expr + ` AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS'), '')`
}

func day(expr string) string { return `coalesce(to_char(` + expr + `, 'YYYY-MM-DD'), '')` }

func text(expr string) string { return `coalesce(` + expr + `::text, '')` }

func yesNo(cond string) string { return `CASE WHEN ` + cond + ` THEN 'yes' ELSE 'no' END` }

// hours is how many hours (one decimal) go from from to to.
func hours(from, to string) string {
	return `coalesce(round((extract(epoch FROM ` + to + ` - ` + from + `) / 3600)::numeric, 1)::text, '')`
}

func person(id string) string {
	return `coalesce((SELECT display_name FROM identities WHERE id = ` + id + `), '')`
}

func email(id string) string {
	return `coalesce((SELECT email FROM identities WHERE id = ` + id + `), '')`
}

// actor is who an actor column names: a person's name, else the actor as
// recorded (a job, a token, an identity source).
func actor(expr string) string {
	return `coalesce((SELECT display_name FROM identities WHERE id = ` + db.ActorIdentitySQL(expr) + `), ` + expr + `, '')`
}

// access is the name of a business role or entitlement, one of them NULL.
func access(role, ent string) string {
	return `coalesce((SELECT name FROM roles WHERE id = ` + role + `), (SELECT name FROM entitlements WHERE id = ` + ent + `), '')`
}

func application(ent string) string {
	return `coalesce((SELECT a_.name FROM entitlements e_ JOIN applications a_ ON a_.id = e_.application_id WHERE e_.id = ` + ent + `), '')`
}

func risk(ent string) string {
	return `coalesce((SELECT risk FROM entitlements WHERE id = ` + ent + `), '')`
}

// inApplication is true when no application was picked, or the role or
// entitlement gives access in it.
func inApplication(role, ent string) string {
	return `(p.app IS NULL
	  OR ` + ent + ` IN (SELECT id FROM entitlements WHERE application_id = p.app)
	  OR ` + role + ` IN (SELECT re_.role_id FROM role_entitlements re_ JOIN entitlements e_ ON e_.id = re_.entitlement_id
	                     WHERE e_.application_id = p.app))`
}

// within is true when the time t falls in the period.
func within(t string) string { return `(` + t + ` >= p.since AND ` + t + ` < p.until)` }

// during is true when something that started at start and ended at end
// (NULL while it goes on) went on at some point of the period.
func during(start, end string) string {
	return `(` + start + ` < p.until AND (` + end + ` IS NULL OR ` + end + ` >= p.since))`
}

// actionMatches is true when an audit event's action is one of actions, each
// a name or a prefix ending in a dot or an underscore.
func actionMatches(col string, actions []string) string {
	quoted := make([]string, len(actions))
	for i, a := range actions {
		quoted[i] = strings.ReplaceAll(a, ".", `\.`)
	}
	return col + ` ~ '^(` + strings.Join(quoted, "|") + `)'`
}

func columns(pairs ...string) []Column {
	out := make([]Column, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, Column{Key: pairs[i], Title: pairs[i+1]})
	}
	return out
}
