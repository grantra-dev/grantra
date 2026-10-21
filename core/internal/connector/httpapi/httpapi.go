// Package httpapi is what connectors that talk to a JSON web API share:
// checking the address, signing requests, waiting when the service asks to
// slow down, reading its errors and paging through its lists.
package httpapi

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// BaseURL checks an address from a connector config: absolute, https
// (http only when allowHTTP, for labs and tests), without a query. The
// trailing slash is dropped.
func BaseURL(kind, field, raw string, allowHTTP bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSuffix(raw, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" {
		return nil, fmt.Errorf("%s config: %s must be an absolute URL, got %q", kind, field, raw)
	}
	if u.Scheme == "http" && !allowHTTP {
		return nil, fmt.Errorf("%s config: %s must use https (set allow_http only for labs)", kind, field)
	}
	return u, nil
}

// Auth signs a request.
type Auth func(ctx context.Context, req *http.Request) error

// Bearer sends a fixed token as "Authorization: Bearer".
func Bearer(token string) Auth { return Header("Authorization", "Bearer "+token) }

// Header sends a fixed header, such as Okta's "Authorization: SSWS".
func Header(name, value string) Auth {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set(name, value)
		return nil
	}
}

// OAuth2 sends a token from ts, which is asked again only when the last one
// is about to expire.
func OAuth2(ts oauth2.TokenSource) Auth {
	ts = oauth2.ReuseTokenSource(nil, ts)
	return func(_ context.Context, req *http.Request) error {
		t, err := ts.Token()
		if err != nil {
			return fmt.Errorf("sign in: %w", err)
		}
		t.SetAuthHeader(req)
		return nil
	}
}

// Client calls one web API.
type Client struct {
	// Base is what relative paths are resolved against.
	Base *url.URL
	Auth Auth
	// Accept is the Accept header; application/json when empty.
	Accept string
	// ContentType is the Content-Type of request bodies; application/json
	// when empty.
	ContentType string
	HTTP        *http.Client
	// MaxWait is the longest Grantra waits before trying again when the
	// service is busy; a longer wait fails the call. Default one minute.
	MaxWait time.Duration
	// Sleep waits; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// New builds a client for base with a 60 second timeout per request.
func New(base *url.URL, auth Auth) *Client {
	return &Client{Base: base, Auth: auth, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// StatusError is an error answer from the API.
type StatusError struct {
	Method string
	Path   string
	Status int
	// Detail is the API's own message, when it gave one.
	Detail string
}

func (e *StatusError) Error() string {
	s := fmt.Sprintf("%s %s: %d %s", e.Method, e.Path, e.Status, http.StatusText(e.Status))
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

// HasStatus reports whether err is an error answer with one of the statuses.
func HasStatus(err error, statuses ...int) bool {
	var e *StatusError
	if !errors.As(err, &e) {
		return false
	}
	for _, s := range statuses {
		if e.Status == s {
			return true
		}
	}
	return false
}

// Ignore returns nil when err is an error answer with one of the statuses,
// and err otherwise: removing what is already gone (404) or adding what is
// already there (409) is not an error for a connector.
func Ignore(err error, statuses ...int) error {
	if HasStatus(err, statuses...) {
		return nil
	}
	return err
}

// tries is how many times a call is made when the service is busy.
const tries = 5

// Get reads ref (a path relative to Base, or an absolute URL the API handed
// out, such as a next page link) into out.
func (c *Client) Get(ctx context.Context, ref string, out any) (http.Header, error) {
	return c.Do(ctx, http.MethodGet, ref, nil, out)
}

// Do sends body as JSON (nothing when nil) and decodes the answer into out
// (unless nil). It tries again, up to five times, when the service answers
// 429 Too Many Requests, or 403 with its rate limit used up, and for reads
// also on 502, 503 and 504, waiting as long as the service asks.
func (c *Client) Do(ctx context.Context, method, ref string, body, out any) (http.Header, error) {
	u, err := c.resolve(ref)
	if err != nil {
		return nil, err
	}
	var payload []byte
	if body != nil {
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	for try := 1; ; try++ {
		h, data, err := c.once(ctx, method, u, payload, body != nil)
		var e *StatusError
		if errors.As(err, &e) && try < tries {
			if wait, again := c.retryAfter(method, e.Status, h, try); again {
				if wait > c.maxWait() {
					return nil, fmt.Errorf("%w (the service asks to wait %s)", err, wait.Round(time.Second))
				}
				if err := c.sleep(ctx, wait); err != nil {
					return nil, err
				}
				continue
			}
		}
		if err != nil {
			return nil, err
		}
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return nil, fmt.Errorf("%s %s: decode response: %w", method, u.Path, err)
			}
		}
		return h, nil
	}
}

func (c *Client) resolve(ref string) (*url.URL, error) {
	r, err := url.Parse(ref)
	if err != nil {
		return nil, err
	}
	if r.IsAbs() {
		// Next page links must stay on the API's host, so a token is never
		// sent anywhere else.
		if r.Host != c.Base.Host || r.Scheme != c.Base.Scheme {
			return nil, fmt.Errorf("the API sent a link to %s://%s, not %s", r.Scheme, r.Host, c.Base.Host)
		}
		return r, nil
	}
	u := *c.Base
	path, query, _ := strings.Cut(ref, "?")
	u.RawPath = u.EscapedPath() + path
	if u.Path, err = url.PathUnescape(u.RawPath); err != nil {
		return nil, err
	}
	u.RawQuery = query
	return &u, nil
}

func (c *Client) once(ctx context.Context, method string, u *url.URL, payload []byte, hasBody bool) (http.Header, []byte, error) {
	var rd io.Reader
	if hasBody {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", cmp.Or(c.Accept, "application/json"))
	if hasBody {
		req.Header.Set("Content-Type", cmp.Or(c.ContentType, "application/json"))
	}
	if c.Auth != nil {
		if err := c.Auth(ctx, req); err != nil {
			return nil, nil, err
		}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.Header, nil, &StatusError{Method: method, Path: u.Path, Status: resp.StatusCode, Detail: Detail(data)}
	}
	return resp.Header, data, nil
}

// retryAfter says whether a failed call is worth trying again, and after
// how long.
func (c *Client) retryAfter(method string, status int, h http.Header, try int) (time.Duration, bool) {
	backoff := time.Duration(1<<(try-1)) * time.Second
	switch {
	case status == http.StatusTooManyRequests:
	case status == http.StatusForbidden && h.Get("X-RateLimit-Remaining") == "0":
		// GitHub says when its limit resets, as a Unix time.
		if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return max(time.Until(time.Unix(reset, 0)), time.Second), true
		}
	case method == http.MethodGet && (status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout):
	default:
		return 0, false
	}
	if v := h.Get("Retry-After"); v != "" {
		if s, err := strconv.Atoi(v); err == nil {
			return time.Duration(s) * time.Second, true
		}
		if t, err := http.ParseTime(v); err == nil {
			return max(time.Until(t), 0), true
		}
	}
	return backoff, true
}

func (c *Client) maxWait() time.Duration {
	if c.MaxWait > 0 {
		return c.MaxWait
	}
	return time.Minute
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Detail finds the message in an error answer, in the shapes the APIs
// Grantra talks to use: SCIM's detail, Microsoft Graph's and Google's
// error.message, Okta's errorSummary, GitHub's message, OAuth's
// error_description, and Salesforce's list of errors.
func Detail(body []byte) string {
	var e struct {
		Detail           string          `json:"detail"`
		Message          string          `json:"message"`
		ErrorSummary     string          `json:"errorSummary"`
		ErrorDescription string          `json:"error_description"`
		Error            json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		var list []struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &list) == nil && len(list) > 0 {
			return clip(list[0].Message)
		}
		return ""
	}
	var nested struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(e.Error, &nested)
	for _, s := range []string{e.Detail, nested.Message, e.ErrorSummary, e.ErrorDescription, e.Message} {
		if s != "" {
			return clip(s)
		}
	}
	var code string
	_ = json.Unmarshal(e.Error, &code)
	return clip(code)
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 300 {
		return string(r[:300]) + "…"
	}
	return s
}

// NextLink returns the rel="next" address of an RFC 8288 Link header, as
// Okta and GitHub page, or "" on the last page.
func NextLink(h http.Header) string {
	for _, v := range h.Values("Link") {
		for link := range strings.SplitSeq(v, ",") {
			target, params, ok := strings.Cut(link, ";")
			if !ok {
				continue
			}
			for p := range strings.SplitSeq(params, ";") {
				k, val, _ := strings.Cut(strings.TrimSpace(p), "=")
				if strings.EqualFold(k, "rel") && strings.Trim(val, `"`) == "next" {
					return strings.Trim(strings.TrimSpace(target), "<>")
				}
			}
		}
	}
	return ""
}

// MaxPages stops a misbehaving API from paging forever.
const MaxPages = 100_000

// Pages reads a list page by page, from first. Each page is decoded into a
// fresh P and handed to fn, which returns the address of the next page, or
// "" when there is none.
func Pages[P any](ctx context.Context, c *Client, first string, fn func(page *P, h http.Header) (next string, err error)) error {
	next := first
	for range MaxPages {
		var page P
		h, err := c.Get(ctx, next, &page)
		if err != nil {
			return err
		}
		if next, err = fn(&page, h); err != nil || next == "" {
			return err
		}
	}
	return fmt.Errorf("GET %s: more than %d pages", first, MaxPages)
}

// LinkPages reads a list that pages with Link headers and answers each page
// with a JSON array, handing every item to fn.
func LinkPages[T any](ctx context.Context, c *Client, first string, fn func(T)) error {
	return Pages(ctx, c, first, func(page *[]T, h http.Header) (string, error) {
		for _, item := range *page {
			fn(item)
		}
		return NextLink(h), nil
	})
}
