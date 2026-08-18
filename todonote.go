package main

// WHAT WAS LEARNED ABOUT A ROW, over HTTP.
//
// POST /api/todo/{id}/note   {note}
// GET  /api/todo/{id}/notes
//
// THE RULES ARE ALL IN THE STORE - see internal/store/todonote.go, which is
// where an append differs from an edit and why - so this file is argument
// checking, a view and status codes. store.AppendTodoNote is the only thing in
// this program that writes one.
//
// WHY THIS IS NOT THE EDIT DOOR NEXT TO IT. An edit rewrites the author's words
// and is refused to everybody else and refused once the work has started, both
// on purpose. A note adds a second person's words BESIDE the author's, changes
// nothing already written, and is most worth writing while the work is under
// way: the agent carrying the row measures something, works out the fix shape,
// finds what it is blocked on. Read permission is the whole bar, which is the
// ruling the assignee, the status and the category already run on - what is
// learned about a row is not authorship of it.
//
// A REFUSAL HERE IS ABOUT THE ROW OR ABOUT THE TEXT, and nothing else. An id the
// caller cannot read is answered as an id that is not there, which is every
// queue door's answer. An empty note and a projectless row are 400 and say why -
// the second one is the case worth reading, because the write would otherwise
// succeed into a place only its writer can see.

import (
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// noteRequest is what appending takes. One field, and it is required: there is
// no version of this call that says nothing.
type noteRequest struct {
	Note string `json:"note"`
}

// noteView is one row's notes as both doors here hand them back: the item, with
// the notes on it, and the same entries at the top level.
//
// The top-level list is a second copy of what is already on the item,
// deliberately. The item carries them because a client that reads a row should
// not have to know this door exists - see the Notes field - and the list is up
// here because a client that called THIS door asked about the notes and should
// not have to dig into the row for the answer it asked for. Both come out of one
// read, so they cannot disagree.
type noteView struct {
	Item  *store.Artifact   `json:"item"`
	Notes []store.NoteEntry `json:"notes"`
}

// viewNotes assembles it off the row the caller already has, with no second
// query: both doors here reach the row through ReadWorkItem, which is the
// permission-filtered read that fills the notes on it, and the append puts the
// entry it just wrote on the row before returning. A read of the log here would
// be the same rows again, asked a second time, with a window between the two in
// which the answers disagree.
//
// The list is never null, even when there is nothing to say. A client
// distinguishing "no notes" from "this endpoint does not carry notes" off a null
// is the ambiguity the two shapes exist to remove.
func viewNotes(art *store.Artifact) *noteView {
	notes := art.Notes
	if notes == nil {
		notes = []store.NoteEntry{}
	}
	return &noteView{Item: art, Notes: notes}
}

// handleTodoNote adds a note to a row - any row the caller can read, wherever it
// was raised and whoever wrote it.
//
// POST /api/todo/{id}/note  {note}
//
// It hands nobody anything: a note is an event on the todo, read back by exactly
// the todo's readers, so a principal who cannot see the row gets the 404 a read
// would have given.
func (s *server) handleTodoNote(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req noteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	art, _, err := s.db.AppendTodoNote(r.Context(), p, r.PathValue("id"), req.Note)
	if err != nil {
		s.writeQueueError(w, r, err)
		return
	}
	// No FillDerived here, unlike the doors that CREATE a row: the append reads
	// the row through the permission-filtered read first, so the assignee, the
	// category and the raiser are already on what it hands back - and so are the
	// notes, with the one just written appended to them.
	writeJSON(w, http.StatusOK, viewNotes(art))
}

// handleTodoNotes reads them back: what has been learned about the row, oldest
// first, with who wrote each one.
//
// GET /api/todo/{id}/notes
func (s *server) handleTodoNotes(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	art, err := s.db.ReadWorkItem(r.Context(), p, r.PathValue("id"))
	if err != nil {
		s.writeQueueError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewNotes(art))
}
