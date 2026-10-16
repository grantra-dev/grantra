package console

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
)

// maxFeedBytes caps an uploaded HR file. 32 MiB holds a few hundred thousand
// employees.
const maxFeedBytes = 32 << 20

func (a *API) listSources(w http.ResponseWriter, r *http.Request) {
	var out []lifecycle.Source
	err := a.guardedOn(r, authz.ReadSources, authz.LifecycleResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = lifecycle.ListSources(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"sources": nonNil(out)}, err)
}

func (a *API) getSource(w http.ResponseWriter, r *http.Request) {
	var out lifecycle.Source
	err := a.guardedOn(r, authz.ReadSources, authz.LifecycleResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = lifecycle.GetSource(r.Context(), tx, r.PathValue("id"))
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// createSource adds a source: {"name": "Workday export", "kind": "csv",
// "config": {"columns": {"employee_id": "Worker ID"}}}.
func (a *API) createSource(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string           `json:"name"`
		Kind   string           `json:"kind"`
		Config lifecycle.Config `json:"config"`
	}
	if !decodeBodyMax(w, r, &body, 64<<10, `{"name": "HR export", "kind": "csv"}`) {
		return
	}
	var out lifecycle.Source
	err := a.guardedOn(r, authz.ManageSources, authz.LifecycleResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		if out, err = lifecycle.CreateSource(r.Context(), tx, a.tenantID, body.Name, body.Kind, body.Config); err != nil {
			return err
		}
		return db.Audit(r.Context(), tx, a.tenantID, me.Actor(), "identity_source.created", "identity_source", out.ID,
			map[string]any{"name": out.Name, "kind": out.Kind, "config": out.Config})
	})
	a.respond(w, r, http.StatusCreated, out, err)
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	var out []lifecycle.Run
	err := a.guardedOn(r, authz.ReadSources, authz.LifecycleResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = lifecycle.ListRuns(r.Context(), tx, r.PathValue("id"), limit)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"runs": nonNil(out)}, err)
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
		httpjson.Error(w, http.StatusBadRequest, "dry_run and force must be true or false")
		return
	}
	// Check the permission before taking the upload, so people who may not
	// sync cannot make the server read 32 MiB bodies.
	if _, err := a.allowed(r, authz.SyncSource, authz.LifecycleResource()); err != nil {
		a.fail(w, r, err)
		return
	}
	// Read the whole file before opening the transaction, so a slow upload
	// does not hold locks.
	feed, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFeedBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpjson.Error(w, http.StatusRequestEntityTooLarge, "the file is larger than 32 MiB")
			return
		}
		httpjson.Error(w, http.StatusBadRequest, "could not read the file")
		return
	}

	var out lifecycle.SyncResult
	err = a.guardedOn(r, authz.SyncSource, authz.LifecycleResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = lifecycle.Sync(r.Context(), tx, a.tenantID, r.PathValue("id"), bytes.NewReader(feed),
			lifecycle.Options{DryRun: dryRun, Force: force, Actor: me.Actor()})
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if out.Status == lifecycle.RunApplied {
		// Give joiners and movers their birthright access now rather than on
		// the next background pass. Failing here loses nothing: the events
		// stay in the outbox for that pass.
		ctx := context.WithoutCancel(r.Context())
		if _, _, err := access.ProcessLifecycle(ctx, a.pool, a.tenantID); err != nil {
			a.log.ErrorContext(ctx, "apply birthright access after sync", "err", err)
		}
	}
	status := http.StatusOK
	switch out.Status {
	case lifecycle.RunInvalid:
		status = http.StatusUnprocessableEntity
	case lifecycle.RunRefused:
		status = http.StatusConflict
	}
	httpjson.Write(w, status, out)
}

// listLifecycleEvents pages backwards from the newest event:
// ?limit=50&before=<id>&identity=<id>.
func (a *API) listLifecycleEvents(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	before, ok := beforeCursor(w, r)
	if !ok {
		return
	}
	identityID, ok := identityParam(w, r)
	if !ok {
		return
	}
	var out []lifecycle.Event
	err := a.guardedOn(r, authz.ReadLifecycle, authz.LifecycleResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = lifecycle.ListEvents(r.Context(), tx, identityID, before, limit)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"events": nonNil(out)}, err)
}
