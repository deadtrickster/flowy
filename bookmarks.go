// BOOKMARKS over HTTP: keep a message, drop it, and read what you are keeping.
//
// The rules are in the store - see internal/store/bookmarks.go - so this file
// is argument checking and status codes, the shape pins.go has and for the same
// reason: an agent going through one door and the console going through another
// must not be able to reach two ideas of what is kept.
//
// THE MESSAGES COME BACK WITH THE LIST, not just their ids. A pin's strip can
// hand back ids because the room's transcript is already on the reader's screen
// and the ids resolve against it; a bookmark list is a page of its own, and a
// page of twenty ULIDs is a page nobody can read. Each one is read through the
// ORDINARY filter, one at a time, so a message that stopped being readable
// simply is not in the answer - a bookmark points at a message, it never keeps
// a copy.

package main

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

type bookmarkRequest struct {
	Message string `json:"message"`
}

// bookmarksView is what a reader's own list looks like: the messages they are
// keeping, newest first, and the log they were folded out of.
//
// The log rides along for pinsView's reason - the ids are the derived thing and
// the entries are the record - and here it also answers "when did I keep this",
// which is most of what somebody wants from their own pile.
type bookmarksView struct {
	Kept     []string              `json:"kept"`
	Messages []*store.Event        `json:"messages"`
	Log      []store.BookmarkEntry `json:"log"`
}

func bookmarkRefusal(w http.ResponseWriter, r *http.Request, err error) {
	var refused store.BookmarkError
	switch {
	case errors.As(err, &refused):
		writeJSON(w, http.StatusBadRequest, errorBody(refused.Why))
	default:
		serverError(w, r, err)
	}
}

// POST /api/bookmark  {message}
func (s *server) handleBookmark(w http.ResponseWriter, r *http.Request) {
	var req bookmarkRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	e, err := s.db.Bookmark(r.Context(), principalOf(r), req.Message)
	if err != nil {
		bookmarkRefusal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// DELETE /api/bookmark/{id}
//
// DELETE, and it appends, for the reason unpin does: the verb says what the
// caller wants to happen to the list, and the log still gains an entry.
func (s *server) handleUnbookmark(w http.ResponseWriter, r *http.Request) {
	e, err := s.db.Unbookmark(r.Context(), principalOf(r), r.PathValue("id"))
	if err != nil {
		bookmarkRefusal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// GET /api/bookmarks
func (s *server) handleBookmarks(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	log, err := s.db.BookmarkLog(r.Context(), p)
	if err != nil {
		bookmarkRefusal(w, r, err)
		return
	}
	kept := store.LiveBookmarks(log)
	// READ BACK ONE AT A TIME through the same door every other reader uses.
	// A message that has become unreadable is DROPPED from the list rather than
	// reported as missing: the reader kept a pointer, and the answer to "what
	// can I still get to" is the honest content of this page. The id stays in
	// `kept` either way, so nothing is silently forgotten in the log.
	messages := make([]*store.Event, 0, len(kept))
	for _, id := range kept {
		e, err := s.db.ReadEvent(r.Context(), p, id)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			serverError(w, r, err)
			return
		}
		if e != nil {
			messages = append(messages, e)
		}
	}
	writeJSON(w, http.StatusOK, bookmarksView{Kept: kept, Messages: messages, Log: log})
}
