package grantralogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/grantra-dev/grantra/core/internal/textutil"
)

// Import brings users, groups, roles and apps over from an existing
// Keycloak, from a realm export such as
//
//	kc.sh export --realm acme --users realm_file --file acme.json
//
// People keep their passwords and second factors: the export carries their
// hashes. Nothing that exists already is changed.

// ImportSet is what an import adds, and what it leaves alone because
// Grantra Login already has it.
type ImportSet struct {
	Add  []string `json:"add"`
	Skip []string `json:"skip"`
}

// ImportPlan is what importing an export does.
type ImportPlan struct {
	// Realm is the exported realm's name.
	Realm  string    `json:"realm"`
	Users  ImportSet `json:"users"`
	Groups ImportSet `json:"groups"`
	Roles  ImportSet `json:"roles"`
	Apps   ImportSet `json:"apps"`
	// Notes are what the import leaves out, and why.
	Notes []string `json:"notes"`
	// Applied is true once the import ran.
	Applied bool `json:"applied"`
}

type object = map[string]any

// export is a realm export cut down to what Grantra imports.
type export struct {
	realm   string
	users   []object
	groups  []object
	roles   []object
	clients []object
	// clientRoles are the roles of the clients imported, by client id.
	clientRoles object
	notes       []string
}

// builtinRoles are realm roles every realm has.
func builtinRole(name string) bool {
	return name == "offline_access" || name == "uma_authorization" || name == MFARole || strings.HasPrefix(name, "default-roles-")
}

func str(o object, k string) string {
	s, _ := o[k].(string)
	return s
}

func objects(v any) []object {
	a, _ := v.([]any)
	out := make([]object, 0, len(a))
	for _, x := range a {
		if o, ok := x.(object); ok {
			out = append(out, o)
		}
	}
	return out
}

// dropIDs removes the source realm's ids, which mean nothing here, from an
// object and the objects inside it.
func dropIDs(v any) {
	switch x := v.(type) {
	case object:
		delete(x, "id")
		delete(x, "containerId")
		for _, y := range x {
			dropIDs(y)
		}
	case []any:
		for _, y := range x {
			dropIDs(y)
		}
	}
}

// parseExport reads a realm export and leaves out what does not belong in
// Grantra Login: Keycloak's own clients and roles, service accounts, and
// links to the old realm's identity providers and user federation. realm is
// the realm imported into.
func parseExport(data []byte, realm string) (export, error) {
	data = bytes.TrimSpace(data)
	var r object
	if len(data) > 0 && data[0] == '[' {
		var all []object
		if err := json.Unmarshal(data, &all); err != nil {
			return export{}, invalid("the file is not a Keycloak realm export: %v", err)
		}
		if len(all) != 1 {
			return export{}, invalid("the file holds %d realms: export one with kc.sh export --realm", len(all))
		}
		r = all[0]
	} else if err := json.Unmarshal(data, &r); err != nil {
		return export{}, invalid("the file is not a Keycloak realm export: %v", err)
	}
	ex := export{realm: str(r, "realm"), clientRoles: object{}}
	if ex.realm == "" {
		return export{}, invalid("the file is not a Keycloak realm export: it has no realm name")
	}
	dropIDs(r)

	var system []string
	for _, cl := range objects(r["clients"]) {
		id := str(cl, "clientId")
		if systemClients[id] || id == ex.realm+"-realm" {
			system = append(system, id)
			continue
		}
		// Client scopes are not imported: the realm's own default and
		// optional scopes apply.
		delete(cl, "defaultClientScopes")
		delete(cl, "optionalClientScopes")
		ex.clients = append(ex.clients, cl)
	}
	if roles, ok := r["roles"].(object); ok {
		for _, role := range objects(roles["realm"]) {
			if !builtinRole(str(role, "name")) {
				ex.roles = append(ex.roles, role)
			}
		}
		if cr, ok := roles["client"].(object); ok {
			for id, rs := range cr {
				if slices.ContainsFunc(ex.clients, func(cl object) bool { return str(cl, "clientId") == id }) {
					ex.clientRoles[id] = rs
				}
			}
		}
	}
	ex.groups = objects(r["groups"])

	var services, federated, linked int
	for _, u := range objects(r["users"]) {
		if str(u, "serviceAccountClientId") != "" {
			services++
			continue
		}
		if _, ok := u["federationLink"]; ok {
			// The old realm's LDAP or other user federation is not here.
			delete(u, "federationLink")
			federated++
		}
		if _, ok := u["federatedIdentities"]; ok {
			delete(u, "federatedIdentities")
			linked++
		}
		if rs, ok := u["realmRoles"].([]any); ok {
			keep := []any{}
			for _, x := range rs {
				switch name, _ := x.(string); {
				case name == "default-roles-"+ex.realm:
					// Everyone's default roles are the ones of this realm.
					keep = append(keep, "default-roles-"+realm)
				case !builtinRole(name):
					keep = append(keep, x)
				}
			}
			u["realmRoles"] = keep
		}
		if cr, ok := u["clientRoles"].(object); ok {
			for id := range cr {
				if _, ok := ex.clientRoles[id]; !ok {
					delete(cr, id)
				}
			}
		}
		ex.users = append(ex.users, u)
	}
	if len(system) > 0 {
		ex.notes = append(ex.notes, textutil.Plural(len(system), "Keycloak client", "Keycloak clients")+" left out ("+strings.Join(system, ", ")+"): Grantra Login has its own.")
	}
	if services > 0 {
		ex.notes = append(ex.notes, textutil.Plural(services, "service account", "service accounts")+" left out: they belong to the clients that own them.")
	}
	if federated > 0 {
		ex.notes = append(ex.notes, textutil.Plural(federated, "user comes", "users come")+" from the old realm's user federation (such as LDAP): imported without it, they need a password reset unless you connect the directory here too.")
	}
	if linked > 0 {
		ex.notes = append(ex.notes, textutil.Plural(linked, "user's link", "users' links")+" to the old realm's identity providers left out: they link again by email when they sign in with a provider added here.")
	}
	if len(ex.users) == 0 {
		ex.notes = append(ex.notes, "The export has no users: export them with kc.sh export --users realm_file.")
	}
	return ex, nil
}

func (ex export) names(items []object, key string) []string {
	out := make([]string, 0, len(items))
	for _, o := range items {
		out = append(out, str(o, key))
	}
	return out
}

func split(names []string, have map[string]bool) ImportSet {
	s := ImportSet{Add: []string{}, Skip: []string{}}
	for _, n := range names {
		if have[n] {
			s.Skip = append(s.Skip, n)
		} else {
			s.Add = append(s.Add, n)
		}
	}
	sort.Strings(s.Add)
	sort.Strings(s.Skip)
	return s
}

// Import plans importing a realm export and, unless dryRun, runs it with
// Keycloak's partial import, which skips whatever exists already.
func (c *Client) Import(ctx context.Context, data []byte, dryRun bool) (ImportPlan, error) {
	ex, err := parseExport(data, c.realm)
	if err != nil {
		return ImportPlan{}, err
	}
	have := func(path, key string) (map[string]bool, error) {
		m := map[string]bool{}
		err := list(ctx, c, path, url.Values{"briefRepresentation": {"true"}}, func(o object) {
			m[str(o, key)] = true
		})
		return m, err
	}
	users, err := have("/users", "username")
	if err != nil {
		return ImportPlan{}, err
	}
	groups, err := have("/groups", "name")
	if err != nil {
		return ImportPlan{}, err
	}
	clients, err := have("/clients", "clientId")
	if err != nil {
		return ImportPlan{}, err
	}
	var rs []object
	if _, err := c.do(ctx, http.MethodGet, "/roles?briefRepresentation=true&max=100000", nil, &rs); err != nil {
		return ImportPlan{}, err
	}
	roles := map[string]bool{}
	for _, r := range rs {
		roles[str(r, "name")] = true
	}
	// Keycloak matches usernames in lowercase.
	var usernames []string
	for _, n := range ex.names(ex.users, "username") {
		usernames = append(usernames, strings.ToLower(n))
	}
	plan := ImportPlan{
		Realm:  ex.realm,
		Users:  split(usernames, users),
		Groups: split(ex.names(ex.groups, "name"), groups),
		Roles:  split(ex.names(ex.roles, "name"), roles),
		Apps:   split(ex.names(ex.clients, "clientId"), clients),
		Notes:  append([]string{}, ex.notes...),
	}
	if dryRun {
		return plan, nil
	}
	body := object{
		"ifResourceExists": "SKIP",
		"users":            ex.users,
		"groups":           ex.groups,
		"clients":          ex.clients,
		"roles":            object{"realm": ex.roles, "client": ex.clientRoles},
	}
	if _, err := c.do(ctx, http.MethodPost, "/partialImport", body, nil); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && (ae.status == http.StatusBadRequest || ae.status == http.StatusConflict) {
			return ImportPlan{}, invalid("Grantra Login refused the import: %s", ae.msg)
		}
		return ImportPlan{}, err
	}
	plan.Applied = true
	return plan, nil
}
