package flowy

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFollowSpoolPrintsOnlyWhatArrivesAfterItStartsAndStopsWhenTheWaiterDies(t *testing.T) {
	dir := t.TempDir()
	spool := filepath.Join(dir, "inbox-spool-x.jsonl")
	os.WriteFile(spool, []byte(`{"room":"general","body":"before"}`+"\n"), 0o600)

	var alive sync.Mutex
	up := true
	isAlive := func() bool { alive.Lock(); defer alive.Unlock(); return up }

	var got []string
	var mu sync.Mutex
	keep := func(line string) bool {
		mu.Lock()
		got = append(got, line)
		mu.Unlock()
		return false // do not print to the test's stdout
	}
	done := make(chan struct{})
	go func() { followSpool(spool, isAlive, keep); close(done) }()
	time.Sleep(200 * time.Millisecond)
	f, _ := os.OpenFile(spool, os.O_APPEND|os.O_WRONLY, 0o600)
	// Two lines, the second written in two halves across a poll: a partial line
	// must not be printed as two.
	f.WriteString(`{"room":"general","body":"one"}` + "\n" + `{"room":"scrollcheck","bo`)
	f.Sync()
	time.Sleep(700 * time.Millisecond)
	f.WriteString(`dy":"two"}` + "\n")
	f.Close()
	time.Sleep(700 * time.Millisecond)
	alive.Lock()
	up = false
	alive.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("followSpool did not return after the waiter died")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected the two lines written after the watcher started, got %q", got)
	}
	if !strings.Contains(got[0], `"one"`) || !strings.Contains(got[1], `"two"`) {
		t.Fatalf("lines out of order or split: %q", got)
	}
}

func TestLineFilterDropsARoomByItsFieldNotByItsName(t *testing.T) {
	keep := lineFilter("scrollcheck")
	if keep(`{"room":"scrollcheck","body":"x"}`) {
		t.Fatal("the ignored room was printed")
	}
	if !keep(`{"room":"general","body":"about scrollcheck"}`) {
		t.Fatal("a body mentioning the room was dropped")
	}
	if !keep(`not json`) {
		t.Fatal("an unparseable line must pass rather than vanish")
	}
	if !lineFilter("")(`{"room":"scrollcheck"}`) {
		t.Fatal("no ignored room means everything passes")
	}
}

func TestAHeldNameIsATypedRefusalThatNamesListen(t *testing.T) {
	e := &errWaiterHeld{name: "seat", pid: 1, kind: "tracked"}
	s := e.Error()
	for _, want := range []string{"pid 1, tracked", "flowy inbox replay --as seat", "flowy waiter check --as seat", "flowy listen --as seat"} {
		if !strings.Contains(s, want) {
			t.Errorf("refusal lacks %q:\n%s", want, s)
		}
	}
}
