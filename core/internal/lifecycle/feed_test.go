package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/connector"
)

// fakeKind stands in for an HR system; it must be a kind the database
// allows.
const fakeKind = "bamboohr"

// fakeFeed answers people, or failure, from the test.
var fakeFeed struct {
	rows []map[string]string
	err  error
}

type fakeConnection struct {
	Token string `json:"token"`
}

func init() {
	RegisterFeed(fakeKind, func(raw json.RawMessage, secrets connector.SecretResolver) (Feed, error) {
		var c fakeConnection
		if err := connector.DecodeConfig(fakeKind, raw, &c); err != nil {
			return nil, err
		}
		if _, err := connector.Secret(fakeKind, "token", c.Token, "env:X", secrets); err != nil {
			return nil, err
		}
		return feedFunc(func(context.Context) ([]map[string]string, error) { return fakeFeed.rows, fakeFeed.err }), nil
	})
}

type feedFunc func(context.Context) ([]map[string]string, error)

func (f feedFunc) Rows(ctx context.Context) ([]map[string]string, error) { return f(ctx) }

func fakeSecrets(ref string) (string, error) {
	if ref != "env:TOKEN" {
		return "", fmt.Errorf("no secret %s", ref)
	}
	return "t", nil
}

func TestText(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want string
		ok   bool
	}{
		{" Ana ", "Ana", true},
		{float64(1000000), "1000000", true},
		{12.5, "12.5", true},
		{true, "true", true},
		{nil, "", false},
		{[]any{"a"}, "", false},
		{map[string]any{"id": "1"}, "", false},
	} {
		if got, ok := Text(tc.in); got != tc.want || ok != tc.ok {
			t.Errorf("Text(%v) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRowsToCSV(t *testing.T) {
	got, err := rowsToCSV([]map[string]string{{"b": "1", "a": "x,y"}, {"c": "3"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "a,b,c\n\"x,y\",1,\n,,3\n"; string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHRSystemSource(t *testing.T) {
	e := newDBEnv(t, Config{})
	ctx := context.Background()
	conn := json.RawMessage(`{"token": "env:TOKEN"}`)
	var inv *apperr.Invalid
	var hr Source
	e.tx(func(tx pgx.Tx) error {
		for _, bad := range []Config{
			{Connection: json.RawMessage(`{"token": "env:NOPE"}`)},
			{Connection: json.RawMessage(`{"token": "env:TOKEN", "extra": 1}`)},
			{Connection: conn, EveryHours: 1000},
		} {
			if _, err := CreateSource(ctx, tx, e.tenant, "Bad", fakeKind, bad, fakeSecrets); !errors.As(err, &inv) {
				t.Errorf("config %+v = %v", bad, err)
			}
		}
		if _, err := UpdateSource(ctx, tx, e.source, "HR", Config{Connection: conn}, fakeSecrets); !errors.As(err, &inv) {
			t.Errorf("a file source took a connection: %v", err)
		}
		var err error
		hr, err = CreateSource(ctx, tx, e.tenant, "People", fakeKind, Config{Connection: conn}, fakeSecrets)
		if err == nil && hr.Config.EveryHours != DefaultEveryHours {
			t.Errorf("every_hours = %d", hr.Config.EveryHours)
		}
		return err
	})
	e.tx(func(tx pgx.Tx) error {
		_, err := Sync(ctx, tx, e.tenant, hr.ID, strings.NewReader("employee_id\nE1\n"), Options{Actor: "t"})
		if !errors.As(err, &inv) {
			t.Errorf("a file was synced into an HR system source: %v", err)
		}
		return nil
	})
	if _, err := Pull(ctx, e.pool, e.tenant, e.source, Options{Actor: "t"}, fakeSecrets); !errors.As(err, &inv) {
		t.Errorf("a file source was pulled: %v", err)
	}

	pull := func(opts Options) SyncResult {
		t.Helper()
		opts.Actor = "t"
		res, err := Pull(ctx, e.pool, e.tenant, hr.ID, opts, fakeSecrets)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	fakeFeed.rows = []map[string]string{
		{"employee_id": "E1", "email": "ana@acme.com", "first_name": "Ana", "last_name": "Ruiz", "status": "active"},
		{"employee_id": "E2", "email": "luis@acme.com", "first_name": "Luis", "last_name": "Gil", "manager_id": "E1", "status": "active"},
	}
	if res := pull(Options{DryRun: true}); res.Status != RunPlanned || res.Counts[Joiner] != 2 {
		t.Errorf("dry run = %+v", res.Run)
	}
	if n := e.count(`SELECT count(*) FROM identities`); n != 0 {
		t.Errorf("a dry run wrote %d identities", n)
	}
	if res := pull(Options{}); res.Status != RunApplied {
		t.Errorf("pull = %+v", res.Run)
	}
	if n := e.count(`SELECT count(*) FROM identities s JOIN identities m ON m.id = s.manager_id WHERE s.external_id = 'E2' AND m.external_id = 'E1'`); n != 1 {
		t.Error("Luis is not Ana's report")
	}

	// A failed read and an empty one are recorded and change nothing.
	for _, tc := range []struct {
		rows []map[string]string
		err  error
		msg  string
	}{
		{err: errors.New("401 Unauthorized"), msg: "could not read the HR system: 401 Unauthorized"},
		{rows: []map[string]string{}, msg: "the HR system returned nobody"},
	} {
		fakeFeed.rows, fakeFeed.err = tc.rows, tc.err
		res := pull(Options{})
		if res.Status != RunInvalid || len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, tc.msg) {
			t.Errorf("run = %+v, want an invalid run saying %q", res.Run, tc.msg)
		}
	}
	if n := e.count(`SELECT count(*) FROM identities WHERE status = 'active'`); n != 2 {
		t.Errorf("a failed read changed people: %d active", n)
	}

	// The applied pull above was just now: nothing is due, until it is older
	// than every_hours.
	fakeFeed.rows, fakeFeed.err = []map[string]string{{"employee_id": "E1", "email": "ana@acme.com", "first_name": "Ana", "last_name": "Ruiz", "status": "active"},
		{"employee_id": "E2", "email": "luis@acme.com", "first_name": "Luis", "last_name": "Gil", "manager_id": "E1", "status": "active", "title": "Engineer"}}, nil
	due := func() []SyncResult {
		t.Helper()
		out, err := PullDue(ctx, e.pool, e.tenant, fakeSecrets)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := due(); len(out) != 0 {
		t.Errorf("pulled %d sources that were not due", len(out))
	}
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE identity_source_runs SET created_at = created_at - interval '25 hours' WHERE source_id = $1`, hr.ID)
		return err
	})
	out := due()
	if len(out) != 1 || out[0].Status != RunApplied || out[0].Actor != "system:hr-sync" || out[0].Counts[Mover] != 1 {
		t.Errorf("due pulls = %+v", out)
	}
}
