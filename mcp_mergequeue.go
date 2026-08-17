package main

// The merge queue surface: reading the queue, and asking it whether something
// may land.
//
// The rule lives in internal/store/mergequeue.go and it already refuses
// correctly. What was missing until this file is that NOTHING ASKED IT. A store
// that says no to nobody is a comment with tests, and the day this was written
// had enough of those - four merges went in by hand against a tip their gate had
// never seen, and the rule that would have caught all four existed in a
// conversation rather than behind a door.
//
// So there is exactly one verb here and it answers one question: given where the
// target is RIGHT NOW, what in this queue may land. It is deliberately a read
// rather than a merge - flowy does not run git, and a tool that claimed to merge
// would be lying about who does the work. The caller merges; this says whether
// the caller is allowed to believe its own gate.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

var mergeTools = []tool{
	{
		Name: "merge_queue",
		Description: "The merge queue: every merge request you can read that is not " +
			"done, and whether each one may land on the target AS IT IS NOW. Pass the " +
			"target's current tip - read it from git, do not guess it - and each item " +
			"comes back admissible or refused with the reason. A request whose gate " +
			"measured a different tip is REFUSED: its checks are evidence about a tree " +
			"that no longer exists, which is how a queue of individually green branches " +
			"turns a target red. Without a tip you get the queue and no verdicts, which " +
			"is a list rather than an answer.",
		InputSchema: object(props{
			"target": str("Which target to ask about, e.g. master. Default master."),
			"target_tip": str("The commit the target is on right now. Leave it out to " +
				"list the queue without deciding anything."),
			"scope": enum("Narrow to one scope.", memScopes),
			"room":  str("Only the requests raised in this chat room."),
		}, nil),
		call: mergeQueueTool,
	},
}

// mergeEntry is one request and what this reader may conclude about it.
//
// Admissible and Reason are a pair on purpose: a bare false with no sentence is
// the refusal shape that sends somebody to measure the wrong thing, which
// happened repeatedly today. The reason names both tips.
type mergeEntry struct {
	Item       *store.Artifact `json:"item"`
	Branch     string          `json:"branch"`
	Target     string          `json:"target"`
	GatedTip   string          `json:"gated_tip"`
	GateRun    string          `json:"gate_run"`
	Status     string          `json:"status"`
	Admissible *bool           `json:"admissible,omitempty"`
	Reason     string          `json:"reason,omitempty"`
}

func mergeQueueTool(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Target    string `json:"target"`
		TargetTip string `json:"target_tip"`
		Scope     string `json:"scope"`
		Room      string `json:"room"`
		Limit     int    `json:"limit"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	q, err := memQuery(a.Scope, "", a.Limit)
	if err != nil {
		return nil, err
	}
	q.Kinds = []string{store.MergeKind}
	q.NotStatus = store.DoneStatus
	if q.Room, err = roomArg(a.Room); err != nil {
		return nil, err
	}

	list, err := m.db.ListArtifacts(ctx, p, q)
	if err != nil {
		return nil, err
	}

	target := strings.TrimSpace(a.Target)
	if target == "" {
		target = store.DefaultMergeTarget
	}
	tip := strings.TrimSpace(a.TargetTip)

	out := make([]mergeEntry, 0, len(list))
	for _, item := range list {
		// A request for master does not answer about a release line, and
		// mixing them is how a queue lands the right branch on the wrong
		// thing.
		if !strings.EqualFold(store.TargetOf(item), target) {
			continue
		}
		e := mergeEntry{
			Item:     item,
			Branch:   store.BranchOf(item),
			Target:   store.TargetOf(item),
			GatedTip: store.GatedTipOf(item),
			GateRun:  store.GateRunOf(item),
			Status:   store.TodoStatusOf(item),
		}
		// No tip, no verdict. Answering "admissible" against a tip nobody
		// stated would be the always-true check this whole surface exists to
		// prevent, and it would be worse here because it would be believed.
		if tip != "" {
			err := store.MergeAdmissible(item, tip)
			ok := err == nil
			e.Admissible = &ok
			if err != nil {
				e.Reason = err.Error()
			}
		}
		out = append(out, e)
	}

	return map[string]any{
		"target":     target,
		"target_tip": tip,
		"items":      out,
		// Said plainly rather than left to be inferred from a missing key,
		// because a caller that treats "no verdict" as "yes" is the failure
		// this is guarding.
		"decided": tip != "",
	}, nil
}
