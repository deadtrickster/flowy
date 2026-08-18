package main

// Filing a merge request through the memory door.
//
// A merge request is a work item like any other - see store.WorkKinds - so it is
// written by mem_write rather than by a verb of its own. What it needs beyond a
// todo is four words about the branch and the verdict, and what THIS file is for
// is making sure those four words can only land where something will read them.

import (
	"fmt"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// mergeFields puts branch, target, gated tip and gate run onto a merge item, and
// refuses them anywhere else.
//
// The refusals, and each one is a thing that would otherwise fail silently:
//
//   - branch or a verdict on an item that is not a merge request. Nothing reads
//     those fields on a todo, so storing them would be a write that succeeded and
//     changed nothing anybody can see - the exact shape of half of today.
//   - a merge request with no branch, ever. Not on a create and not left empty by
//     an update: a merge request that does not say what it would land is not one,
//     and it would sit in the queue looking like work.
//
// What is NOT refused here: a merge request with no gated tip. That is the normal
// state of a branch nobody has gated yet, it is exactly what the queue is for
// holding, and store.MergeAdmissible refuses to LAND it. Filing is not admission.
func mergeFields(art *store.Artifact, fields *map[string]any, a memWriteArgs) error {
	branch := strings.TrimSpace(a.Branch)
	target := strings.TrimSpace(a.Target)
	tip := strings.TrimSpace(a.GatedTip)
	run := strings.TrimSpace(a.GateRun)
	stated := branch != "" || target != "" || tip != "" || run != ""

	if art.Kind != store.MergeKind {
		if stated {
			return fmt.Errorf("branch, target, gated_tip and gate_run belong to a merge "+
				"request, and this item is a %q - nothing would ever read them here. "+
				"Send kind %q to file one", art.Kind, store.MergeKind)
		}
		return nil
	}

	set := func(key, value string) {
		if value == "" {
			return
		}
		if *fields == nil {
			*fields = map[string]any{}
		}
		(*fields)[key] = value
	}
	set(store.BranchField, branch)
	set(store.TargetField, target)
	set(store.GatedTipField, tip)
	set(store.GateRunField, run)

	// Judged on what the ROW will carry, not on what this call mentioned. On an
	// update the map already holds the item's existing fields - memWrite loads
	// them before this runs - so restating nothing keeps the branch it was filed
	// with, and only an item that has never had one is refused.
	if *fields != nil {
		if raw, ok := (*fields)[store.BranchField].(string); ok && strings.TrimSpace(raw) != "" {
			return nil
		}
	}
	return fmt.Errorf("a merge request has to say which branch it would land: send branch")
}

// defaultMemScope is what an UNSTATED scope means on a create, and it depends
// on what is being written.
//
// A memory is personal by default and that is right: it is one agent's note to
// itself, and widening it silently would publish what nobody offered.
//
// A WORK ITEM IS NOT A MEMORY. store.WorkKinds is the queue, and a queue row
// only its author can read is not on the queue. Measured tonight, and it is
// invisible by construction: three merge requests filed at the default sat in
// the queue where the drainer meant to land them could not see them, and every
// symptom of that reads as "the queue is empty". The same default put two todos
// out of the operator's reach after I had told them the ids in the room.
//
// A token with no project keeps personal, because a row cannot live in a
// project this principal does not write to. The alternative is refusing a
// create that used to work, over a default the caller never chose.
//
// An UPDATE is untouched: memWrite keeps the item's own visibility when the
// call says nothing about scope, and healing an old row's scope on an unrelated
// edit would move somebody's work into view behind their back.
func defaultMemScope(kind string, p *store.Principal) string {
	if p == nil || p.Project == "" {
		return "personal"
	}
	if !store.IsWorkKind(kind) {
		return "personal"
	}
	return "project"
}
