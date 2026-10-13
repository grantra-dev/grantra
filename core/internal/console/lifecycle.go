package console

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
)

// maxFeedBytes caps an uploaded HR file. 32 MiB holds a few hundred thousand
// employees.
const maxFeedBytes = 32 << 20

func (a *API) listSources(w http.ResponseWriter, r *http.Request) {
	var out []lifecycle.Source
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.ReadSources, Resource: authz.LifecycleResource()}); err != nil {
			return err
		}
		var err error
		out, err = lifecycle.ListSources(r.Context(), tx)
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": nonNil(out)})
}

func (a *API) getSource(w http.ResponseWriter, r *http.Request) {
	var out lifecycle.Source
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.ReadSources, Resource: authz.LifecycleResource()}); err != nil {
			return err
		}
		var err error
		out, err = lifecycle.GetSource(r.Context(), tx, r.PathValue("id"))
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// createSource adds a source: {"name": "Workday export", "kind": "csv",
// "config": {"columns": {"employee_id": "Worker ID"}}}.
func (a *API) createSource(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string           `json:"name"`
		Kind   string           `json:"kind"`
		Config lifecycle.Config `json:"config"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, `body must be JSON like {"name": "HR export", "kind": "csv"}`)
		return
	}
	var out lifecycle.Source
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.ManageSources, Resource: authz.LifecycleResource()}); err != nil {
			return err
		}
		var err error
		out, err = lifecycle.CreateSource(r.Context(), tx, a.tenantID, body.Name, body.Kind, body.Config)
		if err != nil {
			return err
		}
		return db.Audit(r.Context(), tx, a.tenantID, "identity:"+me.ID, "identity_source.created", "identity_source", out.ID,
			map[string]any{"name": out.Name, "kind": out.Kind, "config": out.Config})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	var out []lifecycle.Run
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.ReadSources, Resource: authz.LifecycleResource()}); err != nil {
			return err
		}
		var err error
		out, err = lifecycle.ListRuns(r.Context(), tx, r.PathValue("id"), limit)
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": nonNil(out)})
}

// syncSource feeds a source with an HR export (the CSV file as the request
// body) and applies what changed. ?dry_run=true only plans; ?force=true
// applies a sync that breaks the leaver safety limit. The answer is the run
// with every change: 200 when planned or applied, 422 when the file has
// errors and 409 when the leaver limit stopped it. Nothing is applied in
// those last two cases.
func (a *API) syncSource(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	dryRun, err1 := parseBool(q.Get("dry_run"))
	force, err2 := parseBool(q.Get("force"))
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusBadRequest, "dry_run and force must be true or false")
		return
	}
	// Read the whole file before opening the transaction, so a slow upload
	// does not hold locks.
	feed, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFeedBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "the file is larger than 32 MiB")
			return
		}
		writeError(w, http.StatusBadRequest, "could not read the file")
		return
	}

	var out lifecycle.SyncResult
	err = a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.SyncSource, Resource: authz.LifecycleResource()}); err != nil {
			return err
		}
		var err error
		out, err = lifecycle.Sync(r.Context(), tx, a.tenantID, r.PathValue("id"), bytes.NewReader(feed),
			lifecycle.Options{DryRun: dryRun, Force: force, Actor: "identity:" + me.ID})
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	switch out.Status {
	case lifecycle.RunInvalid:
		status = http.StatusUnprocessableEntity
	case lifecycle.RunRefused:
		status = http.StatusConflict
	}
	writeJSON(w, status, out)
}

// listLifecycleEvents pages backwards from the newest event:
// ?limit=50&before=<id>&identity=<id>.
func (a *API) listLifecycleEvents(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var before int64
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "before must be a positive event id")
			return
		}
		before = n
	}
	identityID := q.Get("identity")
	if identityID != "" && !db.ValidUUID(identityID) {
		writeError(w, http.StatusBadRequest, "identity must be an identity id")
		return
	}
	var out []lifecycle.Event
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.ReadLifecycle, Resource: authz.LifecycleResource()}); err != nil {
			return err
		}
		var err error
		out, err = lifecycle.ListEvents(r.Context(), tx, identityID, before, limit)
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": nonNil(out)})
}

func parseBool(v string) (bool, error) {
	if v == "" {
		return false, nil
	}
	return strconv.ParseBool(v)
}
