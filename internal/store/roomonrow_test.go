package store

import (
	"encoding/json"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// AN ANSWER SAYS WHICH ROOM THE WORK IS IN, from every read that carries the
// permission filter.
//
// MEASURED before this existed: GET /api/artifact/{id} answered room: null
// while fields.room held "general", so 251 of 251 work rows on the live node
// read as roomless from every list door. A client could only group by room by
// reaching into the fields blob and knowing the key - which is the same defect
// the project half had before it was fixed, an answer that does not say what it
// is about.
//
// The room is the WORK boundary as distinct from the project, which is the
// permission boundary, so every per-room projection reads this.
func TestAnAnswerNamesItsRoom(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "roomrow")
	p := &Principal{UserID: "u-" + ulid.NewString(), AgentID: "a-" + ulid.NewString(), Project: here}

	// todoIn raises into "build" - see the helper.
	inRoom := todoIn(t, ctx, db, p, "raised in a room", VisibilityProjectOnly, "")

	// And one with no room at all, which must answer empty rather than
	// inventing one: a row raised outside any conversation is not in "general".
	fields, err := json.Marshal(map[string]any{})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	roomless := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: "todo", Project: &here,
		OwnerUser: p.UserID, Title: "raised out of nothing",
		Visibility: VisibilityProjectOnly, Fields: fields,
	}
	if err := db.WriteMemory(ctx, roomless); err != nil {
		t.Fatalf("write roomless: %v", err)
	}

	// THE SINGLE-ROW READ.
	got, err := db.ReadArtifact(ctx, p, inRoom.ID, false)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Room != "build" {
		t.Fatalf("the row answers room %q, want the room it was raised in", got.Room)
	}
	bare, err := db.ReadArtifact(ctx, p, roomless.ID, false)
	if err != nil {
		t.Fatalf("read roomless: %v", err)
	}
	if bare.Room != "" {
		t.Fatalf("a row raised in no room answers room %q", bare.Room)
	}

	// THE LIST DOOR, which is where the projections read. Before this, this was
	// the door that answered roomless for every row on the node.
	rows, err := db.ListArtifacts(ctx, p, ArtifactQuery{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[string]string{}
	for _, row := range rows {
		seen[row.ID] = row.Room
	}
	if seen[inRoom.ID] != "build" {
		t.Fatalf("the list door answers room %q for a row in build", seen[inRoom.ID])
	}
	if seen[roomless.ID] != "" {
		t.Fatalf("the list door invented room %q for a roomless row", seen[roomless.ID])
	}

	// AND A ROOMLESS ROW SAYS SO ON THE WIRE rather than omitting the key. The
	// gate check on this commit failed against the first cut - it read null and
	// wanted "" - because Room carried omitempty like the two fields beside it.
	// With the key absent, "this row is in no room" and "this node does not
	// report rooms" are the same bytes, which is the empty-versus-absent
	// collapse six other defects came from tonight.
	wire, err := json.Marshal(bare)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var onWire map[string]any
	if err := json.Unmarshal(wire, &onWire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got2, present := onWire["room"]
	if !present {
		t.Fatal("a roomless row omits `room` entirely, so absent and empty are one answer")
	}
	if got2 != "" {
		t.Fatalf("a roomless row answers room %v", got2)
	}

	// AND THE VALUE IS THE ONE THE FILTER USES, so grouping by the answer and
	// narrowing by the query cannot disagree - which is the whole reason this is
	// derived from RoomOf rather than read a second way.
	narrowed, err := db.ListArtifacts(ctx, p, ArtifactQuery{Room: "build", Limit: 100})
	if err != nil {
		t.Fatalf("narrowed list: %v", err)
	}
	found := false
	for _, row := range narrowed {
		if row.ID == inRoom.ID {
			found = true
		}
		if row.Room != "build" {
			t.Fatalf("a room-narrowed list answered a row saying room %q", row.Room)
		}
	}
	if !found {
		t.Fatal("the row is missing from a list narrowed to its own room")
	}
}
