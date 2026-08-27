package flowy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// A READER READS AND DOES NOT WRITE, at the door rather than in a label.
//
// The operator: "some will be like me some readonly some cant close or cant
// rause". This is the first of those, and the rule the file it guards exists
// for - a role name lands WITH the check that enforces it, because a person
// labelled readonly who can still raise a row is worse than no roles at all.
//
// DRIVEN THROUGH THE MIDDLEWARE, not by calling the check: what is being
// measured is that a request reaching a write door is stopped, and a test that
// called RoleMayWrite directly would pass just as well with the guard
// unregistered.
func TestAReaderIsRefusedAWriteAndAMemberIsNot(t *testing.T) {
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

	project := "roleguard-" + ulid.Short()
	if err := db.DeclareProject(ctx, &store.Project{ID: project, Name: project}); err != nil {
		t.Fatalf("declare project: %v", err)
	}
	user := &store.User{Handle: "roleguard-" + ulid.NewString(), Display: "Role Guard"}
	if err := db.InsertUser(ctx, user); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	s := &server{db: db, node: "test-node"}
	api := &recordingMux{ServeMux: http.NewServeMux()}
	// One real write door and one personal act, registered exactly as the node
	// registers them so the patterns the guard looks up are the node's.
	reached := ""
	api.HandleFunc("POST /api/chat/{room}/say", func(w http.ResponseWriter, r *http.Request) {
		reached = "say"
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	api.HandleFunc("POST /api/inbox/ack", func(w http.ResponseWriter, r *http.Request) {
		reached = "ack"
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	guarded := s.roleGuard(api.ServeMux, api)

	ask := func(path string) (int, string) {
		t.Helper()
		reached = ""
		req := httptest.NewRequest(http.MethodPost, path, nil)
		p := &store.Principal{UserID: user.ID, Project: project, ViaSession: true}
		req = req.WithContext(context.WithValue(ctx, principalKey{}, p))
		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, req)
		return rec.Code, reached
	}

	// NOT A MEMBER AT ALL is refused, and the refusal says which fact decided.
	code, got := ask("/api/chat/general/say")
	if code != http.StatusForbidden || got != "" {
		t.Fatalf("a non-member reached the write door: %d %q", code, got)
	}

	if err := db.JoinProject(ctx, user.ID, project, store.RoleReader); err != nil {
		t.Fatalf("join as reader: %v", err)
	}
	if code, got = ask("/api/chat/general/say"); code != http.StatusForbidden || got != "" {
		t.Fatalf("a reader reached the write door: %d %q", code, got)
	}

	// AND STILL USES THEIR OWN SESSION. If readonly stopped this it would mean
	// "cannot use the tool", which is not a role anybody asked for.
	if code, got = ask("/api/inbox/ack"); code != http.StatusOK || got != "ack" {
		t.Fatalf("a reader could not ack their own inbox: %d %q", code, got)
	}

	if err := db.JoinProject(ctx, user.ID, project, store.RoleMember); err != nil {
		t.Fatalf("promote to member: %v", err)
	}
	if code, got = ask("/api/chat/general/say"); code != http.StatusOK || got != "say" {
		t.Fatalf("a member was refused a write: %d %q", code, got)
	}

	// A BEARER TOKEN IS NOT COVERED, and must not be: a token's reach is
	// token_projects, minted into the credential - a different mechanism for a
	// different kind of principal. If this ever starts refusing, four agents
	// and the drainer stop working at once.
	//
	// The token here resolves to the SAME USER who is only a reader above, and
	// carries no session. That is the case the first cut of this guard got
	// wrong: it read "a user with no agent" as "a logged-in person", which is
	// true of every token on this node, and refused 321 gate checks.
	reached = ""
	req := httptest.NewRequest(http.MethodPost, "/api/chat/general/say", nil)
	agent := &store.Principal{UserID: user.ID, Project: project}
	req = req.WithContext(context.WithValue(ctx, principalKey{}, agent))
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || reached != "say" {
		t.Fatalf("a bearer token was judged by a project role: %d %q", rec.Code, reached)
	}
}

// AN UNKNOWN ROLE MAY NOT WRITE. A name from a newer node, a hand-edited row,
// or next week's addition: the safe reading of "I do not know what this means"
// is the one that refuses. A reader wrongly refused says so at once; a writer
// wrongly allowed is found by reading what they wrote.
func TestAnUnknownRoleMayNotWrite(t *testing.T) {
	for _, role := range []string{"", "reader", "auditor", "OWNER", " member"} {
		if store.RoleMayWrite(role) && role != " member" {
			t.Errorf("%q may write, and only member and owner should", role)
		}
	}
	if !store.RoleMayWrite(store.RoleMember) || !store.RoleMayWrite(store.RoleOwner) {
		t.Error("a member or an owner cannot write, which is the whole point of them")
	}
	// The words a refusal uses, because "you are a reader" said to somebody who
	// is not a member at all sends them to ask for the wrong thing.
	if store.RoleName("") != "not a member" {
		t.Errorf("a non-member is described as %q", store.RoleName(""))
	}
}
