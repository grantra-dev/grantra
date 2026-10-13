// Package lifecycle keeps identities in step with the company's authoritative
// sources (HR) and turns every change into a joiner, mover or leaver event.
//
// A sync has three steps: parse the feed into records, plan the changes by
// comparing the records with the identities the source owns (a pure function,
// see Plan), and apply the plan in one transaction. A plan with errors is
// never applied: a half-read HR file would otherwise look like a wave of
// leavers.
package lifecycle

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Fields Grantra reads from a source. Columns in the feed are matched to these
// by name, through Config.Columns when the feed uses other names.
const (
	FieldEmployeeID  = "employee_id"
	FieldDisplayName = "display_name"
	FieldFirstName   = "first_name"
	FieldLastName    = "last_name"
	FieldEmail       = "email"
	FieldManagerID   = "manager_id"
	FieldDepartment  = "department"
	FieldTitle       = "title"
	FieldLocation    = "location"
	FieldStatus      = "status"
	FieldStartDate   = "start_date"
	FieldEndDate     = "end_date"
)

var knownFields = []string{
	FieldEmployeeID, FieldDisplayName, FieldFirstName, FieldLastName, FieldEmail, FieldManagerID,
	FieldDepartment, FieldTitle, FieldLocation, FieldStatus, FieldStartDate, FieldEndDate,
}

// JobAttributes are the attributes whose change makes someone a mover, on top
// of a change of manager. Other attribute changes are plain updates.
var JobAttributes = []string{FieldDepartment, FieldTitle, FieldLocation}

// Config is how a source is read and what a sync may do. It is stored as the
// source's config.
type Config struct {
	// Columns maps a Grantra field to the column name in the feed, for feeds
	// that do not use Grantra's names, e.g. {"employee_id": "Worker ID"}.
	Columns map[string]string `json:"columns,omitempty"`
	// Missing says what happens to someone the source owns who is no longer in
	// the feed: "leave" (the default) makes them a leaver, "ignore" leaves
	// them alone, for feeds that only carry changes.
	Missing string `json:"missing,omitempty"`
	// MaxLeaverPercent refuses a sync that would turn more than this share of
	// the source's active people into leavers, unless it is forced. A broken
	// export must not offboard the company. Default 10; 100 turns it off.
	MaxLeaverPercent int `json:"max_leaver_percent,omitempty"`
}

// Validate checks the config and fills in defaults.
func (c *Config) Validate() error {
	for field, col := range c.Columns {
		if !slices.Contains(knownFields, field) {
			return fmt.Errorf("columns: unknown field %q (known: %s)", field, strings.Join(knownFields, ", "))
		}
		if strings.TrimSpace(col) == "" {
			return fmt.Errorf("columns: empty column name for %q", field)
		}
	}
	switch c.Missing {
	case "":
		c.Missing = "leave"
	case "leave", "ignore":
	default:
		return fmt.Errorf(`missing must be "leave" or "ignore", got %q`, c.Missing)
	}
	switch {
	case c.MaxLeaverPercent == 0:
		c.MaxLeaverPercent = 10
	case c.MaxLeaverPercent < 0 || c.MaxLeaverPercent > 100:
		return errors.New("max_leaver_percent must be between 1 and 100")
	}
	return nil
}

// Record is one person as the source describes them.
type Record struct {
	Line              int
	ExternalID        string
	DisplayName       string
	Email             string
	ManagerExternalID string
	// Terminated is set when the feed says the person no longer works here.
	Terminated bool
	StartDate  *time.Time
	EndDate    *time.Time
	// Attributes holds department, title, location and every column Grantra
	// has no field for, under the column name in snake_case.
	Attributes map[string]string
}

// Issue is a problem with one line of the feed, or with one person.
type Issue struct {
	Line       int    `json:"line,omitempty"`
	ExternalID string `json:"external_id,omitempty"`
	Message    string `json:"message"`
}

func (i Issue) String() string {
	if i.Line > 0 {
		return fmt.Sprintf("line %d: %s", i.Line, i.Message)
	}
	return i.Message
}

var (
	activeValues     = []string{"", "active", "a", "employed"}
	terminatedValues = []string{"terminated", "inactive", "left", "leaver", "t"}
)

// ParseCSV reads an HR export with a header row. It returns an error when the
// file as a whole cannot be used (not CSV, required columns missing) and
// issues for lines that cannot.
func ParseCSV(r io.Reader, cfg Config) ([]Record, []Issue, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, err
	}
	// Spreadsheet programs like to start UTF-8 files with a byte order mark.
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	cr := csv.NewReader(bytes.NewReader(data))
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, nil, errors.New("the file is empty")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read header: %w", err)
	}

	// Which column holds each field, and which columns are extra attributes.
	col := map[string]int{}
	extra := map[int]string{}
	byName := map[string]int{}
	for i, h := range header {
		name := normalize(h)
		if name == "" {
			continue
		}
		if _, dup := byName[name]; dup {
			return nil, nil, fmt.Errorf("column %q appears twice", strings.TrimSpace(h))
		}
		byName[name] = i
	}
	for _, f := range knownFields {
		name := f
		if c, ok := cfg.Columns[f]; ok {
			name = normalize(c)
		}
		if i, ok := byName[name]; ok {
			col[f] = i
		}
	}
	taken := map[int]bool{}
	for _, i := range col {
		taken[i] = true
	}
	for name, i := range byName {
		// A column named like a field that was mapped elsewhere is ignored
		// rather than allowed to shadow the mapped one.
		if !taken[i] && !slices.Contains(knownFields, name) {
			extra[i] = name
		}
	}
	if _, ok := col[FieldEmployeeID]; !ok {
		return nil, nil, fmt.Errorf("no %q column (map it with columns.%s)", columnName(cfg, FieldEmployeeID), FieldEmployeeID)
	}
	_, hasDisplay := col[FieldDisplayName]
	_, hasFirst := col[FieldFirstName]
	_, hasLast := col[FieldLastName]
	if !hasDisplay && !hasFirst && !hasLast {
		return nil, nil, fmt.Errorf("no %q column, nor %q and %q", columnName(cfg, FieldDisplayName), columnName(cfg, FieldFirstName), columnName(cfg, FieldLastName))
	}

	var (
		records []Record
		issues  []Issue
		seen    = map[string]int{}
	)
	for {
		row, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line, _ := cr.FieldPos(0)
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				return nil, nil, fmt.Errorf("line %d: %v", pe.Line, pe.Err)
			}
			return nil, nil, err
		}
		if blank(row) {
			continue
		}
		get := func(f string) string {
			if i, ok := col[f]; ok && i < len(row) {
				return strings.TrimSpace(row[i])
			}
			return ""
		}
		bad := func(msg string, args ...any) {
			issues = append(issues, Issue{Line: line, ExternalID: get(FieldEmployeeID), Message: fmt.Sprintf(msg, args...)})
		}

		rec := Record{
			Line:              line,
			ExternalID:        get(FieldEmployeeID),
			DisplayName:       get(FieldDisplayName),
			Email:             get(FieldEmail),
			ManagerExternalID: get(FieldManagerID),
			Attributes:        map[string]string{},
		}
		if rec.ExternalID == "" {
			bad("%s is empty", columnName(cfg, FieldEmployeeID))
			continue
		}
		if first, ok := seen[rec.ExternalID]; ok {
			bad("%s %q already appears on line %d", columnName(cfg, FieldEmployeeID), rec.ExternalID, first)
			continue
		}
		seen[rec.ExternalID] = line
		if rec.DisplayName == "" {
			rec.DisplayName = strings.TrimSpace(get(FieldFirstName) + " " + get(FieldLastName))
		}
		if rec.DisplayName == "" {
			bad("the person has no name")
		}
		if rec.Email != "" && !strings.Contains(rec.Email, "@") {
			bad("email %q is not an email address", rec.Email)
		}
		status := strings.ToLower(get(FieldStatus))
		switch {
		case slices.Contains(activeValues, status):
		case slices.Contains(terminatedValues, status):
			rec.Terminated = true
		default:
			bad("unknown status %q (use active or terminated)", get(FieldStatus))
		}
		for _, d := range []struct {
			field string
			dst   **time.Time
		}{{FieldStartDate, &rec.StartDate}, {FieldEndDate, &rec.EndDate}} {
			v := get(d.field)
			if v == "" {
				continue
			}
			t, err := time.Parse(time.DateOnly, v)
			if err != nil {
				bad("%s %q is not a date like 2026-10-31", columnName(cfg, d.field), v)
				continue
			}
			*d.dst = &t
		}
		if rec.StartDate != nil && rec.EndDate != nil && rec.EndDate.Before(*rec.StartDate) {
			bad("end date is before start date")
		}
		for _, f := range JobAttributes {
			if v := get(f); v != "" {
				rec.Attributes[f] = v
			}
		}
		for i, name := range extra {
			if i < len(row) {
				if v := strings.TrimSpace(row[i]); v != "" {
					rec.Attributes[name] = v
				}
			}
		}
		records = append(records, rec)
	}
	return records, issues, nil
}

// normalize turns a column header into snake_case: "Employee ID" becomes
// "employee_id".
func normalize(h string) string {
	var b strings.Builder
	under := false
	for _, r := range strings.ToLower(strings.TrimSpace(h)) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			if under && b.Len() > 0 {
				b.WriteByte('_')
			}
			under = false
			b.WriteRune(r)
		default:
			under = true
		}
	}
	return b.String()
}

func columnName(cfg Config, field string) string {
	if c, ok := cfg.Columns[field]; ok {
		return c
	}
	return field
}

func blank(row []string) bool {
	for _, v := range row {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}
