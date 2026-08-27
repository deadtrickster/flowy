package flowy

// WRITING AN ATTACHMENT OVER HTTP, so that a person can do what an agent could.
//
// GET /api/attachment/{id} has served the bytes since attachments existed, a
// message carries them, and MessageList draws a card for each. The only way to
// CREATE one was attachment_write on the MCP surface - so the console renders a
// card it has no way to produce, and the operator could read a capture an agent
// made and never make one.
//
// That is a sharper thing than a missing feature: the write side exists and is
// reachable by exactly one kind of principal. See 01M0EE6W1N.
//
// The rules are NOT here. writeAttachmentFrom in mcp_attachments.go is what
// writing an attachment is - the scope it is born at, whether the message is
// readable, what a claimed content type may say, what a filename may steer -
// and both doors call it, exactly as POST /api/chat/{room}/say and chat_say
// both call sayInRoom. A second implementation would be a second answer to
// every one of those questions, and the two would drift the first time one was
// fixed.

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// handleWriteAttachment stores bytes and the row that names them.
//
// BASE64 IN JSON RATHER THAN MULTIPART, and it is the same shape the MCP door
// takes. The console is the caller, a browser encodes a File without help, and
// one body format means the ceiling and the decode error are the same sentence
// at both doors. Multipart would be a second parser for the same argument.
//
// POST /api/attachment
func (s *server) handleWriteAttachment(w http.ResponseWriter, r *http.Request) {
	var req attachmentWriteArgs
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	written, err := writeAttachmentFrom(r.Context(), s.db, principalOf(r), req)
	if err != nil {
		attachmentRefusal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"item":          written.Item,
		"size_bytes":    written.Size,
		"digest_sha256": written.Digest,
		"content_type":  written.ContentType,
	})
}

// attachmentRefusal maps the shared path's refusals onto status codes.
//
// Everything writeAttachmentFrom refuses is the CALLER asking for something
// they may not have - a message they cannot read, a scope their token cannot
// write, bytes past the ceiling, a filename with a path in it - and every one
// of them is a plain error carrying its own sentence. So they go back as 400
// with that sentence rather than as 500 with nothing: a caller told the node is
// broken retries the one thing that will never work, which is what the reaction
// ceiling did before it was mapped.
//
// A store failure is the one that is genuinely this node's fault and it keeps
// its 500 - told apart by ErrNotFound and by the store's own error, not by
// guessing from the text.
func attachmentRefusal(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		// An unreadable message and one that is not here answer the same way,
		// which is the rule every read door on this node keeps.
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
}
