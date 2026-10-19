package itdr

import (
	"math"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/connector/grantralogin"
)

// The events below are what Keycloak 26.4 stored for a wrong password, a
// wrong one-time code, an unknown user, a sign-in and an expired page.
func TestSignInFromKeycloakEvent(t *testing.T) {
	cases := []struct {
		name string
		ev   grantralogin.Event
		ok   bool
		want string
	}{
		{"wrong password", grantralogin.Event{ID: "1", Time: 1792430413420, Type: "LOGIN_ERROR", UserID: "u", Error: "invalid_user_credentials",
			Details: map[string]string{"auth_method": "openid-connect", "auth_type": "code", "username": "luis"}}, true, Failure},
		{"wrong code", grantralogin.Event{ID: "2", Time: 1792430413420, Type: "LOGIN_ERROR", UserID: "u", Error: "invalid_user_credentials",
			Details: map[string]string{"auth_method": "openid-connect", "auth_type": "code", "selected_credential_id": "c"}}, true, MFAFailure},
		{"unknown user", grantralogin.Event{ID: "3", Time: 1792430413478, Type: "LOGIN_ERROR", Error: "user_not_found",
			Details: map[string]string{"username": "nobody"}}, true, Failure},
		{"sign-in", grantralogin.Event{ID: "4", Time: 1792430413478, Type: "LOGIN", UserID: "u",
			Details: map[string]string{"credential_type": "otp", "username": "luis"}}, true, Success},
		{"expired page", grantralogin.Event{ID: "5", Type: "LOGIN_ERROR", Error: "cookie_not_found"}, false, ""},
		{"other event", grantralogin.Event{ID: "6", Type: "LOGOUT"}, false, ""},
	}
	for _, c := range cases {
		s, ok := signInFromEvent(c.ev)
		if ok != c.ok || s.Outcome != c.want {
			t.Errorf("%s: %+v, %v; want %q, %v", c.name, s, ok, c.want, c.ok)
		}
	}
	// Without an id, the event's facts make a stable one.
	ev := grantralogin.Event{Time: 1, Type: "LOGIN", UserID: "u", IPAddress: "not an ip"}
	a, _ := signInFromEvent(ev)
	b, _ := signInFromEvent(ev)
	if a.ID == "" || a.ID != b.ID || a.IP != "" {
		t.Errorf("made-up ids %q and %q, ip %q; want the same, and no bad ip", a.ID, b.ID, a.IP)
	}
}

func TestHelpers(t *testing.T) {
	if d := distanceKm(40.42, -3.70, 40.71, -74.0); math.Abs(d-5765) > 30 {
		t.Errorf("Madrid to New York = %.0f km, want about 5,765", d)
	}
	if got := thousands(1234567); got != "1,234,567" {
		t.Errorf("thousands = %s", got)
	}
}
