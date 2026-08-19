package main

// `flowy queue wait --row ID` - block until a queued row stops waiting.
//
// The operator, 2026-08-19: "what other loop you can hardcode?" This is the one
// I could count rather than guess. I hand-rolled
//
//	until ! curl -s $ADDR/api/merge-queue | grep -q "$row"; do sleep 30; done
//
// EIGHT times in one session, five identical lines each time - and
// /api/merge-queue/wait already existed. I quoted that door in a commit message
// the same morning and then polled anyway, which is the finding: a door nobody
// uses is the same as no door, and the gap between them is a verb.
//
// WHAT THE HAND-ROLLED LOOP GOT WRONG EVERY TIME, and why this is not just
// fewer keystrokes:
//
//   - it woke every 30s whether or not anything moved, so the answer was up to
//     half a minute old and the node was asked 120 times an hour by each waiter
//   - "the row is gone from the queue" is not "the row landed". It is also how
//     an ABANDONED row and a row whose scope changed look. Twice I read a
//     disappearance as a landing and had to go back and check the sha
//   - a red leaves the row IN the queue, so the loop waited out its whole
//     deadline on a branch that had already failed and was never going to move
//
// So the outcomes are named, in `flowy inbox`'s vocabulary, and the exit codes
// follow this binary's existing convention rather than a new one: a quiet
// deadline is 1 and broken is 2, because a loop has to tell those apart without
// parsing anything.
//
//	0  it landed, or left the queue without landing - printed with the sha or
//	   the status it left as
//	1  the deadline passed and it is still queued. Not a failure: it is one of
//	   the two things a waiter is for
//	2  broken - the node did not answer, the token was refused
//	3  a red verdict arrived
//
// RED IS ITS OWN CODE and not folded into 2, because 2 is a fact about the
// TOOLING and a red is a fact about the BRANCH. A caller that retries on 2 -
// which is the sane thing to do about a node that blinked - would retry forever
// on a red, and the branch would never be looked at by the person who has to
// fix it.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const queueWaitUsage = `flowy queue wait - block until a queued row stops waiting

usage:
  flowy queue wait --row ID [--deadline 3600]

  --row       the merge row to watch. Required - a wait with no subject is a
              sleep.
  --deadline  seconds to wait before giving up (default 3600). The node is
              asked to hold each request open, so this is not a poll interval.

exits 0 when the row landed or otherwise left the queue, 1 when the deadline
passed with it still queued, 2 when something broke, 3 when a red arrived.
`

// errWaitedOut is the deadline outcome, kept as a value so main can map it to
// exit 2 rather than printing it as a failure. A row still queued after an hour
// is not an error - it is the answer.
var errWaitedOut = errors.New("still queued")

// errRowIsRed is the same for a red verdict: a real outcome, not a fault in the
// waiter, and the caller does something different about it.
var errRowIsRed = errors.New("red")

func queueWaitCmd(rest []string) error {
	fs := flag.NewFlagSet("queue wait", flag.ContinueOnError)
	row := fs.String("row", "", "the merge row to watch")
	deadline := fs.Int("deadline", 3600, "seconds to wait before giving up")
	target := fs.String("target", "", "which target to ask about (default master)")
	urlFlag := fs.String("url", "", "node to talk to (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token := fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)")
	agent := fs.String("agent", "", agentFlagHelp)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	want := strings.TrimSpace(*row)
	if want == "" {
		return errors.New("a wait with no subject is a sleep: pass --row ID\n\n" + queueWaitUsage)
	}

	base := resolveURL(*urlFlag, os.Getenv("FLOWY_ADDR"))
	bearer, err := resolveToken(*token, os.Getenv("FLOWY_TOKEN"), *agent, os.Getenv("FLOWY_AGENT"))
	if err != nil {
		return err
	}
	if bearer == "" {
		return errNoToken()
	}

	// The window each request is held open for. Shorter than the deadline on
	// purpose: the node caps its own wait, and a client that asked for an hour
	// in one request would learn nothing about a node that went away.
	const window = 60
	client := &http.Client{Timeout: time.Duration(window+15) * time.Second}
	give := time.Now().Add(time.Duration(*deadline) * time.Second)

	cursor := ""
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(window+10)*time.Second)
		answer, err := waitOnQueue(ctx, client, base, bearer, *target, cursor, window)
		cancel()
		if err != nil {
			return err
		}
		cursor = answer.Cursor

		// THE ROW'S OWN STATE, ASKED OF THE ANSWER WE JUST GOT rather than of a
		// second read. Two reads a second apart is how a waiter reports a row
		// that landed between them as having vanished.
		var found *mergeQueueItem
		for i := range answer.Items {
			if answer.Items[i].ID == want {
				found = &answer.Items[i]
				break
			}
		}
		switch {
		case found == nil:
			// GONE IS NOT LANDED. Ask the row itself, which is the only thing
			// that knows whether it landed, was abandoned, or simply stopped
			// being readable to this caller.
			return queueWaitGone(client, base, bearer, want)
		case found.Red != nil:
			fmt.Printf("red %s at %s", want, shortSHA(found.Red.Tip))
			if found.Red.Note != "" {
				fmt.Printf(" - %s", firstLine(found.Red.Note))
			}
			fmt.Println()
			return errRowIsRed
		}
		if !time.Now().Before(give) {
			fmt.Println("still queued " + rowLine(*found))
			return errWaitedOut
		}
	}
}

// queueWaitGone says what happened to a row that is no longer in the queue.
//
// It reads the row rather than assuming, because three different things look
// identical from the queue's side: it landed, it was closed without landing, or
// its scope changed and this caller can no longer see it. I read a
// disappearance as a landing twice in one session and had to go back for the
// sha both times.
//
// WHAT LEAVING THE QUEUE ACTUALLY MEANS, measured rather than assumed, because
// my first version of this was built on a wrong model: /api/merge-queue filters
// on status, so a row leaves it when it is DONE. `abandon` does not remove a
// row - it clears the gate DECLARATION and leaves the request at todo, which is
// what its own branch was called (fix/abandon-clears-the-declaration). So the
// terminal state a waiter can see is done, and landed_tip is what tells a
// landing from a closure.
func queueWaitGone(client *http.Client, base, token, id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/artifact/"+id, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("%s left the queue and this seat cannot read it (%s)\n", id, resp.Status)
		return nil
	}
	var art struct {
		Status string            `json:"status"`
		Fields map[string]string `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&art); err != nil {
		return fmt.Errorf("the row did not decode: %w", err)
	}
	branch := art.Fields["branch"]
	switch {
	case art.Fields["landed_tip"] != "":
		fmt.Printf("landed %s as %s\n", branch, shortSHA(art.Fields["landed_tip"]))
	case art.Status == "abandoned":
		fmt.Printf("abandoned %s\n", branch)
	default:
		fmt.Printf("%s left the queue as %s, with no landed tip on it\n", branch, art.Status)
	}
	return nil
}

// waitOnQueue is one held-open read of the queue.
func waitOnQueue(
	ctx context.Context, client *http.Client, base, token, target, since string, window int,
) (mergeQueueAnswer, error) {
	path := fmt.Sprintf("%s/api/merge-queue/wait?window=%d", base, window)
	if strings.TrimSpace(target) != "" {
		path += "&target=" + target
	}
	if since != "" {
		path += "&since=" + since
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return mergeQueueAnswer{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return mergeQueueAnswer{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return mergeQueueAnswer{}, fmt.Errorf("the node answered %s", resp.Status)
	}
	var answer mergeQueueAnswer
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return mergeQueueAnswer{}, fmt.Errorf("the queue answer did not decode: %w", err)
	}
	return answer, nil
}
