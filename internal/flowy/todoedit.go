package flowy

// The edit door: correct a todo nobody has started, and be TOLD if somebody
// starts it while you are typing.
//
// POST /api/todo/{id}/edit  {saw, title?, body?}
// GET  /api/todo/{id}/edits
//
// saw is required and is the status the editor READ. It is not ceremony: it is
// the whole feature. The write underneath is a compare-and-set against it, so a
// todo that went active between the read and the POST refuses the edit instead
// of landing on top of whatever the agent who picked it up is working from. See
// internal/store/todoedit.go, where the argument is written down.
//
// A CONTESTED EDIT IS 409, not 400. The request was well formed and it may be
// made again - after the editor has talked to whoever took the row, which is
// what the message tells them to do. 400 would say they asked wrongly, and a
// client retrying on it would be retrying the one thing that cannot succeed.
//
// title and body are pointers so that "said nothing" and "said empty" are
// different requests. A body may be emptied; the verb refuses an emptied title,
// because a todo nobody can find again is worse than one whose title is wrong.

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// editRequest is what the door takes. Every field is a pointer except the one
// that is required, so an absent title is distinguishable from an empty one -
// see the head of this file.
type editRequest struct {
	Saw   string  `json:"saw"`
	Title *string `json:"title,omitempty"`
	Body  *string `json:"body,omitempty"`
}

// handleTodoEdit rewrites a todo's words, against the state the editor saw.
//
// The author is the only principal who may: an item's words are its author's,
// and that ruling is older than this door - see memWriteQueueOnly. What is new
// is that the author's own edit is now guarded, because the person who loses
// most from a silent overwrite is whoever picked the row up.
func (s *server) handleTodoEdit(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req editRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	art, entry, err := s.db.EditTodo(r.Context(), p, r.PathValue("id"), req.Title, req.Body, req.Saw)
	if err != nil {
		s.writeEditError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": art, "entry": entry})
}

// handleTodoEdits reads the edits back: what the todo used to say, who changed
// it, and which state each change was written against.
//
// GET /api/todo/{id}/edits
func (s *server) handleTodoEdits(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	art, err := s.db.ReadWorkItem(r.Context(), p, r.PathValue("id"))
	if err != nil {
		s.writeQueueError(w, r, err)
		return
	}
	log, err := s.db.TodoEditLog(r.Context(), p, art.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"todo": art.ID, "edits": log})
}

// writeEditError maps this verb's two refusals and hands the rest to the
// mapping the other queue doors already use.
//
// A todo that moved is 409: well formed, and worth making again once the editor
// has spoken to whoever took it. Somebody else's words are 403 rather than 404 -
// the reader can SEE the item, so pretending it is not there would be a worse
// lie than saying it is not theirs, and the message names the doors that do
// work for a principal who only wants the work moved.
func (s *server) writeEditError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		moved store.ErrTodoMoved
		mine  store.ErrNotYoursToEdit
	)
	switch {
	case errors.As(err, &moved):
		s.writeRefusal(w, r, http.StatusConflict, err, moved.Error())
	case errors.As(err, &mine):
		s.writeRefusal(w, r, http.StatusForbidden, err, mine.Error())
	default:
		s.writeQueueError(w, r, err)
	}
}
