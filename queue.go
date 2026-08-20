package main

// `flowy queue` - the merge queue, read by the binary that defines its shape.
//
// This replaces scripts/q.sh's queue and lock verbs, which were curl plus jq
// plus a reading of the payload kept in step by hand. That second reader was
// wrong twice on 2026-08-18, and both times it changed a decision: it read
// `tip` where the answer says `target_tip`, and it quoted a lock that had been
// given back minutes earlier as if it were current.
//
// Neither was a hard bug to fix and that is the point - the class is a client
// that rebuilds a shape the server already knows. So the door's answer is a
// named type (mergeQueueAnswer, api_mergequeue.go) and this decodes THAT. The
// two cannot drift, because there is one definition and the compiler holds
// both ends of it.
//
// WHAT IT KEEPS FROM THE SCRIPT: every lock reading carries the moment it was
// taken. A LOCK READING IS A CLAIM ABOUT THE PAST - three agents quoted a
// minutes-old reading as current in one afternoon, and all three readings were
// true when taken. Printing the time beside the answer makes a stale quote
// visible as stale to whoever reads it next, without anybody having to
// remember to add it. Remembering is what failed.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const queueUsage = `flowy queue - what is waiting to land, and who holds the target

usage:
  flowy queue                 the target, the lock, and every row waiting
  flowy queue lock            the landing lock alone, stamped with when it was read
  flowy queue wait --row ID   block until that row lands, goes red, or the deadline passes

flags:
  --target T   which target to ask about (default master)
  --url URL    node to talk to (default $FLOWY_ADDR)
  --token T    bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)
  --agent NAME speak as a named seat from ~/.config/flowy/agents

Every lock line carries the moment it was read, because a lock reading is a
claim about the past and a stale one is indistinguishable from a current one
once it has been pasted somewhere else.
`

func queueCmd(args []string) error {
	if len(args) > 0 && args[0] == "help" {
		fmt.Print(queueUsage)
		return nil
	}
	sub := ""
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, rest = args[0], args[1:]
	}

	// BEFORE THIS FLAGSET, because the wait verb has flags of its own (--row,
	// --deadline) and parsing them here would refuse them as unknown.
	if sub == "wait" {
		return queueWaitCmd(rest)
	}

	fs := flag.NewFlagSet("queue", flag.ContinueOnError)
	target := fs.String("target", "", "which target to ask about (default master)")
	urlFlag := fs.String("url", "", "node to talk to (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token := fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)")
	agent := fs.String("agent", "", agentFlagHelp)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if sub != "" && sub != "lock" {
		return fmt.Errorf("unknown queue command %q\n\n%s", sub, queueUsage)
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
	answer, read, err := readQueue(ctx, &http.Client{Timeout: 30 * time.Second}, base, bearer, *target)
	if err != nil {
		return err
	}
	if sub == "lock" {
		fmt.Println(lockLine(answer.Lock, read))
		return nil
	}
	fmt.Printf("target %s %s from=%s gating=%d\n",
		answer.Target, shortSHA(answer.TargetTip), answer.TipFrom, answer.Gating)
	fmt.Println("lock   " + lockLine(answer.Lock, read))
	for _, it := range answer.Items {
		fmt.Println("req    " + rowLine(it, answer.TargetTip))
	}
	return nil
}

// readQueue asks the node and says WHEN it asked, because the caller printing
// the answer is not always the caller that read it.
func readQueue(
	ctx context.Context, client *http.Client, base, token, target string,
) (mergeQueueAnswer, time.Time, error) {
	path := base + "/api/merge-queue"
	if strings.TrimSpace(target) != "" {
		path += "?target=" + target
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return mergeQueueAnswer{}, time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// THROUGH A RESTART. 2e2e13e taught the verbs that go via peerRequest to
	// wait out a refused dial; this one builds its own request and so did not
	// get it. A refused connection means nothing was sent, which is what makes
	// retrying it safe - and a deploy takes about ten seconds, during which
	// every client here would otherwise exit with a dial error.
	resp, err := doThroughARestart(ctx, client, req, nil)
	if err != nil {
		return mergeQueueAnswer{}, time.Time{}, err
	}
	defer resp.Body.Close()
	read := time.Now().UTC()
	if resp.StatusCode != http.StatusOK {
		return mergeQueueAnswer{}, read, fmt.Errorf("the node answered %s", resp.Status)
	}
	var answer mergeQueueAnswer
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return mergeQueueAnswer{}, read, fmt.Errorf("the queue answer did not decode: %w", err)
	}
	return answer, read, nil
}

// lockLine says who holds the target and when this was true.
func lockLine(lock *mergeQueueLock, read time.Time) string {
	stamp := "   [read " + read.Format("15:04:05Z") + "]"
	if lock == nil || !lock.Held {
		return "free" + stamp
	}
	who := lock.HolderName
	if who == "" {
		who = lock.Holder
	}
	line := "held by " + who
	if lock.Item != "" {
		line += " for " + lock.Item
	}
	if !lock.Until.IsZero() {
		line += " until " + lock.Until.UTC().Format("15:04:05Z")
	}
	return line + stamp
}

// rowLine is one waiting row: what it is, and the one thing stopping it NOW.
//
// A ROW SAYS WHY IT IS NOT MOVING, which the script's version could not - it
// printed id and status, so a red row, a blocked row and a row nobody has
// reached yet all read the same. Those are the three states somebody asking
// about the queue is actually asking about.
//
// THE ORDER IS BY WHAT IS CURRENT, NOT BY FIELD ORDER, and that distinction
// cost two seats an hour between them on 2026-08-20. One response about one row
// carried three answers at once:
//
//	red      35e4256, base db7ec6b, at 18:56
//	blocked  "checked out in .../wt-sw3", at 19:30
//	reason   "no gate has measured it - there is no verdict to be stale"
//	target   f0f0df8
//
// and this function printed the first one. The red was not merely old, it was
// PROVABLY spent: it was measured from db7ec6b and the target had moved to
// f0f0df8, which the same object says. The store knew, the admissible door
// knew, and drain.sh knew - it keys its skip on the branch and target pair for
// exactly this reason - and the line a person reads was the only thing still
// repeating a dead measurement. An hour went into the test that red named
// before anybody read the field below it.
//
// So a spent red is demoted beneath everything true now, and SAYS it is spent
// rather than disappearing - a row that goes quiet is the other failure.
//
// A LIVE RED IS STILL LOUDER THAN A GATE, which was decided earlier and stands:
// a row being re-measured after a red is where "gating" alone hides the thing
// worth knowing. And a red whose base is unknown counts as live, so this only
// ever demotes one measured from a base we can see the target has left.
func rowLine(it mergeQueueItem, targetTip string) string {
	line := it.ID + " " + it.Branch
	spent := redIsSpent(it.Red, targetTip)
	switch {
	case it.Red != nil && !spent:
		line += "  RED " + shortSHA(it.Red.Tip)
		if it.Red.Note != "" {
			line += " " + firstLine(it.Red.Note)
		}
	case it.Blocked != nil:
		line += "  BLOCKED " + firstLine(it.Blocked.Why)
	case it.Gating:
		line += "  gating"
	case it.Admissible != nil && *it.Admissible:
		line += "  LANDABLE"
	case spent:
		// Named rather than dropped. "The red does not apply" is a fact worth
		// having: it says the row is waiting for a re-gate rather than sitting
		// on a verdict, which is what somebody who saw the red an hour ago
		// needs to be told.
		line += "  red spent - measured from " + shortSHA(it.Red.Base) +
			", target is " + shortSHA(targetTip) + " - waiting to be re-gated"
	default:
		line += "  " + it.Status
	}
	return line
}

// redIsSpent is whether a red was measured from a base the target has left.
//
// FALSE WHEN IT CANNOT TELL, which is the whole rule: an unknown base and a
// base that matches are both reasons to keep repeating the red, because the
// cost of hiding a live red is a branch landing broken and the cost of showing
// a spent one is a sentence. Only a base we can SEE has moved demotes it.
//
// The two shas come from different places and are not always the same length -
// the queue answer carries the tip as the node recorded it, the row keeps what
// the gate wrote down - so this compares on the shorter of the two, and refuses
// to judge on fewer than seven characters. queuewait.go's staleRed makes the
// same kind of comparison about a different pair.
func redIsSpent(red *mergeQueueRed, targetTip string) bool {
	if red == nil || red.Base == "" || targetTip == "" {
		return false
	}
	n := min(len(red.Base), len(targetTip))
	if n < 7 {
		return false
	}
	return red.Base[:n] != targetTip[:n]
}

// shortSHA is twelve characters, which is what this fleet quotes. Named for
// what it shortens because `short` is already taken by sync.go for truncating
// a peer's answer, and two functions called short would be one more shape kept
// in step by hand.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// reasonWidth is what one queue line can spend on why a row is not moving. The
// rest of the line is an id and a branch name, both of which the reader needs
// to act at all.
const reasonWidth = 60

// firstLine is a reason cut to fit a queue line, in the direction that leaves
// it actionable, and MARKED so that a reader can tell it was cut.
//
// IT USED TO CUT SILENTLY, AND FROM THE WRONG END. Measured on a row that sat
// blocked for eleven minutes:
//
//	BLOCKED feat/a-person-belongs-to-projects is checked out in /tmp/cla
//
// Two separate failures in one line. Nothing says that stopped early, so it
// reads as a whole sentence about a directory that does not exist and the
// reader has no reason to look for more. And the sixty bytes it kept were the
// branch name - which the same line already prints - and the boilerplate, so
// the path, the only part anybody can act on, is what fell off the end.
//
// So THE MIDDLE GOES, not the end. Keeping only the tail was the first fix I
// wrote and it was wrong at the other call site: a red note reads
// "passed: 699 failed: 9 - FAIL the tui..." and there the useful half is at the
// FRONT. The two reasons this prints put their meaning at opposite ends, so a
// cut that picks an end is right for one of them and a regression for the
// other. Eliding the middle is what every terminal does to a long path, for
// this reason.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	// Counted in RUNES, not bytes. The old cut sliced a byte index straight
	// through whatever was there, and a path with a non-ASCII character in it
	// would come out ending in a replacement glyph - a corrupted reason that
	// still looks like a reason.
	r := []rune(s)
	if len(r) <= reasonWidth {
		return s
	}
	// The ellipsis costs one of the budget and is the whole point: a cut nobody
	// can see is worse than a shorter reason. The head keeps the larger half,
	// because what a reason leads with is usually what names the failure.
	head := (reasonWidth - 1 + 1) / 2
	tail := reasonWidth - 1 - head
	return strings.TrimRight(string(r[:head]), " ") + "…" + strings.TrimLeft(string(r[len(r)-tail:]), " ")
}
