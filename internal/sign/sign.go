// Package sign is the authorship half of federation: an ed25519 keypair per
// node, a canonical byte encoding per replicated row, and the two operations
// over them.
//
// The merge has always asked whether the principal handing a row over was
// authorised to write it. It could not ask the other question - whether the
// node named on the row is the node that wrote it - because nothing on an
// unsigned row answers it. A peer serving a page is free to rewrite every
// column of every row in it, put somebody else's node name back on, and raise
// the reading; last-writer-wins then makes the rewrite permanent on every node
// downstream. That is what these bytes are for.
//
// Two rules govern everything here:
//
//   - the encoding is canonical. One row has exactly one byte string, and no
//     two different rows have the same one. Every field is length-prefixed with
//     an 8-byte big-endian count, so no run of fields can be re-cut into a
//     different run: "ab"+"c" and "a"+"bc" are different messages here, and
//     that is the whole of why the framing is not simply concatenation.
//   - the message names its own type. Each encoder opens with a domain string,
//     so a signature over an artifact cannot be replayed as the signature of a
//     task that happens to encode to the same fields.
//
// Large fields - a transcript body, a JSON blob - are folded in as their
// sha256 rather than copied, so signing a megabyte artifact costs a hash of it
// and not a second copy of it in memory. That is a substitution of one fixed
// 32-byte field for the value, not a shortcut around it: changing the body
// changes the hash, and the hash is what is signed.
//
// The private key never leaves the node that made it. It is not in any
// replicated payload, it is not in the pull answer, and the only column that
// holds one is the local node's own row in node_identity.
package sign

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"time"
)

// The domain of each message: what this signature is a signature of. It is the
// first field of every encoding, so one row type's signature is never another
// row type's signature.
const (
	domainArtifact = "flowy.artifact.v1"
	domainGrant    = "flowy.grant.v1"
	domainTask     = "flowy.task.v1"
	domainEvent    = "flowy.event.v1"
	domainIdentity = "flowy.identity.v1"
	domainProject  = "flowy.project.v1"
)

// Artifact is the authenticated view of an artifacts row.
//
// The first block is the field set the design names: what the row is, whose it
// is, where it lands, and when. The second block is the rest of the replicated
// content, and it is here because applying a row replaces every column of the
// one it lands on: a field outside the signature is a field a peer may rewrite
// on a row it did not write, on any node that does not hold the row yet and so
// has nothing to compare the reading against.
type Artifact struct {
	ID         string
	OwnerUser  string
	Project    *string
	Visibility string
	Type       string
	Title      string
	Body       string
	HLC        int64
	Node       string
	Tombstone  bool

	Kind      string
	Discovery string
	Status    string
	Severity  string
	Tags      []string
	UserTags  []string
	Related   []string
	FilePath  string
	Fields    []byte
	Reported  bool
	External  []byte

	// Created is when the row says it was written. It is in the signature for
	// the same reason the rest of the second block is: a column outside it is a
	// column an honest-looking relay may rewrite. A date is not decoration -
	// every list, every digest and every reader orders and ages by it - and a
	// signed row carrying an attacker's timestamp is worse than an unsigned
	// one, because everything around it says authentic.
	Created time.Time
}

// CanonicalArtifact is the byte string an artifact's signature is over.
func CanonicalArtifact(a Artifact) []byte {
	m := newMessage(domainArtifact)
	m.text(a.ID)
	m.text(a.OwnerUser)
	m.optional(a.Project)
	m.text(a.Visibility)
	m.text(a.Type)
	m.text(a.Title)
	m.digest([]byte(a.Body))
	m.number(a.HLC)
	m.text(a.Node)
	m.flag(a.Tombstone)

	m.text(a.Kind)
	m.text(a.Discovery)
	m.text(a.Status)
	m.text(a.Severity)
	m.list(a.Tags)
	m.list(a.UserTags)
	m.list(a.Related)
	m.text(a.FilePath)
	m.digest(a.Fields)
	m.flag(a.Reported)
	m.digest(a.External)
	m.moment(a.Created)
	return m.bytes()
}

// Grant is the authenticated view of a grants row.
//
// Tombstone is in the signature and is not optional: a revocation is a grant
// row with the flag set and a later reading, so a signature that did not cover
// it would let a peer turn somebody's revocation back into the grant it
// revoked, or the other way about.
type Grant struct {
	ID          string
	FromProject string
	ToProject   string
	Subject     string
	Artifact    string
	Cap         string
	GrantedBy   string
	HLC         int64
	Node        string
	Tombstone   bool
}

// CanonicalGrant is the byte string a grant's signature is over.
func CanonicalGrant(g Grant) []byte {
	m := newMessage(domainGrant)
	m.text(g.ID)
	m.text(g.FromProject)
	m.text(g.ToProject)
	m.text(g.Subject)
	m.text(g.Artifact)
	m.text(g.Cap)
	m.text(g.GrantedBy)
	m.number(g.HLC)
	m.text(g.Node)
	m.flag(g.Tombstone)
	return m.bytes()
}

// Project is the authenticated view of a projects row: the registry entry that
// every other row's project column points at.
//
// ID is the referent itself: the string that already sits in artifacts.project
// and inside every one of those signatures. Name is the label a person reads.
//
// Origin and Superseded are in the signature and are the reason this row can
// settle a collision. Origin is where the project came from - a canonicalised
// git remote, or a locally derived identity when there is no repo - and
// Superseded is the chain of origins it replaced. A merge decides whether two
// nodes' `flowy` is one project by comparing those, so an origin a peer could
// rewrite in flight would be a merge a peer could decide.
//
// Fixture is in the signature for a smaller version of the same reason: it is
// the one thing this row says that a person acts on, so a flag outside it is a
// warning a relay can switch off on somebody else's project.
type Project struct {
	ID         string
	Name       string
	CreatedBy  string
	Provenance string
	Fixture    bool
	Origin     string
	Superseded []string
	OriginAt   time.Time
	HLC        int64
	Node       string
	Created    time.Time
}

// CanonicalProject is the byte string a project's signature is over.
//
// Superseded is encoded in order rather than sorted: it is a chain and not a
// set, and which origin came before which is the whole of what it records.
func CanonicalProject(p Project) []byte {
	m := newMessage(domainProject)
	m.text(p.ID)
	m.text(p.Name)
	m.text(p.CreatedBy)
	m.text(p.Provenance)
	m.flag(p.Fixture)
	m.text(p.Origin)
	m.list(p.Superseded)
	m.moment(p.OriginAt)
	m.number(p.HLC)
	m.text(p.Node)
	m.moment(p.Created)
	return m.bytes()
}

// Task is the authenticated view of a tasks row.
//
// Thread and Project are in the signature beside the named set for the same
// reason Tombstone is in a grant's: the thread a task names is a read
// capability - the tasks clause in the event filter shows that conversation to
// the parties - so a thread outside the signature is a conversation a peer can
// hand itself by rewriting somebody else's handoff.
type Task struct {
	ID            string
	Artifact      string
	FromUser      string
	ToUser        string
	AssigneeAgent string
	State         string
	HLC           int64
	Node          string

	Project string
	Thread  string
}

// CanonicalTask is the byte string a task's signature is over.
func CanonicalTask(t Task) []byte {
	m := newMessage(domainTask)
	m.text(t.ID)
	m.text(t.Artifact)
	m.text(t.FromUser)
	m.text(t.ToUser)
	m.text(t.AssigneeAgent)
	m.text(t.State)
	m.number(t.HLC)
	m.text(t.Node)

	m.text(t.Project)
	m.text(t.Thread)
	return m.bytes()
}

// Event is the authenticated view of an events row.
//
// Parents is sorted before it is encoded: the DAG is a set of edges and two
// nodes may list them in either order, so sorting is what makes one event have
// one signature. Project and Room are in the signature because where an event
// lands is what the read filter is applied to.
type Event struct {
	ID       string
	Artifact string
	Thread   string
	Actor    string
	Type     string
	Body     string
	Meta     []byte
	Parents  []string
	HLC      int64
	Node     string

	Project *string
	Room    string

	// Created is when the entry says it was made - see Artifact.Created. A log
	// is read in date order, so a date a relay can move is a log a relay can
	// re-order.
	Created time.Time
}

// CanonicalEvent is the byte string an event's signature is over.
func CanonicalEvent(e Event) []byte {
	m := newMessage(domainEvent)
	m.text(e.ID)
	m.text(e.Artifact)
	m.text(e.Thread)
	m.text(e.Actor)
	m.text(e.Type)
	m.digest([]byte(e.Body))
	m.digest(e.Meta)
	m.sortedList(e.Parents)
	m.number(e.HLC)
	m.text(e.Node)

	m.optional(e.Project)
	m.text(e.Room)
	m.moment(e.Created)
	return m.bytes()
}

// CanonicalIdentity is the byte string a node identity's self-signature is
// over: the node's own name and its own public key, and nothing else. It is
// what makes an identity that travels between nodes worth anything - only the
// holder of the private key can produce it, so a relay cannot swap the key in
// an identity it is passing on.
func CanonicalIdentity(nodeID string, public []byte) []byte {
	m := newMessage(domainIdentity)
	m.text(nodeID)
	m.field(public)
	return m.bytes()
}

// Sign is the signature of msg by priv. A key of the wrong size signs nothing
// rather than panicking, because the caller is often holding a key that came
// out of a database column.
func Sign(priv ed25519.PrivateKey, msg []byte) []byte {
	if len(priv) != ed25519.PrivateKeySize {
		return nil
	}
	return ed25519.Sign(priv, msg)
}

// Verify reports whether sig is a signature of msg by the holder of public.
// Anything malformed - a key of the wrong length, a signature of the wrong
// length, either of them absent - is false rather than an error: the caller's
// question is only ever "may this row be applied".
func Verify(public []byte, msg, sig []byte) bool {
	if len(public) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(public), msg, sig)
}

// ------------------------------------------------------------- the framing

// message accumulates length-prefixed fields.
type message struct{ buf bytes.Buffer }

func newMessage(domain string) *message {
	m := &message{}
	m.text(domain)
	return m
}

// field writes one field: an 8-byte big-endian length, then the bytes. The
// length is what makes the encoding unambiguous - without it, two fields'
// contents run together and a different pair of fields can produce the same
// bytes.
func (m *message) field(b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	m.buf.Write(n[:])
	m.buf.Write(b)
}

func (m *message) text(s string) { m.field([]byte(s)) }

// number is a fixed 8-byte field, so a reading is one field however small it is.
func (m *message) number(v int64) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(v))
	m.field(n[:])
}

func (m *message) flag(v bool) {
	if v {
		m.field([]byte{1})
		return
	}
	m.field([]byte{0})
}

// optional is a nullable column: a marker byte says which of the two it is, so
// a NULL project and an empty-string project are different messages. They are
// different rows - the read filter reads NULL as personal - so they had better
// be.
func (m *message) optional(s *string) {
	if s == nil {
		m.field(nil)
		m.flag(false)
		return
	}
	m.text(*s)
	m.flag(true)
}

// digest folds a large or structured value in as its sha256. An absent value is
// distinguishable from a present empty one, for the same reason optional's
// marker is there.
func (m *message) digest(b []byte) {
	if b == nil {
		m.field(nil)
		m.flag(false)
		return
	}
	sum := sha256.Sum256(b)
	m.field(sum[:])
	m.flag(true)
}

// moment is a timestamp column, as microseconds since the epoch, with a marker
// byte for the zero time - a row that carries no date and a row dated at the
// epoch are different rows.
//
// Microseconds, and not nanoseconds, because that is the resolution the column
// has: a timestamptz keeps microseconds, so a signature over anything finer
// would be a signature over digits the database throws away, and the row would
// stop verifying the moment it was read back out. The value is an instant, so
// the zone the reader happens to be in does not enter into it.
func (m *message) moment(t time.Time) {
	if t.IsZero() {
		m.field(nil)
		m.flag(false)
		return
	}
	m.number(t.UnixMicro())
	m.flag(true)
}

// list is an array column, in the order the row carries it: a count, then the
// elements, each length-prefixed.
func (m *message) list(items []string) {
	m.number(int64(len(items)))
	for _, item := range items {
		m.text(item)
	}
}

// sortedList is a list whose order is not part of what it means. It is copied
// before it is sorted: a canonical encoder that reorders its caller's slice
// would be a signature that changes the row it signed.
func (m *message) sortedList(items []string) {
	cp := append([]string(nil), items...)
	sort.Strings(cp)
	m.list(cp)
}

func (m *message) bytes() []byte { return m.buf.Bytes() }
