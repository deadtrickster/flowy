package main

// `flowy nag` - what an idle seat should know, read from the node that decides
// it.
//
// GET /api/nag has carried the whole thing since it moved off scripts/board-nag.sh:
// the caller's own rows, what nobody is carrying, what has gone stale, and the
// distribution probe with its thresholds and its verdict. What it has not had
// is a way to READ it without writing curl and a jq program, so every seat that
// wanted the numbers computed them again from /api/artifacts.
//
// MEASURED, on my own commands in one session: twenty times I grouped the board
// by assignee and eyeballed the shares against the operator's line, with the
// answer one GET away the whole time. That is the same shape as the merge-queue
// wait door somebody built in the morning and then polled around all afternoon:
// A DOOR NOBODY USES IS THE SAME AS NO DOOR, and the reason nobody used this one
// is that reaching it cost more than redoing the arithmetic.
//
// So this decodes nagView, the door's own type, exactly as `flowy queue` decodes
// mergeQueueAnswer and for the same reason: a second reader that rebuilds the
// shape is a second reader that drifts, and this fleet has now been wrong twice
// that way in two days.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const nagUsage = `flowy nag - your rows, what nobody has, and how the work is spread

usage:
  flowy nag            the counts, the shares and the verdict
  flowy nag --json     the node's answer, unchanged, for a script

flags:
  --url URL    node to talk to (default $FLOWY_ADDR)
  --token T    bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)
  --agent NAME speak as a named seat from ~/.config/flowy/agents

EVERY COUNT IS THE CALLER'S. There is no name flag: a door that answered about
somebody else would report on a seat that cannot see the same board, and the
share of a board you cannot read is not a number anybody should act on.

The verdict is the node's and not this client's - ok, check above half the open
rows, rebalance above four fifths, alone for one carrier with nothing unclaimed,
empty for nothing open. It is printed rather than recomputed here.
`

func nagCmd(args []string) error {
	if len(args) > 0 && args[0] == "help" {
		fmt.Print(nagUsage)
		return nil
	}
	fs := flag.NewFlagSet("nag", flag.ContinueOnError)
	urlFlag := fs.String("url", "", "node to talk to (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token := fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)")
	agent := fs.String("agent", "", agentFlagHelp)
	asJSON := fs.Bool("json", false, "print the node's answer unchanged")
	if err := fs.Parse(args); err != nil {
		return err
	}

	base := resolveURL(*urlFlag, os.Getenv("FLOWY_ADDR"))
	bearer, err := resolveToken(*token, os.Getenv("FLOWY_TOKEN"), *agent, os.Getenv("FLOWY_AGENT"))
	if err != nil {
		return err
	}
	if bearer == "" {
		return errNoToken()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	view, raw, err := readNagAnswer(ctx, &http.Client{Timeout: 30 * time.Second}, base, bearer)
	if err != nil {
		return err
	}
	if *asJSON {
		fmt.Println(string(raw))
		return nil
	}
	printNag(view)
	return nil
}

func readNagAnswer(
	ctx context.Context, client *http.Client, base, token string,
) (nagView, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/nag", nil)
	if err != nil {
		return nagView{}, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nagView{}, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nagView{}, nil, fmt.Errorf("the node answered %s", resp.Status)
	}
	// Bounded, like every other body this binary reads off a wire: a node that
	// answers a gigabyte is a node this client should refuse rather than hold.
	buf, err := io.ReadAll(io.LimitReader(resp.Body, maxSyncBody))
	if err != nil {
		return nagView{}, nil, err
	}
	var view nagView
	if err := json.Unmarshal(buf, &view); err != nil {
		return nagView{}, buf, fmt.Errorf("the nag answer did not decode: %w", err)
	}
	return view, buf, nil
}

// printNag draws the answer for a person: the board, then the spread.
//
// The shares are drawn as a list and not as a bar, because this is read in a
// terminal beside a queue and a lock line and it has to line up with them. The
// caller's own row is marked, because the one share a reader most needs to find
// is their own and matching a handle against your own name is what the addressee
// marker in the TUI exists to avoid.
func printNag(view nagView) { fmt.Print(nagLines(view)) }

// nagLines is what printNag prints, as a string, so the wording can be tested
// without a terminal - which is the half that goes wrong: a verdict is one word
// and the two that matter mean different things to do.
func nagLines(view nagView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "board  open %d  unowned %d  mine %d (todo %d)  stale %d\n",
		view.Open, view.Unowned, view.Mine, view.MineTodo, view.Stale)
	// The spread, then who is not listening. The quiet line is NOT inside the
	// spread block, and that is the fix for a bug this file had for one commit:
	// an early return on an empty board skipped it, so the one board where a
	// silent reader matters most - nothing being carried, and a seat that
	// cannot hear you - said nothing about it.
	w := view.Workload
	if len(w.Shares) == 0 {
		fmt.Fprintf(&b, "spread %s - nobody is carrying anything\n", w.Verdict)
		b.WriteString(quietLine(view))
		return b.String()
	}
	fmt.Fprintf(&b, "spread %s", w.Verdict)
	// WHICH LINE, and what to do about it. The word alone leaves the reader
	// doing the arithmetic this verb exists to stop them doing.
	switch w.Verdict {
	case "check":
		fmt.Fprintf(&b, " - %s holds %.0f%%, over the %.0f%% line", w.Top, w.TopShare*100, w.Check*100)
	case "rebalance":
		fmt.Fprintf(&b, " - %s holds %.0f%%, over the %.0f%% line: hand some back",
			w.Top, w.TopShare*100, w.Rebalance*100)
	case "alone":
		fmt.Fprintf(&b, " - one carrier and nothing unclaimed, so the share says nothing")
	}
	b.WriteString("\n")
	for _, s := range w.Shares {
		fmt.Fprintf(&b, "       %-16s %2d  %3.0f%%\n", s.Assignee, s.Open, s.Share*100)
	}
	b.WriteString(quietLine(view))
	return b.String()
}

// quietLine names the readers that have stopped polling, or says nothing.
//
// WHO IS NOT LISTENING is the one thing on this answer that is about the fleet
// rather than about the board. A seat with a share and no reader is a seat
// holding work nothing can reach.
//
// THE NAMES AND NOT THE DURATIONS, on the instruction of whoever wrote that
// field: nagCursor drops the durations deliberately, so a reader which is still
// quiet does not look like news on every poll. Printing the seconds here would
// put a number on the screen that changes every tick and means nothing new -
// the same wake-every-tick the cursor exists to prevent, one surface further
// out.
//
// Nothing at all when nobody is quiet, rather than an empty heading: absent is
// the honest answer, and it is the one the door itself gives.
func quietLine(view nagView) string {
	if len(view.Quiet) == 0 {
		return ""
	}
	names := make([]string, 0, len(view.Quiet))
	for _, q := range view.Quiet {
		// `forked` is the value worth carrying: a reader that holds the cursor
		// and wakes nobody is a different problem from one that has simply
		// stopped, and a seat deciding whether to take the name over needs to
		// tell them apart.
		if q.Kind == "forked" {
			names = append(names, q.Reader+" (forked)")
			continue
		}
		names = append(names, q.Reader)
	}
	return "quiet  " + strings.Join(names, ", ") + "\n"
}
