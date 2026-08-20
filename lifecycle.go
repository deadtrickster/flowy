package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// The issue lifecycle.
//
// A bug moves open -> triaged -> in-progress -> in-review -> done, and may leave
// the line at any point for one of the two terminal exits: wont-fix and
// duplicate. Nothing skips a step, because a status that can jump is a status
// nobody trusts - "in-review" has to mean the work happened.
//
// Every move appends an event of type 'status' naming the artifact, the actor
// and the move itself ("open->triaged"), with the previous status event as its
// parent. That chain is the audit trail: it is in the same log as the chat and
// the handoffs, it carries the same clock, and reading it in order tells you who
// moved the issue and when without a second table.
const statusEventType = "status"

// Statuses.
const (
	statusOpen       = "open"
	statusTriaged    = "triaged"
	statusInProgress = "in-progress"
	statusInReview   = "in-review"
	statusDone       = "done"
	statusWontFix    = "wont-fix"
	statusDuplicate  = "duplicate"
)

// statusFlow is the line: each status and the one that follows it.
var statusFlow = map[string]string{
	statusOpen:       statusTriaged,
	statusTriaged:    statusInProgress,
	statusInProgress: statusInReview,
	statusInReview:   statusDone,
}

// statusExits are the ways out of the line. They are reachable from anywhere
// that is not already terminal: a bug is found to be a duplicate at triage as
// often as in review.
var statusExits = []string{statusWontFix, statusDuplicate}

// terminalStatus is where a lifecycle stops. Nothing moves out of one - an issue
// that comes back is reopened by being worked on again, and the trail should say
// so rather than quietly rewinding.
var terminalStatus = map[string]bool{
	statusDone:      true,
	statusWontFix:   true,
	statusDuplicate: true,
}

// lifecycleTypes are the artifact types that move through THIS workflow. A
// transcript has no status to move, and a queue item has a lifecycle of its own
// - todo, active, done, with reopening - so it is not dragged through the issue
// line. See store.SetTodoStatus, and queueStatusMove below for the door.
//
// It used to say a memory has no lifecycle at all, and that sentence was the
// defect: a todo is a memory item, and the one artifact whose entire purpose is
// to be finished was the one that could not be moved. What was missing was never
// a type. It was a verb and a vocabulary, and the vocabulary the queue already
// reads is not this one.
//
// finding behaves exactly like bug: open -> triaged -> in-progress -> in-review
// -> done, exiting to wont-fix or duplicate same as any of them. It gets no
// column of its own - Kind, Severity, Tags, Related, Discovery and Fields
// already exist on Artifact and already carry what a finding needs to say - so
// the only change a finding required here was one more word in this set.
var lifecycleTypes = map[string]bool{
	"bug": true, "feature": true, "note": true, "task": true, "finding": true,
}

// nextQueueStatuses is everywhere a queue item at from may go: the other two.
//
// The current one is left out because this is what a dropdown is drawn from and
// a move to where it already is changes nothing a reader could see - the verb
// takes a restatement (it is somebody saying the work still stands) but a
// console offering it as a choice would be offering a no-op.
//
// Nothing is terminal here. Done is a claim about the work, and work that turns
// out not to be done is REOPENED rather than refiled - the trail of what
// actually happened to it is the thing a fresh row would lose.
func nextQueueStatuses(from string) []string {
	out := []string{}
	for _, s := range store.QueueStatuses {
		if s != from {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// knownStatus reports whether s is one of the seven.
func knownStatus(s string) bool {
	if _, ok := statusFlow[s]; ok {
		return true
	}
	return terminalStatus[s]
}

// statusOf reads an artifact's current status, treating a row that was never
// given one as open. An artifact created without a status is a fresh issue.
func statusOf(art *store.Artifact) string {
	if s := strings.TrimSpace(art.Status); s != "" {
		return s
	}
	return statusOpen
}

// nextStatuses is everywhere an artifact at from may go, in a stable order so
// the console can draw them and the error message reads the same twice.
func nextStatuses(from string) []string {
	if terminalStatus[from] {
		// Empty rather than nil: a console asking where an issue can go next
		// should be told "nowhere", not handed a null to guard against.
		return []string{}
	}
	out := append([]string{}, statusExits...)
	if next, ok := statusFlow[from]; ok {
		out = append(out, next)
	}
	sort.Strings(out)
	return out
}

// canTransition reports whether from -> to is a move the workflow allows.
func canTransition(from, to string) bool {
	for _, allowed := range nextStatuses(from) {
		if allowed == to {
			return true
		}
	}
	return false
}

// statusRequest is the body of a transition.
type statusRequest struct {
	Status string `json:"status"`
	// Note is what was measured, for a move that closes a queue item: the same
	// text POST /api/todo/{id}/note takes, written in the same transaction as
	// the closure rather than in a call beside it. Optional to the decoder and
	// required by the verb - see store.SetTodoStatus, which is where a close
	// with nothing said is refused, so that the console, the CLI, the TUI and
	// MCP cannot reach four ideas of what closing a row records.
	//
	// The issue workflow below ignores it: a bug's trail is its own, and notes
	// hang off queue items. That is the lifecycle the count was taken on.
	Note string `json:"note"`
	// ReplacedBy is the row that survives, when this close is a deduplication.
	//
	// It writes the same edge a superseding report writes - see
	// store.SupersedesField - rather than a second relation meaning the same
	// thing: "this is a duplicate of that" and "this is replaced by that" are
	// one directed edge with different words on it, and every reader that
	// already draws "replaced by" draws this for free.
	//
	// Only with status done, and only for a queue item. See
	// store.CloseAsDuplicate, which refuses the pair that closes into each
	// other.
	ReplacedBy string `json:"replaced_by"`
}

// handleArtifactStatus moves an artifact through the workflow and records the
// move.
//
// Anybody who can read the artifact can move it, which for a shared bug means
// the person it was assigned to as well as the project it lives in. That is the
// point of the assignment: the share is what makes them a participant, and a
// participant who cannot say "I am working on this" has to ask somebody else to
// say it for them. A queue item is the same rule and the same reason - read
// permission and nothing else - which is the ruling that made assignment
// collaborative, one field along. See store.SetTodoStatus.
//
// WHICH LIFECYCLE a row is in is a property of the row, so the status word is
// checked against that lifecycle and not against the union of both: this door
// used to check the word first and answer 400 for "done" on a todo before it had
// looked at what it was holding. The read moved above it for that reason.
//
// POST /api/artifact/{id}/status  {status}
func (s *server) handleArtifactStatus(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	id := r.PathValue("id")

	var req statusRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	to := strings.TrimSpace(req.Status)

	art, err := s.db.ReadArtifact(ctx, p, id, false)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such artifact"))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	// The queue's own lifecycle, and its own verb. Everything this handler does
	// below - the line, the terminal states, the trail event - is the issue
	// workflow's, and a todo is not in it.
	if store.IsQueueItem(art) {
		s.queueStatusMove(w, r, art, to, req.Note, req.ReplacedBy)
		return
	}
	if !knownStatus(to) {
		writeJSON(w, http.StatusBadRequest, errorBody(
			"status must be one of open, triaged, in-progress, in-review, done, wont-fix, duplicate"))
		return
	}
	if !lifecycleTypes[art.Type] {
		writeJSON(w, http.StatusBadRequest,
			errorBody("a "+whatItIs(art)+" has no lifecycle; bug, feature, note, task and finding do, "+
				"and a queue item has one of its own ("+strings.Join(store.QueueStatuses, ", ")+")"))
		return
	}

	from := statusOf(art)
	if !canTransition(from, to) {
		next := nextStatuses(from)
		msg := from + " is terminal"
		if len(next) > 0 {
			msg = "from " + from + " the workflow allows " + strings.Join(next, ", ")
		}
		writeJSON(w, http.StatusConflict, errorBody("cannot move "+from+" -> "+to+": "+msg))
		return
	}

	event, err := s.statusEvent(r, art, from, to)
	if err != nil {
		serverError(w, r, err)
		return
	}
	// The move and the entry that records it are one write. A status with no
	// entry behind it is a lifecycle nobody can audit, and it is not something
	// anything here would ever notice or repair.
	if err := s.db.MoveArtifactStatus(ctx, art, to, event); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifact": art, "event": event})
}

// whatItIs is how a refusal names the row it refused: the type, narrowed by the
// kind when it has one. "a memory has no lifecycle" was true of a note and false
// of a todo, and told the reader of the sentence which of the two they were
// holding either way.
func whatItIs(art *store.Artifact) string {
	if art.Kind != "" {
		return art.Type + "/" + art.Kind
	}
	return art.Type
}

// queueStatusMove is this door for a queue item: the same route, the same
// request body and the same answer, over the queue's own vocabulary and the
// queue's own verb.
//
// It is four lines because the rules are in the store, which is the shape the
// assignment surface already has and for the same reason: an operator clicking
// "done" in the console, an agent closing its own work over MCP and a drainer
// posting here must not be able to reach three ideas of when work is finished.
// store.SetTodoStatus is the only thing in this program that moves one.
//
// The answer is {artifact, event}, which is what this route has always answered
// with - the console reads one shape whichever lifecycle the row is in.
func (s *server) queueStatusMove(
	w http.ResponseWriter, r *http.Request, art *store.Artifact, to, note, replacedBy string,
) {
	p := principalOf(r)
	// A CLOSE THAT NAMES A SURVIVOR IS A DEDUPLICATION, which is the same verb
	// with an edge on it - see store.CloseAsDuplicate. Naming one on any other
	// move is refused rather than ignored: a caller who asked for something the
	// node did not do should hear about it, and "replaced_by on a row going
	// active" is a mistake with no sensible reading.
	if strings.TrimSpace(replacedBy) != "" {
		if to != store.DoneStatus {
			writeJSON(w, http.StatusBadRequest, errorBody(
				"replaced_by says which row survives a duplicate, so it only goes with "+
					"status done - this move is to "+to))
			return
		}
		art, event, err := s.db.CloseAsDuplicate(r.Context(), p, art.ID, replacedBy, note)
		if err != nil {
			s.writeQueueError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"artifact": art, "event": event})
		return
	}
	art, event, err := s.db.SetTodoStatus(r.Context(), p, art.ID, to, note)
	if err != nil {
		s.writeQueueError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifact": art, "event": event})
}

// handleArtifactHistory is the trail: every status move on this artifact, in
// order, with the parent links that chain them.
//
// It is gated on reading the artifact rather than on reading each event,
// because the trail is a property of the artifact - showing a reader only the
// half of it that was written from their own project would be a history that
// disagrees with itself depending on who asks.
//
// GET /api/artifact/{id}/history
func (s *server) handleArtifactHistory(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	id := r.PathValue("id")

	art, err := s.db.ReadArtifact(ctx, p, id, scopeAll(r, p))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such artifact"))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}

	// A queue item's trail is its own entries, in its own vocabulary. Reading the
	// issue workflow's type here would answer "no moves" for a todo that has been
	// closed and reopened twice - a history that is empty because it was asked
	// the wrong question, which reads exactly like a history that is empty.
	eventType, at := statusEventType, statusOf(art)
	next := nextStatuses(at)
	if store.IsQueueItem(art) {
		eventType, at = store.EventTodoStatus, store.TodoStatusOf(art)
		next = nextQueueStatuses(at)
	}

	events, err := s.db.ArtifactEvents(ctx, art.ID, eventType)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"artifact": art.ID,
		"status":   at,
		"next":     next,
		"events":   events,
	})
}

// statusEvent builds the move's entry in the log, as a child of the move before
// it. The event lands in the artifact's project rather than the actor's: the
// trail belongs with the thing it is about, and an assignee moving a shared bug
// from another project should not fork its history into a second log.
//
// It is built rather than written: the move and its entry go in together, in
// MoveArtifactStatus.
func (s *server) statusEvent(
	r *http.Request, art *store.Artifact, from, to string,
) (*store.Event, error) {
	return s.statusEventVia(r, art, from, to, nil)
}

// statusEventVia is statusEvent with extra meta on the event. Phase 6 uses it
// to mark the one move that does not come from the workflow: an issue closed on
// a forge moves its artifact to done, and the trail has to say so.
func (s *server) statusEventVia(
	r *http.Request, art *store.Artifact, from, to string, extra map[string]string,
) (*store.Event, error) {
	p := principalOf(r)
	actor, kind := chatActor(p)

	fields := map[string]string{
		"actor_kind": kind,
		"actor_user": p.UserID,
		"from":       from,
		"to":         to,
	}
	for k, v := range extra {
		fields[k] = v
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}

	// The chain: the previous status event is this one's parent, and the thread
	// is the one it opened. An artifact whose status has never moved starts
	// both here.
	parents, thread := []string{}, ""
	last, err := s.db.LatestArtifactEvent(r.Context(), art.ID, statusEventType)
	switch {
	case err == nil:
		parents, thread = []string{last.ID}, last.Thread
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}

	return &store.Event{
		Type:     statusEventType,
		Project:  art.Project,
		Thread:   thread,
		Parents:  parents,
		Actor:    actor,
		Artifact: art.ID,
		Body:     from + "->" + to,
		Meta:     json.RawMessage(meta),
	}, nil
}
