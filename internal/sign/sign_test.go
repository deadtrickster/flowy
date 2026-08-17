package sign

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

// The canonical encoders have two properties worth testing, and everything the
// merge relies on is one of them: the same row is always the same bytes, and no
// two rows that differ anywhere are the same bytes.

// written is the date the fixtures carry. A row's date is signed like every
// other replicated column: outside the signature it is a field an honest-
// looking relay may move, and a date is what every list and every reader orders
// by.
var written = time.Date(2026, 8, 15, 9, 30, 0, 0, time.UTC)

func artifact() Artifact {
	project := "pa"
	return Artifact{
		ID: "art-1", OwnerUser: "u-1", Project: &project, Visibility: "project",
		Type: "bug", Title: "the wipers judder", Body: "on the return stroke",
		HLC: 1234567, Node: "nodeA", Tombstone: false,
		Kind: "note", Discovery: "found in the rain", Status: "open", Severity: "low",
		Tags: []string{"a", "b"}, UserTags: []string{"mine"}, Related: []string{"art-0"},
		FilePath: "internal/store/store.go", Fields: []byte(`{"x":1}`),
		Reported: false, External: []byte(`{"repo":"o/r"}`),
		Created: written,
	}
}

func TestCanonicalArtifactIsDeterministic(t *testing.T) {
	a := artifact()
	first, second := CanonicalArtifact(a), CanonicalArtifact(a)
	if !bytes.Equal(first, second) {
		t.Fatal("the same artifact encoded to two different byte strings")
	}
	if len(first) == 0 {
		t.Fatal("the encoding is empty")
	}
}

// Every field, one at a time. A field that can be changed without changing the
// bytes is a field a peer can rewrite on somebody else's row.
func TestEveryArtifactFieldIsInTheSignature(t *testing.T) {
	base := CanonicalArtifact(artifact())
	other := "pb"
	changes := map[string]func(*Artifact){
		"id":         func(a *Artifact) { a.ID = "art-2" },
		"owner":      func(a *Artifact) { a.OwnerUser = "u-2" },
		"project":    func(a *Artifact) { a.Project = &other },
		"no project": func(a *Artifact) { a.Project = nil },
		"visibility": func(a *Artifact) { a.Visibility = "personal" },
		"type":       func(a *Artifact) { a.Type = "note" },
		"title":      func(a *Artifact) { a.Title = "something else" },
		"body":       func(a *Artifact) { a.Body = "on the way down" },
		"hlc":        func(a *Artifact) { a.HLC++ },
		"node":       func(a *Artifact) { a.Node = "nodeB" },
		"tombstone":  func(a *Artifact) { a.Tombstone = true },
		"kind":       func(a *Artifact) { a.Kind = "todo" },
		"discovery":  func(a *Artifact) { a.Discovery = "found in the dry" },
		"status":     func(a *Artifact) { a.Status = "done" },
		"severity":   func(a *Artifact) { a.Severity = "high" },
		"tags":       func(a *Artifact) { a.Tags = []string{"a", "c"} },
		"tag order":  func(a *Artifact) { a.Tags = []string{"b", "a"} },
		"user tags":  func(a *Artifact) { a.UserTags = []string{"yours"} },
		"related":    func(a *Artifact) { a.Related = []string{"art-9"} },
		"file path":  func(a *Artifact) { a.FilePath = "main.go" },
		"fields":     func(a *Artifact) { a.Fields = []byte(`{"x":2}`) },
		"no fields":  func(a *Artifact) { a.Fields = nil },
		"reported":   func(a *Artifact) { a.Reported = true },
		"external":   func(a *Artifact) { a.External = []byte(`{"repo":"o/other"}`) },
		"created":    func(a *Artifact) { a.Created = written.AddDate(0, -2, 0) },
		"no created": func(a *Artifact) { a.Created = time.Time{} },
		"created us": func(a *Artifact) { a.Created = written.Add(time.Microsecond) },
	}
	for what, change := range changes {
		a := artifact()
		change(&a)
		if bytes.Equal(base, CanonicalArtifact(a)) {
			t.Errorf("changing the %s did not change the bytes that are signed", what)
		}
	}
}

// The framing: a field boundary that can be moved is a row that can be rewritten
// into another row with the same signature.
func TestFieldsCannotBeRecut(t *testing.T) {
	one := artifact()
	one.Title, one.Body = "ab", "c"
	two := artifact()
	two.Title, two.Body = "a", "bc"
	if bytes.Equal(CanonicalArtifact(one), CanonicalArtifact(two)) {
		t.Fatal("two rows whose fields run together encode the same")
	}

	// And the same for the two nullable shapes: NULL is not the empty string,
	// because the read filter does not read them the same way.
	empty := ""
	null, blank := artifact(), artifact()
	null.Project, blank.Project = nil, &empty
	if bytes.Equal(CanonicalArtifact(null), CanonicalArtifact(blank)) {
		t.Fatal("a NULL project and an empty one encode the same")
	}
}

func TestCanonicalGrantCoversEveryField(t *testing.T) {
	g := Grant{
		ID: "g-1", FromProject: "pa", ToProject: "pb", Subject: "u-2", Artifact: "art-1",
		Cap: "read", GrantedBy: "u-1", HLC: 99, Node: "nodeA",
	}
	base := CanonicalGrant(g)
	changes := map[string]func(*Grant){
		"id":         func(g *Grant) { g.ID = "g-2" },
		"from":       func(g *Grant) { g.FromProject = "pz" },
		"to":         func(g *Grant) { g.ToProject = "pz" },
		"subject":    func(g *Grant) { g.Subject = "u-3" },
		"artifact":   func(g *Grant) { g.Artifact = "art-2" },
		"cap":        func(g *Grant) { g.Cap = "write" },
		"granted by": func(g *Grant) { g.GrantedBy = "u-3" },
		"hlc":        func(g *Grant) { g.HLC++ },
		"node":       func(g *Grant) { g.Node = "nodeB" },
		"tombstone":  func(g *Grant) { g.Tombstone = true },
	}
	for what, change := range changes {
		one := g
		change(&one)
		if bytes.Equal(base, CanonicalGrant(one)) {
			t.Errorf("changing the %s of a grant did not change the bytes", what)
		}
	}
}

func TestCanonicalTaskCoversEveryField(t *testing.T) {
	tk := Task{
		ID: "t-1", Artifact: "art-1", FromUser: "u-1", ToUser: "u-2",
		AssigneeAgent: "ag-1", State: "open", HLC: 7, Node: "nodeA",
		Project: "pa", Thread: "th-1",
	}
	base := CanonicalTask(tk)
	changes := map[string]func(*Task){
		"id":       func(t *Task) { t.ID = "t-2" },
		"artifact": func(t *Task) { t.Artifact = "art-2" },
		"from":     func(t *Task) { t.FromUser = "u-3" },
		"to":       func(t *Task) { t.ToUser = "u-3" },
		"agent":    func(t *Task) { t.AssigneeAgent = "ag-2" },
		"state":    func(t *Task) { t.State = "done" },
		"hlc":      func(t *Task) { t.HLC++ },
		"node":     func(t *Task) { t.Node = "nodeB" },
		"project":  func(t *Task) { t.Project = "pb" },
		"thread":   func(t *Task) { t.Thread = "th-2" },
	}
	for what, change := range changes {
		one := tk
		change(&one)
		if bytes.Equal(base, CanonicalTask(one)) {
			t.Errorf("changing the %s of a task did not change the bytes", what)
		}
	}
}

func TestCanonicalEventCoversEveryFieldAndSortsParents(t *testing.T) {
	project := "pa"
	e := Event{
		ID: "e-1", Artifact: "art-1", Thread: "th-1", Actor: "u-1", Type: "chat",
		Body: "said it", Meta: []byte(`{"topic":"x"}`), Parents: []string{"e-0", "e-00"},
		HLC: 11, Node: "nodeA", Project: &project, Room: "pa/bugs", Created: written,
	}
	base := CanonicalEvent(e)

	// The DAG is a set: the same two parents in the other order is the same
	// event, and has to be the same bytes.
	swapped := e
	swapped.Parents = []string{"e-00", "e-0"}
	if !bytes.Equal(base, CanonicalEvent(swapped)) {
		t.Error("the same parents in another order encoded differently")
	}
	// And sorting them does not disturb the caller's slice.
	if e.Parents[0] != "e-0" {
		t.Error("encoding an event reordered the row it was given")
	}

	other := "pb"
	changes := map[string]func(*Event){
		"id":          func(e *Event) { e.ID = "e-2" },
		"artifact":    func(e *Event) { e.Artifact = "art-2" },
		"thread":      func(e *Event) { e.Thread = "th-2" },
		"actor":       func(e *Event) { e.Actor = "u-2" },
		"type":        func(e *Event) { e.Type = "status" },
		"body":        func(e *Event) { e.Body = "said something else" },
		"meta":        func(e *Event) { e.Meta = []byte(`{"topic":"y"}`) },
		"no meta":     func(e *Event) { e.Meta = nil },
		"parents":     func(e *Event) { e.Parents = []string{"e-0"} },
		"hlc":         func(e *Event) { e.HLC++ },
		"node":        func(e *Event) { e.Node = "nodeB" },
		"project":     func(e *Event) { e.Project = &other },
		"no project":  func(e *Event) { e.Project = nil },
		"room":        func(e *Event) { e.Room = "pa/quiet" },
		"empty parts": func(e *Event) { e.Parents = nil },
		"created":     func(e *Event) { e.Created = written.AddDate(0, -3, 0) },
		"no created":  func(e *Event) { e.Created = time.Time{} },
		"addressee":   func(e *Event) { e.Addressee = "u-9" },
	}
	for what, change := range changes {
		one := e
		change(&one)
		if bytes.Equal(base, CanonicalEvent(one)) {
			t.Errorf("changing the %s of an event did not change the bytes", what)
		}
	}
}

// One row type's signature is not another's, even where the fields would line
// up: the domain is the first thing in every message.
func TestDomainsDoNotCollide(t *testing.T) {
	a := CanonicalArtifact(Artifact{ID: "x", Node: "n"})
	g := CanonicalGrant(Grant{ID: "x", Node: "n"})
	tk := CanonicalTask(Task{ID: "x", Node: "n"})
	e := CanonicalEvent(Event{ID: "x", Node: "n"})
	for i, one := range [][]byte{a, g, tk, e} {
		for j, two := range [][]byte{a, g, tk, e} {
			if i != j && bytes.Equal(one, two) {
				t.Fatalf("two row types encode the same bytes (%d, %d)", i, j)
			}
		}
	}
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	msg := CanonicalArtifact(artifact())
	sig := Sign(private, msg)
	if !Verify(public, msg, sig) {
		t.Fatal("a signature this key made did not verify under it")
	}

	// One byte of the message, one byte of the signature, and a key that is not
	// the signer's: none of them verify.
	tampered := append([]byte(nil), msg...)
	tampered[len(tampered)-1] ^= 0x01
	if Verify(public, tampered, sig) {
		t.Error("a tampered message verified")
	}
	bad := append([]byte(nil), sig...)
	bad[0] ^= 0x01
	if Verify(public, msg, bad) {
		t.Error("a tampered signature verified")
	}
	stranger, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if Verify(stranger, msg, sig) {
		t.Error("a signature verified under somebody else's key")
	}

	// And the malformed shapes are false rather than a panic: these values come
	// out of database columns.
	if Verify(nil, msg, sig) || Verify(public, msg, nil) || Verify(public, msg, sig[:8]) {
		t.Error("a malformed key or signature verified")
	}
	if Sign(ed25519.PrivateKey("short"), msg) != nil {
		t.Error("a key of the wrong size produced a signature")
	}
}

func TestCanonicalIdentityBindsTheKeyToTheNode(t *testing.T) {
	key := bytes.Repeat([]byte{7}, ed25519.PublicKeySize)
	base := CanonicalIdentity("nodeA", key)
	if bytes.Equal(base, CanonicalIdentity("nodeB", key)) {
		t.Error("the same key under two node names encodes the same")
	}
	other := bytes.Repeat([]byte{8}, ed25519.PublicKeySize)
	if bytes.Equal(base, CanonicalIdentity("nodeA", other)) {
		t.Error("two keys under the same node name encode the same")
	}
}

// The addressee is the one field in these encoders that is written
// conditionally, so it gets a test of its own: the compatibility it buys, and
// the guarantee it must not have given up to buy it.
//
// The compatibility is the reason for the condition. Adding a field to an
// encoding every node in a fleet is already running turns every row those nodes
// signed into a forgery by this node's own definition - a merge that refuses
// everything an older peer sends is a federation break dressed as a feature.
// So an event addressed at nobody encodes to exactly the bytes it did before
// this field existed, and the golden below is what says so a year from now,
// when nobody remembers which fields were added when.
func TestAnUnaddressedEventEncodesAsItAlwaysDid(t *testing.T) {
	project := "pa"
	e := Event{
		ID: "e-1", Artifact: "art-1", Thread: "th-1", Actor: "u-1", Type: "chat",
		Body: "said it", Meta: []byte(`{"topic":"x"}`), Parents: []string{"e-0", "e-00"},
		HLC: 11, Node: "nodeA", Project: &project, Room: "pa/bugs", Created: written,
	}
	// The sha256 of the encoding of that event, taken from the build before the
	// addressee existed rather than from the build that added it - a golden
	// copied out of the code it is checking asserts nothing. If this changes,
	// every event ever signed by another node stops verifying here, and the
	// only correct way to make that happen is on purpose.
	const golden = "a7d2e31a5d7091bf66a50debba9dd18f47116142b7c3b57706a954be57e35e67"
	if got := digestHex(CanonicalEvent(e)); got != golden {
		t.Errorf("the encoding of an unaddressed event moved:\n got  %s\n want %s\n"+
			"every event signed by an older node now fails to verify", got, golden)
	}
	// And an empty addressee is not a third thing. The column is nullable and
	// empty means directed at the room, so the two are one row and one message.
	blank := e
	blank.Addressee = ""
	if !bytes.Equal(CanonicalEvent(e), CanonicalEvent(blank)) {
		t.Error("an absent addressee and an empty one encoded differently")
	}
}

// What the condition must not have cost: an addressee a relay can put on,
// take off, or swap. Each of those is a different message and therefore a
// signature that does not verify.
func TestAnAddresseeCannotBeAddedRemovedOrSwapped(t *testing.T) {
	to := Event{
		ID: "e-1", Actor: "u-1", Type: "chat", Body: "for you", HLC: 11,
		Node: "nodeA", Room: "general", Created: written, Addressee: "u-2",
	}
	room := to
	room.Addressee = ""
	elsewhere := to
	elsewhere.Addressee = "u-3"

	addressed := CanonicalEvent(to)
	if bytes.Equal(addressed, CanonicalEvent(room)) {
		t.Error("taking the addressee off an addressed event did not change the bytes")
	}
	if bytes.Equal(addressed, CanonicalEvent(elsewhere)) {
		t.Error("redirecting an addressed event did not change the bytes")
	}
	if bytes.Equal(CanonicalEvent(room), CanonicalEvent(elsewhere)) {
		t.Error("addressing an unaddressed event did not change the bytes")
	}
}

// digestHex is the sha256 of a message, hex, for pinning an encoding without
// pasting a few hundred bytes into a test.
func digestHex(msg []byte) string {
	sum := sha256.Sum256(msg)
	return hex.EncodeToString(sum[:])
}

// An authorship signature is a different claim from a node signature, and the
// encodings have to make that structural rather than a convention: a node's
// signature over a row must never be usable as the author's, in either
// direction. The domain is the first field of both, so they cannot collide.
func TestAnAuthorshipMessageIsNotTheRowsOwnMessage(t *testing.T) {
	e := Event{
		ID: "e-1", Actor: "u-1", Type: "chat", Body: "said it", HLC: 11,
		Node: "nodeA", Room: "general", Created: written,
	}
	if bytes.Equal(CanonicalEvent(e), CanonicalEventAuthorship("u-1", e)) {
		t.Fatal("an event and its authorship claim encode to the same bytes")
	}
	// And the claim names the principal: the same row claimed by two different
	// people is two different messages.
	if bytes.Equal(CanonicalEventAuthorship("u-1", e), CanonicalEventAuthorship("u-2", e)) {
		t.Fatal("who is claiming authorship is not inside the message")
	}
	// Everything about the event is in it, because an event is immutable.
	moved := e
	moved.Body = "said something else"
	if bytes.Equal(CanonicalEventAuthorship("u-1", e), CanonicalEventAuthorship("u-1", moved)) {
		t.Fatal("the words are not inside the authorship claim")
	}
}

// An artifact's authorship claim covers what only its owner writes, and it has
// to leave out what other people legitimately write - or the first ordinary
// status move by a party would strip the row of its authorship and its owner's
// peers would then refuse it. That is a federation break dressed as a security
// fix, so the two halves are asserted here rather than left to a comment.
func TestAnArtifactsAuthorshipCoversTheOwnersFieldsAndNoOthers(t *testing.T) {
	project := "flowy"
	a := Artifact{
		ID: "a-1", OwnerUser: "u-1", Project: &project, Visibility: "project",
		Type: "bug", Kind: "", Title: "it breaks", Body: "here is how", Status: "open",
		HLC: 11, Node: "nodeA", Created: written,
	}
	base := CanonicalArtifactAuthorship("u-1", a)

	// What a party may write, and what the reading and the relay do to a row in
	// flight: none of it may change the claim.
	for _, other := range []struct {
		what   string
		change func(*Artifact)
	}{
		{"a status move", func(x *Artifact) { x.Status = "in-review" }},
		{"a todo's fields", func(x *Artifact) { x.Fields = []byte(`{"assignee":"u-2"}`) }},
		{"a forge link", func(x *Artifact) { x.Reported = true; x.External = []byte(`{"repo":"o/r"}`) }},
		{"a fresh reading", func(x *Artifact) { x.HLC = 99 }},
		{"another node writing it", func(x *Artifact) { x.Node = "nodeB" }},
		{"a delete", func(x *Artifact) { x.Tombstone = true }},
	} {
		moved := a
		other.change(&moved)
		if !bytes.Equal(base, CanonicalArtifactAuthorship("u-1", moved)) {
			t.Errorf("%s changed the owner's authorship claim", other.what)
		}
	}

	// And what only the owner writes: every one of these is the owner's word
	// about their own artifact, so changing it has to change the claim.
	for _, mine := range []struct {
		what   string
		change func(*Artifact)
	}{
		{"the title", func(x *Artifact) { x.Title = "it does not break" }},
		{"the body", func(x *Artifact) { x.Body = "here is how, allegedly" }},
		{"the owner", func(x *Artifact) { x.OwnerUser = "u-2" }},
		{"the type", func(x *Artifact) { x.Type = "note" }},
		{"the project", func(x *Artifact) { other := "elsewhere"; x.Project = &other }},
		{"the visibility", func(x *Artifact) { x.Visibility = "personal" }},
		{"the tags", func(x *Artifact) { x.Tags = []string{"urgent"} }},
	} {
		moved := a
		mine.change(&moved)
		if bytes.Equal(base, CanonicalArtifactAuthorship("u-1", moved)) {
			t.Errorf("%s did not change the owner's authorship claim", mine.what)
		}
	}
}
