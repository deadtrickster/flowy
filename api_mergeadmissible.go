package main

// GET /api/merge/{id}/admissible - would this row land right now, and if not why.
//
// 01M0B8JFXS, in its own words: "one door for admissible-right-now, so four
// seats stop computing it four ways". On 2026-08-18 the same question had four
// implementations in this fleet - pre-gate.sh, drain.sh, a curl, a python shim -
// and twice two of us disagreed about whether a branch was landable. Once
// because the queue's default target is the DEPLOYED commit rather than master,
// once because a gating flag was residue with no lock behind it. Neither was a
// misreading of the rules; both were readings of the same rows through
// different code.
//
// TWO QUESTIONS, ANSWERED SEPARATELY, because a caller does different things
// with them and collapsing them is how an agent re-gates when it should sleep:
//
//	admissible  would the EVIDENCE THIS ROW CARRIES land on the target as it
//	            stands - store.MergeAdmissible, the same call the queue page
//	            makes. False means re-gate.
//	declarable  would a declaration FROM THIS CALLER be taken right now - the
//	            landing lock, which a declaration takes and a verdict does not.
//	            False means wait, and it names who holds it and until when.
//
// WHAT THIS DOOR WILL NOT ANSWER, and the row drew the line itself: whether the
// branch contains the target, whether it merges cleanly, whether initdb is on
// PATH. Those are facts about a CHECKOUT and a MACHINE. The node has neither,
// and a node that answered them would be answering about a different box -
// which is the exact class of error that produced four wrong numbers the night
// this row was written. pre-gate.sh keeps that half and asks this door for the
// rest.
//
// The tip follows /api/merge-queue's rule exactly, including the fallbacks and
// the word for which one was used: ?target_tip= if the caller measured one,
// then the last landed tip, then this node's build stamp. tip_from is on the
// answer because "not admissible" against a deployed tip a dozen landings old
// is a refusal that says more about the node than about the branch - it cost an
// agent three gate runs and forty minutes of re-derivation.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// mergeAdmissibleAnswer is what this door says.
//
// Item is the queue's own row type, built by the queue's own builder, so the
// two doors cannot drift: a caller that reads this and a caller that reads the
// page are looking at one answer computed once.
type mergeAdmissibleAnswer struct {
	Item      mergeQueueItem  `json:"item"`
	Target    string          `json:"target"`
	TargetTip string          `json:"target_tip"`
	TipFrom   string          `json:"tip_from"`
	Lock      *mergeQueueLock `json:"lock"`
	// LockIsMine is the field this door exists for as much as any other. Every
	// session of a seat shares a principal, so "held by claude-host" is a
	// sentence a claude-host session reads as "held by me" and a different
	// claude-host session reads the same way - and both are sometimes wrong.
	// The node knows which principal is asking; it should not make the caller
	// guess.
	LockIsMine bool `json:"lock_is_mine"`
	// Declarable is whether a declaration from THIS CALLER would be taken now.
	// Distinct from Admissible on the item: one says wait, the other says
	// re-gate.
	Declarable bool `json:"declarable"`
	// Why is the one sentence a person acts on: the refusal that matters most,
	// or what to do next when there is none. Empty is never the answer - a door
	// that says false and nothing else is the fourth hand-written answer with
	// extra steps.
	Why string `json:"why"`
}

func (s *server) handleMergeAdmissible(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	now := time.Now()

	art, err := s.db.ReadArtifact(r.Context(), p, r.PathValue("id"), false)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such merge request"))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	// A row that is not a merge request is a 404 and not a 400, for the reason
	// the gate door gives: an id naming something else is an id this door does
	// not have, and saying which would tell a caller about a row they did not
	// ask about.
	if art.Kind != store.MergeKind {
		writeJSON(w, http.StatusNotFound, errorBody("no such merge request"))
		return
	}

	target := store.TargetOf(art)
	tip, tipFrom := strings.TrimSpace(r.URL.Query().Get("target_tip")), "stated"
	if tip == "" {
		if landed, err := s.db.LandedTipOf(r.Context(), target); err == nil && landed != nil {
			tip, tipFrom = landed.Tip, "landed"
		} else if bs := strings.TrimSpace(buildStamp); bs != "" && bs != "src" {
			tip, tipFrom = bs, "deployed"
		} else {
			tipFrom = "none"
		}
	}

	lock, err := s.db.MergeLockOf(r.Context(), target)
	if err != nil {
		serverError(w, r, err)
		return
	}
	lockLive := lock.Live(now)

	answer := mergeAdmissibleAnswer{
		Item:      queueItemOf(art, tip, lock, lockLive, now),
		Target:    target,
		TargetTip: tip,
		TipFrom:   tipFrom,
		Lock:      &mergeQueueLock{},
	}
	if lock != nil {
		answer.Lock = &mergeQueueLock{
			Held: lockLive, Holder: lock.Holder, HolderName: lock.HolderName,
			Item: lock.Item, Until: lock.Until, TakenAt: lock.TakenAt,
		}
	}
	// BOTH OF THESE ARE THE STORE'S OWN RULE, called rather than restated. That
	// is the whole point of the row this door closes: a caller reconstructing
	// TakeMergeLock's WHERE clause from the outside is the fifth answer to a
	// question that already had four.
	//
	// They are separate because they differ in the one case that has already
	// bitten: my other session holds this target FOR ANOTHER ROW. Then the lock
	// is mine and a declaration for this row is still refused - every subagent
	// of a seat runs under its parent's token, and on 18 Aug a sibling session
	// landed through a lock it never took.
	answer.LockIsMine = lock.HeldBy(p, now)
	answer.Declarable = lock.WouldTake(p, art.ID, now)

	// The row that explains this refusal, when somebody has written one, by the
	// same rule the page uses: the deploy-tip issue first when the tip came from
	// the build stamp, because under that fallback a page of refusals can be
	// entirely an artefact of the node being behind.
	if code := answer.Item.code; code != "" {
		codes := []string{code}
		if tipFrom == "deployed" {
			codes = append([]string{store.RefusalMergeTipDeployed}, codes...)
		}
		if found := knownIssues(r.Context(), s.db, p, codes, scopeAll(r, p)); found != nil {
			if tipFrom == "deployed" {
				answer.Item.KnownIssue = store.PickKnownIssue(found,
					store.RefusalMergeTipDeployed, code)
			} else {
				answer.Item.KnownIssue = store.PickKnownIssue(found, code)
			}
		}
	}

	answer.Why = admissibleWhy(answer)
	writeJSON(w, http.StatusOK, answer)
}

// admissibleWhy is the sentence, and the ORDER is the whole of it.
//
// A caller reads one line and does one thing, so the line has to be the thing
// that blocks them FIRST. A row whose evidence is stale AND whose target is held
// by somebody else must say "wait" rather than "re-gate": re-gating now would
// spend a run measuring from a base the holder is about to move.
func admissibleWhy(a mergeAdmissibleAnswer) string {
	switch {
	case a.TipFrom == "none":
		return "this node does not know where " + a.Target + " is - nothing has landed on " +
			"it here and this binary carries no build stamp, so pass ?target_tip= from your own git"
	// HELD BY ME, FOR SOMETHING ELSE - said first and said differently, because
	// this is the one refusal a reader is most likely to misread as permission.
	// "master is held by claude-host" in front of a claude-host session reads as
	// "held by me, carry on", and carrying on is how a sibling session landed
	// through a lock it never took.
	case a.LockIsMine && !a.Declarable:
		return fmt.Sprintf("%s is held by another session of YOURS, for %s and not for "+
			"this row - wait for it. A lock held for one row does not admit a "+
			"declaration for another, which is the whole reason it records which.",
			a.Target, a.Lock.Item)
	case !a.Declarable:
		name := a.Lock.HolderName
		if name == "" {
			name = a.Lock.Holder
		}
		return fmt.Sprintf("%s is held by %s until %s - wait, do not re-gate: a run started "+
			"now measures from a base they are about to move",
			a.Target, name, a.Lock.Until.UTC().Format(time.RFC3339))
	case a.Item.Gating:
		return "a run is measuring this branch right now (" + a.Item.GateRun +
			") - a second one would measure the same tree"
	case a.Item.Admissible != nil && *a.Item.Admissible:
		return "it may land on " + a.TargetTip + " right now"
	case a.Item.Reason != "":
		return a.Item.Reason
	default:
		return "no verdict has been recorded against " + a.TargetTip
	}
}
