package flowy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// AN ATTACHMENT WITH NO BYTES IS AN ANSWER, NOT A FAULT.
//
// store.ReadAttachment returns ErrNoBytes as a NON-NIL error - attachments.go
// returns (art, nil, ErrNoBytes) for a row whose payload is not on this node,
// which is what a federated attachment looks like before its bytes arrive.
//
// handleAttachmentRead used to take that with `if err != nil { serverError }`
// and reply 500. The clause below it, written to answer politely with
// content:null and bytes:"not on this node", could never run: err was always
// nil by the time it was reached, so only `content == nil` did the work. That
// is why the handler read as correct - a different test caught the same case
// for a different reason, and the comment described behaviour that did not
// happen.
//
// WHY A 500 IS THE WRONG ANSWER RATHER THAN AN UGLY ONE. The sentinel exists to
// keep three replies apart, and attachrefs.ts names them from the other side:
// the caller may not read it, the bytes are not here, or it is not a picture.
// A 500 collapses that into "something went wrong" - the one answer a reader
// can do nothing with - and writes a red line in the log for an ordinary state.
//
// THE ROW IS MADE WITHOUT BYTES ON PURPOSE. CreateArtifact writes the artifact
// and nothing writes attachment_bytes, which is exactly the shape the store's
// own ErrNoBytes test uses. Faking the answer at the HTTP layer would test the
// test.
func TestAnAttachmentWithNoBytesIsNotAServerError(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set; run ./run-tests.sh for the live checks")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	db, err := store.Open(ctx, dsn, "test-node")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	project := "nobytes-" + ulid.NewString()
	if err := db.DeclareProject(ctx, &store.Project{ID: project}); err != nil {
		t.Fatalf("declare project: %v", err)
	}
	p := &store.Principal{UserID: "u-" + ulid.NewString(), Project: project}

	art := &store.Artifact{
		ID:      ulid.NewString(),
		Type:    "attachment",
		Kind:    "binary",
		Project: &project,
		Title:   "a row whose bytes are somewhere else",
	}
	if err := db.CreateArtifact(ctx, art); err != nil {
		t.Fatalf("create the bytesless attachment: %v", err)
	}

	s := &server{db: db, node: "test-node"}
	r := httptest.NewRequest(http.MethodGet, "/api/attachment/"+art.ID, nil)
	r.SetPathValue("id", art.ID)
	r = r.WithContext(context.WithValue(ctx, principalKey{}, p))
	w := httptest.NewRecorder()
	s.handleAttachmentRead(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("a readable attachment whose bytes are elsewhere answered %d, want 200.\n"+
			"ErrNoBytes is an answer and not a fault: the row is here, the payload is not, and\n"+
			"store.ErrNoBytes is deliberately not ErrNotFound so a reader can tell those apart.\n"+
			"A 500 collapses it into \"something went wrong\" and logs an error for an ordinary\n"+
			"state. Body: %s", w.Code, w.Body.String())
	}

	out := map[string]any{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("the answer is not json: %s", w.Body.String())
	}
	if got, ok := out["content"]; !ok || got != nil {
		t.Fatalf("content is %v, want an explicit null - a client tells \"no payload\" from\n"+
			"\"here are the bytes\" by that field, and absence has to be stated", out["content"])
	}
	if out["bytes"] != "not on this node" {
		t.Fatalf("bytes is %q, want \"not on this node\" - the sentence is the whole reason\n"+
			"ErrNoBytes exists rather than being folded into ErrNotFound", out["bytes"])
	}
	if out["item"] == nil {
		t.Fatal("the row itself was not handed back, so a reader cannot even name what is missing")
	}
}
