// Package httpjson writes the JSON answers every Grantra HTTP endpoint gives,
// so they all look the same: a JSON body, and {"error": "..."} on failure.
package httpjson

import (
	"encoding/json"
	"net/http"
)

// Write answers with v encoded as JSON.
func Write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// Error answers with {"error": msg}.
func Error(w http.ResponseWriter, status int, msg string) {
	Write(w, status, map[string]string{"error": msg})
}

// NonNil returns s, or an empty slice for nil, so lists encode as [] and
// not null.
func NonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
