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

// FillDisowned resolves Disowned on every artifact and event given, for the
// repudiations this principal may read.
//
// A reader who may not read a repudiation is not told about it, which follows
// from asking through the permission-filtered list rather than around it: a
// mark whose evidence you cannot open is a rumour, and this fabric already
// refuses to hand those out.
func (d *DB) FillDisowned(
	ctx context.Context, p *Principal, arts []*Artifact, events []*Event,
) error {
	if len(arts) == 0 && len(events) == 0 {
		return nil
	}
	reps, err := d.Repudiations(ctx, p)
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
