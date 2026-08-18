package main

// GET /api/merge-queue - the merge queue as a browser can read it.
//
// This exists because of a gap I made and did not see until somebody needed it:
// merge_queue is an MCP TOOL, and THE CONSOLE CANNOT CALL MCP. So the admission
// rule, the one piece of tonight's work whose whole purpose is to be consulted
// before a merge, was unreachable from the surface a person actually looks at.
// A door only agents can knock on is half a door.
//
// The verdicts are computed HERE rather than in the console, deliberately. The
// browser has no git, no store and no permission filter, and a second
// implementation of "may this land" in TypeScript would be a second answer that
// disagrees with the first one on the day it matters.

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// mergeQueueItem is one request as the console draws it.
//
// Flat on purpose: the panel should not have to reach into fields to find the
// branch, and every reader that has had to do that today got it wrong at least
// once - status from .fields, owner from body text, assignee from two places.
type mergeQueueItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Project  string `json:"project,omitempty"`
	Branch   string `json:"branch"`
	Target   string `json:"target"`
	GatedTip string `json:"gated_tip"`
	GateRun  string `json:"gate_run"`
	// GateRef is where the evidence lives when that is not Branch - the
	// integration branch a union actually measured. A lander reading only
	// Branch lands one commit of a sixteen-commit union and calls it done; it
	// happened twice in one night and nobody lied.
	GateRef    string `json:"gated_ref,omitempty"`
	Status     string `json:"status"`
	Assignee   string `json:"assignee,omitempty"`
	Admissible *bool  `json:"admissible,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// Held says the target is reserved by another declarer's lock - a WAIT, as
	// against Admissible's verdict about this row's own evidence. Distinct on
	// purpose: collapsing them is how an agent re-gates when it should sleep
	// and sleeps when it should re-gate.
	Held bool `json:"held,omitempty"`
	// Gating is true while a run is MEASURING this branch and has not reported.
	//
	// The rule as first written protected the lander and nobody else: "a branch
	// lands only on the tip its gate measured" says nothing about the gate
	// ALREADY RUNNING, so every landing silently invalidated every in-flight run
	// and the invalidated party found out by reading a number that was already
	// worthless. That happened twice in one hour on the night this was written.
	//
	// A run is in flight when its request names the run and has no verdict yet.
	// Recording the verdict afterwards is what made the window invisible; naming
	// the run when it STARTS is what makes it visible.
	Gating bool `json:"gating"`
	// KnownIssue is the row that explains this refusal, when somebody has
	// written one - see knownissue.go. It rides beside the reason rather than
	// arriving as a banner over the page, because the whole point is that it
	// reaches the reader ATTACHED TO THE THING THAT PROVOKED THE QUESTION. A
	// banner is a second announcement, and announcing is what already failed.
	KnownIssue *store.KnownIssue `json:"known_issue,omitempty"`
	// code is the refusal's own token, kept here only to resolve the above in
	// one query after the page is built. Unexported, so it never reaches the
	// wire: the client has the code inside known_issue when there is a row, and
	// no use for it when there is not.
	code string
}

func (s *server) handleMergeQueue(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()

	room, err := roomArg(q.Get("room"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	target := strings.TrimSpace(q.Get("target"))
	if target == "" {
		target = store.DefaultMergeTarget
	}

	// WHERE THE TIP COMES FROM, and the answer says which, because a verdict is
	// only as good as the tip it was measured against and a console that cannot
	// tell the caller's tip from the node's own would be exactly the confusion
	// this rule exists to end.
	//
	//   stated   - the caller read it from git and passed it. The real answer.
	//   landed   - nobody stated one, so the node offers the last sha a land
	//              through the merge door recorded. Fresh by construction: it
	//              moves exactly when a land moves it.
	//   deployed - nothing has landed through the door, so the node offers the
	//              commit IT WAS BUILT FROM. A browser has no git, so this is
	//              the best a page can do on its own, and it is honest: it
	//              answers "may this land on what is running here", which is a
	//              real question, and NOT "may this land on master right now",
	//              which it cannot know.
	//
	// The landed link exists because the deployed one froze: a deploy held for
	// an evening left every bare queue read answering against a tip a dozen
	// landings old, refusing green branches all night for reasons that were
	// already false.
	tip, tipFrom := strings.TrimSpace(q.Get("target_tip")), "stated"
	if tip == "" {
		if landed, err := s.db.LandedTipOf(r.Context(), target); err == nil && landed != nil {
			tip, tipFrom = landed.Tip, "landed"
		} else if bs := strings.TrimSpace(buildStamp); bs != "" && bs != "src" {
			tip, tipFrom = bs, "deployed"
		} else {
			tipFrom = "none"
		}
	}

	list, err := s.db.ListArtifacts(r.Context(), p, store.ArtifactQuery{
		Type:      store.MemoryType,
		Kind:      store.MergeKind,
		Project:   q.Get("project"),
		Room:      room,
		NotStatus: store.DoneStatus,
		ScopeAll:  scopeAll(r, p),
		Limit:     intParam(q.Get("limit")),
	})
	if err != nil {
		serverError(w, r, err)
		return
	}

	// The landing lock on this target, read once for every row. A row held by
	// another declarer's lock is a WAIT and not a verdict about its evidence,
	// and the difference is spelled out below where the two meet.
	lock, err := s.db.MergeLockOf(r.Context(), target)
	if err != nil {
		serverError(w, r, err)
		return
	}
	now := time.Now()
	lockLive := lock.Live(now)

	items := make([]mergeQueueItem, 0, len(list))
	for _, a := range list {
		if !strings.EqualFold(store.TargetOf(a), target) {
			continue
		}
		it := mergeQueueItem{
			ID:       a.ID,
			Title:    a.Title,
			Branch:   store.BranchOf(a),
			Target:   store.TargetOf(a),
			GatedTip: store.GatedTipOf(a),
			GateRun:  store.GateRunOf(a),
			GateRef:  store.GateRefOf(a),
			Status:   store.TodoStatusOf(a),
			Assignee: store.AssigneeOf(a),
		}
		if a.Project != nil {
			it.Project = *a.Project
		}
		// Believed for a bounded time, not forever - see store.GatingAt. A run
		// that died leaves a declaration nobody will ever clear, and the first
		// version of this told the whole room not to land for twenty minutes
		// after a green run had already landed.
		it.Gating = store.GatingAt(a, time.Now())
		if tip != "" {
			err := store.MergeAdmissible(a, tip)
			ok := err == nil
			it.Admissible = &ok
			if err != nil {
				it.Reason = err.Error()
				it.code = store.RefusalCodeOf(err)
			}
		}
		// HELD IS NOT NOT-ADMISSIBLE, and the two must never share a boolean.
		// "The target is reserved until T" says WAIT; "your evidence is stale"
		// says RE-GATE. A caller that cannot tell them apart does one when it
		// means the other, and the row that asked for this lock was explicit:
		// an agent refused because somebody else is mid-land should be told so,
		// with the name and the time, not folded into a bare false.
		if lockLive && store.GateActorOf(a) != lock.Holder {
			it.Held = true
			held := &store.ErrTargetHeld{Target: lock.Target, Held: lock, Now: time.Now()}
			if it.Reason == "" {
				it.Reason = held.Error()
			} else {
				it.Reason = fmt.Sprintf("%s; and %s", it.Reason, held.Error())
			}
		}
		items = append(items, it)
	}

	// The rows explaining these refusals, in one query for the whole page, and
	// only when something was actually refused.
	//
	// WHY THE DEPLOY CODE COMES FIRST when the tip is the node's build stamp.
	// Under that fallback every item is judged against whenever somebody last
	// deployed, so a page of refusals can be entirely an artefact of the node
	// being behind - which happened, and cost an agent three gate runs and
	// another forty minutes of re-derivation. Each item's own reason is true and
	// is the wrong thing to read first. If nobody has written a row about the
	// deploy, this falls straight through to the item's own case.
	codes := make([]string, 0, len(items)+1)
	for _, it := range items {
		if it.code != "" {
			codes = append(codes, it.code)
		}
	}
	// Nothing was refused, so nothing needs explaining, and the query does not
	// run - a queue where everything may land is the common case and must not
	// pay for this.
	if len(codes) > 0 && tipFrom == "deployed" {
		codes = append([]string{store.RefusalMergeTipDeployed}, codes...)
	}
	if found := knownIssues(r.Context(), s.db, p, codes, scopeAll(r, p)); found != nil {
		for i := range items {
			if items[i].code == "" {
				continue
			}
			if tipFrom == "deployed" {
				items[i].KnownIssue = store.PickKnownIssue(found,
					store.RefusalMergeTipDeployed, items[i].code)
				continue
			}
			items[i].KnownIssue = store.PickKnownIssue(found, items[i].code)
		}
	}

	// How many runs are measuring right now. A lander reads this before merging:
	// landing while somebody is gating invalidates their evidence, and the queue
	// saying so is the difference between a decision and an accident.
	gating := 0
	for _, it := range items {
		if it.Gating {
			gating++
		}
	}

	response := map[string]any{
		"target":     target,
		"target_tip": tip,
		"gating":     gating,
		"tip_from":   tipFrom,
		"items":      items,
		// Stated rather than inferred from a missing field. A console that
		// treats "no verdict" as "admissible" is the failure this endpoint is
		// guarding against, and leaving it to be worked out from an absent key
		// is how that happens.
		"decided": tip != "",
	}
	// The lock rides the answer even when nothing is held, as held:false - the
	// caller deciding to declare wants "the target is free" as a fact, not as
	// the absence of a key they have to know the meaning of.
	if lock != nil {
		response["lock"] = map[string]any{
			"held":        lockLive,
			"holder":      lock.Holder,
			"holder_name": lock.HolderName,
			"until":       lock.Until,
			"taken_at":    lock.TakenAt,
		}
	} else {
		response["lock"] = map[string]any{"held": false}
	}
	writeJSON(w, http.StatusOK, response)
}
