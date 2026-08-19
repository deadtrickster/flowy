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
		fmt.Println("req    " + rowLine(it))
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
	resp, err := client.Do(req)
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

// rowLine is one waiting row: what it is, and the one thing stopping it.
//
// A ROW SAYS WHY IT IS NOT MOVING, which the script's version could not - it
// printed id and status, so a red row, a blocked row and a row nobody has
// reached yet all read the same. Those are the three states somebody asking
// about the queue is actually asking about.
func rowLine(it mergeQueueItem) string {
	line := it.ID + " " + it.Branch
	switch {
	case it.Red != nil:
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
	default:
		line += "  " + it.Status
	}
	return line
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

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return strings.TrimSpace(s)
}
