// Package salesforce connects Grantra to a Salesforce org through its REST
// API. Profiles, permission sets and permission set groups become
// entitlements; users become accounts, with their last sign-in.
//
// Grantra signs in with the OAuth 2.0 client credentials flow of an
// external client app (or connected app), which runs as the user the app
// names.
package salesforce

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/httpapi"
)

// Kind is the connector name applications use.
const Kind = "salesforce"

func init() { connector.Register(Kind, New) }

// Config is the Salesforce connector's settings.
type Config struct {
	// InstanceURL is the org's My Domain, e.g. https://acme.my.salesforce.com.
	InstanceURL string `json:"instance_url"`
	// ClientID and ClientSecret are the app's consumer key and a reference
	// to its consumer secret.
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// APIVersion is the REST API version. Default v62.0.
	APIVersion string `json:"api_version,omitempty"`
	AllowHTTP  bool   `json:"allow_http,omitempty"`
}

// Client reads and changes one Salesforce org.
type Client struct {
	api *httpapi.Client
}

var versionPattern = regexp.MustCompile(`^v\d+\.\d$`)

// New builds a Salesforce connector from its config.
func New(raw json.RawMessage, secrets connector.SecretResolver) (connector.Reader, error) {
	var cfg Config
	if err := connector.DecodeConfig(Kind, raw, &cfg); err != nil {
		return nil, err
	}
	instance, err := httpapi.BaseURL(Kind, "instance_url", cfg.InstanceURL, cfg.AllowHTTP)
	if err != nil {
		return nil, err
	}
	if cfg.ClientID == "" {
		return nil, errors.New("salesforce config: client_id is required: the consumer key of the app Grantra signs in with")
	}
	secret, err := connector.Secret(Kind, "client_secret", cfg.ClientSecret, "env:GRANTRA_SECRET_SALESFORCE", secrets)
	if err != nil {
		return nil, err
	}
	version := cmp.Or(cfg.APIVersion, "v62.0")
	if !versionPattern.MatchString(version) {
		return nil, fmt.Errorf("salesforce config: api_version must look like v62.0, got %q", version)
	}
	cc := clientcredentials.Config{ClientID: cfg.ClientID, ClientSecret: secret, TokenURL: instance.String() + "/services/oauth2/token",
		AuthStyle: oauth2.AuthStyleInParams}
	return &Client{api: httpapi.NewOAuth2(httpapi.At(instance, "/services/data/"+version), &cc)}, nil
}

// Native id prefixes of what is not a permission set.
const (
	profilePrefix = "profile:"
	groupPrefix   = "psg:"
)

// query runs a SOQL query and hands every record to fn, following
// nextRecordsUrl.
func query[T any](ctx context.Context, c *Client, soql string, fn func(T)) error {
	return httpapi.Pages(ctx, c.api, "/query?q="+url.QueryEscape(soql), func(p *struct {
		Records        []T    `json:"records"`
		Done           bool   `json:"done"`
		NextRecordsURL string `json:"nextRecordsUrl"`
	}, _ http.Header) (string, error) {
		for _, r := range p.Records {
			fn(r)
		}
		if p.Done || p.NextRecordsURL == "" {
			return "", nil
		}
		// The link is a path from the instance, version included.
		_, rest, ok := strings.Cut(p.NextRecordsURL, "/query/")
		if !ok {
			return "", fmt.Errorf("unexpected nextRecordsUrl %q", p.NextRecordsURL)
		}
		return "/query/" + rest, nil
	})
}

type named struct {
	ID          string `json:"Id"`
	Name        string `json:"Name"`
	Label       string `json:"Label"`
	MasterLabel string `json:"MasterLabel"`
	Description string `json:"Description"`
}

type user struct {
	ID                   string `json:"Id"`
	Username             string `json:"Username"`
	Email                string `json:"Email"`
	Name                 string `json:"Name"`
	FederationIdentifier string `json:"FederationIdentifier"`
	EmployeeNumber       string `json:"EmployeeNumber"`
	IsActive             bool   `json:"IsActive"`
	LastLoginDate        sfTime `json:"LastLoginDate"`
	Title                string `json:"Title"`
	Department           string `json:"Department"`
	UserType             string `json:"UserType"`
	ProfileID            string `json:"ProfileId"`
}

// sfTime reads Salesforce's datetimes, such as 2026-10-01T08:00:00.000+0000.
type sfTime struct{ time.Time }

func (t *sfTime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil || s == "" {
		return nil
	}
	v, err := time.Parse("2006-01-02T15:04:05.000-0700", s)
	if err != nil {
		return fmt.Errorf("datetime %q: %w", s, err)
	}
	t.Time = v
	return nil
}

type assignment struct {
	AssigneeID           string `json:"AssigneeId"`
	PermissionSetID      string `json:"PermissionSetId"`
	PermissionSetGroupID string `json:"PermissionSetGroupId"`
}

// Read returns every profile, permission set, permission set group and
// user.
func (c *Client) Read(ctx context.Context) (connector.Snapshot, error) {
	var b connector.Builder
	err := query(ctx, c, "SELECT Id, Name, Description FROM Profile", func(p named) {
		b.Entitlement(connector.Entitlement{NativeID: profilePrefix + p.ID, Name: p.Name + " (profile)", Description: p.Description})
	})
	if err != nil {
		return connector.Snapshot{}, err
	}
	// Profiles and permission set groups have permission sets of their own
	// behind the scenes; only the ones people are given count.
	err = query(ctx, c, "SELECT Id, Name, Label, Description FROM PermissionSet WHERE Type IN ('Regular', 'Session')", func(p named) {
		b.Entitlement(connector.Entitlement{NativeID: p.ID, Name: cmp.Or(p.Label, p.Name), Description: p.Description})
	})
	if err != nil {
		return connector.Snapshot{}, err
	}
	err = query(ctx, c, "SELECT Id, MasterLabel, Description FROM PermissionSetGroup", func(p named) {
		b.Entitlement(connector.Entitlement{NativeID: groupPrefix + p.ID, Name: p.MasterLabel + " (permission set group)", Description: p.Description})
	})
	if err != nil {
		return connector.Snapshot{}, err
	}
	err = query(ctx, c, "SELECT AssigneeId, PermissionSetId, PermissionSetGroupId FROM PermissionSetAssignment", func(a assignment) {
		if a.PermissionSetGroupID != "" {
			b.Grant(a.AssigneeID, groupPrefix+a.PermissionSetGroupID)
		} else {
			b.Grant(a.AssigneeID, a.PermissionSetID)
		}
	})
	if err != nil {
		return connector.Snapshot{}, err
	}
	err = query(ctx, c, "SELECT Id, Username, Email, Name, FederationIdentifier, EmployeeNumber, IsActive, LastLoginDate, Title, Department, UserType, ProfileId FROM User",
		func(u user) {
			b.Account(connector.Account{
				NativeID:     u.ID,
				Username:     u.Username,
				Email:        u.Email,
				DisplayName:  u.Name,
				ExternalID:   u.EmployeeNumber,
				Active:       u.IsActive,
				Attributes:   connector.Attributes("title", u.Title, "department", u.Department, "user_type", u.UserType, "federation_id", u.FederationIdentifier),
				LastLogin:    u.LastLoginDate.Time,
				Entitlements: []string{profilePrefix + u.ProfileID},
			})
		})
	if err != nil {
		return connector.Snapshot{}, err
	}
	return b.Snapshot(), nil
}

// isDuplicate reports whether Salesforce refused a record because it exists.
func isDuplicate(err error) bool {
	var e *httpapi.StatusError
	return errors.As(err, &e) && e.Status == http.StatusBadRequest && strings.Contains(strings.ToLower(e.Detail), "duplicate")
}

// AddMember assigns a permission set or group, or gives a user a profile
// (which replaces their profile: every user has exactly one).
func (c *Client) AddMember(ctx context.Context, entitlementID, accountID string) error {
	if id, ok := strings.CutPrefix(entitlementID, profilePrefix); ok {
		_, err := c.api.Do(ctx, http.MethodPatch, "/sobjects/User/"+url.PathEscape(accountID), map[string]string{"ProfileId": id}, nil)
		return err
	}
	body := map[string]string{"AssigneeId": accountID, "PermissionSetId": entitlementID}
	if id, ok := strings.CutPrefix(entitlementID, groupPrefix); ok {
		body = map[string]string{"AssigneeId": accountID, "PermissionSetGroupId": id}
	}
	_, err := c.api.Do(ctx, http.MethodPost, "/sobjects/PermissionSetAssignment", body, nil)
	if isDuplicate(err) {
		return nil
	}
	return err
}

// soqlID matches a Salesforce record id, so ids can go in a query as they
// are.
var soqlID = regexp.MustCompile(`^[a-zA-Z0-9]{15,18}$`)

// RemoveMember takes away a permission set or group. A profile cannot be
// taken away, only replaced.
func (c *Client) RemoveMember(ctx context.Context, entitlementID, accountID string) error {
	if strings.HasPrefix(entitlementID, profilePrefix) {
		return errors.New("every Salesforce user has a profile: give them another one instead")
	}
	field, id := "PermissionSetId", entitlementID
	if g, ok := strings.CutPrefix(entitlementID, groupPrefix); ok {
		field, id = "PermissionSetGroupId", g
	}
	if !soqlID.MatchString(id) || !soqlID.MatchString(accountID) {
		return fmt.Errorf("%q or %q is not a Salesforce id", id, accountID)
	}
	var found []named
	err := query(ctx, c, fmt.Sprintf("SELECT Id FROM PermissionSetAssignment WHERE AssigneeId = '%s' AND %s = '%s'", accountID, field, id),
		func(r named) { found = append(found, r) })
	if err != nil {
		return err
	}
	for _, r := range found {
		_, err := c.api.Do(ctx, http.MethodDelete, "/sobjects/PermissionSetAssignment/"+url.PathEscape(r.ID), nil, nil)
		if httpapi.Ignore(err, http.StatusNotFound) != nil {
			return err
		}
	}
	return nil
}

// SetActive activates or deactivates a user; Salesforce never deletes
// users.
func (c *Client) SetActive(ctx context.Context, accountID string, active bool) error {
	_, err := c.api.Do(ctx, http.MethodPatch, "/sobjects/User/"+url.PathEscape(accountID), map[string]bool{"IsActive": active}, nil)
	return err
}
