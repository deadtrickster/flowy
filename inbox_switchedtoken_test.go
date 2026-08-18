package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A waiter resolves its bearer once and polls for hours. When a seat is
// re-minted, every poll under the OLD credential still succeeds - the reader
// exists, the cursor moves, nothing is refused - so the waiter looks healthy
// while polling as somebody the seat no longer is. That cost six and a half
// hours, and the refusal at the start-up door never fires because the waiter is
// never restarted.

func seatDir(t *testing.T, name, token string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "flowy", "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestASeatReMintedUnderARunningWaiterIsNoticed(t *testing.T) {
	seatDir(t, "probe", "second-token")
	if tokenStillOurs("first-token", "probe") {
		t.Fatal("the seat file holds a different credential - that is the case this exists for")
	}
}

func TestAnUnchangedSeatIsNotASwitch(t *testing.T) {
	seatDir(t, "probe", "same-token")
	if !tokenStillOurs("same-token", "probe") {
		t.Fatal("an unchanged seat must not stop a healthy waiter")
	}
}

// ABSENCE IS NOT A SWITCH. A re-mint writing the file, a backup moving it, a
// permission blip - none of those is evidence the credential changed, and a
// waiter that died on them would be worse than the bug: it would fail for
// reasons that have nothing to do with identity.
func TestAnUnreadableSeatIsNotEvidenceOfAnything(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if !tokenStillOurs("first-token", "no-such-seat") {
		t.Error("a missing seat file must not read as a switch - absence is unknown")
	}
}

// A token from a flag or the environment cannot change under a running
// process, so there is nothing to re-read. Saying true here is honest; going to
// the filesystem for a credential that did not come from it would be inventing
// a check.
func TestATokenThatCannotChangeIsNotChecked(t *testing.T) {
	if !tokenStillOurs("from-a-flag", "") {
		t.Error("with no seat name there is no file to compare against")
	}
}
