package textutil

import (
	"testing"
	"unicode/utf8"
)

func TestClip(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"  short  ", 10, "short"},
		{"abcdef", 3, "abc"},
		// "ñ" is two bytes: cutting in its middle drops it whole.
		{"señor", 3, "se"},
		{"señor", 4, "señ"},
		{"", 5, ""},
	} {
		got := Clip(tc.in, tc.n)
		if got != tc.want || !utf8.ValidString(got) {
			t.Errorf("Clip(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestPlural(t *testing.T) {
	if got := Plural(1, "account", "accounts"); got != "1 account" {
		t.Errorf("got %q", got)
	}
	if got := Plural(0, "account", "accounts"); got != "0 accounts" {
		t.Errorf("got %q", got)
	}
}
