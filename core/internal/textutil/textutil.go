// Package textutil has the small text helpers several packages need when
// they write messages and store what others send.
package textutil

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Clip trims s and shortens it to at most n bytes, on a character
// boundary, so what is stored is still valid UTF-8.
func Clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Plural writes n with the word that goes with it: "1 account", "3 accounts".
func Plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
