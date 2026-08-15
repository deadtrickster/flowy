package sign

import (
	"bytes"
	"crypto/ed25519"
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
