package store

import (
	"context"
	"strings"
)

// Reading a repudiation, which is the half that reaches a person.
//
// The store half landed a type, a window, a write door and Repudiated(). None
// of it changed anything anybody sees, so a repudiation was a row that made no
// difference to the one reader it exists for: the person whose name was used.
//
// THE MARK IS A THIRD READING AND IT REPLACES NEITHER OF THE OTHER TWO.
// Authorship records whether a signature verified HERE, and that stays true of
// a stolen key - the bytes really were signed with it. So a row does not become
// "attributed" when its author disowns it; it becomes "authored, and its author
// disowns it", which is a stranger sentence than either half and the only
// accurate one.
//
// ONE READ PER PAGE, NOT ONE PER ROW. FillDisowned takes the rows it is given
// and asks the database once, because a per-row query would put a round trip
// inside a render loop - and worse, would let two rows on one page be judged
// against two different states of the repudiation list. Repudiated() takes a
// list for exactly that reason and this is the caller it was written for.
//
// THE SUBJECT IS HALF THE QUESTION, and forgetting it is the mistake this
// carries a negative control against. A window is a range of clock readings and
// EVERY principal writes into it at once, so a reader that matched on the
// window alone would disown the whole fabric for that period - every row by
// everybody, on the word of one person about their own key.

// FillDisowned resolves Disowned on every artifact and event given, against
// every repudiation this node holds.
//
// EVERY ONE, not the ones this reader may open, and the change is the point of
// the row it comes from (01M0BNAWCP). A repudiation is a fact about a
// PRINCIPAL, and a principal writes in more than one project; artifact reach is
// project-scoped, so asking through the filtered list answered "the
// repudiations in your project" - which meant a subject had to file one per
// project, and any project they forgot kept rendering the thief's rows as their
// own word. `flowy principal repudiate` needed --project for that reason, and
// the requirement was the defect.
//
// The first version of this file argued the opposite: a mark whose evidence you
// cannot open is a rumour. That argument is about what a mark REVEALS, and this
// one reveals nothing. It annotates rows the caller can already read, with a
// claim the subject signed and published about their own authorship; the
// repudiation's own body, reason and project stay behind the ordinary filter
// (Repudiations), so what a reader who cannot open it learns is exactly what
// its author wrote it to say - "that was not me".
//
// It is the same shape as the authorship check it sits beside: principalKeyOf
// reads keys with no permission filter, because "whose word is this row" is not
// a question about who is asking.
// THE PRINCIPAL IS GONE FROM THE SIGNATURE, and that is not tidying. A
// permission argument a function does not use is a lie about what it does: the
// next reader sees `p` and concludes the answer is filtered by the caller, which
// is exactly the belief this change makes false.
func (d *DB) FillDisowned(
	ctx context.Context, arts []*Artifact, events []*Event,
) error {
	if len(arts) == 0 && len(events) == 0 {
		return nil
	}
	reps, err := d.LiveRepudiations(ctx)
	if err != nil {
		return err
	}
	if len(reps) == 0 {
		return nil
	}
	for _, a := range arts {
		if a == nil || a.Type == RepudiationType {
			// A repudiation is not itself disowned by its own window - it is
			// the statement, not one of the rows the statement is about.
			continue
		}
		a.Disowned = disownedBy(reps, a.OwnerUser, a.HLC)
	}
	for _, e := range events {
		if e == nil {
			continue
		}
		e.Disowned = disownedBy(reps, e.Actor, e.SeqHLC)
	}
	return nil
}

// disownedBy is Repudiated() turned into what a reader is handed.
func disownedBy(reps []*Artifact, author string, at int64) *Disowned {
	rep := Repudiated(reps, author, at)
	if rep == nil {
		return nil
	}
	from, to := RepudiationWindowOf(rep)
	return &Disowned{
		By:      rep.ID,
		Subject: strings.TrimSpace(author),
		Reason:  strings.TrimSpace(rep.Body),
		From:    from,
		To:      to,
	}
}
