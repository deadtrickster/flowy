package store

import (
	"strings"
)

// WHO RAISED A QUEUE ITEM, which is not the same fact as who wrote the row.
//
// owner_user is already on every artifact and it is the SIGNING AUTHOR: the
// seat whose token wrote the row, inside the signature, checked on the way in
// and never rewritten. For a person filing their own work that is also the
// answer to "where did this come from", and for a long time nothing here
// needed a second word for it.
//
// Then four agents shared one board. An agent files a row because the operator
// asked for it in #general, and owner_user says the agent - which is true, and
// is not the question a person reading the board is asking. The trail back to
// the request is gone: the row reads as work the agent invented, and the ask
// that produced it exists only in a conversation nobody rereads. The other way
// round loses the same thing more quietly - the operator raising something
// through the room panel gets owner_user=operator, which happens to be right,
// and nothing on the row tells a reader which of the two they are looking at.
//
// So a queue item carries a RAISER beside its assignee: who the work came
// from, as a handle, the same kind of claim an assignee is. Raised by X,
// carried by Y - two facts, and a surface that draws one of them is the
// ambiguity this exists to end.
//
// WHAT IT IS NOT, and each of these is a thing it would be easy to make it.
//
// It is not owner_user renamed and it migrates nothing. owner_user stays the
// signing author, inside the signed payload, load-bearing for provenance; this
// is a second and WEAKER fact that lives beside it, and a row where the two
// disagree is the ordinary case rather than a contradiction.
//
// It is not a principal. It is a handle - whatever the party the work came
// from is called around here - and the node resolves it to nothing. The
// permission filter has never looked inside fields and does not start here, so
// naming somebody as the raiser hands them exactly what naming them as the
// assignee hands them, which is nothing. See AssigneeField, which says the same
// thing at more length and for the same reason.
//
// It is not a second assignee. An assignee CHANGES HANDS - it is a claim
// somebody makes, overrides and hands on, which is why there is an event behind
// every one of them (see assign.go). Where the work CAME FROM is settled at the
// moment the row is raised and does not happen again, so there is nothing to
// fold and no log to keep: the row's own creation is the record, and the write
// paths refuse to restate it on an update rather than growing a second
// half-recorded verb.
//
// AND NOTHING IS GUESSED FOR A ROW THAT HAS NONE. Every queue item written
// before this key carries no raiser, and RaiserOf answers "" for those rather
// than falling back to owner_user - which would be this node asserting, on the
// whole board at once, a fact nobody stated. There is no older convention to be
// compatible with the way AssigneeOf is compatible with an OWNER line in a
// body: either the key is on the row because somebody or something said so, or
// the answer is that nobody said.
const RaiserField = "raiser"

// MaxRaiserName is the longest name a write may hand a queue item as its
// raiser. It is MaxAssigneeName because it is the same kind of value drawn in
// the same kind of column - a handle, on one line, beside the party carrying
// the work - and two different bars for two handles on one row would be a
// difference nobody could predict from either surface.
const MaxRaiserName = MaxAssigneeName

// NormalizeRaiser validates a name a write offers as the raiser and returns it
// as the node stores it.
//
// Empty is the ordinary case and it means nobody said. So do the words this
// queue has always used for nobody - they collapse the same way an assignee's
// do, so that "unassigned" and "?" and an absent key are ONE state on every
// surface rather than three that read as distinctions. See NobodyName.
//
// It is here rather than beside a door because both doors that create queue
// items call it: a name that is a pasted paragraph has to be refused wherever
// it arrives, and a refusal only one of the two doors makes is a rule that
// depends on which client you used.
func NormalizeRaiser(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || NobodyName(name) {
		return "", nil
	}
	if strings.ContainsAny(name, "\n\r\t") || len(name) > MaxRaiserName {
		return "", refuseAssign("%q is not a name: a raiser is a handle of at most %d "+
			"characters on one line", name, MaxRaiserName)
	}
	return name, nil
}

// RaiserOf is who a queue item says the work came from, and "" for one that
// says nobody.
//
// It is one function for the reason RoomOf is one: the key is one key, and the
// console, the terminal client and the node reading it three times is three
// chances to disagree about what an absent one means. Here it means exactly
// what it says - this row does not record where the work came from - and there
// is deliberately no second place to look and no id to fall back on.
func RaiserOf(a *Artifact) string {
	name, _ := artifactField(a, RaiserField).(string)
	return strings.TrimSpace(name)
}
