package store

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/sign"
)

// Signing a row on the way out, and verifying one on the way in.
//
// The adapters below are the only place the store's row types meet the
// canonical encoding in internal/sign. They are deliberately dull: a struct
// literal per table, field for field, so that adding a replicated column and
// forgetting to authenticate it is a diff somebody can see rather than an
// omission buried in a hash.
//
// Local writes sign. Every path that mints or moves a replicated row stamps the
// reading and this node's name and then signs the result, in the same statement
// that writes it - so node is always this node on a signature this node makes,
// and the row is self-consistent by construction.
//
// Local reads do not verify. The store is this node's own, a signature check on
// every read would be a signature check on every row of every list, and a
// database whose rows an attacker can edit directly is a database whose
// node_identity they can edit directly too. Verification is at the merge,
// which is where rows arrive from somewhere else.

// artifactView is the row as the signing package sees it. It is split out from
// canonicalArtifact because two different signatures are made over two
// different cuts of the same struct - the node's, over all of it, and the
// author's, over the fields only the owner writes - and one adapter is one
// place to forget a column rather than two.
func artifactView(a *Artifact) sign.Artifact {
	return sign.Artifact{
		ID: a.ID, OwnerUser: a.OwnerUser, Project: a.Project, Visibility: a.Visibility,
		Type: a.Type, Title: a.Title, Body: a.Body, HLC: a.HLC, Node: a.Node,
		Tombstone: a.Tombstone,
		Kind:      a.Kind, Discovery: a.Discovery, Status: a.Status, Severity: a.Severity,
		Tags: a.Tags, UserTags: a.UserTags, Related: a.Related, FilePath: a.FilePath,
		Fields: canonicalJSON(a.Fields), Reported: a.Reported, External: externalBytes(a.External),
		Created: a.Created,
	}
}

// canonicalArtifact is the byte string an artifact's signature is over.
func canonicalArtifact(a *Artifact) []byte {
	return sign.CanonicalArtifact(artifactView(a))
}

// canonicalArtifactAuthorship is the byte string the OWNER's signature over an
// artifact is made against: a subset of the above, and see
// sign.CanonicalArtifactAuthorship for why it has to be one.
func canonicalArtifactAuthorship(principal string, a *Artifact) []byte {
	return sign.CanonicalArtifactAuthorship(principal, artifactView(a))
}

// canonicalJSON is a jsonb column as the bytes that are signed: parsed and
// re-encoded, rather than taken as it arrived.
//
// It has to be. A jsonb column is not a string - Postgres parses it, drops the
// whitespace, orders the keys its own way and normalises the numbers - so the
// bytes a node signs on the way in are not the bytes it reads back out, and a
// signature over the request body would fail on the first row that carried a
// meta or a fields object. Both ends normalise the same way instead: parse,
// re-encode with Go's own ordering, hash that. What is authenticated is the
// value, which is what the column holds.
//
// A value that will not parse is signed as it stands. It cannot have come from
// a jsonb column, so there is nothing for a round trip to change.
func canonicalJSON(raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	out, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return out
}

// externalBytes is the forge link as the bytes that are signed. It is
// re-marshalled rather than taken as it arrived, because what this node stores
// is the re-marshalled form: signing the wire bytes would make a row that
// verifies here and not on the peer that received it from here.
func externalBytes(ref *ExternalRef) []byte {
	if ref == nil {
		return nil
	}
	raw, err := json.Marshal(ref)
	if err != nil {
		// Marshalling an ExternalRef cannot fail - it is strings, ints and
		// times - and a signature over a marker is still a signature over
		// something that changes when the link changes.
		return []byte("unmarshalable-external-ref")
	}
	return raw
}

// createdNow is the date a local write stamps on a row it is about to insert.
//
// The column has a DEFAULT now() and used to be left to it, which put the date
// outside the signature: signing happens here, before the statement runs, so a
// value the database invents after it is a value nothing signed - and a signed
// row whose date a relay may rewrite is worse than an unsigned one, because
// everything around it says authentic. So the node mints the date itself and
// passes it in, and the row that lands is the row that was signed.
//
// Truncated to the microsecond because that is what a timestamptz keeps: a
// finer value would be rounded by the column and the row would stop verifying
// the moment it was read back out.
//
// It is not taken from the caller. A create arrives from a request body, and a
// date somebody may choose for their own row is a row that sorts wherever they
// like in everybody's list. An update keeps the date the row already has - see
// upsertArtifact, which reads it - because an edit is not a new artifact.
//
// Grants and tasks have no such column, so there is nothing there to sign.
func createdNow() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func canonicalGrant(g *Grant) []byte {
	return sign.CanonicalGrant(sign.Grant{
		ID: g.ID, FromProject: g.FromProject, ToProject: g.ToProject, Subject: g.Subject,
		Artifact: g.Artifact, Cap: g.capOrDefault(), GrantedBy: g.GrantedBy,
		HLC: g.HLC, Node: g.Node, Tombstone: g.Tombstone,
	})
}

// capOrDefault is what the column holds after the write: insertGrant defaults an
// empty cap to 'read', and the read side coalesces to 'read', so the signature
// is over the value the row will have rather than the one the caller left out.
func (g *Grant) capOrDefault() string {
	if g.Cap == "" {
		return "read"
	}
	return g.Cap
}

// canonicalProject is the byte string a registry row's signature is over. The
// origin chain is in it because the merge decides a name collision by comparing
// chains - an origin a relay could rewrite in flight would be a decision a
// relay could make.
func canonicalProject(p *Project) []byte {
	return sign.CanonicalProject(sign.Project{
		ID: p.ID, Name: p.Name, CreatedBy: p.CreatedBy, Provenance: p.Provenance,
		Fixture: p.Fixture, Origin: p.Origin, Superseded: p.Superseded,
		OriginAt: p.OriginAt, HLC: p.HLC, Node: p.Node, Created: p.Created,
	})
}

func canonicalTask(t *Task) []byte {
	return sign.CanonicalTask(sign.Task{
		ID: t.ID, Artifact: t.Artifact, FromUser: t.FromUser, ToUser: t.ToUser,
		AssigneeAgent: t.AssigneeAgent, State: t.State, HLC: t.HLC, Node: t.Node,
		Project: t.Project, Thread: t.Thread,
	})
}

// eventView is the row as the signing package sees it - see artifactView.
func eventView(e *Event) sign.Event {
	return sign.Event{
		ID: e.ID, Artifact: e.Artifact, Thread: e.Thread, Actor: e.Actor, Type: e.Type,
		Body: e.Body, Meta: canonicalJSON(e.Meta), Parents: e.Parents, HLC: e.SeqHLC, Node: e.Node,
		Project: e.Project, Room: e.Room, Created: e.Created, Addressee: e.Addressee,
	}
}

func canonicalEvent(e *Event) []byte { return sign.CanonicalEvent(eventView(e)) }

// canonicalEventAuthorship is the byte string the ACTOR's signature over an
// event is made against: the whole event, because an event is immutable.
func canonicalEventAuthorship(principal string, e *Event) []byte {
	return sign.CanonicalEventAuthorship(principal, eventView(e))
}

// CanonicalEventBytes is canonicalEvent, for a caller outside this package that
// needs to ask what an event's signature is over.
//
// It exists so that a surface which puts a CLAIM on a row can prove the claim is
// inside the signature rather than beside it. A field a relay can strip or swap
// on a row it did not write is decoration, and a marker saying "this entry was
// written about somebody else's work" is exactly the kind of field that is
// believed - so the worklog's test builds the row its own write builds and asks
// these bytes whether the marker changed them. See worklog_test.go in the server
// package, and Event.Addressee in the sign package for the same argument about
// the field that got a column of its own.
//
// It answers bytes and never signs anything: the private key is this package's
// and stays in it.
func CanonicalEventBytes(e *Event) []byte { return canonicalEvent(e) }

func canonicalIdentity(node string, public []byte) []byte {
	return sign.CanonicalIdentity(node, public)
}

func signBytes(priv ed25519.PrivateKey, msg []byte) []byte { return sign.Sign(priv, msg) }

func verifyBytes(public, msg, sig []byte) bool { return sign.Verify(public, msg, sig) }

// SignArtifact stamps a's signature. The four Sign* functions are exported for
// the callers that hold a key and no database: `flowy sign`, which is how a
// test or an operator hands a node a delta it will actually take, and the tests
// that stand in for a second node.
func SignArtifact(priv ed25519.PrivateKey, a *Artifact) {
	a.Sig = signBytes(priv, canonicalArtifact(a))
}

// SignGrant stamps g's signature.
func SignGrant(priv ed25519.PrivateKey, g *Grant) { g.Sig = signBytes(priv, canonicalGrant(g)) }

// SignTask stamps t's signature.
func SignTask(priv ed25519.PrivateKey, t *Task) { t.Sig = signBytes(priv, canonicalTask(t)) }

// SignProject stamps p's signature.
func SignProject(priv ed25519.PrivateKey, p *Project) {
	p.Sig = signBytes(priv, canonicalProject(p))
}

// SignEvent stamps e's signature.
func SignEvent(priv ed25519.PrivateKey, e *Event) { e.Sig = signBytes(priv, canonicalEvent(e)) }

// SignEventAs stamps the AUTHOR's signature on an event: the second of the two
// signatures a row can carry, made with the key of the principal named in its
// actor column rather than with any node's. See internal/store/principal.go for
// what the two claims are and why they are never one.
//
// It takes the principal rather than reading e.Actor so that signing as
// somebody the row does not name is possible to write, which is how the refusal
// is tested.
func SignEventAs(priv ed25519.PrivateKey, principal string, e *Event) {
	e.AuthorSig = signBytes(priv, canonicalEventAuthorship(principal, e))
}

// SignArtifactAs stamps the OWNER's signature on an artifact.
func SignArtifactAs(priv ed25519.PrivateKey, principal string, a *Artifact) {
	a.AuthorSig = signBytes(priv, canonicalArtifactAuthorship(principal, a))
}

// SignSet signs every row of a delta with one key. It does not touch the node
// column: a row says which node wrote it, and signing is not the place to
// decide that. A set whose rows name a node this key does not belong to is
// exactly the forgery the merge is there to refuse, and being able to build one
// is how that refusal is tested.
//
// It does date a row that carries no date, because created is inside the
// signature. A row signed without one can be merged - the receiver puts its own
// clock in the column, since the column has to hold something - but it can
// never be relayed on from there: what the next node is handed is the date the
// receiver invented, and the signature is over a row that had none. So a delta
// assembled by hand gets this moment as its date, in the same call that signs
// it, and travels as far as any other row.
func SignSet(priv ed25519.PrivateKey, set *SyncSet) {
	if set == nil {
		return
	}
	for _, a := range set.Artifacts {
		if a.Created.IsZero() {
			a.Created = createdNow()
		}
		SignArtifact(priv, a)
	}
	for i := range set.Grants {
		SignGrant(priv, &set.Grants[i])
	}
	for _, t := range set.Tasks {
		SignTask(priv, t)
	}
	for _, project := range set.Projects {
		if project.Created.IsZero() {
			project.Created = createdNow()
		}
		SignProject(priv, project)
	}
	for _, e := range set.Events {
		if e.Created.IsZero() {
			e.Created = createdNow()
		}
		SignEvent(priv, e)
	}
}

// The local write paths. Each of them stamps the row first and signs what it is
// about to write, so the signature is over the values that land in the columns.
//
// EVERY ONE OF THEM TAKES THE THING THE WRITE IS BEING MADE AGAINST, and reads
// the keys it needs through that, because signing is not a pure function of the
// row: it reads this node's key and it reads the author's. Those reads used to
// go to d.sql - the pool - whatever the caller had in hand, and a caller with a
// transaction in hand therefore held one connection while queueing for a second.
//
// The pool is finite. Once as many writers were in flight as the pool has
// connections, every connection was inside a transaction and every transaction
// was waiting for a connection, and the only thing that ever broke the cycle was
// a context expiring. It did not present as a deadlock - the writes eventually
// returned, having failed, so it read as latency - which is how it survived.
//
// So the rule is: a read a write makes goes through the same connection as the
// write. Enlarging the pool would only move the number of concurrent writers at
// which the cycle closes.

func (d *DB) signArtifact(ctx context.Context, q execer, a *Artifact) error {
	priv, err := d.signer(ctx, q)
	if err != nil {
		return err
	}
	SignArtifact(priv, a)
	return d.authorArtifact(ctx, q, a)
}

// authorArtifact puts the owner's own signature on a row this node is about to
// write, when this node is the one holding that owner's key.
//
// It does not clear an author signature it cannot make. A status move, a todo's
// assignee or a forge link is written by somebody who is not the owner, and the
// signature already on the row covers none of those columns - see
// sign.CanonicalArtifactAuthorship - so it is still the owner's signature over
// the owner's words and it travels on. Clearing it would turn every party's
// ordinary write into a row the owner's peers then refuse.
func (d *DB) authorArtifact(ctx context.Context, q execer, a *Artifact) error {
	priv, held, err := d.principalSigner(ctx, q, a.OwnerUser)
	if err != nil {
		return err
	}
	if held {
		SignArtifactAs(priv, a.OwnerUser, a)
		a.Authorship = AuthorshipAuthored
		return nil
	}
	a.Authorship, err = d.authorshipHere(ctx, q, a.OwnerUser,
		canonicalArtifactAuthorship(a.OwnerUser, a), a.AuthorSig)
	return err
}

func (d *DB) signEvent(ctx context.Context, q execer, e *Event) error {
	priv, err := d.signer(ctx, q)
	if err != nil {
		return err
	}
	SignEvent(priv, e)
	return d.authorEvent(ctx, q, e)
}

// authorEvent puts the actor's own signature on an entry this node is about to
// append. An event's actor is decided by the token that appended it - that is
// what POST /api/events enforces and what checkEvent enforces at the merge - so
// this node signing as that principal is this node saying what its own
// credentials already said, with a key rather than with a claim.
func (d *DB) authorEvent(ctx context.Context, q execer, e *Event) error {
	priv, held, err := d.principalSigner(ctx, q, e.Actor)
	if err != nil {
		return err
	}
	if held {
		SignEventAs(priv, e.Actor, e)
		e.Authorship = AuthorshipAuthored
		return nil
	}
	e.Authorship, err = d.authorshipHere(ctx, q, e.Actor,
		canonicalEventAuthorship(e.Actor, e), e.AuthorSig)
	return err
}

// authorshipHere is what a local write may claim about a row it cannot sign: a
// signature that verifies under the public key this node holds for the author,
// or attributed.
//
// The second case is nearly everything today and it is not a failure. A node
// that holds no key for a principal is in the position every node was in before
// principal signing existed, and the mark says so rather than dressing the
// node's own word up as the author's.
func (d *DB) authorshipHere(
	ctx context.Context, q execer, author string, msg, sig []byte,
) (string, error) {
	if author == "" || len(sig) == 0 {
		return AuthorshipAttributed, nil
	}
	// principalKeyOf rather than GetPrincipalKey because this read is made on
	// behalf of a write and must go through the same connection as the write -
	// see the note at the top of the local write paths. It is the same lookup
	// the merge makes of every row that names an author.
	public, _, ok, err := principalKeyOf(ctx, q, author)
	if err != nil {
		return AuthorshipAttributed, err
	}
	if !ok {
		return AuthorshipAttributed, nil
	}
	if verifyBytes(public, msg, sig) {
		return AuthorshipAuthored, nil
	}
	return AuthorshipAttributed, nil
}

func (d *DB) signGrant(ctx context.Context, q execer, g *Grant) error {
	priv, err := d.signer(ctx, q)
	if err != nil {
		return err
	}
	SignGrant(priv, g)
	return nil
}

func (d *DB) signProject(ctx context.Context, q execer, p *Project) error {
	priv, err := d.signer(ctx, q)
	if err != nil {
		return err
	}
	SignProject(priv, p)
	return nil
}

func (d *DB) signTask(ctx context.Context, q execer, t *Task) error {
	priv, err := d.signer(ctx, q)
	if err != nil {
		return err
	}
	SignTask(priv, t)
	return nil
}

// DecodeKey reads a public key as an operator wrote it down: hex, or base64 in
// either alphabet. It is the one place the two spellings are accepted, so a key
// copied out of `flowy identity` and a key copied out of a JSON payload are the
// same key here.
func DecodeKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("store: no key given")
	}
	if key, err := hex.DecodeString(raw); err == nil && len(key) == ed25519.PublicKeySize {
		return key, nil
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if key, err := enc.DecodeString(raw); err == nil && len(key) == ed25519.PublicKeySize {
			return key, nil
		}
	}
	return nil, fmt.Errorf("store: %q is not an ed25519 public key in hex or base64", short(raw))
}

// EncodeKey writes a key the way `flowy identity` prints it: lower case hex,
// which is what a pin is copied from.
func EncodeKey(key []byte) string { return hex.EncodeToString(key) }

// short truncates a value for an error message.
func short(s string) string {
	const max = 32
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
