package lifecycle

import (
	"maps"
	"slices"
	"strings"
	"time"
)

// Kind classifies a change to one identity.
type Kind string

// Lifecycle kinds. Joiner, mover, leaver and rehire become lifecycle events;
// update only changes the identity's details.
const (
	Joiner Kind = "joiner"
	Mover  Kind = "mover"
	Leaver Kind = "leaver"
	Rehire Kind = "rehire"
	Update Kind = "update"
)

// Why someone became a leaver.
const (
	ReasonTerminated = "terminated in the source"
	ReasonEndDate    = "end date passed"
	ReasonMissing    = "no longer in the source"
	// ReasonAlreadyGone marks a first sighting of someone who already left.
	ReasonAlreadyGone = "already left when first seen"
)

// Current is an identity as it is stored today.
type Current struct {
	ID                string
	SourceID          string
	ExternalID        string
	Kind              string
	Status            string
	DisplayName       string
	Email             string
	ManagerExternalID string
	StartDate         *time.Time
	EndDate           *time.Time
	Attributes        map[string]string
}

// FieldChange is the before and after of one field.
type FieldChange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Change is what a sync does to one identity.
type Change struct {
	Kind Kind `json:"kind"`
	// IdentityID is empty for joiners until the plan is applied.
	IdentityID  string `json:"identity_id,omitempty"`
	ExternalID  string `json:"external_id"`
	DisplayName string `json:"display_name"`
	// Status is the identity's status after the change.
	Status string `json:"status"`
	// Linked is set when an identity that no source owned (created by hand or
	// by the bootstrap) is taken over by this source, matched by email.
	Linked bool   `json:"linked,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Changes lists the fields that change, keyed by field name. Manager
	// changes are given as employee ids.
	Changes map[string]FieldChange `json:"changes,omitempty"`

	record *Record
}

// Result of planning a sync.
type Result struct {
	Changes   []Change `json:"changes"`
	Unchanged int      `json:"unchanged"`
	// Errors stop the sync: nothing is applied while there is one.
	Errors []Issue `json:"errors"`
	// Warnings are applied around: a manager that is not in the source, for
	// example, is left empty.
	Warnings []Issue `json:"warnings"`
	// Active is how many people the source owned and had active before the
	// sync, the base for the leaver safety limit.
	Active int `json:"active_before"`
}

// Counts tallies the changes by kind.
func (r Result) Counts() map[Kind]int {
	c := map[Kind]int{Joiner: 0, Mover: 0, Leaver: 0, Rehire: 0, Update: 0}
	for _, ch := range r.Changes {
		c[ch.Kind]++
	}
	return c
}

// TooManyLeavers reports whether the plan breaks the leaver safety limit.
// Up to three leavers always pass, so small companies are not stuck.
func (r Result) TooManyLeavers(maxPercent int) bool {
	n := r.Counts()[Leaver]
	return n > 3 && n*100 > maxPercent*r.Active
}

// Plan compares what the source says with what is stored and returns the
// changes a sync makes. sourceID is the source being synced; current holds
// every identity of the tenant, so that employee ids owned by another source
// and unowned identities to link are found. today decides which start and end
// dates have passed.
func Plan(sourceID string, cfg Config, records []Record, current []Current, today time.Time) Result {
	today = dateOnly(today)
	res := Result{Errors: []Issue{}, Warnings: []Issue{}}

	byExternal := map[string]*Current{}
	byEmail := map[string][]*Current{}
	for i := range current {
		c := &current[i]
		if c.ExternalID != "" {
			byExternal[c.ExternalID] = c
		}
		if c.SourceID == "" && c.ExternalID == "" && c.Email != "" && c.Kind == "human" {
			k := strings.ToLower(c.Email)
			byEmail[k] = append(byEmail[k], c)
		}
		if c.SourceID == sourceID && c.Status == "active" {
			res.Active++
		}
	}

	inFeed := map[string]bool{}
	for i := range records {
		inFeed[records[i].ExternalID] = true
	}
	// A manager must be someone Grantra knows: in this feed or already stored.
	knownManager := func(id string) bool {
		if inFeed[id] {
			return true
		}
		_, ok := byExternal[id]
		return ok
	}

	claimed := map[string]bool{}
	for i := range records {
		rec := &records[i]
		if rec.ManagerExternalID == rec.ExternalID && rec.ManagerExternalID != "" {
			res.Warnings = append(res.Warnings, Issue{Line: rec.Line, ExternalID: rec.ExternalID, Message: "the person is their own manager; manager left empty"})
			rec.ManagerExternalID = ""
		}
		if rec.ManagerExternalID != "" && !knownManager(rec.ManagerExternalID) {
			res.Warnings = append(res.Warnings, Issue{Line: rec.Line, ExternalID: rec.ExternalID, Message: "manager " + rec.ManagerExternalID + " is not in the source; manager left empty"})
			rec.ManagerExternalID = ""
		}
		desired := desiredStatus(rec, today)

		cur, ok := byExternal[rec.ExternalID]
		if ok && cur.SourceID != "" && cur.SourceID != sourceID {
			res.Errors = append(res.Errors, Issue{Line: rec.Line, ExternalID: rec.ExternalID, Message: "employee id " + rec.ExternalID + " belongs to another source"})
			continue
		}
		linked := false
		if !ok && rec.Email != "" {
			for _, cand := range byEmail[strings.ToLower(rec.Email)] {
				if !claimed[cand.ID] {
					cur, ok, linked = cand, true, true
					break
				}
			}
		}
		if !ok {
			ch := Change{Kind: Joiner, ExternalID: rec.ExternalID, DisplayName: rec.DisplayName, Status: desired, record: rec}
			if desired == "terminated" {
				// Someone who left before Grantra ever saw them: keep the
				// record, so their leftover accounts can still be matched to
				// them, with no joiner event.
				ch.Kind = Update
				ch.Reason = ReasonAlreadyGone
			}
			res.Changes = append(res.Changes, ch)
			continue
		}
		claimed[cur.ID] = true
		if cur.SourceID == "" {
			// An identity with this employee id that no source owned yet.
			linked = true
		}

		ch := Change{
			IdentityID:  cur.ID,
			ExternalID:  rec.ExternalID,
			DisplayName: rec.DisplayName,
			Linked:      linked,
			Changes:     diff(cur, rec),
			record:      rec,
		}
		status := nextStatus(cur.Status, desired)
		if status != cur.Status {
			ch.Changes["status"] = FieldChange{cur.Status, status}
		}
		ch.Status = status
		switch {
		case cur.Status == "terminated" && status != "terminated":
			ch.Kind = Rehire
		case cur.Status != "terminated" && status == "terminated":
			ch.Kind = Leaver
			ch.Reason = ReasonTerminated
			if !rec.Terminated {
				ch.Reason = ReasonEndDate
			}
		case isMove(ch.Changes):
			ch.Kind = Mover
		case len(ch.Changes) > 0 || linked:
			ch.Kind = Update
		default:
			res.Unchanged++
			continue
		}
		if len(ch.Changes) == 0 {
			ch.Changes = nil
		}
		res.Changes = append(res.Changes, ch)
	}

	if cfg.Missing != "ignore" {
		for i := range current {
			c := &current[i]
			if c.SourceID != sourceID || inFeed[c.ExternalID] || c.Status == "terminated" {
				continue
			}
			res.Changes = append(res.Changes, Change{
				Kind:        Leaver,
				IdentityID:  c.ID,
				ExternalID:  c.ExternalID,
				DisplayName: c.DisplayName,
				Status:      "terminated",
				Reason:      ReasonMissing,
				Changes:     map[string]FieldChange{"status": {c.Status, "terminated"}},
			})
		}
	}
	return res
}

// desiredStatus is the status the source asks for, given today's date. The
// end date is the last working day.
func desiredStatus(rec *Record, today time.Time) string {
	switch {
	case rec.Terminated, rec.EndDate != nil && rec.EndDate.Before(today):
		return "terminated"
	case rec.StartDate != nil && rec.StartDate.After(today):
		return "pending"
	default:
		return "active"
	}
}

// nextStatus moves an identity towards the status the source asks for.
// Termination and rehire always follow the source. Otherwise an identity only
// moves forward (pending to active), and a suspension decided in Grantra
// stays until Grantra lifts it.
func nextStatus(current, desired string) string {
	switch {
	case desired == "terminated", current == "terminated":
		return desired
	case current == "pending":
		return desired
	default:
		return current
	}
}

func diff(cur *Current, rec *Record) map[string]FieldChange {
	out := map[string]FieldChange{}
	add := func(field, from, to string) {
		if from != to {
			out[field] = FieldChange{from, to}
		}
	}
	add(FieldEmployeeID, cur.ExternalID, rec.ExternalID)
	add(FieldDisplayName, cur.DisplayName, rec.DisplayName)
	add(FieldEmail, cur.Email, rec.Email)
	add(FieldManagerID, cur.ManagerExternalID, rec.ManagerExternalID)
	add(FieldStartDate, formatDate(cur.StartDate), formatDate(rec.StartDate))
	add(FieldEndDate, formatDate(cur.EndDate), formatDate(rec.EndDate))
	keys := slices.Sorted(maps.Keys(cur.Attributes))
	for k := range rec.Attributes {
		if _, ok := cur.Attributes[k]; !ok {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		add(k, cur.Attributes[k], rec.Attributes[k])
	}
	return out
}

func isMove(changes map[string]FieldChange) bool {
	if _, ok := changes[FieldManagerID]; ok {
		return true
	}
	for _, f := range JobAttributes {
		if _, ok := changes[f]; ok {
			return true
		}
	}
	return false
}

func formatDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.DateOnly)
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
