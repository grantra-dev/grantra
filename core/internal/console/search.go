package console

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
)

// searchHit is one thing the search box found.
type searchHit struct {
	// Kind is person, role, entitlement, application, vault_account,
	// server or remote_target. The console picks the page from it.
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Title string `json:"title"`
	// Detail tells similar hits apart: an email, an application, a kind.
	Detail string `json:"detail,omitempty"`
	// ApplicationID is the application an entitlement belongs to.
	ApplicationID string `json:"application_id,omitempty"`
}

// searchPerKind is how many hits of each kind a search returns.
const searchPerKind = 5

// likeContains is an SQL LIKE pattern matching any text that contains s,
// whatever its case once both sides are lowercased.
func likeContains(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(strings.ToLower(s)) + "%"
}

// search finds people, access, applications and privileged targets whose
// name contains ?q=, at least 2 characters. Each kind only appears when the
// person may see it on its own page, and holds what that page would show
// them, so the box never reveals more than the console already does.
func (a *API) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if n := utf8.RuneCountInString(q); n < 2 || n > 100 {
		httpjson.Error(w, http.StatusBadRequest, "q must be between 2 and 100 characters")
		return
	}
	pattern := likeContains(q)
	out := []searchHit{}
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		ctx := r.Context()
		may := func(action authz.Action, res authz.Resource) bool {
			return a.authz.Authorize(authz.Request{Principal: me, Action: action, Resource: res}).Allowed
		}
		find := func(sql string, args ...any) error {
			rows, err := tx.Query(ctx, sql, args...)
			if err != nil {
				return err
			}
			hits, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (searchHit, error) {
				var h searchHit
				err := row.Scan(&h.Kind, &h.ID, &h.Title, &h.Detail, &h.ApplicationID)
				return h, err
			})
			out = append(out, hits...)
			return err
		}

		if may(authz.ListIdentities, authz.DirectoryResource()) {
			// Whoever is still here first, then by name.
			if err := find(
				`SELECT 'person', id::text, display_name, coalesce(email, ''), '' FROM identities
				 WHERE lower(display_name) LIKE $1 OR lower(email) LIKE $1
				 ORDER BY status = 'terminated', display_name, id LIMIT $2`, pattern, searchPerKind); err != nil {
				return err
			}
		}
		// Everyone may see what they could ask for: the Request access list.
		if may(authz.ReadRequestable, authz.CatalogResource()) {
			if err := find(
				`(SELECT 'role', id::text, name, 'Business role', '' FROM roles
				  WHERE lower(name) LIKE $1 OR lower(coalesce(description, '')) LIKE $1
				  ORDER BY name, id LIMIT $3)
				 UNION ALL
				 (SELECT 'entitlement', e.id::text, e.name, a.name, a.id::text
				  FROM entitlements e JOIN applications a ON a.id = e.application_id
				  WHERE a.connector <> $2 AND e.missing_since IS NULL
				    AND (lower(e.name) LIKE $1 OR lower(coalesce(e.description, '')) LIKE $1)
				  ORDER BY e.name, e.id LIMIT $3)`, pattern, authz.BuiltinConnector, searchPerKind); err != nil {
				return err
			}
		}
		if may(authz.ReadCatalog, authz.CatalogResource()) {
			if err := find(
				`SELECT 'application', id::text, name, '', '' FROM applications
				 WHERE connector <> $2 AND lower(name) LIKE $1 ORDER BY name, id LIMIT $3`,
				pattern, authz.BuiltinConnector, searchPerKind); err != nil {
				return err
			}
		}

		on, err := moduleOn(ctx, tx, ModulePAM)
		if err != nil || !on {
			return err
		}
		// Admins and auditors see every vault account, server and target;
		// everyone else the ones they hold access to, as on those pages.
		holder := me.ID
		if may(authz.ReadVault, authz.VaultResource()) {
			holder = ""
		}
		held := func(entitlement string) string { return catalog.HeldSQL(`nullif($2, '')::uuid`, entitlement) }
		return find(
			`(SELECT 'vault_account', va.id::text, va.name, va.username, '' FROM vault_accounts va
			  WHERE (lower(va.name) LIKE $1 OR lower(va.username) LIKE $1)
			    AND ($2 = '' OR `+held("va.entitlement_id")+`
			         OR EXISTS (SELECT 1 FROM vault_break_glass_responders br WHERE br.vault_account_id = va.id AND br.identity_id = nullif($2, '')::uuid))
			  ORDER BY va.name, va.id LIMIT $3)
			 UNION ALL
			 (SELECT 'server', t.id::text, t.name, t.login || ' on ' || t.address, '' FROM ssh_targets t
			  WHERE lower(t.name) LIKE $1 AND ($2 = '' OR `+held("t.entitlement_id")+`)
			  ORDER BY t.name, t.id LIMIT $3)
			 UNION ALL
			 (SELECT 'remote_target', t.id::text, t.name, t.kind, '' FROM gateway_targets t
			  WHERE lower(t.name) LIKE $1 AND ($2 = '' OR `+held("t.entitlement_id")+`)
			  ORDER BY t.name, t.id LIMIT $3)`, pattern, holder, searchPerKind)
	})
	a.respond(w, r, http.StatusOK, map[string]any{"results": out}, err)
}
