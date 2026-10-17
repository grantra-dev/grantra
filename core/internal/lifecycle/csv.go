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
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/grantra-dev/grantra/core/internal/csvfile"
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
	if err := csvfile.ValidateMapping(c.Columns, knownFields); err != nil {
		return err
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
	f, err := csvfile.Open(r, knownFields, cfg.Columns)
	if err != nil {
		return nil, nil, err
	}
	if !f.Has(FieldEmployeeID) {
		return nil, nil, fmt.Errorf("no %q column (map it with columns.%s)", f.Column(FieldEmployeeID), FieldEmployeeID)
	}
	if !f.Has(FieldDisplayName) && !f.Has(FieldFirstName) && !f.Has(FieldLastName) {
		return nil, nil, fmt.Errorf("no %q column, nor %q and %q", f.Column(FieldDisplayName), f.Column(FieldFirstName), f.Column(FieldLastName))
	}

	var (
		records []Record
		issues  []Issue
		seen    = map[string]int{}
	)
	for {
		line, err := f.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		bad := func(msg string, args ...any) {
			issues = append(issues, Issue{Line: line, ExternalID: f.Get(FieldEmployeeID), Message: fmt.Sprintf(msg, args...)})
		}

		rec := Record{
			Line:              line,
			ExternalID:        f.Get(FieldEmployeeID),
			DisplayName:       f.Get(FieldDisplayName),
			Email:             f.Get(FieldEmail),
			ManagerExternalID: f.Get(FieldManagerID),
			Attributes:        map[string]string{},
		}
		if rec.ExternalID == "" {
			bad("%s is empty", f.Column(FieldEmployeeID))
			continue
		}
		if first, ok := seen[rec.ExternalID]; ok {
			bad("%s %q already appears on line %d", f.Column(FieldEmployeeID), rec.ExternalID, first)
			continue
		}
		seen[rec.ExternalID] = line
		if rec.DisplayName == "" {
			rec.DisplayName = strings.TrimSpace(f.Get(FieldFirstName) + " " + f.Get(FieldLastName))
		}
		if rec.DisplayName == "" {
			bad("the person has no name")
		}
		if rec.Email != "" && !strings.Contains(rec.Email, "@") {
			bad("email %q is not an email address", rec.Email)
		}
		status := strings.ToLower(f.Get(FieldStatus))
		switch {
		case slices.Contains(activeValues, status):
		case slices.Contains(terminatedValues, status):
			rec.Terminated = true
		default:
			bad("unknown status %q (use active or terminated)", f.Get(FieldStatus))
		}
		for _, d := range []struct {
			field string
			dst   **time.Time
		}{{FieldStartDate, &rec.StartDate}, {FieldEndDate, &rec.EndDate}} {
			v := f.Get(d.field)
			if v == "" {
				continue
			}
			t, err := time.Parse(time.DateOnly, v)
			if err != nil {
				bad("%s %q is not a date like 2026-10-31", f.Column(d.field), v)
				continue
			}
			*d.dst = &t
		}
		if rec.StartDate != nil && rec.EndDate != nil && rec.EndDate.Before(*rec.StartDate) {
			bad("end date is before start date")
		}
		for _, field := range JobAttributes {
			if v := f.Get(field); v != "" {
				rec.Attributes[field] = v
			}
		}
		f.Extra(func(name, v string) { rec.Attributes[name] = v })
		records = append(records, rec)
	}
	return records, issues, nil
}
