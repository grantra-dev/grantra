package itdr

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Detections.
const (
	LeaverSignIn     = "leaver_sign_in"
	ImpossibleTravel = "impossible_travel"
	NewCountry       = "new_country"
	DormantAccount   = "dormant_account"
	BruteForce       = "brute_force"
	PasswordSpray    = "password_spray"
	MFAFatigue       = "mfa_fatigue"
	OutsideGrantra   = "privilege_outside_grantra"
	NewPlatformAdmin = "new_platform_admin"
)

// Param is a number a detection's admin can tune.
type Param struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Default     int    `json:"default"`
	Min         int    `json:"min"`
	Max         int    `json:"max"`
}

// Definition describes a detection.
type Definition struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Severity is what its alerts are unless an admin changes it.
	Severity string  `json:"severity"`
	Params   []Param `json:"params"`
	// AutoResponses are the responses Grantra may take on its own when it
	// fires. Detections that only see failed sign-ins never suspend anyone:
	// otherwise anybody could lock a colleague out by typing wrong
	// passwords in their name.
	AutoResponses []string `json:"auto_responses"`
}

// Definitions lists every detection, in the order the console shows them.
var Definitions = []Definition{
	{LeaverSignIn, "Leaver signed in",
		"Someone who left, or whom Grantra suspended, signed in successfully: an account that should be off still works.",
		"critical", nil, []string{RevokeSessions}},
	{ImpossibleTravel, "Impossible travel",
		"Two sign-ins of one person from places too far apart for the time between them. Needs the provider to report locations.",
		"high", []Param{
			{"min_speed_kmh", "Speed above which the trip is impossible, in km/h", 900, 300, 5000},
			{"min_distance_km", "Ignore places closer than this, in km (locations from IP addresses are rough)", 500, 50, 5000},
			{"window_hours", "Compare with the previous sign-in within this many hours", 24, 1, 168},
		}, []string{RevokeSessions, RequireMFA, Suspend}},
	{NewCountry, "New country",
		"A sign-in from a country this person has not signed in from lately. Needs the provider to report countries.",
		"medium", []Param{
			{"lookback_days", "Countries seen in this many days count as known", 90, 7, 365},
			{"min_history", "Only for people with at least this many sign-ins with a country in that time", 5, 1, 100},
		}, []string{RevokeSessions, RequireMFA}},
	{DormantAccount, "Dormant account used",
		"Someone signed in after not doing so for a long time: often an account forgotten about, and taken over.",
		"medium", []Param{
			{"dormant_days", "Days without signing in that make an account dormant", 90, 14, 365},
		}, []string{RevokeSessions, RequireMFA, Suspend}},
	{BruteForce, "Password guessing",
		"Many wrong passwords for one person in a short time. If a sign-in succeeds right after, the alert becomes high.",
		"medium", []Param{
			{"failures", "Wrong passwords that raise it", 10, 3, 1000},
			{"window_minutes", "Within this many minutes", 15, 1, 1440},
		}, []string{RequireMFA}},
	{PasswordSpray, "Password spray",
		"One address trying passwords for many different people: someone testing a common password against the whole company.",
		"high", []Param{
			{"users", "Different people tried that raise it", 10, 3, 10000},
			{"window_minutes", "Within this many minutes", 15, 1, 1440},
		}, nil},
	{MFAFatigue, "MFA fatigue",
		"The second factor refused again and again for one person: someone who has the password is pushing prompts until they accept one. Critical if a sign-in then succeeds.",
		"high", []Param{
			{"failures", "Refused or wrong second factors that raise it", 5, 2, 100},
			{"window_minutes", "Within this many minutes", 10, 1, 1440},
		}, []string{RevokeSessions, Suspend}},
	{OutsideGrantra, "Privilege granted outside Grantra",
		"High or critical access appeared on an account in an application, and Grantra did not give it: nobody requested or approved it here.",
		"high", nil, nil},
	{NewPlatformAdmin, "New Grantra admin",
		"Someone was made a Grantra admin. Admins can change everything, so every new one is worth a look.",
		"medium", nil, nil},
}

// Definition returns the detection with id.
func definition(id string) (Definition, bool) {
	i := slices.IndexFunc(Definitions, func(d Definition) bool { return d.ID == id })
	if i < 0 {
		return Definition{}, false
	}
	return Definitions[i], true
}

// Setting is how a detection is set up for the tenant.
type Setting struct {
	Definition
	Enabled bool `json:"enabled"`
	// Severity is the one its alerts get, which the tenant may have changed
	// from the definition's.
	Severity     string         `json:"severity"`
	AutoResponse string         `json:"auto_response"`
	Values       map[string]int `json:"values"`
	UpdatedBy    string         `json:"updated_by,omitempty"`
	UpdatedAt    *time.Time     `json:"updated_at,omitempty"`
}

// param is the value of one of the detection's numbers.
func (s Setting) param(name string) int { return s.Values[name] }

func defaults(d Definition) Setting {
	s := Setting{Definition: d, Enabled: true, Severity: d.Severity, AutoResponse: None, Values: map[string]int{}}
	for _, p := range d.Params {
		s.Values[p.Name] = p.Default
	}
	return s
}

// Settings returns every detection as the tenant has it.
func Settings(ctx context.Context, tx pgx.Tx) ([]Setting, error) {
	rows, err := tx.Query(ctx,
		`SELECT detection, enabled, severity, auto_response, params, updated_by, updated_at FROM threat_detections`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	saved := map[string]Setting{}
	for rows.Next() {
		var (
			id, sev, auto, by string
			on                bool
			params            map[string]int
			at                time.Time
		)
		if err := rows.Scan(&id, &on, &sev, &auto, &params, &by, &at); err != nil {
			return nil, err
		}
		d, ok := definition(id)
		if !ok {
			continue
		}
		s := defaults(d)
		s.Enabled, s.Severity, s.AutoResponse, s.UpdatedBy, s.UpdatedAt = on, sev, auto, by, &at
		for _, p := range d.Params {
			if v, ok := params[p.Name]; ok {
				s.Values[p.Name] = v
			}
		}
		saved[id] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Setting, 0, len(Definitions))
	for _, d := range Definitions {
		if s, ok := saved[d.ID]; ok {
			out = append(out, s)
		} else {
			out = append(out, defaults(d))
		}
	}
	return out, nil
}

// settingsByID is Settings as a map.
func settingsByID(ctx context.Context, tx pgx.Tx) (map[string]Setting, error) {
	list, err := Settings(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Setting, len(list))
	for _, s := range list {
		out[s.ID] = s
	}
	return out, nil
}

// SettingInput changes a detection. Fields left out keep their value.
type SettingInput struct {
	Enabled      *bool          `json:"enabled"`
	Severity     string         `json:"severity"`
	AutoResponse string         `json:"auto_response"`
	Values       map[string]int `json:"values"`
}

// SaveSetting changes how a detection works, and audits it.
func SaveSetting(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, in SettingInput) (Setting, error) {
	all, err := settingsByID(ctx, tx)
	if err != nil {
		return Setting{}, err
	}
	s, ok := all[id]
	if !ok {
		return Setting{}, apperr.ErrNotFound
	}
	if in.Enabled != nil {
		s.Enabled = *in.Enabled
	}
	if in.Severity != "" {
		if severityRank[in.Severity] == 0 {
			return Setting{}, apperr.Invalidf("severity must be low, medium, high or critical")
		}
		s.Severity = in.Severity
	}
	if in.AutoResponse != "" {
		if in.AutoResponse != None && !slices.Contains(s.AutoResponses, in.AutoResponse) {
			allowed := append([]string{None}, s.AutoResponses...)
			return Setting{}, apperr.Invalidf("auto_response for %s must be one of: %s", id, strings.Join(allowed, ", "))
		}
		s.AutoResponse = in.AutoResponse
	}
	names := make([]string, 0, len(in.Values))
	for name := range in.Values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		i := slices.IndexFunc(s.Params, func(p Param) bool { return p.Name == name })
		if i < 0 {
			return Setting{}, apperr.Invalidf("%s has no setting %q", id, name)
		}
		p, v := s.Params[i], in.Values[name]
		if v < p.Min || v > p.Max {
			return Setting{}, apperr.Invalidf("%s must be between %d and %d", name, p.Min, p.Max)
		}
		s.Values[name] = v
	}
	params, err := json.Marshal(s.Values)
	if err != nil {
		return Setting{}, err
	}
	var at time.Time
	if err := tx.QueryRow(ctx,
		`INSERT INTO threat_detections (tenant_id, detection, enabled, severity, auto_response, params, updated_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (tenant_id, detection) DO UPDATE SET enabled = EXCLUDED.enabled, severity = EXCLUDED.severity,
		   auto_response = EXCLUDED.auto_response, params = EXCLUDED.params, updated_by = EXCLUDED.updated_by, updated_at = now()
		 RETURNING updated_at`, tenantID, id, s.Enabled, s.Severity, s.AutoResponse, params, actor).Scan(&at); err != nil {
		return Setting{}, err
	}
	s.UpdatedBy, s.UpdatedAt = actor, &at
	return s, db.Audit(ctx, tx, tenantID, actor, "itdr.detection_saved", "threat_detection", id, map[string]any{
		"enabled": s.Enabled, "severity": s.Severity, "auto_response": s.AutoResponse, "values": s.Values})
}

// severityRank orders severities; unknown ones are 0.
var severityRank = map[string]int{"low": 1, "medium": 2, "high": 3, "critical": 4}

func minutes(n int) time.Duration { return time.Duration(n) * time.Minute }

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
