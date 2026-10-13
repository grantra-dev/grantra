// Package reviews runs access reviews (certifications). A campaign takes
// every assignment given by hand or by request, optionally only for some
// applications, and asks a reviewer to keep or revoke each one: the person's
// manager, or the owner of the application or role. Revoked access is
// removed at once; provisioning then takes it out of the application.
//
// Birthright access is not reviewed here: rules give it and take it back, so
// what to review for it is the rule. Items with nobody to ask (no manager or
// owner, an inactive reviewer, or a reviewer who would review their own
// access) go to the admins. When a campaign ends, what nobody reviewed is
// kept or revoked as the campaign says.
package reviews

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Reviewer kinds.
const (
	ByManager = "manager"
	ByOwner   = "owner"
)

// Decisions.
const (
	Pending = "pending"
	Keep    = "keep"
	Revoke  = "revoke"
)

// Campaign statuses.
const (
	Active    = "active"
	Completed = "completed"
)

// Campaign is one access review.
type Campaign struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Reviewer       string     `json:"reviewer"`
	ApplicationIDs []string   `json:"application_ids"`
	OnMissing      string     `json:"on_missing"`
	DueAt          time.Time  `json:"due_at"`
	Status         string     `json:"status"`
	CreatedBy      string     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	ClosedAt       *time.Time `json:"closed_at,omitempty"`
	// Progress counts items by decision.
	Progress map[string]int `json:"progress"`
}

// Input describes a campaign to launch.
type Input struct {
	Name           string   `json:"name"`
	Reviewer       string   `json:"reviewer"`
	ApplicationIDs []string `json:"application_ids"`
	OnMissing      string   `json:"on_missing"`
	// DueInDays is how long reviewers have. Default 14, at most 90.
	DueInDays int `json:"due_in_days"`
}

func (in *Input) check(ctx context.Context, tx pgx.Tx) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 200 {
		return apperr.Invalidf("name is required, up to 200 characters")
	}
	if in.Reviewer == "" {
		in.Reviewer = ByManager
	}
	if in.Reviewer != ByManager && in.Reviewer != ByOwner {
		return apperr.Invalidf(`reviewer must be "manager" or "owner"`)
	}
	if in.OnMissing == "" {
		in.OnMissing = Keep
	}
	if in.OnMissing != Keep && in.OnMissing != Revoke {
		return apperr.Invalidf(`on_missing must be "keep" or "revoke"`)
	}
	switch {
	case in.DueInDays == 0:
		in.DueInDays = 14
	case in.DueInDays < 1 || in.DueInDays > 90:
		return apperr.Invalidf("due_in_days must be between 1 and 90")
	}
	if in.ApplicationIDs == nil {
		in.ApplicationIDs = []string{}
	}
	for _, id := range in.ApplicationIDs {
		var connector string
		err := pgx.ErrNoRows
		if db.ValidUUID(id) {
			err = tx.QueryRow(ctx, `SELECT connector FROM applications WHERE id = $1`, id).Scan(&connector)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.Invalidf("application %s does not exist", id)
		}
		if err != nil {
			return err
		}
		if connector == authz.BuiltinConnector {
			return apperr.Invalidf("platform roles are not reviewed in campaigns; see them under platform roles")
		}
	}
	return nil
}

// Launch creates a campaign and its items, one per assignment in scope.
func Launch(ctx context.Context, tx pgx.Tx, tenantID, actor string, in Input) (Campaign, error) {
	if err := in.check(ctx, tx); err != nil {
		return Campaign{}, err
	}
	var id string
	err := tx.QueryRow(ctx,
		`INSERT INTO review_campaigns (tenant_id, name, reviewer, application_ids, on_missing, due_at, created_by)
		 VALUES ($1, $2, $3, $4, $5, now() + make_interval(days => $6), $7) RETURNING id`,
		tenantID, in.Name, in.Reviewer, in.ApplicationIDs, in.OnMissing, in.DueInDays, actor).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Campaign{}, apperr.Conflictf("a campaign named %q already exists", in.Name)
	}
	if err != nil {
		return Campaign{}, err
	}

	// The reviewer of each item: the manager, or the owner of the role or
	// of the entitlement's application; nobody (admins) when there is no
	// one, they are inactive, or they would review their own access.
	reviewer := `i.manager_id`
	if in.Reviewer == ByOwner {
		reviewer = `coalesce(ro.owner_id, app.owner_id)`
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO review_items (tenant_id, campaign_id, assignment_id, identity_id, role_id, entitlement_id, origin, granted_at, reviewer_id)
		SELECT $1, $2, x.id, x.identity_id, x.role_id, x.entitlement_id, x.origin, x.granted_at,
		       CASE WHEN r.id IS NULL OR r.status <> 'active' OR r.id = x.identity_id THEN NULL ELSE r.id END
		FROM access_assignments x
		JOIN identities i ON i.id = x.identity_id
		LEFT JOIN roles ro ON ro.id = x.role_id
		LEFT JOIN entitlements e ON e.id = x.entitlement_id
		LEFT JOIN applications app ON app.id = e.application_id
		LEFT JOIN identities r ON r.id = `+reviewer+`
		WHERE x.origin <> $3
		  AND (x.expires_at IS NULL OR x.expires_at > now())
		  AND (cardinality($4::uuid[]) = 0 OR e.application_id = ANY($4))`,
		tenantID, id, access.OriginBirthright, in.ApplicationIDs)
	if err != nil {
		return Campaign{}, err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "review_campaign.launched", "review_campaign", id,
		map[string]any{"name": in.Name, "reviewer": in.Reviewer, "application_ids": in.ApplicationIDs, "on_missing": in.OnMissing, "items": tag.RowsAffected()}); err != nil {
		return Campaign{}, err
	}
	if tag.RowsAffected() == 0 {
		if err := closeCampaign(ctx, tx, tenantID, actor, id); err != nil {
			return Campaign{}, err
		}
	}
	return Get(ctx, tx, id)
}

const selectCampaigns = `
	SELECT c.id, c.name, c.reviewer, c.application_ids::text[], c.on_missing, c.due_at, c.status, c.created_by, c.created_at, c.closed_at,
	       coalesce((SELECT jsonb_object_agg(decision, n) FROM (
	         SELECT decision, count(*) AS n FROM review_items WHERE campaign_id = c.id GROUP BY decision) d), '{}')
	FROM review_campaigns c`

func scanCampaign(row pgx.CollectableRow) (Campaign, error) {
	var c Campaign
	err := row.Scan(&c.ID, &c.Name, &c.Reviewer, &c.ApplicationIDs, &c.OnMissing, &c.DueAt, &c.Status, &c.CreatedBy,
		&c.CreatedAt, &c.ClosedAt, &c.Progress)
	return c, err
}

// Get returns a campaign with its progress.
func Get(ctx context.Context, tx pgx.Tx, id string) (Campaign, error) {
	if !db.ValidUUID(id) {
		return Campaign{}, apperr.ErrNotFound
	}
	rows, err := tx.Query(ctx, selectCampaigns+` WHERE c.id = $1`, id)
	if err != nil {
		return Campaign{}, err
	}
	c, err := pgx.CollectExactlyOneRow(rows, scanCampaign)
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, apperr.ErrNotFound
	}
	return c, err
}

// List returns every campaign, newest first.
func List(ctx context.Context, tx pgx.Tx) ([]Campaign, error) {
	rows, err := tx.Query(ctx, selectCampaigns+` ORDER BY c.created_at DESC, c.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanCampaign)
}

// Item is one access to review.
type Item struct {
	ID           string `json:"id"`
	CampaignID   string `json:"campaign_id"`
	CampaignName string `json:"campaign_name"`
	IdentityID   string `json:"identity_id"`
	IdentityName string `json:"identity_name"`
	// Type is "role" or "entitlement".
	Type        string    `json:"type"`
	TargetID    string    `json:"target_id"`
	TargetName  string    `json:"target_name"`
	Application string    `json:"application,omitempty"`
	Risk        string    `json:"risk,omitempty"`
	Origin      string    `json:"origin"`
	GrantedAt   time.Time `json:"granted_at"`
	// ReviewerID is empty when an admin reviews it.
	ReviewerID   string     `json:"reviewer_id,omitempty"`
	ReviewerName string     `json:"reviewer_name,omitempty"`
	Decision     string     `json:"decision"`
	DecidedBy    string     `json:"decided_by,omitempty"`
	DecidedAt    *time.Time `json:"decided_at,omitempty"`
	Comment      string     `json:"comment,omitempty"`

	campaignStatus string
	assignmentID   string
}

// Resource describes the item to the policies.
func (it Item) Resource() authz.Resource {
	var reviewers []string
	if it.ReviewerID != "" {
		reviewers = []string{it.ReviewerID}
	}
	return authz.ReviewItemResource(it.ID, it.IdentityID, reviewers)
}

const selectItems = `
	SELECT it.id, it.campaign_id, c.name, c.status, it.identity_id, i.display_name,
	       CASE WHEN it.role_id IS NULL THEN 'entitlement' ELSE 'role' END,
	       coalesce(it.role_id, it.entitlement_id), coalesce(ro.name, e.name), coalesce(app.name, ''), coalesce(e.risk, ''),
	       it.origin, it.granted_at, coalesce(it.reviewer_id::text, ''), coalesce(rv.display_name, ''),
	       it.decision, coalesce(it.decided_by, ''), it.decided_at, coalesce(it.comment, ''), coalesce(it.assignment_id::text, '')
	FROM review_items it
	JOIN review_campaigns c ON c.id = it.campaign_id
	JOIN identities i ON i.id = it.identity_id
	LEFT JOIN identities rv ON rv.id = it.reviewer_id
	LEFT JOIN roles ro ON ro.id = it.role_id
	LEFT JOIN entitlements e ON e.id = it.entitlement_id
	LEFT JOIN applications app ON app.id = e.application_id`

func scanItem(row pgx.CollectableRow) (Item, error) {
	var it Item
	err := row.Scan(&it.ID, &it.CampaignID, &it.CampaignName, &it.campaignStatus, &it.IdentityID, &it.IdentityName,
		&it.Type, &it.TargetID, &it.TargetName, &it.Application, &it.Risk,
		&it.Origin, &it.GrantedAt, &it.ReviewerID, &it.ReviewerName,
		&it.Decision, &it.DecidedBy, &it.DecidedAt, &it.Comment, &it.assignmentID)
	return it, err
}

// GetItem returns one item, locked for a decision.
func GetItem(ctx context.Context, tx pgx.Tx, id string) (Item, error) {
	if !db.ValidUUID(id) {
		return Item{}, apperr.ErrNotFound
	}
	rows, err := tx.Query(ctx, selectItems+` WHERE it.id = $1 FOR UPDATE OF it`, id)
	if err != nil {
		return Item{}, err
	}
	it, err := pgx.CollectExactlyOneRow(rows, scanItem)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, apperr.ErrNotFound
	}
	return it, err
}

// ItemFilter selects items.
type ItemFilter struct {
	CampaignID string
	// Mine lists only what Me reviews; admins (Admin) also get the items
	// with nobody else to ask. Never their own access.
	Mine     bool
	Me       string
	Admin    bool
	Decision string
	Limit    int
	After    string
}

// Items lists review items.
func Items(ctx context.Context, tx pgx.Tx, f ItemFilter) ([]Item, error) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.CampaignID != "" {
		if !db.ValidUUID(f.CampaignID) {
			return nil, apperr.Invalidf("campaign_id must be a campaign id")
		}
		where = append(where, "it.campaign_id = "+arg(f.CampaignID))
	}
	if f.Mine {
		me := arg(f.Me)
		cond := "it.reviewer_id = " + me
		if f.Admin {
			cond = "(" + cond + " OR it.reviewer_id IS NULL)"
		}
		where = append(where, cond, "it.identity_id <> "+me, "c.status = 'active'")
	}
	if f.Decision != "" {
		if f.Decision != Pending && f.Decision != Keep && f.Decision != Revoke {
			return nil, apperr.Invalidf("decision must be pending, keep or revoke")
		}
		where = append(where, "it.decision = "+arg(f.Decision))
	}
	if f.After != "" {
		if !db.ValidUUID(f.After) {
			return nil, apperr.Invalidf("after must be an item id")
		}
		where = append(where, "it.id > "+arg(f.After))
	}
	q := selectItems
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY it.id LIMIT " + arg(f.Limit)
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanItem)
}

// Decide keeps or revokes the access of an item. The caller has checked
// that actor may decide it. Revoking removes the assignment at once.
func Decide(ctx context.Context, tx pgx.Tx, tenantID, actor string, it Item, decision, comment string) (Item, error) {
	if decision != Keep && decision != Revoke {
		return it, apperr.Invalidf(`decision must be "keep" or "revoke"`)
	}
	comment = strings.TrimSpace(comment)
	if len(comment) > 2000 {
		return it, apperr.Invalidf("comment is longer than 2000 characters")
	}
	if it.campaignStatus != Active {
		return it, apperr.Conflictf("the campaign is over")
	}
	if it.Decision != Pending {
		return it, apperr.Conflictf("this access was already reviewed (%s)", it.Decision)
	}
	if err := decide(ctx, tx, tenantID, actor, it, decision, comment); err != nil {
		return it, err
	}
	var open bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM review_items WHERE campaign_id = $1 AND decision = 'pending')`, it.CampaignID).Scan(&open); err != nil {
		return it, err
	}
	if !open {
		if err := closeCampaign(ctx, tx, tenantID, actor, it.CampaignID); err != nil {
			return it, err
		}
	}
	return GetItem(ctx, tx, it.ID)
}

func decide(ctx context.Context, tx pgx.Tx, tenantID, actor string, it Item, decision, comment string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE review_items SET decision = $2, decided_by = $3, decided_at = now(), comment = nullif($4, '') WHERE id = $1`,
		it.ID, decision, actor, comment); err != nil {
		return err
	}
	details := map[string]any{"campaign_id": it.CampaignID, "identity_id": it.IdentityID, it.Type + "_id": it.TargetID, "decision": decision}
	if comment != "" {
		details["comment"] = comment
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "review_item.decided", "review_item", it.ID, details); err != nil {
		return err
	}
	if decision != Revoke || it.assignmentID == "" {
		return nil
	}
	tag, err := tx.Exec(ctx, `DELETE FROM access_assignments WHERE id = $1`, it.assignmentID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return db.Audit(ctx, tx, tenantID, actor, "access.removed", "identity", it.IdentityID,
		map[string]any{"assignment_id": it.assignmentID, it.Type + "_id": it.TargetID, "origin": it.Origin,
			"reason": "revoked in access review", "review_item_id": it.ID})
}

// Close ends an active campaign now. What nobody reviewed is kept or revoked
// as the campaign says.
func Close(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) (Campaign, error) {
	c, err := Get(ctx, tx, id)
	if err != nil {
		return c, err
	}
	if c.Status != Active {
		return c, apperr.Conflictf("the campaign is already over")
	}
	if err := finish(ctx, tx, tenantID, actor, c); err != nil {
		return c, err
	}
	return Get(ctx, tx, id)
}

// finish applies on_missing to the pending items and closes the campaign.
func finish(ctx context.Context, tx pgx.Tx, tenantID, actor string, c Campaign) error {
	rows, err := tx.Query(ctx, selectItems+` WHERE it.campaign_id = $1 AND it.decision = 'pending' FOR UPDATE OF it`, c.ID)
	if err != nil {
		return err
	}
	pending, err := pgx.CollectRows(rows, scanItem)
	if err != nil {
		return err
	}
	for _, it := range pending {
		if err := decide(ctx, tx, tenantID, actor, it, c.OnMissing, "not reviewed before the campaign ended"); err != nil {
			return err
		}
	}
	return closeCampaign(ctx, tx, tenantID, actor, c.ID)
}

func closeCampaign(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE review_campaigns SET status = 'completed', closed_at = now() WHERE id = $1 AND status = 'active'`, id); err != nil {
		return err
	}
	return db.Audit(ctx, tx, tenantID, actor, "review_campaign.completed", "review_campaign", id, nil)
}

// CloseDue ends every campaign past its due date and returns how many.
func CloseDue(ctx context.Context, pool *pgxpool.Pool, tenantID string) (int, error) {
	n := 0
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			selectCampaigns+` WHERE c.status = 'active' AND c.due_at <= now() FOR UPDATE OF c SKIP LOCKED`)
		if err != nil {
			return err
		}
		due, err := pgx.CollectRows(rows, scanCampaign)
		if err != nil {
			return err
		}
		for _, c := range due {
			if err := finish(ctx, tx, tenantID, "system:reviews", c); err != nil {
				return err
			}
		}
		n = len(due)
		return nil
	})
	return n, err
}
