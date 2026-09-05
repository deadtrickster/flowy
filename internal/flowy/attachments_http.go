package flowy

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
	// BEFORE THE GENERIC ERROR, because ErrNoBytes is an ANSWER and not a
	// fault. ReadAttachment returns it as a non-nil error (attachments.go:137),
	// so the `if err != nil` below used to take it and reply 500 - and the
	// clause further down that exists to say "not on this node" politely could
	// never run, because err was always nil by the time it was reached. Only
	// `content == nil` did the work there, which is why the handler read as
	// correct: a different test caught the same case for a different reason.
	//
	// The distinction is the whole point of the sentinel. store.ErrNoBytes is
	// deliberately not ErrNotFound, and attachrefs.ts names all three answers a
	// reader can get - may not read it, bytes are elsewhere, not a picture. A
	// 500 collapses that into "something went wrong", which tells a reader
	// nothing and writes a red line in the log for an ordinary state: an
	// attachment replicated here without its payload.
	noBytes := errors.Is(err, store.ErrNoBytes)
	if err != nil && !noBytes {
		serverError(w, r, err)
		return
	}

	body := map[string]any{"item": art}
	if noBytes || content == nil {
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
