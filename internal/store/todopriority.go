package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deadtrickster/flowy/internal/otel"
)

// WHAT TO DO FIRST.
//
// The operator, on the board: "add priorities to todos, and merges". Sixteen
// unowned rows at the time, six of them theirs, the oldest from 18:47 - and
// nothing on any of them said which one they wanted first. A board with no
// order is a board where every reader picks by their own taste, which is what
// this fleet did all night: we shipped ten things and only ten of the sixty
// rows we closed were the operator's.
//
// A CLOSED SET AND NOT A NUMBER, for the reason the category field gives: a
// free integer becomes 1..5 and then an argument about whether 2 outranks 3,
// and nothing downstream can act on the population. Three words, each of which
// a reader can act on without asking anybody:
//
//   - now: before whatever you were going to do next.
//   - next: when the thing in your hands is finished.
//   - later: real work, deliberately not now - the value that says somebody
//     LOOKED and decided it can wait.
//
// ABSENT IS NOT "NORMAL". A row with no priority has not been judged, and that
// is a different fact from one judged unimportant. It is the same distinction
// the category field keeps, and the same one this fleet spent the night finding
// in other places: a count of zero and a count nobody took are not the same
// answer. So the ordering below places unjudged ABOVE later - an unjudged row
// might be the most urgent thing on the board and nobody has looked at it, and
// a row somebody deliberately shelved has an owner's decision on it.
//
// IT IS THE SAME VERB FOR A TODO AND A MERGE, because the operator asked for
// both in one sentence and because they are the same question: which of the
// things waiting should move first. readWorkItem is what makes that free - it
// reads either kind - so a merge row carries the same field, the queue orders
// by it, and nothing had to learn a second word.
const PriorityField = "priority"

const (
	PriorityNow   = "now"
	PriorityNext  = "next"
	PriorityLater = "later"
)

// TodoPriorities is the vocabulary, most urgent first. The order of this slice
// is the order of the board: it is what SortRank reads, so adding a word here
// puts it in its place everywhere at once rather than in four sort functions.
var TodoPriorities = []string{PriorityNow, PriorityNext, PriorityLater}

// priorityRank is where each word sits, and where an unjudged row sits between
// them.
//
// UNJUDGED IS 2, BETWEEN next AND later, which is the whole opinion in this
// file. A row nobody has ranked may be the most urgent thing here; a row somebody
// ranked `later` has been looked at and set down. Sorting the unjudged to the
// bottom would bury exactly the rows that have had the least attention, which is
// the failure this field exists to fix rather than a shape of it.
var priorityRank = map[string]int{
	PriorityNow:   0,
	PriorityNext:  1,
	"":            2,
	PriorityLater: 3,
}

// PriorityOf is what a row says about its own urgency, or "" for one nobody has
// ranked. Trimmed and lowercased on the way out for the reason CategoryOf does
// it: a value written by an older client or by hand should read as the word it
// obviously is.
func PriorityOf(a *Artifact) string {
	return strings.ToLower(strings.TrimSpace(artifactString(a, PriorityField)))
}

// PriorityRankOf is that word as a number, for sorting. An unknown word - which
// the write door refuses, but a replicated row from a newer peer might carry -
// sorts with the unjudged rather than at either end: this node cannot know what
// a word it has never heard means, and guessing is how a queue quietly reorders
// itself around a value nobody here can read.
func PriorityRankOf(a *Artifact) int {
	if rank, known := priorityRank[PriorityOf(a)]; known {
		return rank
	}
	return priorityRank[""]
}

// NormalizeTodoPriority is the word this store will keep, or a refusal naming
// the set.
//
// EMPTY IS ALLOWED AND MEANS UNJUDGED, which is how a priority is taken back.
// It is not an unknown word; it is the value every row on this board already
// has, and a field that could only ever be set would make a mistaken `now`
// permanent.
func NormalizeTodoPriority(asked string) (string, error) {
	priority := strings.ToLower(strings.TrimSpace(asked))
	if priority == "" {
		return "", nil
	}
	for _, known := range TodoPriorities {
		if priority == known {
			return priority, nil
		}
	}
	return "", fmt.Errorf("store: %q is not a priority - it is one of %s, or empty to take one back",
		asked, strings.Join(TodoPriorities, ", "))
}

// EventTodoPriority is what a ranking leaves in the log.
//
// The value on the row says what is true now; the log says who decided it and
// what it was before. A priority that changed with no record is an argument
// nobody can settle - "I thought we agreed this was next" - and the operator
// setting one is exactly the kind of decision a reader will want attributed.
const EventTodoPriority = "todo.priority"

// SetTodoPriority ranks a work item, or takes its ranking away.
//
// READ PERMISSION IS THE BAR, as it is for the category and the assignee.
// Whoever can see the work can say when it should happen: the agent that opened
// a row and found it blocks three others is in a position to say `now`, and it
// is routinely not whoever filed it. It hands the caller nothing - the
// permission filter has never looked at this key - so the widest it reaches is
// "whoever reads it can rank it".
func (d *DB) SetTodoPriority(
	ctx context.Context, p *Principal, todo, asked string,
) (*Artifact, *Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "todo.priority")
	defer span.End()

	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, fmt.Errorf("store: this token resolves to nobody, so it cannot say " +
			"what to do first")
	}
	priority, err := NormalizeTodoPriority(asked)
	if err != nil {
		return nil, nil, err
	}
	art, err := d.readWorkItem(ctx, p, strings.TrimSpace(todo))
	if err != nil {
		return nil, nil, err
	}

	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, nil, err
	}
	from := PriorityOf(art)
	if priority == "" {
		// DELETED RATHER THAN SET EMPTY. An empty string in the column would
		// read as a value to anything counting the vocabulary - a board saying
		// "3 now, 2 next, 41 ''" - and the absence is the thing being restored.
		delete(fields, PriorityField)
	} else {
		fields[PriorityField] = priority
	}
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: rank %s: %w", art.ID, err)
	}

	meta, err := json.Marshal(map[string]string{
		PriorityField: priority,
		"from":        from,
		"actor_kind":  actorKind,
		"actor_user":  p.UserID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("store: rank %s: %w", art.ID, err)
	}
	entry := &Event{
		Type:     EventTodoPriority,
		Project:  art.Project,
		Room:     categoryRoom(art),
		Thread:   art.ID,
		Artifact: art.ID,
		Actor:    actor,
		Body:     priorityLine(art, from, priority),
		Meta:     meta,
	}
	if err := d.SetArtifactFields(ctx, art, column, entry); err != nil {
		return nil, nil, err
	}
	span.SetArtifact(art.ID)
	return art, entry, nil
}

// priorityLine is what the log says in words, and it says BOTH ENDS for the
// reason the category entry does: "next" alone is a fact about now, and the
// question a reader has later is what changed.
func priorityLine(a *Artifact, from, to string) string {
	what := "todo"
	if a.Kind == MergeKind {
		what = "merge"
	}
	switch {
	case to == "":
		return "took the priority off this " + what + " (was " + from + ")"
	case from == "":
		return "this " + what + " is " + to
	default:
		return "this " + what + " is " + to + " (was " + from + ")"
	}
}
