package kms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// callJSON sends in as JSON (nothing when nil) and decodes the answer into
// out. auth sets credentials on the request.
func callJSON(ctx context.Context, hc *http.Client, method, url string, auth func(*http.Request) error, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != nil {
		if err := auth(req); err != nil {
			return err
		}
	}
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %s", method, redact(url), res.Status, strings.TrimSpace(string(data[:min(len(data), 500)])))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// redact drops the query, where credentials sometimes go.
func redact(url string) string {
	u, _, _ := strings.Cut(url, "?")
	return u
}
