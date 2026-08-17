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
	"net/http"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// mergeQueueItem is one request as the console draws it.
//
// Flat on purpose: the panel should not have to reach into fields to find the
// branch, and every reader that has had to do that today got it wrong at least
// once - status from .fields, owner from body text, assignee from two places.
type mergeQueueItem struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Project    string `json:"project,omitempty"`
	Branch     string `json:"branch"`
	Target     string `json:"target"`
	GatedTip   string `json:"gated_tip"`
	GateRun    string `json:"gate_run"`
	Status     string `json:"status"`
	Assignee   string `json:"assignee,omitempty"`
	Admissible *bool  `json:"admissible,omitempty"`
	Reason     string `json:"reason,omitempty"`
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
	//   deployed - nobody stated one, so the node offers the commit IT WAS BUILT
	//              FROM. A browser has no git, so this is the best a page can do
	//              on its own, and it is honest: it answers "may this land on
	//              what is running here", which is a real question, and NOT "may
	//              this land on master right now", which it cannot know.
	tip, tipFrom := strings.TrimSpace(q.Get("target_tip")), "stated"
	if tip == "" {
		if bs := strings.TrimSpace(buildStamp); bs != "" && bs != "src" {
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
			Status:   store.TodoStatusOf(a),
			Assignee: store.AssigneeOf(a),
		}
		if a.Project != nil {
			it.Project = *a.Project
		}
		it.Gating = it.GateRun != "" && it.GatedTip == ""
		if tip != "" {
			err := store.MergeAdmissible(a, tip)
			ok := err == nil
			it.Admissible = &ok
			if err != nil {
				it.Reason = err.Error()
			}
		}
		items = append(items, it)
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

	writeJSON(w, http.StatusOK, map[string]any{
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
	})
}
