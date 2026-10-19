package grantralogin

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
)

// SignInEventTypes are the Keycloak events Grantra reads for threat
// detection: sign-ins and failed sign-ins.
var SignInEventTypes = []string{"LOGIN", "LOGIN_ERROR"}

// eventsExpiration is how long Keycloak keeps events, in seconds. Grantra
// reads them every minute and keeps its own copy, so a week is plenty to
// get over Grantra being down.
const eventsExpiration = 7 * 24 * 60 * 60

// Event is a Keycloak user event.
type Event struct {
	ID        string            `json:"id"`
	Time      int64             `json:"time"`
	Type      string            `json:"type"`
	ClientID  string            `json:"clientId"`
	UserID    string            `json:"userId"`
	SessionID string            `json:"sessionId"`
	IPAddress string            `json:"ipAddress"`
	Error     string            `json:"error"`
	Details   map[string]string `json:"details"`
}

type eventsConfig struct {
	EventsEnabled     bool     `json:"eventsEnabled"`
	EventsExpiration  *int64   `json:"eventsExpiration,omitempty"`
	EventsListeners   []string `json:"eventsListeners"`
	EnabledEventTypes []string `json:"enabledEventTypes"`
}

// EnsureEvents makes Keycloak store sign-in events, so Grantra can read
// them; it changes nothing when it already does. It needs the
// manage-events role, and reading events view-events.
func (c *Client) EnsureEvents(ctx context.Context) error {
	var cfg eventsConfig
	if _, err := c.do(ctx, http.MethodGet, "/events/config", nil, &cfg); err != nil {
		return err
	}
	missing := false
	for _, t := range SignInEventTypes {
		if !slices.Contains(cfg.EnabledEventTypes, t) {
			cfg.EnabledEventTypes = append(cfg.EnabledEventTypes, t)
			missing = true
		}
	}
	if cfg.EventsEnabled && !missing && cfg.EventsExpiration != nil {
		return nil
	}
	cfg.EventsEnabled = true
	if cfg.EventsExpiration == nil {
		exp := int64(eventsExpiration)
		cfg.EventsExpiration = &exp
	}
	_, err := c.do(ctx, http.MethodPut, "/events/config", cfg, nil)
	return err
}

// Events calls fn with each sign-in event from fromMillis (Unix time in
// milliseconds, inclusive) on, newest first.
func (c *Client) Events(ctx context.Context, fromMillis int64, fn func(Event)) error {
	q := url.Values{"type": SignInEventTypes, "dateFrom": {strconv.FormatInt(fromMillis, 10)}}
	return list(ctx, c, "/events", q, fn)
}
