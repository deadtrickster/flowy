package flowy

// WHOSE MOVE IT IS, over HTTP.
//
// THE RULES ARE ALL IN THE STORE - internal/store/todowaiting.go, which is
// where the resolution of a self-name, the clock reading and the clearing rule
// live. This file is argument checking, a view and status codes, which is the
// shape priority.go has and for its reason: an operator asking in the console,
// an agent asking over MCP and the nag counting the answer must not reach three
// ideas of what waiting means.
//
// ONE DOOR. The value rides on the row and every list already returns it, so a
// read door would answer what the caller is holding - priority.go's argument,
// unchanged.
//
// A REFUSAL IS ABOUT THE ROW. Read permission is the whole bar: an id you
// cannot read is answered as an id that is not there. Naming somebody in this
// field hands them nothing, exactly as the assignee field hands them nothing -
// the permission filter never looks at either.

import (
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// waitingRequest is what asking takes. An empty WaitingOn clears it, which is
// how a question is withdrawn.
type waitingRequest struct {
	WaitingOn string `json:"waiting_on"`
	Asked     string `json:"asked"`
}

// waitingView is the row as it now stands, and the two facts separately.
//
// Neither is omitempty: "" is a real answer here - nobody owes anything - and a
// key that vanished when empty would make a row waiting on nobody
// indistinguishable from an older node that does not carry the field at all.
// That distinction is the whole subject of the row this implements.
type waitingView struct {
	Item      *store.Artifact `json:"item"`
	WaitingOn string          `json:"waiting_on"`
	Asked     string          `json:"asked"`
}

// handleTodoWaiting records that a work item is waiting on somebody, or takes
// that back.
//
// POST /api/todo/{id}/waiting-on  {waiting_on, asked}
//
// It does NOT touch the assignee, and that is the point rather than an
// omission: the two ways of saying this before were handing the row over, which
// says they are carrying work they are only answering, and leaving it with the
// question in a note, which says nothing a counter can read.
func (s *server) handleTodoWaiting(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req waitingRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	art, _, err := s.db.SetWaitingOn(r.Context(), p, r.PathValue("id"), req.WaitingOn, req.Asked)
	if err != nil {
		s.writeQueueError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, &waitingView{
		Item: art, WaitingOn: store.WaitingOnOf(art), Asked: store.AskedOf(art),
	})
}
