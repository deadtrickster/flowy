package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// loginUser mints a user with a handle nothing else will collide with.
func loginUser(t *testing.T, ctx context.Context, db *DB) *User {
	t.Helper()

	// THE WHOLE ULID, not a prefix. The first ten characters are the
	// timestamp, so two users minted in the same millisecond collide on the
	// handle's unique index - which is what the first cut of this did, and it
	// failed in the one test that mints two users in a row.
	u := &User{Handle: "login-" + ulid.NewString(), Display: "Login Test"}
	if err := db.InsertUser(ctx, u); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return u
}

// The whole verb, in the order a person meets it.
func TestAPasswordLetsSomebodyInAndTheWrongOneDoesNot(t *testing.T) {
	ctx, db := open(t)
	u := loginUser(t, ctx, db)

	// A user with no secret cannot log in, and the refusal says nothing about
	// whether the handle is real.
	if _, err := db.VerifyLogin(ctx, u.Handle, "hunter2hunter2"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("a user with no password: got %v, want ErrBadLogin", err)
	}
	if err := db.SetPassword(ctx, u.ID, "correct horse battery"); err != nil {
		t.Fatalf("set password: %v", err)
	}

	got, err := db.VerifyLogin(ctx, u.Handle, "correct horse battery")
	if err != nil {
		t.Fatalf("the right password was refused: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("logged in as %s, not %s", got.ID, u.ID)
	}
	if _, err := db.VerifyLogin(ctx, u.Handle, "correct horse batterz"); !errors.Is(err, ErrBadLogin) {
		t.Errorf("a one-character-wrong password: got %v, want ErrBadLogin", err)
	}
}

// A HANDLE THAT DOES NOT EXIST AND A PASSWORD THAT IS WRONG ANSWER THE SAME.
//
// Told apart, the pair is an oracle for which accounts exist. This asserts the
// error is identical rather than merely both non-nil - two different sentences
// reaching a login form is exactly how that leaks.
func TestAMissingHandleAndAWrongPasswordAreOneAnswer(t *testing.T) {
	ctx, db := open(t)
	u := loginUser(t, ctx, db)
	if err := db.SetPassword(ctx, u.ID, "correct horse battery"); err != nil {
		t.Fatalf("set password: %v", err)
	}

	_, missing := db.VerifyLogin(ctx, "nobody-"+ulid.NewString(), "correct horse battery")
	_, wrong := db.VerifyLogin(ctx, u.Handle, "not the password")
	if !errors.Is(missing, ErrBadLogin) || !errors.Is(wrong, ErrBadLogin) {
		t.Fatalf("missing=%v wrong=%v, want both ErrBadLogin", missing, wrong)
	}
	if missing.Error() != wrong.Error() {
		t.Errorf("the two failures read differently:\n  missing: %s\n  wrong:   %s", missing, wrong)
	}
}

// A secret cannot be written for a user that is not there. The foreign key
// would refuse it; this is about the caller getting a sentence rather than a
// constraint name, and about the write not being reported as done.
func TestAPasswordForNobodyIsRefused(t *testing.T) {
	ctx, db := open(t)
	if err := db.SetPassword(ctx, "01USER-DOES-NOT-EXIST", "correct horse battery"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// bcrypt reads 72 bytes and silently ignores the rest, so a longer password
// would be accepted here and verified on its prefix - two different passwords
// that both work. The floor is the ordinary one.
func TestAPasswordTooShortOrLongerThanBcryptReadsIsRefused(t *testing.T) {
	ctx, db := open(t)
	u := loginUser(t, ctx, db)

	if err := db.SetPassword(ctx, u.ID, "short"); err == nil {
		t.Error("an eight-character floor let a five-character password through")
	}
	long := ""
	for len(long) < 100 {
		long += "abcdefghij"
	}
	if err := db.SetPassword(ctx, u.ID, long); err == nil {
		t.Error("a password past bcrypt's 72 bytes was accepted, and would verify on its prefix")
	}
}

// A session is a row, which is what makes logout mean something.
func TestASessionResolvesUntilItIsEndedOrExpires(t *testing.T) {
	ctx, db := open(t)
	u := loginUser(t, ctx, db)

	s, err := db.StartSession(ctx, u.ID, "go test")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	if s.Expires.Before(time.Now()) {
		t.Fatalf("a fresh session is already expired: %v", s.Expires)
	}

	got, err := db.UserForSession(ctx, s.ID)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("session resolved to %s, not %s", got.ID, u.ID)
	}

	if err := db.EndSession(ctx, s.ID); err != nil {
		t.Fatalf("end session: %v", err)
	}
	if _, err := db.UserForSession(ctx, s.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a session that was logged out still resolves: %v", err)
	}
	// Twice is not an error. A person clicking logout on a stale tab is not
	// reporting a fault.
	if err := db.EndSession(ctx, s.ID); err != nil {
		t.Errorf("logging out twice: %v", err)
	}
}

// AN EXPIRED SESSION IS REFUSED BY THE READ, not by a sweep that may not have
// run. The row is left in place here on purpose - if only ExpireSessions
// enforced this, a cleanup that fell over would silently extend every session
// on the node.
func TestAnExpiredSessionIsRefusedBeforeAnythingSweepsIt(t *testing.T) {
	ctx, db := open(t)
	u := loginUser(t, ctx, db)
	s, err := db.StartSession(ctx, u.ID, "go test")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE sessions SET expires = now() - interval '1 second' WHERE id = $1`, s.ID); err != nil {
		t.Fatalf("age the session: %v", err)
	}

	if _, err := db.UserForSession(ctx, s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired session resolved: %v", err)
	}
	// And the row was still there when it was refused, which is the point.
	var alive bool
	if err := db.sql.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM sessions WHERE id = $1)`, s.ID).Scan(&alive); err != nil {
		t.Fatal(err)
	}
	if !alive {
		t.Error("the read deleted the row - then this test cannot tell enforcement from cleanup")
	}
	if _, err := db.ExpireSessions(ctx); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if err := db.sql.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM sessions WHERE id = $1)`, s.ID).Scan(&alive); err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Error("ExpireSessions left an expired row")
	}
}

// Sign out everywhere, which is the reason sessions are rows and not a signed
// blob. It counts, because "we ended some of them" is not an answer.
func TestEndingEverySessionEndsEveryOne(t *testing.T) {
	ctx, db := open(t)
	u := loginUser(t, ctx, db)
	other := loginUser(t, ctx, db)

	var mine []string
	for i := 0; i < 3; i++ {
		s, err := db.StartSession(ctx, u.ID, "go test")
		if err != nil {
			t.Fatalf("start session: %v", err)
		}
		mine = append(mine, s.ID)
	}
	keep, err := db.StartSession(ctx, other.ID, "go test")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	n, err := db.EndSessionsFor(ctx, u.ID)
	if err != nil {
		t.Fatalf("end sessions: %v", err)
	}
	if n != 3 {
		t.Errorf("ended %d sessions, started 3", n)
	}
	for _, id := range mine {
		if _, err := db.UserForSession(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("session %s survived: %v", id, err)
		}
	}
	// AND NOBODY ELSE'S. A delete keyed on the wrong column would pass every
	// assertion above.
	if _, err := db.UserForSession(ctx, keep.ID); err != nil {
		t.Errorf("another user's session was ended too: %v", err)
	}
}

// A session for a user that does not exist is refused rather than written. The
// cookie would otherwise name a row whose user is gone, which resolves to
// nothing on every request and looks like an expiry.
func TestASessionForNobodyIsRefused(t *testing.T) {
	ctx, db := open(t)
	if _, err := db.StartSession(ctx, "01USER-DOES-NOT-EXIST", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// Deleting the user takes their secret and their sessions with it. This is the
// ON DELETE CASCADE, asserted because a password that outlives its account is
// a credential nobody can see and nobody can revoke.
func TestDeletingAUserTakesTheirSecretAndSessions(t *testing.T) {
	ctx, db := open(t)
	u := loginUser(t, ctx, db)
	if err := db.SetPassword(ctx, u.ID, "correct horse battery"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	s, err := db.StartSession(ctx, u.ID, "go test")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	if _, err := db.sql.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	var secrets, sessions int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT (SELECT count(*) FROM user_secrets WHERE user_id = $1),
		        (SELECT count(*) FROM sessions WHERE id = $2)`, u.ID, s.ID).
		Scan(&secrets, &sessions); err != nil {
		t.Fatal(err)
	}
	if secrets != 0 || sessions != 0 {
		t.Errorf("after deleting the user: %d secrets and %d sessions remain", secrets, sessions)
	}
}

// A person can be named after they exist, which nothing could do before: handle
// was written by InsertUser and by MintAgent and by no other statement.
func TestAPersonCanBeRenamed(t *testing.T) {
	ctx, db := open(t)
	u := loginUser(t, ctx, db)

	if err := db.SetHandle(ctx, u.ID, "renamed-"+ulid.NewString()); err != nil {
		t.Fatalf("rename: %v", err)
	}
	// AND THE PASSWORD FOLLOWS THE PERSON, because the secret is keyed on the
	// user id. A rename that silently invalidated a login would be the worst
	// possible shape: the account works until the next time somebody types the
	// new name.
	if err := db.SetPassword(ctx, u.ID, "correct horse battery"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	renamed, err := db.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.VerifyLogin(ctx, renamed.Handle, "correct horse battery"); err != nil {
		t.Errorf("the new handle cannot log in: %v", err)
	}
	if _, err := db.VerifyLogin(ctx, u.Handle, "correct horse battery"); !errors.Is(err, ErrBadLogin) {
		t.Errorf("the OLD handle still logs in: %v", err)
	}
}

// Somebody else's handle is refused in a sentence, not as a constraint name.
func TestATakenHandleIsRefusedByName(t *testing.T) {
	ctx, db := open(t)
	first := loginUser(t, ctx, db)
	second := loginUser(t, ctx, db)

	err := db.SetHandle(ctx, second.ID, first.Handle)
	if err == nil {
		t.Fatal("two people now share a handle, which login resolves through")
	}
	if !strings.Contains(err.Error(), first.Handle) {
		t.Errorf("the refusal does not name the handle: %v", err)
	}
	// And the second user still has their own.
	still, err := db.GetUser(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.Handle != second.Handle {
		t.Errorf("a refused rename changed the handle to %q", still.Handle)
	}
}

// Renaming somebody who is not there is ErrNotFound, not a silent success.
func TestRenamingNobodyIsRefused(t *testing.T) {
	ctx, db := open(t)
	if err := db.SetHandle(ctx, "01USER-DOES-NOT-EXIST", "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}
