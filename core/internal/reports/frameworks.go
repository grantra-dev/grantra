package reports

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Control is one control of a framework and the reports that give its
// evidence.
type Control struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Reports []string `json:"reports"`
}

// Framework is a set of controls an auditor checks, such as SOC 2.
type Framework struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Builtin frameworks come with Grantra; Customized says the tenant
	// changed one, and resetting it brings the defaults back.
	Builtin    bool       `json:"builtin"`
	Customized bool       `json:"customized"`
	Enabled    bool       `json:"enabled"`
	Controls   []Control  `json:"controls"`
	UpdatedBy  string     `json:"updated_by,omitempty"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
}

// Limits of what an admin may write.
const (
	maxControls    = 200
	maxControlID   = 40
	maxControlText = 300
	maxName        = 100
	maxDescription = 1000
)

// builtins are the frameworks every tenant starts with: a suggested mapping
// of each framework's access controls to the reports that evidence them,
// for the tenant to rename and adjust to its own control matrix.
var builtins = []Framework{
	{ID: "sox", Name: "SOX (IT general controls)",
		Description: "Access controls over financial systems, as auditors test them under the Sarbanes-Oxley Act. Control ids are examples: use your own matrix's.",
		Controls: []Control{
			{"ITGC-AC-01", "Access is approved before it is granted", []string{Requests}},
			{"ITGC-AC-02", "Access of leavers is removed on time", []string{Leavers}},
			{"ITGC-AC-03", "Access is reviewed periodically and inappropriate access removed", []string{Reviews}},
			{"ITGC-AC-04", "Privileged access is restricted and monitored", []string{Privileged, Admin}},
			{"ITGC-AC-05", "Incompatible duties are segregated", []string{SoD}},
			{"ITGC-AC-06", "Complete population of user access", []string{Access}},
		}},
	{ID: "iso27001", Name: "ISO/IEC 27001:2022",
		Description: "The Annex A controls on identities and access.",
		Controls: []Control{
			{"A.5.3", "Segregation of duties", []string{SoD}},
			{"A.5.15", "Access control", []string{Requests, Access}},
			{"A.5.16", "Identity management", []string{Leavers, Threats}},
			{"A.5.18", "Access rights", []string{Requests, Reviews, Leavers}},
			{"A.8.2", "Privileged access rights", []string{Privileged}},
			{"A.8.15", "Logging", []string{Admin}},
			{"A.8.16", "Monitoring activities", []string{Threats}},
		}},
	{ID: "soc2", Name: "SOC 2",
		Description: "The Trust Services Criteria on logical access, monitoring and change management.",
		Controls: []Control{
			{"CC6.1", "Logical access security over protected information assets", []string{Access, Privileged}},
			{"CC6.2", "Users are registered and authorized before access is granted, and removed when no longer needed", []string{Requests, Leavers}},
			{"CC6.3", "Access is granted, changed and removed based on roles, least privilege and segregation of duties", []string{Reviews, SoD, Leavers}},
			{"CC7.2", "Anomalies that could be security events are monitored and evaluated", []string{Threats}},
			{"CC8.1", "Changes to the system are authorized and recorded", []string{Admin}},
		}},
	{ID: "nis2", Name: "NIS2",
		Description: "Article 21(2) cybersecurity risk-management measures on access, incidents and their effectiveness.",
		Controls: []Control{
			{"21(2)(b)", "Incident handling", []string{Threats}},
			{"21(2)(f)", "Assessing the effectiveness of the measures", []string{Admin, Reviews}},
			{"21(2)(i)", "Access control policies", []string{Access, Requests, Reviews, Leavers, SoD}},
			{"21(2)(i) privileged", "Privileged access", []string{Privileged}},
		}},
}

func builtin(id string) (Framework, bool) {
	i := slices.IndexFunc(builtins, func(f Framework) bool { return f.ID == id })
	if i < 0 {
		return Framework{}, false
	}
	f := builtins[i]
	f.Builtin, f.Enabled, f.Controls = true, true, slices.Clone(f.Controls)
	return f, true
}

// Frameworks returns the built-in frameworks as the tenant has them, then
// the ones it added, by name.
func Frameworks(ctx context.Context, tx pgx.Tx) ([]Framework, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, name, coalesce(description, ''), enabled, controls, updated_by, updated_at FROM report_frameworks ORDER BY lower(name), id`)
	if err != nil {
		return nil, err
	}
	saved, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Framework, error) {
		var f Framework
		err := row.Scan(&f.ID, &f.Name, &f.Description, &f.Enabled, &f.Controls, &f.UpdatedBy, &f.UpdatedAt)
		return f, err
	})
	if err != nil {
		return nil, err
	}
	out := make([]Framework, 0, len(builtins)+len(saved))
	for _, b := range builtins {
		f, _ := builtin(b.ID)
		if i := slices.IndexFunc(saved, func(s Framework) bool { return s.ID == b.ID }); i >= 0 {
			f.Customized, f.Enabled, f.Controls, f.UpdatedBy, f.UpdatedAt = true, saved[i].Enabled, saved[i].Controls, saved[i].UpdatedBy, saved[i].UpdatedAt
		}
		out = append(out, f)
	}
	for _, s := range saved {
		if _, ok := builtin(s.ID); !ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// GetFramework returns the framework id as the tenant has it.
func GetFramework(ctx context.Context, tx pgx.Tx, id string) (Framework, error) {
	all, err := Frameworks(ctx, tx)
	if err != nil {
		return Framework{}, err
	}
	i := slices.IndexFunc(all, func(f Framework) bool { return f.ID == id })
	if i < 0 {
		return Framework{}, apperr.ErrNotFound
	}
	return all[i], nil
}

// FrameworkInput is what an admin writes. A built-in framework keeps its
// name and description.
type FrameworkInput struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Enabled     bool      `json:"enabled"`
	Controls    []Control `json:"controls"`
}

func (in *FrameworkInput) validate() error {
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	switch {
	case in.Name == "" || len(in.Name) > maxName:
		return apperr.Invalidf("name must be between 1 and %d characters", maxName)
	case len(in.Description) > maxDescription:
		return apperr.Invalidf("description must be at most %d characters", maxDescription)
	case len(in.Controls) > maxControls:
		return apperr.Invalidf("a framework has at most %d controls", maxControls)
	}
	seen := map[string]bool{}
	for i := range in.Controls {
		c := &in.Controls[i]
		c.ID, c.Title = strings.TrimSpace(c.ID), strings.TrimSpace(c.Title)
		switch {
		case c.ID == "" || len(c.ID) > maxControlID:
			return apperr.Invalidf("control ids must be between 1 and %d characters", maxControlID)
		case seen[strings.ToLower(c.ID)]:
			return apperr.Invalidf("control %q is listed twice", c.ID)
		case c.Title == "" || len(c.Title) > maxControlText:
			return apperr.Invalidf("control %s needs a title of at most %d characters", c.ID, maxControlText)
		case len(c.Reports) == 0:
			return apperr.Invalidf("control %s needs at least one report", c.ID)
		}
		seen[strings.ToLower(c.ID)] = true
		var reports []string
		for _, r := range c.Reports {
			if _, ok := Lookup(r); !ok {
				return apperr.Invalidf("control %s: there is no report %q", c.ID, r)
			}
			if !slices.Contains(reports, r) {
				reports = append(reports, r)
			}
		}
		c.Reports = reports
	}
	if in.Controls == nil {
		in.Controls = []Control{}
	}
	return nil
}

// SaveFramework writes the framework id, or adds one when id is empty.
func SaveFramework(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, in FrameworkInput) (Framework, error) {
	if b, ok := builtin(id); ok {
		in.Name, in.Description = b.Name, b.Description
	} else if id != "" {
		if _, err := GetFramework(ctx, tx, id); err != nil {
			return Framework{}, err
		}
	}
	if err := in.validate(); err != nil {
		return Framework{}, err
	}
	err := tx.QueryRow(ctx,
		`INSERT INTO report_frameworks (tenant_id, id, name, description, enabled, controls, updated_by)
		 VALUES ($1, coalesce(nullif($2, ''), gen_random_uuid()::text), $3, nullif($4, ''), $5, $6, $7)
		 ON CONFLICT (tenant_id, id) DO UPDATE
		 SET name = $3, description = nullif($4, ''), enabled = $5, controls = $6, updated_by = $7, updated_at = now()
		 RETURNING id`, tenantID, id, in.Name, in.Description, in.Enabled, in.Controls, actor).Scan(&id)
	if err != nil {
		return Framework{}, err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "report_framework.saved", "report_framework", id,
		map[string]any{"name": in.Name, "enabled": in.Enabled, "controls": in.Controls}); err != nil {
		return Framework{}, err
	}
	return GetFramework(ctx, tx, id)
}

// DeleteFramework removes a framework the tenant added, or brings a
// built-in one back to its defaults.
func DeleteFramework(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error {
	var name string
	err := tx.QueryRow(ctx, `DELETE FROM report_frameworks WHERE id = $1 RETURNING name`, id).Scan(&name)
	_, isBuiltin := builtin(id)
	switch {
	case errors.Is(err, pgx.ErrNoRows) && isBuiltin:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return apperr.ErrNotFound
	case err != nil:
		return err
	}
	action := "report_framework.deleted"
	if isBuiltin {
		action = "report_framework.reset"
	}
	return db.Audit(ctx, tx, tenantID, actor, action, "report_framework", id, map[string]any{"name": name})
}
