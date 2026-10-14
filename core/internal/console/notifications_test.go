package console

import (
	"net/http"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/notify"
)

func TestNotificationSettings(t *testing.T) {
	e := newEnv(t)
	ana := e.identity("ana", "")
	bob := e.identity("bob", "")

	got := decode[notificationSettings](t, e.do(ana, http.MethodGet, "/api/v1/me/notifications", ""))
	if got.EmailEnabled || !got.HasEmail || len(got.Kinds) != len(notify.Kinds) {
		t.Fatalf("settings = %+v", got)
	}
	for _, k := range got.Kinds {
		if !k.Enabled || k.Title == "" {
			t.Errorf("default %+v", k)
		}
	}

	rec := e.do(ana, http.MethodPut, "/api/v1/me/notifications/request_decided", `{"enabled": false}`)
	got = decode[notificationSettings](t, rec)
	for _, k := range got.Kinds {
		if k.Enabled != (k.Kind != notify.RequestDecided) {
			t.Errorf("after turning off: %+v", k)
		}
	}
	// Settings are per person.
	for _, k := range decode[notificationSettings](t, e.do(bob, http.MethodGet, "/api/v1/me/notifications", "")).Kinds {
		if !k.Enabled {
			t.Errorf("bob's %s is off", k.Kind)
		}
	}

	for body, want := range map[string]int{`{}`: http.StatusBadRequest, `{"enabled": "no"}`: http.StatusBadRequest} {
		if code := e.do(ana, http.MethodPut, "/api/v1/me/notifications/request_decided", body).Code; code != want {
			t.Errorf("PUT %s = %d, want %d", body, code, want)
		}
	}
	if code := e.do(ana, http.MethodPut, "/api/v1/me/notifications/spam", `{"enabled": true}`).Code; code != http.StatusNotFound {
		t.Errorf("unknown kind = %d", code)
	}
}
