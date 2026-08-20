package store

// A DEDUP IS A DIRECTED EDGE, and two rows closed into each other leave the
// work filed nowhere.
//
// HAPPENED TWICE IN ONE EVENING, both times between two seats working the same
// ask. Two seats file near-identical rows within a minute. Each then does the
// polite thing and closes THEIR OWN as a duplicate of the other's. Both rows
// end up done, the work is filed nowhere, and it looks exactly like a thing
// that got finished. The second time it also split the record: a progress note
// went onto the closed row while the reopened one knew nothing.
//
// What made it invisible is that neither close is wrong on its own. "This
// duplicates that, closing mine" is correct behaviour from both seats, at the
// same moment, and nothing had an opinion about the pair.
//
// THE EDGE ALREADY EXISTS UNDER ANOTHER NAME, which is why this is small.
// SupersedesField is "a report names the report it replaces, pointing
// backwards" - written by mem_write and the reports tool, read by replacedBy,
// announced by supersedeHeardIn, and indexed. "This is a duplicate of that" and
// "this is replaced by that" are the same directed edge with different words on
// it, so closing as a duplicate writes THAT field rather than inventing a
// second relation. One edge, two vocabularies, and every reader that already
// draws "replaced by" draws this for free.
//
// TWO REFUSALS, and the second is the one that was needed:
//
//   - a row cannot supersede ITSELF. A self-edge is a row that replaces
//     nothing, and every reader walking the chain has to special-case it.
//   - a row cannot supersede one that ALREADY POINTS AT IT. That is the
//     two-node cycle both seats built by being polite, and it is refusable at
//     the moment of the second close because the first edge is already written.
//
// LONGER CYCLES ARE NOT WALKED, deliberately, and this is the limit worth
// stating rather than hiding: A->B->C->A needs a walk, and deps.go has one
// (maxDepWalk) if it is ever wanted. Both observed failures were two-node, the
// two-node check is one read, and a walk that costs a query per hop on every
// close would be paid by every caller to catch a shape nobody has produced.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deadtrickster/flowy/internal/otel"
)

// CheckSupersedes refuses an edge that cannot mean what it says.
//
// It takes the row being closed and the row it names, and answers before
// anything is written: a refusal that wrote the edge and then complained would
// be the defect with a sentence attached.
func (d *DB) CheckSupersedes(ctx context.Context, p *Principal, id, replacedBy string) error {
	id = strings.TrimSpace(id)
	replacedBy = strings.TrimSpace(replacedBy)
	if id == "" || replacedBy == "" {
		return nil
	}
	if id == replacedBy {
		return refuseStatus("%s cannot replace itself - name the row that survives, "+
			"or close it without naming one", id)
	}
	// The row it names has to exist and be readable, for the reason every other
	// id argument here does: an edge pointing at something this caller cannot
	// see is an edge nobody can follow, and a typo would otherwise be recorded
	// as a fact.
	other, err := d.ReadArtifact(ctx, p, replacedBy, false)
	if err != nil {
		return refuseStatus("%s names %s as the row that survives, and that id is not "+
			"one this token can read - check it before closing", id, replacedBy)
	}
	// THE CYCLE. If the other row already says THIS one replaces it, then both
	// seats have decided the other's row is the survivor and neither is left
	// open. Refused at the second close, which is the last moment anybody is
	// holding the context to choose.
	if SupersedesOf(other) == id {
		return refuseStatus("%s and %s would replace each other, so neither would be left "+
			"open and the work would be filed nowhere. %s already names %s as its "+
			"survivor - reopen one of them, or close this without naming a survivor",
			id, replacedBy, replacedBy, id)
	}
	return nil
}

// CloseAsDuplicate closes a row and records which row survives, in one write.
//
// It is the close verb with the edge on it rather than a second call beside it,
// for the reason SetTodoStatus takes its note: the survivor is what makes the
// closure readable in a week, and a second call is one that can be skipped -
// which is how both of tonight's dedups ended up as prose in a note that only
// one of the two rows carried.
//
// THE NOTE IS STILL REQUIRED and naming a survivor satisfies it. A close that
// says "duplicate of X" has said the measurement: the work is not gone, it is
// over there. So a caller who gives no words gets that sentence rather than a
// refusal - see IsSilentClose, whose rule this keeps rather than bypasses.
func (d *DB) CloseAsDuplicate(
	ctx context.Context, p *Principal, todo, replacedBy string, said ...string,
) (*Artifact, *Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "todo.duplicate")
	defer span.End()

	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, refuseStatus("this token resolves to nobody, so it cannot close a " +
			"todo as a duplicate")
	}
	art, err := d.readWorkItem(ctx, p, strings.TrimSpace(todo))
	if err != nil {
		return nil, nil, err
	}
	if err := d.CheckSupersedes(ctx, p, art.ID, replacedBy); err != nil {
		return nil, nil, err
	}
	replacedBy = strings.TrimSpace(replacedBy)
	if replacedBy == "" {
		return nil, nil, refuseStatus("closing %s as a duplicate says which row survives - "+
			"name it, or close it as done with what was measured", art.ID)
	}

	note := strings.TrimSpace(strings.Join(said, "\n"))
	if note == "" {
		note = "duplicate of " + replacedBy
	}

	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, nil, err
	}
	fields[SupersedesField] = replacedBy
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: close %s as duplicate: %w", art.ID, err)
	}

	from := TodoStatusOf(art)
	entry, err := statusEntryEvent(art, p, actor, actorKind, from, DoneStatus)
	if err != nil {
		return nil, nil, err
	}
	written := []*Event{entry}
	noted, err := TodoNoteEntryEvent(art, p, note)
	if err != nil {
		return nil, nil, err
	}
	written = append(written, noted)

	// One write: the edge, the closure and both entries. A row that says
	// "duplicate" with no survivor on it, or a survivor with no closure, is the
	// half-written state this verb exists to make unreachable.
	if err := d.SetArtifactFieldsAndStatusIf(ctx, art, column, DoneStatus, "", written...); err != nil {
		return nil, nil, err
	}
	art.Notes = append(art.Notes, TodoNoteEntryOf(noted))
	span.SetArtifact(art.ID)
	return art, entry, nil
}
