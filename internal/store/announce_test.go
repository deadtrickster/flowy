package store

import (
	"encoding/json"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestAnAgentWithNoKindIsAWorker is the compatibility half of the new column.
//
// Every seed, every fixture and every agents row written before agent_kind
// existed says nothing about it, and all of them have to keep working - so the
// default is worker and it is applied by the write rather than only by the DDL.
// A kind nothing implements is refused instead of being coalesced, because a
// typo that silently becomes the default is a system agent somebody thinks they
// created and did not.
func TestAnAgentWithNoKindIsAWorker(t *testing.T) {
	ctx, db := open(t)

	user := &User{Handle: "kind-" + ulid.NewString()}
	if err := db.InsertUser(ctx, user); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	declare(t, ctx, db, "pk")
	plain := &Agent{UserID: user.ID, Kind: "claude", Project: "pk"}
	if err := db.InsertAgent(ctx, plain); err != nil {
		t.Fatalf("insert an agent that says nothing about its kind: %v", err)
	}
	if plain.AgentKind != AgentKindWorker {
		t.Fatalf("an agent with no kind came back as %q, want %q", plain.AgentKind, AgentKindWorker)
	}
	read, err := db.GetAgent(ctx, plain.ID)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if read.AgentKind != AgentKindWorker {
		t.Fatalf("stored kind is %q, want %q", read.AgentKind, AgentKindWorker)
	}
	if read.Kind != "claude" {
		t.Fatalf("the runtime is %q, want claude - the two columns are different questions", read.Kind)
	}

	system := &Agent{UserID: user.ID, Kind: "claude", AgentKind: AgentKindSystem, Project: "pk"}
	if err := db.InsertAgent(ctx, system); err != nil {
		t.Fatalf("insert a system agent: %v", err)
	}

	bogus := &Agent{UserID: user.ID, Kind: "claude", AgentKind: "overlord", Project: "pk"}
	if err := db.InsertAgent(ctx, bogus); err == nil {
		t.Fatal("an agent kind nothing implements was accepted")
	}

	// And the kind reaches the principal, which is where the capability is
	// read. A person's own token names no agent, so it carries no kind at all -
	// not the default, which would read as "an agent of the least privileged
	// sort" rather than as "not an agent".
	tokens := map[string]*Principal{
		"worker": {Token: "tw-" + ulid.NewString(), AgentID: plain.ID},
		"system": {Token: "ts-" + ulid.NewString(), AgentID: system.ID},
		"person": {Token: "tp-" + ulid.NewString(), UserID: user.ID, Project: "pk"},
	}
	for _, tok := range tokens {
		if err := db.InsertToken(ctx, tok); err != nil {
			t.Fatalf("insert token: %v", err)
		}
	}
	want := map[string]string{"worker": AgentKindWorker, "system": AgentKindSystem, "person": ""}
	for name, tok := range tokens {
		p, err := db.PrincipalForToken(ctx, tok.Token)
		if err != nil {
			t.Fatalf("resolve the %s token: %v", name, err)
		}
		if p.AgentKind != want[name] {
			t.Fatalf("the %s token resolved to kind %q, want %q", name, p.AgentKind, want[name])
		}
		if MayAnnounceFederation(p.AgentKind) != (name == "system") {
			t.Fatalf("the %s token may announce to the fabric: %v", name,
				MayAnnounceFederation(p.AgentKind))
		}
	}
}

// TestANodeAnnouncementCrossesNeitherDoor is the scope rule, asked of both
// halves of replication.
//
// The two doors are the pull that offers rows to a peer and the push that takes
// them from one, and this project's history is a list of rules that were on one
// and not the other. A node-scope announcement is refused on both: it is not
// offered by SyncPull however readable it is, and a peer that pushes one -
// correctly signed, by a principal that may write it - is refused with the
// reason rather than quietly applied.
func TestANodeAnnouncementCrossesNeitherDoor(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pann")
	user := &User{Handle: "ann-" + ulid.NewString()}
	if err := db.InsertUser(ctx, user); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	token := &Principal{Token: "ta-" + ulid.NewString(), UserID: user.ID, Project: project}
	if err := db.InsertToken(ctx, token); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	p := &Principal{UserID: user.ID, Project: project}

	fields := func(scope string) json.RawMessage {
		raw, err := AnnouncementFields{Scope: scope}.Encode()
		if err != nil {
			t.Fatalf("encode fields: %v", err)
		}
		return raw
	}

	before := packed(t, db)
	local := &Artifact{
		Type: AnnouncementType, Kind: ScopeNode, Project: &project, OwnerUser: user.ID,
		Title: "this node reboots", Status: AnnouncementActive, Severity: SeverityWarning,
		Visibility: VisibilityShared, Fields: fields(ScopeNode),
	}
	fabric := &Artifact{
		Type: AnnouncementType, Kind: ScopeFederation, Project: &project, OwnerUser: user.ID,
		Title: "the fabric changes", Status: AnnouncementActive, Severity: SeverityWarning,
		Visibility: VisibilityShared, Fields: fields(ScopeFederation),
	}
	for _, art := range []*Artifact{local, fabric} {
		if err := db.UpsertArtifact(ctx, art); err != nil {
			t.Fatalf("write %s: %v", art.Title, err)
		}
	}

	// The reader can see both - a plain list hands over the pair, so what the
	// pull leaves out below is the scope rule and not the permission filter.
	list, err := db.ListArtifacts(ctx, p, ArtifactQuery{Type: AnnouncementType})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[string]bool{}
	for _, art := range list {
		seen[art.ID] = true
	}
	if !seen[local.ID] || !seen[fabric.ID] {
		t.Fatalf("the reader cannot see both announcements: %v", seen)
	}

	// Door one: the pull.
	set, err := db.SyncPull(ctx, p, SyncQuery{Since: before})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	offered := map[string]bool{}
	for _, art := range set.Artifacts {
		offered[art.ID] = true
	}
	if offered[local.ID] {
		t.Fatal("the pull offered a node-scope announcement to a peer")
	}
	if !offered[fabric.ID] {
		t.Fatal("the pull withheld a federation announcement the reader may read")
	}

	// Door two: the push. Signed by a node this one has pinned and pushed by a
	// principal that may write into the project, so nothing but the scope is
	// left to refuse it.
	peer := "peer-" + ulid.NewString()
	key := pinTestNode(t, ctx, db, peer)
	at := packed(t, db)
	incoming := &Artifact{
		ID: ulid.NewString(), Type: AnnouncementType, Kind: ScopeNode,
		Project: &project, OwnerUser: user.ID, Title: "somebody else's node reboots",
		Status: AnnouncementActive, Severity: SeverityWarning, Visibility: VisibilityShared,
		Fields: fields(ScopeNode), HLC: at + 1, Node: peer,
	}
	SignArtifact(key, incoming)
	res, err := db.SyncApplyAs(ctx, p, &SyncSet{Artifacts: []*Artifact{incoming}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Applied["artifacts"] != 0 || res.Refused["artifacts"] != 1 {
		t.Fatalf("the push applied %d and refused %d, want 0 applied and 1 refused: %v",
			res.Applied["artifacts"], res.Refused["artifacts"], res.Reasons)
	}
	if _, err := db.ReadArtifact(ctx, p, incoming.ID, false); err == nil {
		t.Fatal("the pushed node-scope announcement is in the store")
	}

	// And the same row, federation-scope and otherwise identical, is taken -
	// so what refused the one above is the scope and not the shape of the test.
	crossing := *incoming
	crossing.ID = ulid.NewString()
	crossing.Kind = ScopeFederation
	crossing.Fields = fields(ScopeFederation)
	crossing.HLC = at + 2
	SignArtifact(key, &crossing)
	res, err = db.SyncApplyAs(ctx, p, &SyncSet{Artifacts: []*Artifact{&crossing}})
	if err != nil {
		t.Fatalf("apply the federation one: %v", err)
	}
	if res.Applied["artifacts"] != 1 {
		t.Fatalf("the federation announcement was refused too: %v", res.Reasons)
	}
}

// TestAnUnreadableScopeDoesNotTravel is the decode's default.
//
// fields is a jsonb blob, and a blob can be absent, malformed, or carry a scope
// nothing implements. None of those is a licence to replicate: "no scope" reads
// as node scope everywhere it matters, which is the end of the decision that
// does not hand somebody else's readers an announcement.
func TestAnUnreadableScopeDoesNotTravel(t *testing.T) {
	for _, raw := range []string{"", "not json at all", `{}`, `{"scope":"everywhere"}`, `{"scope":null}`} {
		art := &Artifact{Type: AnnouncementType, Fields: json.RawMessage(raw)}
		if !IsLocalAnnouncement(art) {
			t.Fatalf("fields %q read as a scope that travels", raw)
		}
	}
	fabric := &Artifact{Type: AnnouncementType, Fields: json.RawMessage(`{"scope":"federation"}`)}
	if IsLocalAnnouncement(fabric) {
		t.Fatal("a federation announcement was read as local")
	}
	// And nothing that is not an announcement is caught by the rule, whatever
	// its fields happen to say.
	note := &Artifact{Type: "note", Fields: json.RawMessage(`{"scope":"node"}`)}
	if IsLocalAnnouncement(note) {
		t.Fatal("a note with a scope in its fields was held back from replication")
	}
}
