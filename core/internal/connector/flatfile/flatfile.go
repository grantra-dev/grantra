// Package flatfile reads an application's accounts from a CSV file that
// someone uploads, for applications Grantra cannot reach with SCIM or LDAP:
// legacy ERPs, in-house tools, anything that can export a list of users.
//
// The file has one row per account, or one row per account and entitlement
// (rows of the same account are merged), so both a user list and the result
// of a "users join roles" query work as they are.
package flatfile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/csvfile"
)

// Kind is the connector name applications use.
const Kind = "csv"

func init() { connector.Register(Kind, New) }

// Columns Grantra reads from an account file.
const (
	FieldAccountID    = "account_id"
	FieldUsername     = "username"
	FieldEmail        = "email"
	FieldDisplayName  = "display_name"
	FieldEmployeeID   = "employee_id"
	FieldStatus       = "status"
	FieldEntitlements = "entitlements"
	FieldLastLogin    = "last_login"
)

// Fields lists every column Grantra reads, for mappings.
var Fields = []string{FieldAccountID, FieldUsername, FieldEmail, FieldDisplayName, FieldEmployeeID, FieldStatus, FieldEntitlements, FieldLastLogin}

// Config is how an application's files are read.
type Config struct {
	// Columns maps a field to the column that holds it in the file, for
	// files that use other names, e.g. {"username": "Login"}.
	Columns map[string]string `json:"columns,omitempty"`
	// Separator splits several entitlements in one cell. Default ";".
	Separator string `json:"separator,omitempty"`
}

func parseConfig(raw json.RawMessage) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return cfg, fmt.Errorf("csv config: %w", err)
		}
	}
	if err := csvfile.ValidateMapping(cfg.Columns, Fields); err != nil {
		return cfg, fmt.Errorf("csv config: %w", err)
	}
	if cfg.Separator == "" {
		cfg.Separator = ";"
	}
	if strings.TrimSpace(cfg.Separator) == "" || len(cfg.Separator) > 3 {
		return cfg, errors.New("csv config: separator must be 1 to 3 visible characters, such as ;")
	}
	return cfg, nil
}

// ErrUploadOnly is what reading an application with the csv connector
// without a file answers: there is nothing to connect to.
var ErrUploadOnly = errors.New("this application's accounts come from uploaded files; upload one to read it")

type reader struct{}

func (reader) Read(context.Context) (connector.Snapshot, error) {
	return connector.Snapshot{}, ErrUploadOnly
}

// New checks a csv connector config. The connector it returns cannot read
// by itself: files come in through Parse.
func New(raw json.RawMessage, _ connector.SecretResolver) (connector.Reader, error) {
	if _, err := parseConfig(raw); err != nil {
		return nil, err
	}
	return reader{}, nil
}

var (
	activeValues   = []string{"", "active", "enabled", "a", "yes", "y", "true", "1"}
	disabledValues = []string{"disabled", "inactive", "locked", "suspended", "d", "no", "n", "false", "0"}
)

// maxReported caps how many problems an error lists.
const maxReported = 20

// Parse reads an account file with the application's connector config. A
// file with any problem is refused as a whole, listing what is wrong: a
// half-read file would look like deleted accounts.
func Parse(raw json.RawMessage, r io.Reader) (connector.Snapshot, error) {
	cfg, err := parseConfig(raw)
	if err != nil {
		return connector.Snapshot{}, err
	}
	f, err := csvfile.Open(r, Fields, cfg.Columns)
	if err != nil {
		return connector.Snapshot{}, err
	}
	idField := FieldAccountID
	if !f.Has(FieldAccountID) {
		idField = FieldUsername
	}
	if !f.Has(idField) {
		return connector.Snapshot{}, fmt.Errorf("no %q column, nor %q (map them with columns)", f.Column(FieldAccountID), f.Column(FieldUsername))
	}

	var (
		snap     connector.Snapshot
		problems []string
		// Where each account is in snap.Accounts, and the line it was
		// first seen on, to merge the rows of one account.
		at      = map[string]int{}
		firstAt = map[string]int{}
		held    = map[string]map[string]bool{}
		// Whether an account's status came from the file or is the default.
		statusGiven = map[string]bool{}
		ents        = map[string]bool{}
	)
	bad := func(line int, msg string, args ...any) {
		problems = append(problems, fmt.Sprintf("line %d: %s", line, fmt.Sprintf(msg, args...)))
	}
	for {
		line, err := f.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return connector.Snapshot{}, err
		}
		a := connector.Account{
			NativeID:    f.Get(idField),
			Username:    f.Get(FieldUsername),
			Email:       f.Get(FieldEmail),
			DisplayName: f.Get(FieldDisplayName),
			ExternalID:  f.Get(FieldEmployeeID),
			Attributes:  map[string]string{},
		}
		if a.NativeID == "" {
			bad(line, "%s is empty", f.Column(idField))
			continue
		}
		switch status := strings.ToLower(f.Get(FieldStatus)); {
		case slices.Contains(activeValues, status):
			a.Active = true
		case slices.Contains(disabledValues, status):
		default:
			bad(line, "unknown status %q (use active or disabled)", f.Get(FieldStatus))
			continue
		}
		if v := f.Get(FieldLastLogin); v != "" {
			if a.LastLogin, err = parseTime(v); err != nil {
				bad(line, "%s %q is not a date (use 2026-10-19 or 2026-10-19T08:30:00Z)", f.Column(FieldLastLogin), v)
				continue
			}
		}
		f.Extra(func(name, v string) { a.Attributes[name] = v })
		var mine []string
		for _, e := range strings.Split(f.Get(FieldEntitlements), cfg.Separator) {
			if e = strings.TrimSpace(e); e != "" {
				mine = append(mine, e)
				if !ents[e] {
					ents[e] = true
					snap.Entitlements = append(snap.Entitlements, connector.Entitlement{NativeID: e, Name: e})
				}
			}
		}

		given := f.Get(FieldStatus) != ""
		i, seen := at[a.NativeID]
		if !seen {
			at[a.NativeID], firstAt[a.NativeID] = len(snap.Accounts), line
			held[a.NativeID] = map[string]bool{}
			statusGiven[a.NativeID] = given
			snap.Accounts = append(snap.Accounts, a)
			i = len(snap.Accounts) - 1
		} else if field, here, there := merge(&snap.Accounts[i], a, f, given, statusGiven); field != "" {
			bad(line, "account %q: %s is %q here but %q on line %d", a.NativeID, field, here, there, firstAt[a.NativeID])
			continue
		}
		for _, e := range mine {
			if !held[a.NativeID][e] {
				held[a.NativeID][e] = true
				snap.Accounts[i].Entitlements = append(snap.Accounts[i].Entitlements, e)
			}
		}
	}
	if len(problems) > 0 {
		msg := strings.Join(problems[:min(len(problems), maxReported)], "; ")
		if len(problems) > maxReported {
			msg += fmt.Sprintf("; and %d more", len(problems)-maxReported)
		}
		return connector.Snapshot{}, errors.New(msg)
	}
	return snap, nil
}

// merge folds a later row of an account into the first one: values the
// first row left empty are filled in. A value that differs is a conflict,
// returned as the column and both values.
func merge(first *connector.Account, later connector.Account, f *csvfile.File, given bool, statusGiven map[string]bool) (field, here, there string) {
	for _, v := range []struct {
		field string
		dst   *string
		val   string
	}{
		{FieldUsername, &first.Username, later.Username},
		{FieldEmail, &first.Email, later.Email},
		{FieldDisplayName, &first.DisplayName, later.DisplayName},
		{FieldEmployeeID, &first.ExternalID, later.ExternalID},
	} {
		switch {
		case v.val == "" || v.val == *v.dst:
		case *v.dst == "":
			*v.dst = v.val
		default:
			return f.Column(v.field), v.val, *v.dst
		}
	}
	switch {
	case !given:
	case !statusGiven[first.NativeID]:
		first.Active, statusGiven[first.NativeID] = later.Active, true
	case later.Active != first.Active:
		return f.Column(FieldStatus), status(later.Active), status(first.Active)
	}
	// Rows of one account may come from several systems: the latest
	// sign-in counts.
	if later.LastLogin.After(first.LastLogin) {
		first.LastLogin = later.LastLogin
	}
	for k, v := range later.Attributes {
		switch old, ok := first.Attributes[k]; {
		case !ok:
			first.Attributes[k] = v
		case old != v:
			return k, v, old
		}
	}
	return "", "", ""
}

// timeLayouts are the ways a last_login cell may write its date.
var timeLayouts = []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"}

// parseTime reads a date and time, taken as UTC when it gives no zone.
func parseTime(v string) (time.Time, error) {
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("not a date: %q", v)
}

func status(active bool) string {
	if active {
		return "active"
	}
	return "disabled"
}
