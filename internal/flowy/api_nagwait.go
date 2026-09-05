package flowy

// GET /api/nag/wait - block until what an idle agent should know changes.
//
// WHY THIS EXISTS. /api/nag moved the DECISION into the node: what counts as
// work, what counts as mine, when a claim has gone quiet, whether one seat is
// carrying the board. What it did not move is the WAITING. board-nag.sh still
// carries `--watch`, which is a bash loop with its own interval, its own idea
// of a deadline and its own behaviour when the node blinks - the same three
// things every other hand-rolled waiter in this fleet got wrong differently.
//
// The row this closes (01M0B86CR0) asked for the decision AND the saying to
// move, and named a goroutine that announces into the room. This is the saying,
// and it is a door rather than an announcement for two measured reasons:
//
//   - A NAG IS ADDRESSED OR IT IS NOISE. Every count here is the CALLER'S -
//     "mine" is their rows, and a reader who cannot see a row does not see it in
//     any total. An announcement into a room says one seat's nag to four seats,
//     three of whom it is not about, and the one it IS about learns to skip it.
//   - THE NODE WOULD NEED A MOUTH. Speaking in a room means a principal to speak
//     as, and the only identity a node has to hand is the operator's. This fleet
//     has a standing rule against an agent speaking as the operator, and a
//     goroutine doing it hourly is that rule broken on a timer.
//
// So the node decides and the seat's own monitor delivers, which is the split
// the row itself drew: "the per-seat delivery does not move - a monitor still
// has to be what wakes an agent, because that is a property of the seat".
//
// THE THREE OUTCOMES ARE `flowy inbox`'s, as they are at every other waiter
// here, because a caller that has learned one waiter should not learn a second
// vocabulary:
//
//	something changed   200, changed:true, with the counts that changed
//	the window passed   200, changed:false, with the counts as they stand
//	broken              4xx/5xx, as any other door

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
)

func (s *server) handleNagWait(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	since := r.URL.Query().Get("since")

	var (
		view    nagView
		cursor  string
		changed bool
	)
	err := pollUntil(r.Context(), s.draining, waitWindowOf(r.URL.Query().Get("window")), func() (bool, error) {
		next, err := s.readNag(r.Context(), p, scopeAll(r, p))
		if err != nil {
			return false, err
		}
		digest, err := nagCursor(next)
		if err != nil {
			return false, err
		}
		view, cursor = next, digest
		// A CALLER WITH NO CURSOR IS ASKING, NOT WAITING - the merge queue
		// waiter's rule and for its reason: a waiter that held a caller for a
		// minute before telling it anything is one nobody uses twice. It also
		// makes the first call of a seat that started at 22:00 answer with the
		// same board a seat that started at 06:00 is looking at, which is the
		// whole complaint this door was raised for.
		//
		// A cursor this node cannot place is the same question. The digest is a
		// function of the answer and nothing keeps a history of them, so a
		// cursor from a restarted caller and one that is simply mangled are
		// indistinguishable - and both readings are wrong in the same direction
		// if guessed. It answers at once with what is true now and a cursor
		// that is current.
		changed = since != "" && digest != since
		return since == "" || changed, nil
	})
	if errors.Is(err, errClientGone) {
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}

	// The counts ride out whether or not they changed, because the caller that
	// waited quietly still wants to know what it is looking at - and because a
	// quiet window with no answer is indistinguishable from a broken one to
	// anybody who was not watching the status code.
	view.Changed = &changed
	view.Cursor = cursor
	writeJSON(w, http.StatusOK, view)
}

// nagCursor digests the parts of the nag a caller would ACT on.
//
// IT IS DELIBERATELY NOT THE WHOLE ANSWER. store.Workload carries a share per
// seat, and a share is a ratio: one row filed anywhere on the board moves every
// seat's share by a fraction of a percent and would wake every waiter in the
// fleet for a number none of them act on. What a reader acts on is the counts
// and the verdict - there is unowned work, my claim has gone quiet, somebody is
// carrying the board - so those are the cursor and the shares ride along
// underneath it.
//
// StaleAfter is not in it either: it is a constant this node compiled in, and a
// waiter that woke when a threshold was re-read would be waking on its own
// arithmetic.
func nagCursor(view nagView) (string, error) {
	shape := map[string]any{
		"mine": view.Mine, "mine_todo": view.MineTodo, "unowned": view.Unowned,
		"open": view.Open, "stale": view.Stale,
		"verdict": view.Workload.Verdict,
	}
	// WHICH READERS ARE QUIET, by name, and never for how long. A seat going
	// quiet is exactly the kind of change a waiter should be woken for - it is
	// somebody else's death, and the only reader who can act on it is one who
	// is still alive. But the silence GROWS every second, so putting the
	// duration in the cursor would wake every waiter on every poll forever,
	// which is the always-speaking feed nobody reads.
	names := make([]string, 0, len(view.Quiet))
	for _, q := range view.Quiet {
		names = append(names, q.Reader)
	}
	sort.Strings(names)
	shape["quiet"] = names
	raw, err := json.Marshal(shape)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16]), nil
}
