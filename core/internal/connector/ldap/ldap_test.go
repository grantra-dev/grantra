package ldap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/connector"
)

func secrets(ref string) (string, error) {
	if ref == "env:GRANTRA_SECRET_AD" {
		return "s3cret", nil
	}
	return "", errors.New("no such secret")
}

func newClient(t *testing.T, cfg map[string]any) *Client {
	t.Helper()
	raw, _ := json.Marshal(cfg)
	r, err := connector.New(Kind, raw, secrets)
	if err != nil {
		t.Fatal(err)
	}
	return r.(*Client)
}

// guid builds a raw objectGUID whose text form ends in n.
func guid(n int) string {
	b, err := parseGUID(fmt.Sprintf("6f9619ff-8b86-d011-b42d-00c04fc9%04x", n))
	if err != nil {
		panic(err)
	}
	return string(b)
}

func guidText(n int) string { return fmt.Sprintf("6f9619ff-8b86-d011-b42d-00c04fc9%04x", n) }

const base = "DC=example,DC=com"

// adDir is an Active Directory with three people, a computer and three
// groups, one of them nested in another.
func adDir(t *testing.T) *fakeDir {
	d := &fakeDir{bindDN: "CN=grantra,OU=Service,DC=example,DC=com", password: "s3cret", maxRange: 2}
	user := func(n int, cn, sam, uac string, extra map[string][]string) {
		a := map[string][]string{
			"objectClass": {"top", "person", "user"}, "objectCategory": {"person"},
			"objectGUID": {guid(n)}, "cn": {cn}, "sAMAccountName": {sam}, "displayName": {cn},
			"userAccountControl": {uac}, "userPrincipalName": {sam + "@corp.example.com"},
		}
		for k, v := range extra {
			a[k] = v
		}
		d.add("CN="+cn+",OU=People,"+base, a)
	}
	user(1, "Ana García", "agarcia", "512", map[string][]string{"mail": {"ana@example.com"}, "employeeID": {"1001"}, "title": {"CEO"}, "department": {"Executive"}})
	user(2, "Luis Pérez", "lperez", "66048", map[string][]string{"mail": {"luis@example.com"}})
	user(3, "Marta Ruiz", "mruiz", "514", nil) // disabled
	d.add("CN=LAPTOP1,OU=Computers,"+base, map[string][]string{"objectClass": {"computer", "user"}, "objectCategory": {"computer"}, "objectGUID": {guid(9)}})
	group := func(n int, cn string, members ...string) {
		d.add("CN="+cn+",OU=Groups,"+base, map[string][]string{"objectClass": {"top", "group"}, "objectGUID": {guid(n)}, "cn": {cn}, "description": {cn + " group"}, "member": members})
	}
	people := "OU=People," + base
	// More members than maxRange: read in ranges. Different case and spacing
	// in the DNs must still match.
	group(100, "Finance", "cn=ana garcía,ou=people,dc=example,dc=com", "CN=Luis Pérez, OU=People, DC=example, DC=com",
		"CN=Marta Ruiz,"+people, "CN=LAPTOP1,OU=Computers,"+base, "CN=Gone,"+people)
	group(101, "Admins", "CN=Finance,OU=Groups,"+base)
	group(102, "Empty")
	d.start(t)
	return d
}

func adConfig(d *fakeDir) map[string]any {
	return map[string]any{"url": d.url, "allow_insecure": true, "bind_dn": d.bindDN, "password": "env:GRANTRA_SECRET_AD", "base_dn": base, "page_size": 2}
}

func TestReadActiveDirectory(t *testing.T) {
	d := adDir(t)
	snap, err := newClient(t, adConfig(d)).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.pages < 4 {
		t.Errorf("read in %d pages, want paging", d.pages)
	}
	var names []string
	for _, e := range snap.Entitlements {
		names = append(names, e.Name+"="+e.NativeID)
	}
	slices.Sort(names)
	want := []string{"Admins=" + guidText(101), "Empty=" + guidText(102), "Finance=" + guidText(100)}
	if !slices.Equal(names, want) {
		t.Fatalf("entitlements %v, want %v", names, want)
	}
	if len(snap.Accounts) != 3 {
		t.Fatalf("got %d accounts, want 3 (no computers)", len(snap.Accounts))
	}
	got := map[string]connector.Account{}
	for _, a := range snap.Accounts {
		got[a.Username] = a
	}
	ana := got["agarcia"]
	if ana.NativeID != guidText(1) || ana.Email != "ana@example.com" || ana.ExternalID != "1001" || ana.DisplayName != "Ana García" || !ana.Active {
		t.Errorf("ana = %+v", ana)
	}
	if ana.Attributes["dn"] != "CN=Ana García,OU=People,"+base || ana.Attributes["upn"] != "agarcia@corp.example.com" || ana.Attributes["title"] != "CEO" || ana.Attributes["department"] != "Executive" {
		t.Errorf("ana attributes = %v", ana.Attributes)
	}
	if !got["lperez"].Active || got["mruiz"].Active {
		t.Errorf("active: luis %t (want true), marta %t (want false)", got["lperez"].Active, got["mruiz"].Active)
	}
	for _, u := range []string{"agarcia", "lperez", "mruiz"} {
		if !slices.Equal(got[u].Entitlements, []string{guidText(100)}) {
			t.Errorf("%s holds %v, want only Finance (nested groups are not expanded)", u, got[u].Entitlements)
		}
	}
}

func TestReadFailsWhole(t *testing.T) {
	d := adDir(t)
	cfg := adConfig(d)
	d.password = "changed"
	if _, err := newClient(t, cfg).Read(context.Background()); err == nil || !strings.Contains(err.Error(), "Invalid Credentials") {
		t.Fatalf("err = %v, want a bind failure", err)
	}
	d.password = "s3cret"
	cfg["group_filter"] = "(objectClass=group)"
	cfg["attributes"] = map[string]string{"id": "entryUUID"}
	if _, err := newClient(t, cfg).Read(context.Background()); err == nil || !strings.Contains(err.Error(), "has no entryUUID") {
		t.Fatalf("err = %v, want entries without an id to fail the read", err)
	}
}

func TestWriteActiveDirectory(t *testing.T) {
	d := adDir(t)
	c := newClient(t, adConfig(d))
	ctx := context.Background()
	var w connector.Writer = c
	if _, ok := w.(connector.AccountCreator); ok {
		t.Fatal("the ldap connector must not offer to create accounts")
	}
	finance, admins := "CN=Finance,OU=Groups,"+base, "CN=Admins,OU=Groups,"+base
	ana := "CN=Ana García,OU=People," + base

	if err := w.AddMember(ctx, guidText(101), guidText(1)); err != nil {
		t.Fatal(err)
	}
	if m := d.entry(admins).get("member").Values; !slices.Contains(m, ana) {
		t.Fatalf("admins = %v", m)
	}
	// Already a member (Finance has more members than one read returns).
	n := len(d.modifies)
	if err := w.AddMember(ctx, guidText(100), guidText(1)); err != nil || len(d.modifies) != n {
		t.Fatalf("repeated add: err %v, %d modifies", err, len(d.modifies)-n)
	}
	if err := w.RemoveMember(ctx, guidText(100), guidText(2)); err != nil {
		t.Fatal(err)
	}
	if m := d.entry(finance).get("member").Values; len(m) != 4 || slices.ContainsFunc(m, func(v string) bool { return strings.Contains(v, "Luis") }) {
		t.Fatalf("finance = %v", m)
	}
	n = len(d.modifies)
	if err := w.RemoveMember(ctx, guidText(100), guidText(2)); err != nil || len(d.modifies) != n {
		t.Fatalf("repeated remove: err %v, %d modifies", err, len(d.modifies)-n)
	}

	if err := w.SetActive(ctx, guidText(2), false); err != nil {
		t.Fatal(err)
	}
	if v := d.entry("CN=Luis Pérez,OU=People," + base).get("userAccountControl").Values; !slices.Equal(v, []string{"66050"}) {
		t.Fatalf("luis userAccountControl = %v, want the disable flag added and the rest kept", v)
	}
	n = len(d.modifies)
	if err := w.SetActive(ctx, guidText(2), false); err != nil || len(d.modifies) != n {
		t.Fatalf("repeated disable: err %v, %d modifies", err, len(d.modifies)-n)
	}
	if err := w.SetActive(ctx, guidText(3), true); err != nil {
		t.Fatal(err)
	}
	if v := d.entry("CN=Marta Ruiz,OU=People," + base).get("userAccountControl").Values; !slices.Equal(v, []string{"512"}) {
		t.Fatalf("marta userAccountControl = %v", v)
	}

	if err := w.AddMember(ctx, guidText(101), guidText(77)); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown account: err = %v", err)
	}
	if err := w.SetActive(ctx, "not-a-guid", true); err == nil {
		t.Fatal("a malformed id must fail")
	}
}

func TestOpenLDAP(t *testing.T) {
	const base = "dc=example,dc=org"
	d := &fakeDir{bindDN: "cn=admin," + base, password: "s3cret"}
	d.add("uid=ana,ou=people,"+base, map[string][]string{"objectClass": {"inetOrgPerson"}, "entryUUID": {"u-ana"}, "uid": {"ana"}, "cn": {"Ana García"}, "mail": {"ana@example.org"}, "employeeNumber": {"1001"}})
	d.add("uid=luis,ou=people,"+base, map[string][]string{"objectClass": {"inetOrgPerson"}, "entryUUID": {"u-luis"}, "uid": {"luis"}, "cn": {"Luis"}, "pwdAccountLockedTime": {lockedForever}})
	d.add("uid=sara,ou=people,"+base, map[string][]string{"objectClass": {"inetOrgPerson"}, "entryUUID": {"u-sara"}, "uid": {"sara"}, "cn": {"Sara"}, "pwdAccountLockedTime": {"20261014101500Z"}})
	d.add("cn=devs,ou=groups,"+base, map[string][]string{"objectClass": {"groupOfNames"}, "entryUUID": {"g-devs"}, "cn": {"devs"}, "member": {"uid=ana,ou=people," + base}})
	d.start(t)
	c := newClient(t, map[string]any{"url": d.url, "allow_insecure": true, "directory": "openldap", "bind_dn": d.bindDN, "password": "env:GRANTRA_SECRET_AD", "base_dn": base})
	ctx := context.Background()

	snap, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]connector.Account{}
	for _, a := range snap.Accounts {
		got[a.NativeID] = a
	}
	if a := got["u-ana"]; a.Username != "ana" || a.Email != "ana@example.org" || a.ExternalID != "1001" || !a.Active || !slices.Equal(a.Entitlements, []string{"g-devs"}) {
		t.Errorf("ana = %+v", a)
	}
	if got["u-luis"].Active || !got["u-sara"].Active {
		t.Errorf("luis is locked for good (inactive), sara only after failed logins (active): %t %t", got["u-luis"].Active, got["u-sara"].Active)
	}

	if err := c.SetActive(ctx, "u-ana", false); err != nil {
		t.Fatal(err)
	}
	if v := d.entry("uid=ana,ou=people," + base).get("pwdAccountLockedTime"); v == nil || v.Values[0] != lockedForever {
		t.Fatalf("ana pwdAccountLockedTime = %v", v)
	}
	if err := c.SetActive(ctx, "u-luis", true); err != nil {
		t.Fatal(err)
	}
	if d.entry("uid=luis,ou=people,"+base).get("pwdAccountLockedTime") != nil {
		t.Fatal("luis is still locked")
	}
	if err := c.SetActive(ctx, "u-luis", true); err != nil {
		t.Fatalf("repeated enable: %v", err)
	}
	if err := c.AddMember(ctx, "g-devs", "u-luis"); err != nil {
		t.Fatal(err)
	}
	if m := d.entry("cn=devs,ou=groups," + base).get("member").Values; len(m) != 2 {
		t.Fatalf("devs = %v", m)
	}
}

func TestConfig(t *testing.T) {
	ok := map[string]any{"url": "ldaps://dc1.example.com", "bind_dn": "CN=grantra,DC=example,DC=com", "password": "env:GRANTRA_SECRET_AD", "base_dn": base}
	if _, err := json.Marshal(ok); err != nil {
		t.Fatal(err)
	}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for k, v := range ok {
			m[k] = v
		}
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	for _, tc := range []struct {
		cfg  map[string]any
		want string
	}{
		{with("url", "ldap://dc1.example.com"), "start_tls"},
		{with("url", "https://dc1.example.com"), "url must look like"},
		{with("url", "ldaps://dc1.example.com/dc=x"), "url must look like"},
		{with("start_tls", true), "already TLS"},
		{with("password", nil), "password are required"},
		{with("password", "env:GRANTRA_SECRET_NOPE"), "no such secret"},
		{with("base_dn", nil), "base_dn is required"},
		{with("bind_dn", "not a dn"), "bind_dn"},
		{with("directory", "novell"), "directory must be"},
		{with("user_filter", "(objectClass=user"), "user_filter"},
		{with("page_size", 5000), "page_size"},
		{with("ca_cert", "env:GRANTRA_SECRET_AD"), "no PEM certificate"},
		{with("tokn", "x"), "unknown field"},
	} {
		raw, _ := json.Marshal(tc.cfg)
		if _, err := New(raw, secrets); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.cfg, err, tc.want)
		}
	}
	raw, _ := json.Marshal(with("url", "ldap://dc1.example.com:389"))
	raw = []byte(strings.Replace(string(raw), "{", `{"start_tls": true,`, 1))
	if _, err := New(raw, secrets); err != nil {
		t.Errorf("ldap:// with start_tls: %v", err)
	}
	raw, _ = json.Marshal(ok)
	if _, err := New(raw, secrets); err != nil {
		t.Errorf("ldaps: %v", err)
	}
}

func TestGUID(t *testing.T) {
	// The example from Microsoft's documentation of objectGUID.
	raw := []byte{0xff, 0x19, 0x96, 0x6f, 0x86, 0x8b, 0x11, 0xd0, 0xb4, 0x2d, 0x00, 0xc0, 0x4f, 0xc9, 0x64, 0xff}
	s := formatGUID(raw)
	if s != "6f9619ff-8b86-d011-b42d-00c04fc964ff" {
		t.Fatalf("formatGUID = %s", s)
	}
	back, err := parseGUID(s)
	if err != nil || string(back) != string(raw) {
		t.Fatalf("parseGUID = %x, %v", back, err)
	}
	if _, err := parseGUID("6f9619ff8b86d011b42d00c04fc964ff"); err == nil {
		t.Error("a GUID without dashes must fail")
	}
}

func TestSetPasswordActiveDirectory(t *testing.T) {
	d := adDir(t)
	c := newClient(t, adConfig(d))
	if !connector.SetsPasswords(Kind) {
		t.Fatal("the ldap connector must say it sets passwords")
	}
	if err := c.SetPassword(context.Background(), guidText(1), "Nü-pass1"); err != nil {
		t.Fatal(err)
	}
	got := d.entry("CN=Ana García,OU=People," + base).get("unicodePwd").Values
	// "Nü-pass1" in quotes, UTF-16 little endian.
	want := string([]byte{'"', 0, 'N', 0, 0xfc, 0, '-', 0, 'p', 0, 'a', 0, 's', 0, 's', 0, '1', 0, '"', 0})
	if len(got) != 1 || got[0] != want {
		t.Fatalf("unicodePwd = %q, want %q", got, want)
	}
	if err := c.SetPassword(context.Background(), guidText(77), "x"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown account: err = %v", err)
	}
}

func TestSetPasswordOpenLDAP(t *testing.T) {
	const base = "dc=example,dc=org"
	d := &fakeDir{bindDN: "cn=admin," + base, password: "s3cret"}
	d.add("uid=ana,ou=people,"+base, map[string][]string{"objectClass": {"inetOrgPerson"}, "entryUUID": {"u-ana"}, "uid": {"ana"}, "cn": {"Ana"}})
	d.start(t)
	c := newClient(t, map[string]any{"url": d.url, "allow_insecure": true, "directory": "openldap", "bind_dn": d.bindDN, "password": "env:GRANTRA_SECRET_AD", "base_dn": base})
	if err := c.SetPassword(context.Background(), "u-ana", "n3w-Secret"); err != nil {
		t.Fatal(err)
	}
	if v := d.entry("uid=ana,ou=people," + base).get("userPassword"); v == nil || v.Values[0] != "n3w-Secret" {
		t.Fatalf("userPassword = %v, want it set through the Password Modify operation", v)
	}
	if !slices.Contains(d.modifies, "uid=ana,ou=people,"+base+" userPassword passwd-modify") {
		t.Fatalf("modifies = %v, want a Password Modify extended operation", d.modifies)
	}
}
