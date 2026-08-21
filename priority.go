package main

// WHAT TO DO FIRST, over HTTP.
//
// THE RULES ARE ALL IN THE STORE - see internal/store/todopriority.go, which is
// where the vocabulary, the ordering and the reason unjudged outranks `later`
// live. This file is argument checking, a view and status codes, which is the
// shape the category door already has and for its reason: an operator ranking a
// row in the console, an agent ranking its own work and a drainer reading the
// order must not reach three ideas of what the words mean.
//
// ONE DOOR, and deliberately not two. The category has a read door beside its
// write door because a classification carries an argument - who called it a bug
// and who disagreed. A priority is one word that the row itself already carries
// and every list already returns, so a second endpoint to read it back would be
// a call that answers what the caller is holding.
//
// A REFUSAL IS ABOUT THE ROW OR ABOUT THE WORD. Read permission is the whole
// bar: an id you cannot read is answered as an id that is not there. A word
// outside the set is 400 and says the set.

import (
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// priorityRequest is what ranking takes. One field, and an empty one means
// unjudged - which is legal, and is how a ranking is taken back.
type priorityRequest struct {
	Priority string `json:"priority"`
}

// priorityView is what every surface hands back: the row as it now stands, the
// word, and the vocabulary.
//
// The vocabulary rides on the answer for the reason categoryView carries its
// own: a console drawing the control should be told the words by the node
// rather than keep a copy that drifts, and an agent that was just refused reads
// what it may say next out of the same field it was refused from.
type priorityView struct {
	Item     *store.Artifact `json:"item"`
	Priority string          `json:"priority"`
	// Not omitempty, either of them: "" is a real answer here - the row is
	// unjudged - and a field that vanished when it was empty would make an
	// unranked row indistinguishable from an older node that does not rank at
	// all.
	Vocabulary []string `json:"vocabulary"`
}

// handleTodoPriority ranks a work item - a todo or a merge request, whichever
// the id names - or takes its ranking away.
//
// POST /api/todo/{id}/priority  {priority}
//
// It is /todo/ for a merge row as well, which reads odd and is the honest
// spelling: the door is over a WORK ITEM, the store verb reads either kind, and
// a second /merge/{id}/priority would be the same rule in two places for the
// sake of a nicer path. The queue's own doors are the ones about landing.
func (s *server) handleTodoPriority(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req priorityRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	art, _, err := s.db.SetTodoPriority(r.Context(), p, r.PathValue("id"), req.Priority)
	if err != nil {
		s.writeQueueError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, &priorityView{
		Item: art, Priority: store.PriorityOf(art), Vocabulary: store.TodoPriorities,
	})
}
