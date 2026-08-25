// THREAD UNFOLDS over HTTP: see a thread's replies in the room stream, fold
// them away again, and read what this reader has unfolded.
//
// The rules are in the store - see internal/store/threadfolds.go - so this
// file is argument checking and status codes, the shape bookmarks.go has and
// for the same reason: the room stream the console draws is one reader's view,
// and an agent going through one door and the console going through another
// must not be able to reach two ideas of what is unfolded.
//
// NO ROOM IN THE PATH, deliberately, for bookmarks.go's reason: a thread id
// is global, and the set has exactly one reader. The state only ever acts on
// threads that reader can already read, because the fold computation runs over
// the events the room read admitted.

package main

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

type threadUnfoldRequest struct {
	Thread string `json:"thread"`
}

// threadsUnfoldedView is what a reader's own state looks like: the threads
// they have unfolded, newest first, and the log they were folded out of. The
// log rides along for bookmarksView's reason - the ids are the derived thing
// and the entries are the record.
type threadsUnfoldedView struct {
	Threads []string            `json:"threads"`
	Log     []store.UnfoldEntry `json:"log"`
}

func unfoldRefusal(w http.ResponseWriter, r *http.Request, err error) {
	var refused store.UnfoldError
	switch {
	case errors.As(err, &refused):
		writeJSON(w, http.StatusBadRequest, errorBody(refused.Why))
	default:
		serverError(w, r, err)
	}
}

// POST /api/thread-unfolded  {thread}
func (s *server) handleThreadUnfold(w http.ResponseWriter, r *http.Request) {
	var req threadUnfoldRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	e, err := s.db.Unfold(r.Context(), principalOf(r), req.Thread)
	if err != nil {
		unfoldRefusal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// DELETE /api/thread-unfolded/{id}
//
// DELETE, and it appends, for the reason unbookmark does: the verb says what
// the caller wants to happen to the view, and the log still gains an entry.
func (s *server) handleThreadFold(w http.ResponseWriter, r *http.Request) {
	e, err := s.db.Fold(r.Context(), principalOf(r), r.PathValue("id"))
	if err != nil {
		unfoldRefusal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// GET /api/threads-unfolded
func (s *server) handleThreadsUnfolded(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	log, err := s.db.UnfoldedLog(r.Context(), p)
	if err != nil {
		unfoldRefusal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, threadsUnfoldedView{Threads: store.LiveUnfolded(log), Log: log})
}
