package flowy

// PINS over HTTP: put a message up in a room, take it down, and read what is up.
//
// The rules are in the store - see internal/store/pins.go - so this file is
// argument checking and status codes, the same shape deps.go has and for the
// same reason: an agent going through one door and the console going through
// another must not be able to reach two ideas of what is pinned.

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// pinsView is a room's strip as every surface hands it back: what is up, and
// the log it was folded out of.
//
// The log rides along for depsView's reason. The ids are the derived thing and
// the entries are the record - a reader given only "these three are up" cannot
// answer who pinned them or when, and "who decided this was the decision" is
// most of why a room pins anything.
type pinsView struct {
	Room   string           `json:"room"`
	Pinned []string         `json:"pinned"`
	Log    []store.PinEntry `json:"log"`
}

type pinRequest struct {
	Message string `json:"message"`
}

// pinRefusal maps the store's refusals onto status codes. A refusal that
// arrives as a 500 tells the caller the node is broken when the truth is that
// they asked for something they may not have.
func pinRefusal(w http.ResponseWriter, r *http.Request, err error) {
	var refused store.PinError
	switch {
	case errors.As(err, &refused):
		writeJSON(w, http.StatusBadRequest, errorBody(refused.Why))
	default:
		serverError(w, r, err)
	}
}

// POST /api/chat/{room}/pin  {message}
func (s *server) handleRoomPin(w http.ResponseWriter, r *http.Request) {
	room := r.PathValue("room")
	var req pinRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	e, err := s.db.Pin(r.Context(), principalOf(r), room, req.Message)
	if err != nil {
		pinRefusal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// DELETE /api/chat/{room}/pin/{id}
//
// DELETE, and it appends. The verb describes what the caller wants to happen to
// the strip; the log still gains an entry, because a decision that was pinned
// for a day and then taken down is a different history from one that was never
// pinned, and only the log can tell them apart.
func (s *server) handleRoomUnpin(w http.ResponseWriter, r *http.Request) {
	room, id := r.PathValue("room"), r.PathValue("id")
	e, err := s.db.Unpin(r.Context(), principalOf(r), room, id)
	if err != nil {
		pinRefusal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// GET /api/chat/{room}/pins
func (s *server) handleRoomPins(w http.ResponseWriter, r *http.Request) {
	room := r.PathValue("room")
	p := principalOf(r)
	log, err := s.db.PinLog(r.Context(), p, room)
	if err != nil {
		pinRefusal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pinsView{
		Room:   room,
		Pinned: store.LivePins(log),
		Log:    log,
	})
}
