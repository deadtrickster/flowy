package store

import (
	"context"
	"fmt"
)

// A write this node cannot sign for a principal it holds a key for.
//
// THE GAP, named by @orchestrator when the twelve keygens were being weighed:
// a principal's key is per-principal, and a node may hold only its PUBLIC half
// - that is what `flowy principal pin` is for, and it is how a second machine
// learns whose word a row is. On such a node the write path finds no private
// key, signs nothing, and marks the row attributed. The row lands. It looks
// exactly like every other row here.
//
// AND EVERY PEER HOLDING THAT KEY REFUSES IT, at or after the epoch, because a
// row naming an author who has a key and carries no signature of theirs is the
// forgery the rule exists to stop. So the failure happens at a relay the writer
// never watches, minutes or days later, to somebody else. The writer's own node
// told them it worked.
//
// So it is refused HERE, at the door, where the person who typed it is standing.
// Two ways out and the message names both: write from the node that holds the
// private half, or install it here.
//
// WHAT IS NOT REFUSED, and each of these is a real state:
//
//   - a principal with no key anywhere. That is nearly every principal on a
//     fabric that has provisioned nothing, it is what `flowy principal exposed`
//     counts, and their rows are taken as attributed exactly as before.
//   - a row that ALREADY CARRIES a signature. A merge applying a peer's row, or
//     a caller writing one signed elsewhere, has brought the author's own word
//     with it - authorshipHere then checks it against the key, which is the
//     check that belongs to that case.
//   - a write by a party who is not the author. A status move, an assignee, a
//     forge link: the owner's signature covers the owner's words and travels on
//     untouched. This is asked of the AUTHOR of the row, which is the only
//     principal whose signature is missing.

// UnsignableWriteError is a local write by a principal whose key this node
// holds only the public half of.
type UnsignableWriteError struct {
	Principal string
}

func (e UnsignableWriteError) Error() string {
	return fmt.Sprintf("this node holds %s's public key and not their private half, so it "+
		"cannot sign what they write - and every node that holds their key refuses a row of "+
		"theirs that carries no signature of theirs. Write from the node that holds the "+
		"private half, or install it here with `flowy principal keygen --as %s` if this is "+
		"where they write", named(e.Principal), e.Principal)
}

// depRefusal marks this as the caller's situation rather than a broken node, so
// every door already turns it into a 400 quoting the sentence instead of
// reporting the store as down.
func (e UnsignableWriteError) depRefusal() {}

// refuseUnsignableWrite is asked on the local write paths, after the private
// key was looked for and not found.
//
// signed says the row already carries an authorship signature: then this is not
// a write nobody can sign, it is one signed somewhere else, and the check that
// belongs to it is authorshipHere's.
//
// The read is principalKeyOf through the caller's connection, like every other
// read a write makes - see the note at the top of the local write paths on why
// a read on the pool inside a write deadlocks.
func (d *DB) refuseUnsignableWrite(ctx context.Context, q execer, author string, signed bool) error {
	if author == "" || signed {
		return nil
	}
	_, _, ok, err := principalKeyOf(ctx, q, author)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return UnsignableWriteError{Principal: author}
}
