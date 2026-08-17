package main

// WHAT KIND OF WORK A TODO IS, over HTTP.
//
// THE RULES ARE ALL IN THE STORE - see internal/store/todocategory.go, which is
// where they are and why they are those rules - so this file is argument
// checking, a view, and status codes. That is deliberate and it is the shape the
// assignment and status surfaces already have: an operator picking "bug" in the
// console, an agent classifying its own work over MCP and a drainer posting here
// must not be able to reach three ideas of what the vocabulary is, and the way
// to guarantee that is that none of them holds one. store.SetTodoCategory is the
// only thing in this program that sets a category.
//
// TWO DOORS AND ONE WAY IN.
//
//   - POST /api/todo/{id}/category - any todo you can read, from anywhere.
//   - todo_category over MCP - see mcp_category.go, which is a caller of this
//     path and not a second implementation of it.
//
// There is deliberately no room-scoped door beside these, unlike the assignee.
// The room's door exists because a handover is news the CONVERSATION wants said
// out loud in it; a classification is a fact about the item, the entry it leaves
// is readable by everybody who can read the todo, and a second sentence in the
// room every time somebody corrects a label is noise in the one place this
// fabric cannot afford it. It is also one less door to keep in step.
//
// A REFUSAL HERE IS ABOUT THE TODO OR ABOUT THE WORD, and nothing else. Read
// permission is the whole bar: an id you cannot read is answered as an id that
// is not there. A word outside the vocabulary is 400 and says the vocabulary,
// which is the refusal the closed set exists for.

import (
	"context"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// categoryRequest is what classifying a todo takes. One field, and an empty one
// means unclassified - which is legal, and is how a wrong call is taken back.
type categoryRequest struct {
	Category string `json:"category"`
}

// categoryView is one todo's classification as every surface hands it back: the
// item, what it is, who said so, and the log the answer was folded out of.
//
// The log is in it rather than behind a second call, for assignView's reason.
// The word is the value and the entries are the record: a reader given only
// "bug" cannot answer who called it one, and a reclassification is an argument
// somebody had - which is the whole reason this is an event.
type categoryView struct {
	Item     *store.Artifact             `json:"item"`
	Category string                      `json:"category"`
	Standing *store.TodoCategoryStanding `json:"standing"`
	Log      []store.CategoryEntry       `json:"log"`
	// Vocabulary is the closed set, on every answer. A client that has to know
	// the four words to draw the control should be told them by the node rather
	// than carry its own copy that drifts the day a fifth is added - and an agent
	// that got a refusal reads what it may say next out of the same field.
	Vocabulary []string `json:"vocabulary"`
}

// viewCategory assembles it. Every surface calls this, so a console and an agent
// are looking at one answer rather than at two that agree today.
func viewCategory(
	ctx context.Context, db *store.DB, p *store.Principal, art *store.Artifact,
) (*categoryView, error) {
	log, err := db.TodoCategoryLog(ctx, p, art.ID)
	if err != nil {
		return nil, err
	}
	return &categoryView{
		Item: art, Category: store.CategoryOf(art),
		Standing: store.LatestTodoCategory(log), Log: log,
		Vocabulary: store.TodoCategories,
	}, nil
}

// handleTodoCategory says what kind of work a todo is - any todo the caller can
// read, wherever it was raised.
//
// POST /api/todo/{id}/category  {category}
//
// Whoever can READ the todo may classify it, and may override what somebody else
// called it. That is the ruling this endpoint implements, and it is the same one
// that governs the assignee and the status: what kind of work this is, is a
// claim about the WORK. The agent that picked the row up and found a bug
// underneath is in a position to say so, and it is routinely not whoever typed
// the title.
//
// It hands nobody anything - the category is a word in fields and the permission
// filter has never looked there - so a principal who cannot see the todo gets
// the 404 a read would have given.
func (s *server) handleTodoCategory(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req categoryRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	art, _, err := s.db.SetTodoCategory(r.Context(), p, r.PathValue("id"), req.Category)
	if err != nil {
		writeQueueError(w, r, err)
		return
	}
	view, err := viewCategory(r.Context(), s.db, p, art)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleTodoCategoryRead reads it back: what the todo is, who called it that,
// and every call anybody has made on it that this reader may see.
//
// GET /api/todo/{id}/category
func (s *server) handleTodoCategoryRead(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	art, err := s.db.ReadWorkItem(r.Context(), p, r.PathValue("id"))
	if err != nil {
		writeQueueError(w, r, err)
		return
	}
	view, err := viewCategory(r.Context(), s.db, p, art)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
