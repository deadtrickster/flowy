package main

// GET /api/merge-queue/wait - block until the queue changes, and say whether it did.
//
// WHY: waiting for the landing lock has been a bash poll loop written fresh
// every time, with its own idea of the interval and its own idea of the
// deadline, and no idea what to do when the node blinks. I wrote one at 20:19
// and another at 22:41 and they did not agree. The node already knows how to
// block a reader properly - /api/inbox/wait is that exact thing for messages -
// and the fleet's own rule is that a waiter is a monitor rather than a poll.
//
// THE THREE OUTCOMES ARE `flowy inbox`'s, deliberately, because a caller that
// has learned one waiter should not have to learn a second vocabulary:
//
//	something changed   200, changed:true, with the answer that changed
//	the window passed   200, changed:false, with the answer as it stands
//	broken              4xx/5xx, as any other door
//
// WHAT COUNTS AS A CHANGE is a digest of the parts a caller would ACT on: the
// lock (held, by whom, for which item), and per row its id, status, whether
// something is gating it, whether it is admissible, and whether it carries a
// red or a skip. Not `until`, which counts down on every read, and not the
// titles, which nobody waits on.
//
// The digest is the cursor. A caller passes back what it last saw and is told
// whether the world moved, so two waiters that started at different moments
// agree about what "changed" means - which a timestamp cursor would not, since
// the queue's own writes and the caller's clock are different clocks.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
)

func (s *server) handleMergeQueueWait(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")

	var (
		answer  mergeQueueAnswer
		cursor  string
		changed bool
	)
	err := pollUntil(r.Context(), waitWindowOf(r.URL.Query().Get("window")), func() (bool, error) {
		next, err := s.readMergeQueue(r)
		if err != nil {
			return false, err
		}
		digest, err := queueCursor(next)
		if err != nil {
			return false, err
		}
		answer, cursor = next, digest
		// A CALLER WITH NO CURSOR IS NOT WAITING, IT IS ASKING. The first call
		// answers immediately with the queue as it stands and a cursor to wait
		// on next time - a waiter that blocked for a minute before telling a
		// caller anything would be one nobody uses twice.
		//
		// AND A CURSOR THIS NODE CANNOT PLACE IS THE SAME QUESTION. The digest
		// is a function of the answer and nothing keeps a history of them, so
		// "stale" and "never existed" are indistinguishable here - a cursor
		// from another target, from a restarted caller, or simply mangled.
		// Both possible readings are wrong in the same direction if guessed:
		// answering `changed:false` to a cursor from another world tells a
		// caller nothing moved when everything did, and blocking on it holds a
		// caller against a state that will never come back.
		//
		// So it answers AT ONCE with what is true now and a cursor that is
		// current, which loses nothing: a caller that uses the cursor it was
		// given converges on the next call - measured, 0s then a full quiet
		// window. A caller that ignores it spins, and that is visible in its
		// own request rate rather than hidden in a wait that never returns.
		changed = since != "" && digest != since
		return since == "" || changed, nil
	})
	if errors.Is(err, errClientGone) {
		return
	}
	if err != nil {
		if errors.Is(err, errBadQueueParam) {
			writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
			return
		}
		serverError(w, r, err)
		return
	}

	// The answer rides out whether or not it changed, because the caller that
	// waited quietly still wants to know what it is looking at - and because a
	// quiet window with no answer is indistinguishable from a broken one to
	// anybody who was not watching the status code.
	answer.Changed = &changed
	answer.Cursor = cursor
	writeJSON(w, http.StatusOK, answer)
}

// queueCursor digests the parts of the queue a caller acts on.
//
// IT IS DELIBERATELY NOT THE WHOLE ANSWER. `until` counts down on every read
// and a title changes when somebody edits their own prose; a cursor that moved
// for either would wake every waiter for nothing, which is how a feed becomes
// one nobody reads.
func queueCursor(answer mergeQueueAnswer) (string, error) {
	shape := map[string]any{"target": answer.Target, "target_tip": answer.TargetTip}
	if lock := answer.Lock; lock != nil {
		shape["lock"] = map[string]any{
			"held": lock.Held, "holder": lock.Holder, "item": lock.Item,
		}
	}
	rows := []map[string]any{}
	for _, it := range answer.Items {
		row := map[string]any{
			"id": it.ID, "status": it.Status, "gating": it.Gating,
			"admissible": it.Admissible, "held": it.Held,
		}
		if it.Red != nil {
			row["red"] = it.Red.Tip
		}
		if it.Blocked != nil {
			row["blocked"] = it.Blocked.Why
		}
		rows = append(rows, row)
	}
	shape["items"] = rows
	raw, err := json.Marshal(shape)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16]), nil
}
