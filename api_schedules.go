package main

// THE SCHEDULE'S FOUR DOORS - the HTTP half of row 01M0EW45RE.
//
// The store decides (internal/store/schedule.go); these carry the decision and
// its REASON. A refused cron comes back with the parser's own sentence - which
// names the field and the token - rather than a generic 400, so the console can
// show it verbatim and does not need a second copy of the rules that would
// drift from the first.
//
// WHY FOUR AND NOT THREE. Listing what is SET at a scope and resolving what a
// reader RECEIVES are different questions with different answers, and a console
// that offers only the first cannot show a person the thing worth checking:
// "this is what orchestrator will actually get". Deleting is its own door for
// the same reason it is its own verb in the store - unchecking realtime and
// clearing the cron stores an explicit NEVER that overrides the parent, and
// there is no way back to inheriting except removing the row. A surface with
// only a checkbox can enter a state it cannot leave.
//
// PERMISSION FOLLOWS SCOPE, because scope is not just a lookup key. A fleet row
// is global: one write changes what every project and every room resolves. So
// fleet is the operator's, and project and room are anyone who can reach that
// project. The check is in one helper rather than at four call sites for the
// reason operatorOnly is a wrapper - a set of routes that all need the same
// gate is a set where one of them eventually does not have it.

import (
	"encoding/json"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// scheduleScope reads the scope out of a request and says whether this
// principal may touch it.
//
// The project defaults to the one the principal is ACTING IN rather than being
// required, because a token names exactly one such project and making every
// caller repeat it is how a console ends up sending the wrong one. Naming it
// explicitly is still allowed, and still checked.
func (s *server) scheduleScope(r *http.Request, kind, project, room string) (store.Scope, int, string) {
	p := principalOf(r)
	if p == nil {
		return store.Scope{}, http.StatusUnauthorized, "no principal on this request"
	}
	if kind == "" {
		return store.Scope{}, http.StatusBadRequest,
			"scope is required (fleet, project, room) - a schedule with no scope is a setting with no reader"
	}

	switch kind {
	case store.SchedFleet:
		if !s.isOperator(r.Context(), r) {
			return store.Scope{}, http.StatusForbidden,
				"a fleet schedule is the operator's: one row changes what every project and every room resolves"
		}
		if project != "" || room != "" {
			return store.Scope{}, http.StatusBadRequest,
				"fleet scope takes no project or room - it is the scope that applies to all of them"
		}
		return store.FleetScope(), 0, ""

	case store.SchedProject, store.SchedRoom:
		if project == "" {
			project = p.Project
		}
		if project == "" {
			return store.Scope{}, http.StatusBadRequest,
				"no project on this request and none on this credential, so there is nothing to scope to"
		}
		if !p.CanReachProject(project) {
			return store.Scope{}, http.StatusForbidden, "you cannot reach project " + project
		}
		if kind == store.SchedProject {
			if room != "" {
				return store.Scope{}, http.StatusBadRequest,
					"project scope takes no room - name the room and the scope is room"
			}
			return store.ProjectScope(project), 0, ""
		}
		if room == "" {
			return store.Scope{}, http.StatusBadRequest, "room scope needs a room"
		}
		return store.RoomScope(project, room), 0, ""

	default:
		return store.Scope{}, http.StatusBadRequest,
			kind + " is not a scope (fleet, project, room)"
	}
}

// GET /api/schedules?scope=...[&project=][&room=] - what is SET at exactly one
// scope.
//
// It lists what was written, so a signal with no row is ABSENT rather than
// present-and-off. Those are different states everywhere else in this design,
// and this list is where a person first learns which one they are looking at.
func (s *server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope, code, why := s.scheduleScope(r, q.Get("scope"), q.Get("project"), q.Get("room"))
	if code != 0 {
		writeJSON(w, code, errorBody(why))
		return
	}

	rows, err := s.db.ListSchedules(r.Context(), scope)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scope_kind": scope.Kind,
		"scope_id":   scope.ID,
		"signals":    store.Signals,
		"schedules":  rows,
	})
}

// scheduleWrite is a PUT body. Scope arrives in the body rather than the query
// because it is part of what is being written, not a filter on it.
type scheduleWrite struct {
	Scope    string `json:"scope"`
	Project  string `json:"project"`
	Room     string `json:"room"`
	Signal   string `json:"signal"`
	Realtime bool   `json:"realtime"`
	Cron     string `json:"cron"`
}

// PUT /api/schedules - write one scope's setting for one signal.
//
// A cron that cannot be read or cannot ever fire is a 400 carrying the parser's
// sentence. That refusal is the point of the whole slice: a schedule that saves,
// displays, and never fires is worse than one rejected, because the display is
// evidence that it works.
func (s *server) handlePutSchedule(w http.ResponseWriter, r *http.Request) {
	var body scheduleWrite
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body is not JSON: "+err.Error()))
		return
	}

	scope, code, why := s.scheduleScope(r, body.Scope, body.Project, body.Room)
	if code != 0 {
		writeJSON(w, code, errorBody(why))
		return
	}

	row, err := s.db.PutSchedule(r.Context(), store.Schedule{
		Scope:    scope,
		Signal:   body.Signal,
		Realtime: body.Realtime,
		Cron:     body.Cron,
	}, whoWrote(r))
	if err != nil {
		// Every refusal the store makes here is about what the caller
		// sent - an unknown signal, an unreadable cron, one that can
		// never fire - so it is a 400 with the store's own words. A
		// database failure would not have reached this line with a
		// message worth showing, and is the case the log is for.
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, row)
}

// DELETE /api/schedules/{signal}?scope=... - stop overriding, inherit again.
//
// Answering 404 for a scope that had no row is deliberate: "there was nothing
// to remove" and "the override is gone" are different outcomes, and a console
// that cannot tell them apart cannot tell a person whether the thing they were
// looking at was ever theirs.
func (s *server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope, code, why := s.scheduleScope(r, q.Get("scope"), q.Get("project"), q.Get("room"))
	if code != 0 {
		writeJSON(w, code, errorBody(why))
		return
	}

	signal := r.PathValue("signal")
	removed, err := s.db.DeleteSchedule(r.Context(), scope, signal)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	if !removed {
		writeJSON(w, http.StatusNotFound, errorBody(
			scope.String()+" has no row for "+signal+", so it was already inheriting"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scope_kind": scope.Kind,
		"scope_id":   scope.ID,
		"signal":     signal,
		"inheriting": true,
	})
}

// GET /api/schedules/resolved[?project=&room=] - what a reader here actually
// receives, for every signal.
//
// THE VIEW WORTH CHECKING. Each entry says which scope answered, because an
// inherited fleet default and a room's own setting are identical in every other
// field - without from_kind the resolved view can be read but not checked.
//
// It resolves for the CALLER's own position rather than taking an agent name.
// A door that resolves "what would orchestrator receive" for anyone who asks is
// a door that reports another seat's settings, and the console's own use is
// always the reader in front of it. Resolving for somebody else is a question
// worth a separate door with its own guard, and it does not exist yet.
func (s *server) handleResolvedSchedules(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if p == nil {
		writeJSON(w, http.StatusUnauthorized, errorBody("no principal on this request"))
		return
	}
	q := r.URL.Query()
	project := q.Get("project")
	if project == "" {
		project = p.Project
	}
	if project != "" && !p.CanReachProject(project) {
		writeJSON(w, http.StatusForbidden, errorBody("you cannot reach project "+project))
		return
	}
	room := q.Get("room")

	out := make([]store.Resolved, 0, len(store.Signals))
	for _, signal := range store.Signals {
		resolved, err := s.db.ResolveSchedule(r.Context(), project, room, signal)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
			return
		}
		out = append(out, resolved)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  project,
		"room":     room,
		"resolved": out,
	})
}

// whoWrote is what goes on the row's updated_by: the AGENT id when the
// credential names one, the user id otherwise.
//
// IT IS AN ID AND NOT A NAME, deliberately. The first version of this read a
// name out of an X-Flowy-Agent header - a header nothing in this program sends,
// so it would have been dead code that looked like attribution and quietly fell
// through to the id on every call. An id the console already resolves is worth
// more than a name nobody sets.
func whoWrote(r *http.Request) string {
	p := principalOf(r)
	if p == nil {
		return ""
	}
	if p.AgentID != "" {
		return p.AgentID
	}
	return p.UserID
}
