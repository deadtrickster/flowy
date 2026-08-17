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

// canonicalArtifact is the byte string an artifact's signature is over.
func canonicalArtifact(a *Artifact) []byte {
	return sign.CanonicalArtifact(sign.Artifact{
		ID: a.ID, OwnerUser: a.OwnerUser, Project: a.Project, Visibility: a.Visibility,
		Type: a.Type, Title: a.Title, Body: a.Body, HLC: a.HLC, Node: a.Node,
		Tombstone: a.Tombstone,
		Kind:      a.Kind, Discovery: a.Discovery, Status: a.Status, Severity: a.Severity,
		Tags: a.Tags, UserTags: a.UserTags, Related: a.Related, FilePath: a.FilePath,
		Fields: canonicalJSON(a.Fields), Reported: a.Reported, External: externalBytes(a.External),
		Created: a.Created,
	})
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

func canonicalEvent(e *Event) []byte {
	return sign.CanonicalEvent(sign.Event{
		ID: e.ID, Artifact: e.Artifact, Thread: e.Thread, Actor: e.Actor, Type: e.Type,
		Body: e.Body, Meta: canonicalJSON(e.Meta), Parents: e.Parents, HLC: e.SeqHLC, Node: e.Node,
		Project: e.Project, Room: e.Room, Created: e.Created, Addressee: e.Addressee,
	})
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

func (d *DB) signArtifact(ctx context.Context, a *Artifact) error {
	priv, err := d.signer(ctx)
	if err != nil {
		return err
	}
	SignArtifact(priv, a)
	return nil
}

func (d *DB) signGrant(ctx context.Context, g *Grant) error {
	priv, err := d.signer(ctx)
	if err != nil {
		return err
	}
	SignGrant(priv, g)
	return nil
}

func (d *DB) signProject(ctx context.Context, p *Project) error {
	priv, err := d.signer(ctx)
	if err != nil {
		return err
	}
	SignProject(priv, p)
	return nil
}

func (d *DB) signTask(ctx context.Context, t *Task) error {
	priv, err := d.signer(ctx)
	if err != nil {
		return err
	}
	SignTask(priv, t)
	return nil
}

func (d *DB) signEvent(ctx context.Context, e *Event) error {
	priv, err := d.signer(ctx)
	if err != nil {
		return err
	}
	SignEvent(priv, e)
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
