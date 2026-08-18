package main

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// The room doors. A room used to be whatever string somebody typed into a
// message, which meant it could not be created before it had traffic, nobody
// could be invited to one, and the console had to carry a hardcoded list of
// which rooms exist. See internal/store/rooms.go.

type createRoomRequest struct {
	Name  string `json:"name"`
	Topic string `json:"topic"`
}

type inviteRoomRequest struct {
	Principal string `json:"principal"`
}

// POST /api/rooms - make a room, and be its owner.
func (s *server) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var req createRoomRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	room, err := s.db.CreateRoom(r.Context(), p, req.Name, req.Topic)
	// TAKEN IS 409, NOT 400. "that name is in use" and "that name is not a
	// name" send the caller to different places, and a create that collides is
	// usually two people wanting the same room rather than anybody's mistake.
	if errors.Is(err, store.ErrRoomTaken) {
		writeJSON(w, http.StatusConflict, errorBody(err.Error()))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"room": room})
}

// GET /api/rooms - what this principal may see, and their standing in each.
//
// This is what replaces the three names hardcoded in the console at
// web/src/lib/unread.tsx:66. A client that asks the node cannot be wrong about
// which rooms exist; a client with a literal array always eventually is.
func (s *server) handleListRooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := s.db.RoomsFor(r.Context(), principalOf(r))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": rooms})
}

// POST /api/rooms/{room}/invite - an owner adds somebody.
func (s *server) handleInviteRoom(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var req inviteRoomRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	if err := s.db.InviteToRoom(r.Context(), p, r.PathValue("room"), req.Principal); err != nil {
		// The store's refusal already names which of the two things is wrong -
		// the room, or your standing in it - so it is passed through rather
		// than flattened into "forbidden", which is what sends somebody to
		// re-read a token that was never the problem.
		writeJSON(w, http.StatusForbidden, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invited": req.Principal, "room": r.PathValue("room")})
}

// POST /api/rooms/{room}/leave - remove yourself, and only yourself.
func (s *server) handleLeaveRoom(w http.ResponseWriter, r *http.Request) {
	left, err := s.db.LeaveRoom(r.Context(), principalOf(r), r.PathValue("room"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	// Not a member is not an error: the caller wanted to not be in the room and
	// they are not in the room. Saying which happened is still worth doing.
	writeJSON(w, http.StatusOK, map[string]any{"left": left, "room": r.PathValue("room")})
}
