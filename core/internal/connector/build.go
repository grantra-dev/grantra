package connector

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DecodeConfig reads a connector config into cfg and fails on any field cfg
// does not have, so a typo surfaces when the connector is set up. Errors
// start with the kind.
func DecodeConfig(kind string, raw json.RawMessage, cfg any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("%s config: %w", kind, err)
	}
	return nil
}

// Secret resolves the secret reference a config holds under field, which is
// required. example is a reference to show when it is missing.
func Secret(kind, field, ref, example string, secrets SecretResolver) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("%s config: %s is required: type it in the console, or give a reference like %q", kind, field, example)
	}
	v, err := secrets(ref)
	if err != nil {
		return "", fmt.Errorf("%s config: %s: %w", kind, field, err)
	}
	return v, nil
}

// Builder puts a snapshot together for applications that list memberships
// apart from accounts (the members of each group, say). Grants are kept once
// however often they are seen, and grants to members that are not accounts
// (nested groups, devices, other directories' objects) are dropped.
type Builder struct {
	entitlements []Entitlement
	known        map[string]bool
	accounts     []Account
	grants       map[string][]string
	granted      map[[2]string]bool
}

// Entitlement adds an entitlement. A second one with the same native id is
// ignored.
func (b *Builder) Entitlement(e Entitlement) {
	if b.known == nil {
		b.known = map[string]bool{}
	}
	if b.known[e.NativeID] {
		return
	}
	b.known[e.NativeID] = true
	if e.Name == "" {
		e.Name = e.NativeID
	}
	b.entitlements = append(b.entitlements, e)
}

// Grant records that the account holds the entitlement.
func (b *Builder) Grant(accountID, entitlementID string) {
	if b.granted == nil {
		b.granted, b.grants = map[[2]string]bool{}, map[string][]string{}
	}
	k := [2]string{accountID, entitlementID}
	if b.granted[k] {
		return
	}
	b.granted[k] = true
	b.grants[accountID] = append(b.grants[accountID], entitlementID)
}

// Account adds an account. Its Entitlements are granted too.
func (b *Builder) Account(a Account) {
	for _, e := range a.Entitlements {
		b.Grant(a.NativeID, e)
	}
	a.Entitlements = nil
	b.accounts = append(b.accounts, a)
}

// Snapshot returns what was added, each account with every entitlement
// granted to it that was also added.
func (b *Builder) Snapshot() Snapshot {
	snap := Snapshot{Entitlements: b.entitlements, Accounts: b.accounts}
	for i := range snap.Accounts {
		a := &snap.Accounts[i]
		for _, e := range b.grants[a.NativeID] {
			if b.known[e] {
				a.Entitlements = append(a.Entitlements, e)
			}
		}
	}
	return snap
}

// Attributes makes an account's attributes from name, value pairs, leaving
// out empty values.
func Attributes(pairs ...string) map[string]string {
	out := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			out[pairs[i]] = pairs[i+1]
		}
	}
	return out
}
