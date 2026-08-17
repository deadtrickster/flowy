package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// spoolFixture writes a spool for one waiter name and points the waiter
// directory at it, so a replay reads exactly these lines.
func spoolFixture(t *testing.T, as string, lines ...string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	path := filepath.Join(dir, "flowy", "inbox-spool-"+as+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// captureStdout runs fn with stdout redirected and hands back what it printed.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	// Drained while the command runs: a pipe holds 64KB and a replay of a real
	// spool is larger than that, so reading after the write would deadlock on
	// exactly the backlogs this command is for.
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	runErr := fn()
	os.Stdout = saved
	w.Close()
	out := <-done
	r.Close()
	return out, runErr
}

// The case this command exists for: an agent that could not read comes back and
// asks for what the waiter took on its behalf. The node is not consulted.
func TestReplayReadsTheSpool(t *testing.T) {
	spoolFixture(t, "glm",
		`{"room":"general","actor":"a","body":"one","created":"2026-08-17T21:00:00Z"}`,
		`{"room":"general","actor":"b","body":"two","created":"2026-08-17T21:05:00Z"}`,
	)
	out, err := captureStdout(t, func() error { return inboxReplayCmd([]string{"--as", "glm"}) })
	if err != nil {
		t.Fatalf("replay of a readable spool failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("replay printed %d line(s), want the 2 that were spooled:\n%s", len(lines), out)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("replay printed a line that is not a message: %v", err)
	}
	if first["body"] != "one" {
		t.Errorf("replay printed %q first, want the oldest message - a reader that "+
			"replays newest-first reads the conversation backwards", first["body"])
	}
}

// A waiter killed mid-write leaves half a JSON object as the last line. Losing
// the hours in front of it over that tail is the failure this guards.
func TestReplaySkipsATornLineRatherThanRefusingTheFile(t *testing.T) {
	spoolFixture(t, "glm",
		`{"room":"general","actor":"a","body":"kept","created":"2026-08-17T21:00:00Z"}`,
		`{"room":"general","actor":"b","bo`,
	)
	out, err := captureStdout(t, func() error { return inboxReplayCmd([]string{"--as", "glm"}) })
	if err != nil {
		t.Fatalf("a torn last line failed the whole replay: %v", err)
	}
	if !strings.Contains(out, `"kept"`) {
		t.Errorf("the message before the torn line was dropped:\n%s", out)
	}
}

// --since and --room are what make a five-hour backlog readable, so they have
// to filter on the message rather than on the file.
func TestReplayFiltersBySinceAndRoom(t *testing.T) {
	spoolFixture(t, "glm",
		`{"room":"general","actor":"a","body":"old","created":"2026-08-17T20:00:00Z"}`,
		`{"room":"general","actor":"b","body":"new","created":"2026-08-17T22:00:00Z"}`,
		`{"room":"other","actor":"c","body":"elsewhere","created":"2026-08-17T22:30:00Z"}`,
	)
	out, err := captureStdout(t, func() error {
		return inboxReplayCmd([]string{"--as", "glm", "--since", "2026-08-17T21:00:00Z", "--room", "general"})
	})
	if err != nil {
		t.Fatalf("filtered replay failed: %v", err)
	}
	if strings.Contains(out, "old") || strings.Contains(out, "elsewhere") {
		t.Errorf("a filter let through what it was asked to exclude:\n%s", out)
	}
	if !strings.Contains(out, "new") {
		t.Errorf("the one message inside both filters was dropped:\n%s", out)
	}
}

// A name with no spool is not a crash and not silence: it says where it looked,
// because "never delivered" and "misspelled the name" look identical without it.
func TestReplaySaysWhereItLooked(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	err := inboxReplayCmd([]string{"--as", "nobody"})
	if err == nil {
		t.Fatal("replay of a name with no spool returned success")
	}
	if !strings.Contains(err.Error(), "inbox-spool-nobody.jsonl") {
		t.Errorf("the error does not name the path it tried: %v", err)
	}
}
