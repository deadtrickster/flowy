package flowy

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// joinLimiter is the whole defence on the one door that takes no token.
//
// The endpoint writes one kind of row and grants nothing, so the worst an abuser
// achieves is a queue full of requests the operator has to sweep. That is still
// worth refusing, and a limiter is the proportionate answer: a real agent asks
// once, notices it is pending, and waits.
type joinLimiter struct {
	mu   sync.Mutex
	seen map[string][]time.Time
}

const (
	joinWindow = time.Minute
	joinBurst  = 3
)

func newJoinLimiter() *joinLimiter { return &joinLimiter{seen: map[string][]time.Time{}} }

func (l *joinLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	var keep []time.Time
	for _, t := range l.seen[key] {
		if now.Sub(t) < joinWindow {
			keep = append(keep, t)
		}
	}
	if len(keep) >= joinBurst {
		l.seen[key] = keep
		return false
	}
	l.seen[key] = append(keep, now)
	return true
}

// handleJoin takes a request to exist from something that does not yet.
//
// THIS IS THE ONLY ROUTE HERE THAT TAKES NO PRINCIPAL, and it earns that by
// granting nothing. It writes a join request row and stops. Until a human
// approves it, the only thing that has happened is that the board says somebody
// asked.
//
// It exists because minting is the operator's and an agent with no token cannot
// post to the room to ask for one. Today every seat exists because a person
// already knew it should, which works while a person starts every agent by hand.
//
// POST /api/join {handle, kind, project, reason}
func (s *server) handleJoin(w http.ResponseWriter, r *http.Request) {
	if !s.joins.allow(clientKey(r), time.Now()) {
		writeJSON(w, http.StatusTooManyRequests,
			errorBody("too many join requests from here - one is enough, it is waiting on a person"))
		return
	}

	var req struct {
		Handle  string `json:"handle"`
		Kind    string `json:"kind"`
		Project string `json:"project"`
		Reason  string `json:"reason"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}

	art, _, err := s.db.RequestJoin(r.Context(), store.JoinRequest{
		Handle:  req.Handle,
		Kind:    req.Kind,
		Project: req.Project,
		Reason:  req.Reason,
	})
	var taken *store.ErrHandleTaken
	switch {
	case errors.As(err, &taken):
		// Conflict rather than a bad request: the ask was well formed and the
		// answer is that somebody got there first. Naming who saves the asker
		// from inventing a new handle they did not need.
		writeJSON(w, http.StatusConflict, errorBody(taken.Error()))
		return
	case err != nil:
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}

	// What comes back is deliberately thin: the id to quote and the fact that
	// nothing has been granted. An asker that reads this as success and starts
	// working would be wrong, so the answer says so in words.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"request": art.ID,
		"state":   "pending",
		"granted": false,
		"message": "recorded - this grants nothing. An operator decides, and the token arrives on approval.",
	})
}

// clientKey is what the limiter counts by. The remote address, coarsely: it is
// the only thing an unauthenticated caller has, and the limit is about volume
// rather than identity.
func clientKey(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-For"); h != "" {
		if i := strings.IndexByte(h, ','); i > 0 {
			return strings.TrimSpace(h[:i])
		}
		return strings.TrimSpace(h)
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return host
}

// handleJoinApprove grants a request: mints the seat and hands the token back
// once.
//
// OPERATOR ONLY. The check is here rather than in the store because who may
// approve is an authorisation question about this node, and what approval MEANS
// - mint, record, close - is the store's. Putting the check at the door also
// means it reads the same as every other privileged act here.
//
// POST /api/join/{id}/approve
func (s *server) handleJoinApprove(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if !p.Operator {
		writeJSON(w, http.StatusForbidden,
			errorBody("only this node's operator admits a new seat - that is what minting is"))
		return
	}
	art, minted, err := s.db.ApproveJoin(r.Context(), p, r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrNotAJoinRequest):
		writeJSON(w, http.StatusNotFound, errorBody("no such join request"))
		return
	case err != nil:
		writeJSON(w, http.StatusConflict, errorBody(err.Error()))
		return
	}
	// THE TOKEN IS HERE AND NOWHERE ELSE. Not on the row, not in the log: a
	// credential written into an artifact is a credential in every replica of
	// it. This response is the one place it exists outside the asker's hands.
	writeJSON(w, http.StatusOK, map[string]any{
		"request": art.ID,
		"handle":  store.JoinHandleOf(art),
		"user":    minted.User,
		"agent":   minted.Agent,
		"token":   minted.Token,
		"message": "minted. This token is shown once - it is not stored on the row.",
	})
}

// handleJoinRefuse says no, with a reason.
//
// POST /api/join/{id}/refuse {reason}
func (s *server) handleJoinRefuse(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if !p.Operator {
		writeJSON(w, http.StatusForbidden,
			errorBody("only this node's operator decides who joins"))
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	art, err := s.db.RefuseJoin(r.Context(), p, r.PathValue("id"), req.Reason)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrNotAJoinRequest):
		writeJSON(w, http.StatusNotFound, errorBody("no such join request"))
		return
	case err != nil:
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"request": art.ID, "state": "refused"})
}
