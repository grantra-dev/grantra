package insights

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/catalog"
)

// What a suggestion rests on.
const (
	// SignalUnused: Grantra tracks the access and nobody used it in
	// settings.UnusedDays.
	SignalUnused = "unused"
	// SignalUsed: they used it in the last settings.UnusedDays.
	SignalUsed = "used"
	// SignalCommon: at least half of their peers hold it too.
	SignalCommon = "common"
	// SignalRare: at most one in ten of their peers holds it.
	SignalRare = "rare"
)

// Suggestion is what Grantra makes of some access someone holds, for the
// person deciding whether they keep it.
type Suggestion struct {
	// Action is "keep", "revoke", or empty when nothing stands out.
	Action  string   `json:"action,omitempty"`
	Signals []string `json:"signals"`
	Use
	// PeerGroup is who they were compared with: "department" (the people
	// in Department) or "team" (the people with the same manager). Empty
	// when they have no peers to compare with, as for service accounts, AI
	// agents and workloads: each one does its own job.
	PeerGroup  string `json:"peer_group,omitempty"`
	Department string `json:"department,omitempty"`
	// Peers is how many people are in the group, them aside, and
	// PeerHolders how many of those hold the same access.
	Peers       int `json:"peers"`
	PeerHolders int `json:"peer_holders"`
}

// Access is some access someone holds, to make a suggestion about.
type Access struct {
	IdentityID string
	// One of RoleID and EntitlementID.
	RoleID        string
	EntitlementID string
	// EligibilityID is set when they may activate it rather than hold it.
	EligibilityID string
	GrantedAt     time.Time
}

// holdsSQL is an SQL condition, true when the identity p.id holds or may
// activate the role %[1]s or the entitlement %[2]s (one of them NULL): an
// assignment (%[3]s is catalog.HeldSQL for the entitlement), an
// eligibility, or for an entitlement an active account that holds it.
const holdsSQL = `(CASE WHEN %[1]s IS NOT NULL THEN
	  EXISTS (SELECT 1 FROM access_assignments x WHERE x.identity_id = p.id AND x.role_id = %[1]s
	          AND (x.expires_at IS NULL OR x.expires_at > now()))
	ELSE %[3]s
	  OR EXISTS (SELECT 1 FROM accounts a JOIN account_entitlements ae ON ae.account_id = a.id
	             WHERE a.identity_id = p.id AND a.status = 'active' AND ae.entitlement_id = %[2]s
	               AND (ae.expires_at IS NULL OR ae.expires_at > now()))
	END
	OR EXISTS (SELECT 1 FROM access_eligibilities el WHERE el.identity_id = p.id
	           AND coalesce(el.role_id, el.entitlement_id) = coalesce(%[1]s, %[2]s)
	           AND (el.expires_at IS NULL OR el.expires_at > now())))`

// Suggest says, for each access, what Grantra makes of it: whether it is
// used, and how common it is among the person's peers. It reads all of
// them in one query.
func Suggest(ctx context.Context, tx pgx.Tx, items []Access) ([]Suggestion, error) {
	out := make([]Suggestion, len(items))
	if len(items) == 0 {
		return out, nil
	}
	n := make([]int, len(items))
	ids := make([]string, len(items))
	roles := make([]string, len(items))
	ents := make([]string, len(items))
	els := make([]string, len(items))
	for i, it := range items {
		n[i], ids[i], roles[i], ents[i], els[i] = i, it.IdentityID, it.RoleID, it.EntitlementID, it.EligibilityID
	}
	const (
		role = `nullif(k.role_id, '')::uuid`
		ent  = `nullif(k.entitlement_id, '')::uuid`
	)
	l, err := loadLimits(ctx, tx)
	if err != nil {
		return nil, err
	}
	holds := fmt.Sprintf(holdsSQL, role, ent, catalog.HeldSQL("p.id", ent))
	rows, err := tx.Query(ctx, `
		WITH k AS (SELECT * FROM unnest($1::int[], $2::uuid[], $3::text[], $4::text[], $5::text[])
		           AS k(n, identity_id, role_id, entitlement_id, eligibility_id))
		SELECT k.n, u.at, u.tracked, g.by, coalesce(me.attributes ->> 'department', ''), g.peers, g.holders
		FROM k
		JOIN identities me ON me.id = k.identity_id
		CROSS JOIN LATERAL (`+useSQL("k.identity_id", role, ent, `nullif(k.eligibility_id, '')::uuid`)+`) u
		CROSS JOIN LATERAL (
			SELECT CASE WHEN me.kind <> 'human' THEN ''
			            WHEN coalesce(me.attributes ->> 'department', '') <> '' THEN 'department'
			            WHEN me.manager_id IS NOT NULL THEN 'team' ELSE '' END AS by,
			       count(p.id) AS peers, count(p.id) FILTER (WHERE `+holds+`) AS holders
			FROM (SELECT q.id FROM identities q
			      WHERE me.kind = 'human' AND coalesce(me.attributes ->> 'department', '') <> '' AND q.status = 'active' AND q.kind = 'human'
			        AND q.attributes ->> 'department' = me.attributes ->> 'department'
			      UNION ALL
			      SELECT q.id FROM identities q
			      WHERE me.kind = 'human' AND coalesce(me.attributes ->> 'department', '') = '' AND q.status = 'active' AND q.kind = 'human'
			        AND q.manager_id = me.manager_id) p
			WHERE p.id <> me.id
		) g`, n, ids, roles, ents, els)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	for rows.Next() {
		var (
			i int
			s Suggestion
		)
		if err := rows.Scan(&i, &s.LastUsedAt, &s.Tracked, &s.PeerGroup, &s.Department, &s.Peers, &s.PeerHolders); err != nil {
			return nil, err
		}
		if s.PeerGroup != "department" {
			s.Department = ""
		}
		s.decide(l, items[i].GrantedAt, now)
		out[i] = s
	}
	return out, rows.Err()
}

// decide works out the signals and the action from what is known.
func (s *Suggestion) decide(l limits, grantedAt, now time.Time) {
	s.Signals = []string{}
	usedLately := s.LastUsedAt != nil && !s.LastUsedAt.Before(l.unusedSince(now))
	switch {
	case l.unused(s.Use, grantedAt, now):
		s.Signals = append(s.Signals, SignalUnused)
	case usedLately:
		s.Signals = append(s.Signals, SignalUsed)
	}
	switch {
	case s.Peers >= l.commonMinPeers && s.PeerHolders*2 >= s.Peers:
		s.Signals = append(s.Signals, SignalCommon)
	case s.Peers >= l.rareMinPeers && s.PeerHolders*10 <= s.Peers:
		s.Signals = append(s.Signals, SignalRare)
	}
	// Use says more than habits: access nobody uses can go however common
	// it is, and access in use stays however rare.
	has := func(sig string) bool { return slices.Contains(s.Signals, sig) }
	switch {
	case has(SignalUnused):
		s.Action = "revoke"
	case has(SignalUsed), has(SignalCommon):
		s.Action = "keep"
	case has(SignalRare):
		s.Action = "revoke"
	}
}
