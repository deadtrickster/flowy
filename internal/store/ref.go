package store

import (
	"errors"
	"fmt"
	"strings"
)

// Ref is an artifact address as new code should pass it: the project the
// artifact lives in, its type, and its id. Filed as 01M08FK999F2JWY9RQV5VC821N
// - A REFERENCE IS (PROJECT, TYPE, ID), NEVER A BARE ID.
//
// The reason is a debugging session, not a style preference. A bare id tells a
// reader nothing about where the thing lives or what it is, so every call site
// that has only an id has to guess one of the two - and the console has at
// least one place that guesses the type by assuming it matches the artifact
// standing next to the id, which is wrong exactly when the reference points
// somewhere else. A Ref carries the pieces instead of making a caller invent
// them.
//
// This type is for VALUES PASSED AROUND IN GO CODE, not for a new column or a
// new wire shape. A field that is already stored as a bare id - BlockerField,
// CiteRef.Message, SupersedesField - keeps that shape: those ids ride inside
// signed, replicated rows (event meta or artifact fields), and widening what
// is stored there breaks federation and every row already written. See the
// comment on each of those for why it stays a bare id in the row, and RefOf
// below for the one safe way to get a Ref: build it from an *Artifact code
// already has in hand, never by guessing a project or type onto an id read
// from somewhere else.
type Ref struct {
	Project string
	Type    string
	ID      string
}

// refSep separates a Ref's three segments in its canonical string form. A
// project name can never contain one - see cleanProjectName, which refuses a
// slash for the same reason a project is a directory in the FUSE mount - so
// splitting on it is unambiguous and nothing has to be escaped.
const refSep = "/"

// String is the canonical form: project/type/id. It is deliberately the same
// three segments in the same order as the console's route,
// /p/{project}/{type}/{id}, so a Ref reads as the path it names rather than
// as a shape somebody has to remember to translate.
func (r Ref) String() string {
	return r.Project + refSep + r.Type + refSep + r.ID
}

// ErrBadRef is what ParseRef and RefOf return for anything that is not a
// complete, well-formed reference. It is its own sentinel rather than
// ErrBadProjectName or ErrNotFound: a malformed ref is a caller's mistake
// about SHAPE, not a name the registry rejects or a row that is not there,
// and collapsing it into either of those would make a caller's errors.Is
// check for one of the others also catch this by accident.
var ErrBadRef = errors.New("store: not a well-formed reference")

// ParseRef reads a Ref back out of its canonical string form. It refuses
// anything that is not exactly three non-empty segments rather than taking
// however many it can find and leaving the rest zero: a Ref with a silently
// empty project is indistinguishable from one that correctly has none, and
// nothing downstream would know to ask. Refusing outright is what
// ParseCiteRef already does for the same reason - a reference that half-
// parses and renders as a link is worse than one that visibly failed to
// parse at all.
func ParseRef(s string) (Ref, error) {
	parts := strings.Split(s, refSep)
	if len(parts) != 3 {
		return Ref{}, fmt.Errorf("%w: %q is not project/type/id (%d segment(s), want 3)",
			ErrBadRef, short(s), len(parts))
	}
	project, typ, id := parts[0], parts[1], parts[2]
	switch {
	case project == "":
		return Ref{}, fmt.Errorf("%w: %q has no project", ErrBadRef, short(s))
	case typ == "":
		return Ref{}, fmt.Errorf("%w: %q has no type", ErrBadRef, short(s))
	case id == "":
		return Ref{}, fmt.Errorf("%w: %q has no id", ErrBadRef, short(s))
	}
	return Ref{Project: project, Type: typ, ID: id}, nil
}

// RefOf builds the triple for an artifact code already has in hand. It is the
// one door a Ref is meant to come through - built from a row this node just
// read, not assembled by a caller guessing a project or type onto an id that
// arrived alone.
//
// It refuses a personal artifact (no project) rather than inventing a
// sentinel for one: a personal artifact's project is nothing another reader's
// route resolves to, so a Ref standing for it would parse and then fail to go
// anywhere, which is a worse answer than refusing here where the caller can
// still say why. It refuses a missing type or id for the same reason - either
// makes a Ref that names a route nobody can follow.
func RefOf(a *Artifact) (Ref, error) {
	if a == nil {
		return Ref{}, fmt.Errorf("%w: no artifact to build a reference from", ErrBadRef)
	}
	if a.ID == "" {
		return Ref{}, fmt.Errorf("%w: artifact has no id", ErrBadRef)
	}
	if a.Project == nil || *a.Project == "" {
		return Ref{}, fmt.Errorf("%w: artifact %s has no project, and a reference without "+
			"one is not a route anybody else can follow", ErrBadRef, short(a.ID))
	}
	if a.Type == "" {
		return Ref{}, fmt.Errorf("%w: artifact %s has no type", ErrBadRef, short(a.ID))
	}
	return Ref{Project: *a.Project, Type: a.Type, ID: a.ID}, nil
}
