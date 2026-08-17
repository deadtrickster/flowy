package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// What a waiter took while its agent could not read it.
//
// The cursor is server-side and it moves on DELIVERY, not on comprehension: a
// waiter writes the messages to stdout, acks, and returns. That is right when
// the agent is reading, and wrong in the one case that keeps happening here -
// an agent rate limited for hours whose waiter is still healthy and still
// polling. Measured on 2026-08-17: both glm waiters were attached and polling
// through a five-hour limit, so the room emptied into background-task output
// that nothing was left to read.
//
// The events are not lost. spoolEvents writes every delivery to a JSONL file
// before the ack, precisely so this case is recoverable. But nothing read that
// file - it had a writer, a comment calling it "a file the hook drains", and
// no drain. This is the drain.
//
// It reads the local spool, not the node. The node has already moved on, and
// re-reading from it would mean rewinding a cursor that other tools trust.

const inboxReplayUsage = `flowy inbox replay - reread what was delivered while you were away

usage:
  flowy inbox replay --as NAME [--last N] [--since TIME] [--room R]

  --as NAME     the waiter whose spool to read - the same name it waits under
  --last N      only the last N messages, newest last (default 50, 0 for all)
  --since TIME  only messages created after TIME (RFC3339, e.g. 2026-08-17T21:00:00Z)
  --room R      only messages in this room

Output matches the waiter's: one JSON object per line, oldest first, so
anything that reads a delivery reads a replay without changing.

This reads the spool on THIS machine. It does not touch the node and does not
move any cursor, so replaying is safe while a waiter is running.

exit codes:
  0  the spool was read; matching messages, if any, are on stdout
  2  no spool for that name, or it could not be read
`

// inboxReplayCmd is `flowy inbox replay`.
func inboxReplayCmd(args []string) error {
	fs := flag.NewFlagSet("inbox replay", flag.ContinueOnError)
	as := fs.String("as", "", "the waiter whose spool to read")
	last := fs.Int("last", 50, "how many of the most recent messages to print, 0 for all")
	since := fs.String("since", "", "only messages created after this RFC3339 time")
	room := fs.String("room", "", "only messages in this room")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 && fs.Args()[0] == "help" {
		fmt.Print(inboxReplayUsage)
		return nil
	}
	if strings.TrimSpace(*as) == "" {
		return errors.New("which waiter: pass --as NAME\n\n" + inboxReplayUsage)
	}
	if *last < 0 {
		return errors.New("--last is a count of messages and cannot be negative")
	}
	var after time.Time
	if strings.TrimSpace(*since) != "" {
		t, err := time.Parse(time.RFC3339, *since)
		if err != nil {
			return fmt.Errorf("--since is an RFC3339 time like 2026-08-17T21:00:00Z: %w", err)
		}
		after = t
	}

	path, err := spoolPath(*as)
	if err != nil {
		return err
	}
	fh, err := os.Open(path)
	if err != nil {
		// SAY WHERE IT LOOKED. A waiter that has never delivered has no spool,
		// and that is not the same as a typo in the name - but the two are
		// indistinguishable without the path.
		return fmt.Errorf("no spool to replay at %s: %w", path, err)
	}
	defer fh.Close()

	// A LINE THAT WILL NOT PARSE IS SKIPPED, NOT FATAL. The spool is appended
	// to by a process that can be killed mid-write, so the last line of a
	// crashed waiter's spool is routinely half a message. Refusing the whole
	// file over its tail would lose the hours in front of it.
	var kept []map[string]any
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	torn := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			torn++
			continue
		}
		if *room != "" && asString(e["room"]) != *room {
			continue
		}
		if !after.IsZero() {
			t, err := time.Parse(time.RFC3339Nano, asString(e["created"]))
			if err != nil || !t.After(after) {
				continue
			}
		}
		kept = append(kept, e)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if *last > 0 && len(kept) > *last {
		kept = kept[len(kept)-*last:]
	}

	out := bufio.NewWriter(os.Stdout)
	enc := json.NewEncoder(out)
	for _, e := range kept {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	if err := out.Flush(); err != nil {
		return err
	}
	// Counts on stderr, like the waiter's, so stdout stays a clean stream of
	// whole messages for whatever reads deliveries.
	fmt.Fprintf(os.Stderr, "replayed %d message(s) from %s\n", len(kept), path)
	if torn > 0 {
		fmt.Fprintf(os.Stderr, "%d unreadable line(s) skipped - a waiter killed mid-write leaves one\n", torn)
	}
	return nil
}

// spoolPath is where spoolEvents writes, derived the same way rather than
// remembered - two spellings of one path is how a drain misses its spool.
func spoolPath(as string) (string, error) {
	dir, err := waiterDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "inbox-spool-"+unsafeInName.ReplaceAllString(as, "-")+".jsonl"), nil
}

// asString is the JSON reading of a field that should be a string, without
// panicking on a spool line where it is not.
func asString(v any) string {
	s, _ := v.(string)
	return s
}
