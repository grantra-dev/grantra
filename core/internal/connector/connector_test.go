package connector

import "testing"

func TestEnvSecretsOnlyReadsPrefixedVariables(t *testing.T) {
	t.Setenv("GRANTRA_SECRET_CRM_TOKEN", "t0ken")
	t.Setenv("GRANTRA_DATABASE_URL", "postgres://grantra:hunter2@db/grantra")

	if v, err := EnvSecrets("env:GRANTRA_SECRET_CRM_TOKEN"); err != nil || v != "t0ken" {
		t.Fatalf("prefixed secret: got %q, %v", v, err)
	}
	for _, ref := range []string{
		"env:GRANTRA_DATABASE_URL", // the server's own settings stay out of reach
		"env:GRANTRA_SECRET_",
		"env:",
		"GRANTRA_SECRET_CRM_TOKEN",
		"env:GRANTRA_SECRET_UNSET",
	} {
		if v, err := EnvSecrets(ref); err == nil {
			t.Errorf("%q resolved to %q, want an error", ref, v)
		}
	}
}
