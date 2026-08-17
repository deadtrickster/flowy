package store

// What a citer may pass on is decided by what they may DO, not by what they can
// see - the operator's rule, and it is sharper than the read-only default this
// file otherwise inherits:
//
//	IF I HAVE READ PERMISSION ON A RESOURCE I MAY NOT CITE IT TO SOMEBODY WHO
//	CANNOT ALREADY READ IT. IF I HAVE WRITE, I MAY.
//
// Read is permission to know. Write is permission to decide, and deciding who
// else may know is a decision. So a citation has two readings depending on who
// makes it:
//
//	from a READER   a POINTER. It resolves for whoever already had access and
//	                refuses for everybody else, which citations.go already does:
//	                the quote is derived under the READER's permission filter,
//	                so a recipient who cannot reach the source is told the
//	                citation is of something out of reach and nothing more.
//
//	from a WRITER   a GRANT, and recorded as one with the granter named. This
//	                file is that half.
//
// A grant that leaves no trace is indistinguishable from a leak afterwards.
// That is the whole safety of it: this is the mechanism by which a private row
// legitimately becomes visible, so it goes in the grants table like every other
// capability - signed, replicated, revocable by tombstone, and carrying
// granted_by.
//
// WHAT IS DELIBERATELY NOT GRANTED HERE
//
// Only a PERSONAL artifact is granted, to a NAMED addressee. Both limits are
// the same argument, which is that a grant must name a subject who did not
// already have the access:
//
//   - A project artifact is already readable by every member of its project, so
//     citing one to a colleague grants nothing and would write a capability row
//     per quotation. Citing one OUTWARD, to somebody outside the project, is a
//     project-level decision - a project-to-project edge, or a share of that
//     artifact - and it is too big a thing to happen as a side effect of
//     quoting. It stays a pointer until somebody decides it deliberately.
//
//   - An undirected room message has no subject to grant to. Its audience is
//     the room, which is the project, which returns the same question one step
//     out. A citation to the room is a pointer.
//
// So the shipped rule is the unambiguous corner of the operator's: a personal
// row, cited to one named person, by the one principal who may decide about it.

import (
	"context"
	"fmt"
)

// GrantCitedArtifact records the grant a citation makes when the citer is
// deciding rather than merely knowing.
//
// It reports whether a grant was written. False and no error is the ordinary
// answer - most citations grant nothing - so a caller says nothing about it
// rather than treating it as a failure.
//
// The check is the SAME predicate a read makes, asked about the citer: a
// principal whose own access to this artifact came from a grant may not pass it
// on, because their capability is read. Only the owner of a personal artifact
// may, and for a personal artifact that is the only principal who can read it
// at all.
func (d *DB) GrantCitedArtifact(ctx context.Context, citer *Principal, artifactID, subject string) (bool, error) {
	if citer == nil || artifactID == "" || subject == "" {
		return false, nil
	}
	// Granting to yourself is not a decision about anybody.
	if subject == citer.UserID {
		return false, nil
	}
	art, err := d.GetArtifact(ctx, artifactID)
	if err != nil {
		// A citation of a message that names an artifact this node has never
		// seen grants nothing, and is not an error in the send: the pointer
		// half already tells the reader it is out of reach.
		return false, nil
	}
	if !citerMayGrant(citer, art) {
		return false, nil
	}
	// Not twice. A room where somebody quotes the same private row four times
	// would otherwise carry four identical capabilities, and a revocation would
	// have to find all of them to mean anything.
	var already bool
	if err := d.sql.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM grants
		                 WHERE coalesce(tombstone, false) = false
		                   AND artifact = $1 AND subject = $2)`,
		artifactID, subject).Scan(&already); err != nil {
		return false, fmt.Errorf("store: look for the grant this citation would repeat: %w", err)
	}
	if already {
		return false, nil
	}
	g := &Grant{
		Subject:   subject,
		Artifact:  artifactID,
		Cap:       CapRead,
		GrantedBy: citer.UserID,
	}
	if err := d.InsertGrant(ctx, g); err != nil {
		return false, fmt.Errorf("store: record the grant this citation makes: %w", err)
	}
	return true, nil
}

// citerMayGrant is the write half of the rule, as a predicate over the artifact
// rather than over a capability column - there is no write capability in this
// fabric, so "may write" is the set of principals whose access does not come
// from somebody else's grant.
//
// Personal and owned is the case this ships. A project artifact returns false
// for the reason at the top of the file: nothing to grant to a member, and a
// decision too large to make by quotation for anybody else.
func citerMayGrant(p *Principal, art *Artifact) bool {
	if p == nil || art == nil || p.UserID == "" {
		return false
	}
	if art.Visibility != VisibilityPersonal && art.Project != nil {
		return false
	}
	return art.OwnerUser == p.UserID
}
