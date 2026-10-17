package servicetoken

import "testing"

func TestAllows(t *testing.T) {
	tok := Token{ID: "t1", Scopes: []string{ScopeSyncSource + ":s1", ScopeImportAccounts}}
	for _, tt := range []struct {
		scope, target string
		want          bool
	}{
		{ScopeSyncSource, "s1", true},
		{ScopeSyncSource, "s2", false},
		{ScopeSyncSource, "", false},
		{ScopeImportAccounts, "any-app", true},
		{"identity_source", "sync:s1", false},
	} {
		if got := tok.Allows(tt.scope, tt.target); got != tt.want {
			t.Errorf("Allows(%q, %q) = %v, want %v", tt.scope, tt.target, got, tt.want)
		}
	}
	if tok.Actor() != "service_token:t1" {
		t.Errorf("actor = %q", tok.Actor())
	}
}
