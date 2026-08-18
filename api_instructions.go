package main

// The doors for instructions - see internal/store/instructions.go for why they
// are rows at all.
//
// GET is the one that matters. An agent asks what applies to it and gets an
// ORDERED list, not a merged document: node rules, then project rules, then its
// own. Composition is the reader's, because the failure being fixed is that
// today the winner is whichever file loaded last and nobody can say which rule
// bound them.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// InstructionRoom is where the write event lands, beside the quiesce room's
// precedent: a rule changing is news, and news belongs in a room somebody can
// watch rather than only in a row somebody must remember to re-read.
const InstructionRoom = "instructions"

type instructionRequest struct {
	Scope string `json:"scope"`
	Seat  string `json:"seat"`
	Title string `json:"title"`
	Body  string `json:"body"`
	// Supersedes names the instruction this one replaces. The old row stays,
	// signed, and reads as replaced - which is how "who decided this and when"
	// survives an edit. See SupersedesField.
	Supersedes string `json:"supersedes"`
}

func (s *server) handleWriteInstruction(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req instructionRequest
	if err := decodeStrict(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}

	fields := map[string]string{store.ScopeField: strings.TrimSpace(req.Scope)}
	if seat := strings.TrimSpace(req.Seat); seat != "" {
		fields[store.SeatField] = seat
	}
	if sup := strings.TrimSpace(req.Supersedes); sup != "" {
		fields[store.SupersedesField] = sup
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		serverError(w, r, err)
		return
	}

	// An instruction lands in the principal's own project and is visible to it,
	// exactly as an announcement does. What makes a node-scoped one node-scoped
	// is its scope, not a wider readership.
	project, visibility := homeProject(p), store.VisibilityShared
	if project == nil {
		visibility = store.VisibilityPersonal
	}

	art := &store.Artifact{
		Type:       store.InstructionType,
		Kind:       strings.TrimSpace(req.Scope),
		Project:    project,
		OwnerUser:  p.UserID,
		Title:      strings.TrimSpace(req.Title),
		Body:       req.Body,
		Visibility: visibility,
		Fields:     encoded,
	}

	// A SEAT-SCOPED RULE FOR SOMEBODY ELSE IS NOT AN INSTRUCTION, IT IS AN
	// ORDER. The operator may write one for any seat - that is what running the
	// node means - and an agent may write its own. Anything else is one agent
	// binding another, which this fleet does through the board, where it can be
	// argued with.
	if store.InstructionScopeOf(art) == store.ScopeSeat && !p.Operator {
		if actor, _ := chatActor(p); actor != store.InstructionSeatOf(art) {
			writeJSON(w, http.StatusForbidden, errorBody(
				"a seat-scoped instruction for somebody else is an order, not a rule - "+
					"the operator writes those, or raise it on the board"))
			return
		}
	}

	event, err := s.instructionEvent(r, art)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := s.db.WriteInstruction(r.Context(), art, event); err != nil {
		// The store's own sentences say what is wrong with the row - an unknown
		// scope, a seat rule naming no seat, a project rule outside a project -
		// and they are better than anything this handler could restate.
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"instruction": art})
}

// handleReadInstructions answers what binds the caller, widest scope first.
func (s *server) handleReadInstructions(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	list, err := s.db.InstructionsFor(r.Context(), p)
	if err != nil {
		serverError(w, r, err)
		return
	}
	// EACH ROW CARRIES ITS SCOPE AND ITS ID, so a reader can say which rule
	// bound it. That is the whole product: an agent citing "the node rule,
	// 01M0..." is something a file on disk could never support.
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, map[string]any{
			"id":    a.ID,
			"scope": store.InstructionScopeOf(a),
			"seat":  store.InstructionSeatOf(a),
			"title": a.Title,
			"body":  a.Body,
			"since": a.Created,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"instructions": out,
		// SAID OUT LOUD RATHER THAN IMPLIED. A caller that merges these itself
		// should know the node did not, and a caller that finds two rules
		// contradicting should know that is the answer rather than a fault.
		"composed": false,
		"order":    []string{store.ScopeNode, store.ScopeProject, store.ScopeSeat},
	})
}

// instructionEvent is who decided this, and when - the question a file on disk
// cannot answer.
func (s *server) instructionEvent(r *http.Request, art *store.Artifact) (*store.Event, error) {
	p := principalOf(r)
	actor, kind := chatActor(p)

	meta, err := json.Marshal(map[string]string{
		"actor_kind": kind,
		"actor_user": p.UserID,
		"scope":      store.InstructionScopeOf(art),
		"seat":       store.InstructionSeatOf(art),
	})
	if err != nil {
		return nil, err
	}
	return &store.Event{
		Type:    store.EventInstruction,
		Room:    InstructionRoom,
		Parents: []string{},
		Actor:   actor,
		Body:    "wrote a " + store.InstructionScopeOf(art) + " instruction: " + art.Title,
		Meta:    meta,
	}, nil
}
