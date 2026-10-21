package httpapi_test

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/grantra-dev/grantra/core/internal/connector/connectortest"
	"github.com/grantra-dev/grantra/core/internal/connector/httpapi"
)

func client(t *testing.T, srv *connectortest.Server) (*httpapi.Client, *[]time.Duration) {
	t.Helper()
	u, err := httpapi.BaseURL("test", "url", srv.URL+"/api/", true)
	if err != nil {
		t.Fatal(err)
	}
	c := httpapi.New(u, httpapi.Bearer("t0ken"))
	var waits []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	return c, &waits
}

func TestBaseURL(t *testing.T) {
	for raw, allowHTTP := range map[string]bool{
		"crm.example.com":              false,
		"http://crm.example.com":       false,
		"https://crm.example.com/?x=1": false,
		"ftp://crm.example.com":        true,
	} {
		if _, err := httpapi.BaseURL("test", "url", raw, allowHTTP); err == nil {
			t.Errorf("%q was accepted", raw)
		}
	}
	u, err := httpapi.BaseURL("test", "url", "https://crm.example.com/api/", false)
	if err != nil || u.String() != "https://crm.example.com/api" {
		t.Fatalf("got %v, %v", u, err)
	}
}

func TestAt(t *testing.T) {
	base, _ := url.Parse("https://graph.example.com/root")
	if got := httpapi.At(base, "/v1.0").String(); got != "https://graph.example.com/root/v1.0" {
		t.Errorf("At = %s", got)
	}
	if base.Path != "/root" {
		t.Errorf("At changed its base: %s", base)
	}
}

func TestRetriesWhenBusy(t *testing.T) {
	srv := connectortest.New(t, func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer t0ken" })
	tries := 0
	srv.Handle("GET /api/items", func(w http.ResponseWriter, _ *http.Request) {
		tries++
		switch tries {
		case 1:
			w.Header().Set("Retry-After", "7")
			connectortest.Reply(w, http.StatusTooManyRequests, nil)
		case 2:
			connectortest.Reply(w, http.StatusServiceUnavailable, nil)
		case 3:
			// GitHub's way of saying its limit is used up.
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "1")
			connectortest.Reply(w, http.StatusForbidden, map[string]string{"message": "API rate limit exceeded"})
		default:
			connectortest.Reply(w, http.StatusOK, map[string]int{"n": 3})
		}
	})
	c, waits := client(t, srv)
	var out struct{ N int }
	if _, err := c.Get(context.Background(), "/items", &out); err != nil || out.N != 3 {
		t.Fatalf("got %+v, %v", out, err)
	}
	if want := []time.Duration{7 * time.Second, 2 * time.Second, time.Second}; !slices.Equal(*waits, want) {
		t.Errorf("waited %v, want %v", *waits, want)
	}
}

func TestDoesNotRetryWritesOnServerErrors(t *testing.T) {
	srv := connectortest.New(t, nil)
	srv.Status("POST /api/items", http.StatusServiceUnavailable, map[string]any{"error": map[string]string{"code": "x", "message": "try later"}})
	c, waits := client(t, srv)
	_, err := c.Do(context.Background(), http.MethodPost, "/items", map[string]int{"a": 1}, nil)
	if !httpapi.HasStatus(err, http.StatusServiceUnavailable) || !strings.Contains(err.Error(), "try later") {
		t.Fatalf("got %v", err)
	}
	if len(*waits) != 0 || len(srv.Calls()) != 1 {
		t.Errorf("retried a write: %v", srv.Calls())
	}
}

func TestGivesUpOnLongWaits(t *testing.T) {
	srv := connectortest.New(t, nil)
	srv.Handle("GET /api/items", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3600")
		connectortest.Reply(w, http.StatusTooManyRequests, nil)
	})
	c, _ := client(t, srv)
	if _, err := c.Get(context.Background(), "/items", nil); err == nil || !strings.Contains(err.Error(), "wait 1h0m0s") {
		t.Fatalf("got %v", err)
	}
}

func TestDetail(t *testing.T) {
	for body, want := range map[string]string{
		`{"detail": "bad filter"}`: "bad filter",
		`{"error": {"code": "Request_BadRequest", "message": "Invalid object identifier"}}`: "Invalid object identifier",
		`{"errorCode": "E0000007", "errorSummary": "Not found: Resource not found"}`:        "Not found: Resource not found",
		`{"message": "Not Found", "documentation_url": "https://docs.github.com"}`:          "Not Found",
		`{"error": "invalid_client", "error_description": "bad secret"}`:                    "bad secret",
		`{"error": "unauthorized_client"}`:                                                  "unauthorized_client",
		`[{"message": "Session expired", "errorCode": "INVALID_SESSION_ID"}]`:               "Session expired",
		`<html>oops</html>`: "",
	} {
		if got := httpapi.Detail([]byte(body)); got != want {
			t.Errorf("Detail(%s) = %q, want %q", body, got, want)
		}
	}
}

func TestLinkPagesStayOnTheAPIHost(t *testing.T) {
	srv := connectortest.New(t, nil)
	srv.Handle("GET /api/items", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("after") {
		case "":
			w.Header().Set("Link", `<`+srv.URL+`/api/items?after=2>; rel="next", <`+srv.URL+`/api/items>; rel="first"`)
			connectortest.Reply(w, http.StatusOK, []int{1, 2})
		case "2":
			w.Header().Set("Link", `<https://evil.example/steal>; rel="next"`)
			connectortest.Reply(w, http.StatusOK, []int{3})
		}
	})
	c, _ := client(t, srv)
	var got []int
	err := httpapi.LinkPages(context.Background(), c, "/items", func(n int) { got = append(got, n) })
	if err == nil || !strings.Contains(err.Error(), "evil.example") {
		t.Fatalf("followed a link off the API host: %v", err)
	}
	if !slices.Equal(got, []int{1, 2, 3}) {
		t.Errorf("got %v", got)
	}
}

func TestRelativePathsAreEscaped(t *testing.T) {
	srv := connectortest.New(t, nil)
	srv.Handle("DELETE /api/groups/{id}/members/{member}/$ref", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "a/b" {
			t.Errorf("id = %q", r.PathValue("id"))
		}
		connectortest.Reply(w, http.StatusNoContent, nil)
	})
	c, _ := client(t, srv)
	if _, err := c.Do(context.Background(), http.MethodDelete, "/groups/"+url.PathEscape("a/b")+"/members/u1/$ref", nil, nil); err != nil {
		t.Fatal(err)
	}
}
