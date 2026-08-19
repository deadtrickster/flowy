package main

// REACTIONS over HTTP: put an emoji on a message, take your own off, and read
// what is on a page of them.
//
// The rules are in the store - see internal/store/reactions.go - so this file
// is argument checking and status codes, the same shape pins.go has and for the
// same reason: an agent going through one door and the console going through
// another must not reach two ideas of what is on a message.

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

type reactRequest struct {
	Message string `json:"message"`
	Emoji   string `json:"emoji"`
	// On is what the caller wants to be true afterwards, and it defaults to
	// TRUE because the absent value has to mean the common case. A body that
	// omits it is somebody adding a reaction, which is what almost every call
	// is; making the zero value "remove" would turn a forgotten field into a
	// silent retraction of somebody's ack.
	On *bool `json:"on"`
}

// reactionRefusal maps the store's refusals onto status codes, so a caller
// asking for something they may not have is not told the node is broken.
func reactionRefusal(w http.ResponseWriter, r *http.Request, err error) {
	var refused store.ReactionError
	switch {
	case errors.As(err, &refused):
		writeJSON(w, http.StatusBadRequest, errorBody(refused.Why))
	default:
		serverError(w, r, err)
	}
}

// POST /api/chat/{room}/react  {message, emoji, on}
func (s *server) handleRoomReact(w http.ResponseWriter, r *http.Request) {
	room := r.PathValue("room")
	var req reactRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	on := true
	if req.On != nil {
		on = *req.On
	}
	e, err := s.db.React(r.Context(), principalOf(r), room, req.Message, req.Emoji, on)
	if err != nil {
		reactionRefusal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}
