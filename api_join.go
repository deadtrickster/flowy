package main

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
