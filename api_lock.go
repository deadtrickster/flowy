package main

// The landing lock, reachable on its own.
//
// It was named merge_locks and taken only by the merge verbs, and that name
// described its first caller rather than what it is: EXCLUSION ON THE SHARED
// CHECKOUT AND THE TARGET BRANCH. Three things need that exclusion - landing,
// deploying, and once upon a time gating - and only landing ever took it.
//
// Gating no longer needs it: every seat runs the suite in its own worktree.
// Deploying still does. scripts/deploy.sh builds the console and installs a
// binary from the one shared checkout, so two deploys at once are two npm
// builds and two go builds over each other, with one installing while the
// other is still writing. Nothing prevents that today, and the only reason
// nobody has hit it is that one agent has been doing all the deploying.
//
// So the lock gets a door of its own. Same table, same holder rules, same
// expiry - a second mechanism for the same exclusion would be two locks that
// do not know about each other, which is the defect this fixes, not a fix.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// lockRequest names WHAT is being held and WHY.
//
// item is required and free text. A merge holds the target for a row id; a
// deploy holds it for "deploy". It is what the holder sees in a refusal, so
// "held by orchestrator for deploy" reads as a fact rather than a puzzle, and
// it is what the release must match - releasing by holder alone would let a
// deploy give back a landing's lock.
type lockRequest struct {
	Target string `json:"target"`
	Item   string `json:"item"`
}

func (s *server) handleTakeLock(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req lockRequest
	if err := decodeStrict(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	if strings.TrimSpace(req.Item) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody(
			"item is required - it says what the lock is being held FOR, and a refusal that "+
				"cannot say that sends the loser looking for a landing that is not happening"))
		return
	}

	lock, err := s.db.TakeMergeLock(r.Context(), p, req.Target, req.Item)
	var held *store.ErrTargetHeld
	if errors.As(err, &held) {
		// 409, like the merge door's: the request was well formed and may be
		// made again later. The holder is in the body, because "wait" without
		// "for whom, until when" is not something a caller can act on.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": err.Error(),
			"lock":  held.Held,
		})
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lock": lock})
}

func (s *server) handleReleaseLock(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req lockRequest
	if err := decodeStrict(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}

	released, err := s.db.ReleaseMergeLock(r.Context(), p, req.Target, req.Item)
	if err != nil {
		serverError(w, r, err)
		return
	}
	// RELEASED FALSE IS NOT AN ERROR, and the answer says which happened.
	//
	// A release that finds nothing means the lock had already expired, or
	// somebody else holds it now. Neither is a fault of the caller - a deploy
	// that ran long is exactly the case - and 404 would send a script into an
	// error path for the ordinary end of its own work. The flag is there so a
	// caller that cares can tell "I gave it back" from "it was not mine to
	// give", which are different facts about what just happened to the target.
	writeJSON(w, http.StatusOK, map[string]any{"released": released})
}

// handleReadLock says who holds the target, or that nobody does.
//
// A read rather than a probe: taking a lock to find out whether it is free is
// how two callers each take it in turn and neither does any work.
func (s *server) handleReadLock(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.URL.Query().Get("target"))

	lock, err := s.db.MergeLockOf(r.Context(), target)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if lock == nil {
		// EXPLICITLY FREE, not an empty object. "lock": null and a missing key
		// are the same JSON to a careless reader and different facts to a
		// careful one; `held: false` is the answer either can act on.
		writeJSON(w, http.StatusOK, map[string]any{"held": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"held": lock.Live(time.Now().UTC()), "lock": lock})
}

// decodeStrict refuses a field the struct does not know, like every other write
// door here: `targets` for `target` would otherwise be a lock on the default
// target, silently, and the caller would read the 200 as agreement.
func decodeStrict(r *http.Request, into any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}
