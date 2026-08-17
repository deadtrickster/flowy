package main

// The HTTP read of an attachment: the route a message's cards point at.
//
// The store answers permission through the same filter as every read, and the
// two honest failures stay two answers: not-there and out-of-reach are the
// same 404 (the reader cannot tell which, by design), while a row the reader
// may see whose bytes are not on this node is a 200 with no content - the card
// still renders, the download says why.

import (
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// GET /api/attachment/{id}
func (s *server) handleAttachmentRead(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	id := r.PathValue("id")

	art, content, err := s.db.ReadAttachment(r.Context(), p, id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such attachment: "+id))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}

	body := map[string]any{"item": art}
	if errors.Is(err, store.ErrNoBytes) || content == nil {
		// Readable row, absent bytes. The content type travels as a claim in
		// fields and never decides how a client renders - same rule the write
		// made - so absence is stated, not inferred from a null.
		body["content"] = nil
		body["bytes"] = "not on this node"
	} else {
		body["content"] = base64.StdEncoding.EncodeToString(content)
	}
	writeJSON(w, http.StatusOK, body)
}
