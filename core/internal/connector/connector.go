// Package connector is how Grantra talks to the applications it governs.
//
// A connector reads what exists in an application (aggregation): its
// entitlements (groups, roles, licenses) and its accounts with the
// entitlements each one holds. Connectors that implement Writer can also
// change the application (provisioning).
//
// Connector kinds register a Factory under their name; an application's
// connector column names the kind and its config column holds the settings.
// Secrets never live in the config: it holds references such as
// "env:GRANTRA_SECRET_CRM_SCIM_TOKEN" that a SecretResolver turns into values.
package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// Entitlement is a permission as the application describes it.
type Entitlement struct {
	NativeID    string
	Name        string
	Description string
}

// Account is a login as the application describes it.
type Account struct {
	NativeID    string
	Username    string
	Email       string
	DisplayName string
	// ExternalID is the id the application keeps for the person in the
	// company's records, often the HR employee id.
	ExternalID string
	Active     bool
	// Entitlements are the native ids of the entitlements the account holds.
	Entitlements []string
	Attributes   map[string]string
}

// Snapshot is everything an aggregation read.
type Snapshot struct {
	Entitlements []Entitlement
	Accounts     []Account
}

// Reader reads an application.
type Reader interface {
	// Read returns every entitlement and account. It must return an error
	// rather than a partial snapshot: a missing page would look like deleted
	// accounts.
	Read(ctx context.Context) (Snapshot, error)
}

// NewAccount describes an account to create.
type NewAccount struct {
	Username    string
	Email       string
	DisplayName string
	ExternalID  string
}

// Writer changes an application. Connectors that can provision implement it
// on top of Reader. Its methods must be safe to repeat: adding a member twice
// or disabling a disabled account is not an error.
type Writer interface {
	// SetActive enables or disables an account.
	SetActive(ctx context.Context, accountID string, active bool) error
	// AddMember and RemoveMember change who holds an entitlement.
	AddMember(ctx context.Context, entitlementID, accountID string) error
	RemoveMember(ctx context.Context, entitlementID, accountID string) error
}

// AccountCreator is a Writer that can also create accounts. Connectors that
// cannot (a directory where accounts are made by another process, say) leave
// it out, and provisioning only works with the accounts already there.
type AccountCreator interface {
	Writer
	// CreateAccount creates an active account and returns its native id.
	CreateAccount(ctx context.Context, a NewAccount) (string, error)
}

// PasswordSetter is a connector that can set an account's password, which
// the vault uses to rotate the passwords of privileged accounts. Kinds that
// implement it also call RegisterPasswordSetter, so Grantra can tell which
// applications support rotation without connecting to them.
type PasswordSetter interface {
	// SetPassword replaces the account's password. Setting the same password
	// again is not an error.
	SetPassword(ctx context.Context, accountID, password string) error
}

// SecretResolver turns a secret reference from a connector config into its
// value.
type SecretResolver func(ref string) (string, error)

// SecretEnvPrefix is the prefix every environment variable holding a
// connector secret must carry. Connector configs are written by Grantra
// admins, who are not necessarily the people running the server: without the
// prefix, a config could name GRANTRA_DATABASE_URL or the OIDC client secret
// as its token and send it to a server of its choosing.
const SecretEnvPrefix = "GRANTRA_SECRET_"

// EnvSecrets resolves "env:NAME" references from environment variables, which
// is how a self-hosted install passes connector credentials. Only variables
// named with SecretEnvPrefix can be referenced.
func EnvSecrets(ref string) (string, error) {
	name, ok := strings.CutPrefix(ref, "env:")
	if !ok || name == "" {
		return "", fmt.Errorf(`secret %q must be a reference like "env:%sNAME"`, ref, SecretEnvPrefix)
	}
	if !strings.HasPrefix(name, SecretEnvPrefix) || name == SecretEnvPrefix {
		return "", fmt.Errorf("secret %q: only environment variables starting with %s can hold connector secrets", ref, SecretEnvPrefix)
	}
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("environment variable %s is not set", name)
	}
	return v, nil
}

// Factory builds a connector from its config. It must validate the config
// and fail on anything unknown, so typos surface when the connector is set up.
type Factory func(config json.RawMessage, secrets SecretResolver) (Reader, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
	passwords = map[string]bool{}
)

// RegisterPasswordSetter records that connectors of kind implement
// PasswordSetter.
func RegisterPasswordSetter(kind string) {
	mu.Lock()
	defer mu.Unlock()
	passwords[kind] = true
}

// SetsPasswords reports whether connectors of kind can set passwords.
func SetsPasswords(kind string) bool {
	mu.RLock()
	defer mu.RUnlock()
	return passwords[kind]
}

// Register makes a connector kind available. It panics on a duplicate name.
func Register(kind string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[kind]; dup {
		panic("connector " + kind + " registered twice")
	}
	factories[kind] = f
}

// ErrUnknownKind is returned for a connector kind nobody registered.
var ErrUnknownKind = errors.New("unknown connector")

// New builds a connector of the given kind.
func New(kind string, config json.RawMessage, secrets SecretResolver) (Reader, error) {
	mu.RLock()
	f, ok := factories[kind]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w %q (available: %s)", ErrUnknownKind, kind, strings.Join(Kinds(), ", "))
	}
	return f(config, secrets)
}

// Kinds lists the registered connector kinds.
func Kinds() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(factories))
	for k := range factories {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
