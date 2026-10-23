// Package reports builds the compliance reports auditors ask for (who
// reviewed what, how fast leavers lost their access, who approved each
// request, separation of duties, privileged access, who holds what, admin
// changes, identity threats) from what Grantra already records, maps them
// to the controls of frameworks such as SOX, ISO 27001, SOC 2 and NIS2, and
// packs them as evidence: CSV files, a printable summary and a manifest with
// the SHA-256 of each file, whose own hash goes to the audit log so a pack
// can be verified later.
package reports

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/settings"
)

// MaxRows is the most rows a table of a report may have. A report bigger
// than this asks for a shorter period or one application, rather than
// handing an auditor a list cut short.
const MaxRows = 100_000

// MaxPeriod is the longest period a report covers.
const MaxPeriod = 3 * 366 * 24 * time.Hour

// Column is one column of a table, as CSV headers and the console show it.
type Column struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

// Table is one list in a report. Its query returns one text column for
// each of Columns, in order.
type Table struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Columns     []Column `json:"columns"`
	sql         string
}

// Fact is a count shown at the top of a report: the rows of a table, or
// those whose column holds one of values.
type fact struct {
	label, table, column string
	values               []string
}

// Definition is one report.
type Definition struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Period says the report covers what happened between two dates;
	// without it, it shows how things are now and ignores the dates.
	Period bool `json:"period"`
	// Application says the report can be narrowed to one application.
	Application bool    `json:"application"`
	Tables      []Table `json:"tables"`
	facts       []fact
}

// Params are what a report is run for: from and to are days (to included),
// and an empty application means all of them.
type Params struct {
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	ApplicationID string    `json:"application_id,omitempty"`
}

// Fact is a count at the top of a report.
type Fact struct {
	Label string `json:"label"`
	Value int    `json:"value"`
}

// TableResult is a table with its rows. RowCount is how many it has, also
// in a preview that leaves some out.
type TableResult struct {
	Table
	Rows     [][]string `json:"rows"`
	RowCount int        `json:"row_count"`
}

// Result is a report as it was run.
type Result struct {
	Report      Definition    `json:"report"`
	Params      Params        `json:"params"`
	Application string        `json:"application,omitempty"`
	GeneratedAt time.Time     `json:"generated_at"`
	Facts       []Fact        `json:"facts"`
	Tables      []TableResult `json:"tables"`
}

// List returns every report, in the order the console shows them.
func List() []Definition { return slices.Clone(definitions) }

// Lookup returns the report id.
func Lookup(id string) (Definition, bool) {
	i := slices.IndexFunc(definitions, func(d Definition) bool { return d.ID == id })
	if i < 0 {
		return Definition{}, false
	}
	return definitions[i], true
}

// Day parses a day written YYYY-MM-DD, as reports take their periods.
func Day(s string) (time.Time, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, apperr.Invalidf("dates are written YYYY-MM-DD, such as 2026-03-31")
	}
	return t, nil
}

// Check validates p, filling in the tenant's default period when it has
// no dates.
func (p *Params) Check(ctx context.Context, tx pgx.Tx, now time.Time) error {
	if p.From.IsZero() && p.To.IsZero() {
		from, to, err := DefaultPeriod(ctx, tx, now)
		if err != nil {
			return err
		}
		p.From, p.To = from, to
	}
	switch {
	case p.From.IsZero() || p.To.IsZero():
		return apperr.Invalidf("give both dates of the period, or neither for the default one")
	case p.To.Before(p.From):
		return apperr.Invalidf("the period ends before it starts")
	case p.To.Sub(p.From) > MaxPeriod:
		return apperr.Invalidf("a report covers at most three years")
	}
	if p.ApplicationID != "" && !db.ValidUUID(p.ApplicationID) {
		return apperr.Invalidf("application must be an application id")
	}
	return nil
}

// DefaultPeriod is the last full period the tenant reports on (Settings →
// General: how many months, from the first month of the fiscal year).
func DefaultPeriod(ctx context.Context, tx pgx.Tx, now time.Time) (from, to time.Time, err error) {
	v, err := settings.Values(ctx, tx, settings.ReportPeriodMonths, settings.FiscalYearStartMonth)
	if err != nil {
		return from, to, err
	}
	from, to = LastPeriod(now, v[settings.ReportPeriodMonths], v[settings.FiscalYearStartMonth])
	return from, to, nil
}

// LastPeriod is the last period of months months that ended before now,
// with periods counted from startMonth (1 for January) of each year: with 3
// and 4, quarters start in April, July, October and January.
func LastPeriod(now time.Time, months, startMonth int) (from, to time.Time) {
	now = now.UTC()
	// Months since the start of a fiscal year in year 0.
	n := now.Year()*12 + int(now.Month()) - startMonth
	start := (n/months - 1) * months
	from = time.Date(start/12, time.Month(start%12+startMonth), 1, 0, 0, 0, 0, time.UTC)
	return from, from.AddDate(0, months, -1)
}

// Run runs the report id for p, which Check validated.
func Run(ctx context.Context, tx pgx.Tx, id string, p Params, now time.Time) (Result, error) {
	def, ok := Lookup(id)
	if !ok {
		return Result{}, apperr.ErrNotFound
	}
	res := Result{Report: def, Params: p, GeneratedAt: now.UTC()}
	if p.ApplicationID != "" {
		if !def.Application {
			return res, apperr.Invalidf("%s cannot be narrowed to one application", def.Title)
		}
		name, err := db.QueryOne(ctx, tx, pgx.RowTo[string], `SELECT name FROM applications WHERE id = $1`, p.ApplicationID)
		if err != nil {
			return res, err
		}
		res.Application = name
	}
	since, until := p.From, p.To.AddDate(0, 0, 1)
	for _, t := range def.Tables {
		rows, err := tx.Query(ctx, withParams+t.sql+fmt.Sprintf(" LIMIT %d", MaxRows+1), since, until, p.ApplicationID)
		if err != nil {
			return res, fmt.Errorf("%s/%s: %w", id, t.Name, err)
		}
		out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([]string, error) {
			vals := make([]string, len(t.Columns))
			ptrs := make([]any, len(vals))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			return vals, row.Scan(ptrs...)
		})
		if err != nil {
			return res, fmt.Errorf("%s/%s: %w", id, t.Name, err)
		}
		if len(out) > MaxRows {
			return res, apperr.Invalidf("%s has more than %d rows; pick a shorter period or one application", t.Title, MaxRows)
		}
		res.Tables = append(res.Tables, TableResult{Table: t, Rows: out, RowCount: len(out)})
	}
	res.Facts = res.count(def.facts)
	return res, nil
}

// Preview is the result with at most rows rows in each table, for a screen.
func (r Result) Preview(rows int) Result {
	r.Tables = slices.Clone(r.Tables)
	for i := range r.Tables {
		r.Tables[i].Rows = r.Tables[i].Rows[:min(rows, len(r.Tables[i].Rows))]
	}
	return r
}

// WhoIs names an actor for a pack: a person's name, or a service token's.
func WhoIs(ctx context.Context, tx pgx.Tx, actor string) (Who, error) {
	w := Who{Actor: actor}
	err := tx.QueryRow(ctx, `SELECT coalesce(
	    (SELECT display_name FROM identities WHERE id = `+db.ActorIdentitySQL("$1::text")+`),
	    (SELECT 'service token ' || name FROM service_tokens WHERE 'service_token:' || id::text = $1), '')`, actor).Scan(&w.Name)
	return w, err
}

// count works out the facts from the tables.
func (r Result) count(facts []fact) []Fact {
	out := make([]Fact, 0, len(facts))
	for _, f := range facts {
		n := 0
		for _, t := range r.Tables {
			if t.Name != f.table {
				continue
			}
			col := slices.IndexFunc(t.Columns, func(c Column) bool { return c.Key == f.column })
			for _, row := range t.Rows {
				if f.column == "" || slices.Contains(f.values, row[col]) {
					n++
				}
			}
		}
		out = append(out, Fact{Label: f.label, Value: n})
	}
	return out
}
