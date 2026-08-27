package flowy

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
  flowy queue wait --row ID [--tip SHA] [--deadline 3600]

  --row       the merge row to watch. Required - a wait with no subject is a
              sleep.
  --tip       the branch tip you are waiting for a verdict on. A red measured at
              any other tip is not the answer and the wait carries on - which is
              what you want after re-tipping a red row, because the node cannot
              see that the branch moved.
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
	// THE TIP THE CALLER IS WAITING FOR, and only the caller knows it.
	//
	// The node holds red_tip (where a verdict was measured), gated_tip (the same
	// for a green) and gated_base (where the target was) - and NOTHING that says
	// where the branch points now, because nothing on the node ever reads a
	// branch. Measured on 2026-08-20: a row re-tipped from fa0e9ea to aa30f5f is
	// byte-identical on the node to one nobody touched.
	//
	// So a caller who has just fixed a red and wants the NEW verdict states the
	// sha they are waiting for, and a red measured at any other tip is not the
	// answer. `--tip $(git rev-parse --short HEAD)` is not a convenience: it is
	// the only party in the exchange that has a git repository.
	tip := fs.String("tip", "", "the branch tip you are waiting for a verdict on")
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
	// Said once rather than on every return, so a wait across a long gate does
	// not fill a terminal with the same sentence.
	saidStale := false
	// The row as it was last seen, so a wait that ends on its own cap can still
	// say what it was watching rather than only that it stopped.
	var last mergeQueueItem
	last.ID = want
	// The target tip as of the last answer, kept beside the row for the same
	// reason the row is kept: rowLine needs it to tell a live red from one
	// measured off a base the target has left, and the line that reports a
	// timed-out wait is printed after the loop has stopped reading.
	lastTip := ""
	// Whether this wait has ever had an answer from the node. A read cancelled
	// by our own cap is the quiet deadline only once we have been TALKING to
	// the node: before that, a cancellation means the question was never asked,
	// and exit 1 would promise a caller that it was.
	answered := false
	for {
		// BOUNDED BY THE CALLER'S DEADLINE AS WELL AS BY THE WINDOW. The
		// request is not the only thing that can outlast a short wait:
		// doThroughARestart rides out a refused dial for its own twenty seconds
		// before returning anything, so a --deadline shorter than that used to
		// be overrun by it. The retry loop selects on ctx.Done(), so bounding
		// the context bounds every layer under it - which is what keeps the cap
		// the caller chose the only one.
		hold := time.Duration(window+10) * time.Second
		if left := time.Until(give); left < hold {
			hold = left
		}
		if hold <= 0 {
			hold = time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), hold)
		answer, err := waitOnQueue(ctx, client, base, bearer, *target, "", cursor, window)
		cancel()
		if err != nil && answered && spentDeadline(err, give) {
			// MY OWN CAP CANCELLING MY OWN REQUEST IS THE QUIET DEADLINE, not a
			// broken node. Bounding the context by the caller's remaining time
			// - which is what stops the dial retry overrunning a short wait -
			// means the LAST request of every wait is cancelled by this verb
			// itself, and it surfaced as "context deadline exceeded" with exit
			// 2. Measured on the deployed binary: `--deadline 12` on a queued
			// row answered 2 in twelve seconds, where the whole point of 1 is
			// that a wait which found nothing is not a failure. A script that
			// retries on 2 would have retried a perfectly ordinary quiet
			// deadline forever.
			fmt.Println("still queued " + rowLine(last, lastTip))
			return errWaitedOut
		}
		if err != nil {
			// A DEPLOY IS NOT A FAILURE OF THE WAIT, and for this verb it is
			// the single most likely interruption there is: landing is what it
			// is waiting FOR, so the moment its subject happens is the moment
			// the node goes away.
			//
			// doThroughARestart already rides out a refused dial, but only for
			// restartWindow - twenty seconds, sized in 2e2e13e for a ONE-SHOT
			// call that must not hang forever on a dead host. A deploy takes
			// longer than that, so the retry did its job and then correctly
			// gave up on a wait that had fifty-nine minutes left.
			//
			// MEASURED TWICE on 2026-08-20, by two seats independently: this
			// verb exited 2 mid-deploy while, through the same restart on the
			// same binary, `flowy wait --deploy` rode it out - because that one
			// polls against its OWN deadline and never goes near the constant.
			// One verb, two waits, one deploy, opposite outcomes.
			//
			// So the caller's deadline is the only cap. Lengthening
			// restartWindow would fix this by making every one-shot verb hang
			// longer on a genuinely dead host, which is the trade it was
			// deliberately not making.
			if !waitOutRestart(err, give, "the queue") {
				return err
			}
			continue
		}
		answered = true
		cursor = answer.Cursor

		// THE ROW'S OWN STATE, ASKED OF THE ANSWER WE JUST GOT rather than of a
		// second read. Two reads a second apart is how a waiter reports a row
		// that landed between them as having vanished.
		found, err := rowInQueue(answer.Items, want)
		if err != nil {
			return err
		}
		if found != nil {
			last = *found
		}
		lastTip = answer.TargetTip
		switch {
		case found == nil:
			// GONE IS NOT LANDED. Ask the row itself, which is the only thing
			// that knows whether it landed, was abandoned, or simply stopped
			// being readable to this caller.
			//
			// ASKED WITH THE ID THIS WAIT RESOLVED, not with what the caller
			// typed. rowInQueue accepts a prefix - `--row 01M0FCNZJ2` finds the
			// row - but GET /api/artifact takes the whole id and 404s on a
			// prefix. So a short-id wait watched its row land and then reported
			// "not in the queue and this seat cannot read it", exit 2, about a
			// row that had just landed 706/0. Measured on my own row, twenty
			// minutes after landing the prefix match: the resolution existed
			// and was not carried one line further.
			//
			// last.ID is `want` until the row is seen and the full id after, so
			// a wait that never saw its row still asks with what it was given -
			// and the answer already says it cannot tell.
			return queueWaitGone(client, base, bearer, last.ID)
		case found.Red != nil && staleRed(*tip, found.Red.Tip):
			// A RED ABOUT A TREE THE CALLER HAS ALREADY REPLACED, said once and
			// then waited past. Measured as the cost of not doing this: a
			// re-tipped row answered exit 3 in under a second, about the verdict
			// on the tree its fix replaced - and the row-waiter was therefore
			// unusable on the single most likely row to be waiting on, which is
			// a red one you have just fixed.
			if !saidStale {
				fmt.Fprintf(os.Stderr,
					"a red at %s, which is not the %s you are waiting for - still waiting\n",
					shortSHA(found.Red.Tip), *tip)
				saidStale = true
			}
		case found.Red != nil:
			fmt.Printf("red %s at %s", want, shortSHA(found.Red.Tip))
			if found.Red.Note != "" {
				// The wider budget, because this line carries one row rather
				// than a listing: there is no id and branch column beside it
				// spending the width. Still bounded and still marked - a note
				// can be a whole run's output.
				fmt.Printf(" - %s", elide(found.Red.Note, reasonWrapWidth))
			}
			fmt.Println()
			return errRowIsRed
		}
		if !time.Now().Before(give) {
			fmt.Println("still queued " + rowLine(*found, answer.TargetTip))
			return errWaitedOut
		}
	}
}

// waitOutRestart decides whether a failed read is worth carrying on from, and
// says so on the way past.
//
// True means the node was not there and the caller still has time: a waiter
// should keep waiting. False means either the deadline is spent or this is not
// a node that went away, and the caller should report it.
//
// IT SLEEPS, so the caller's loop cannot spin against a refused dial. A second
// is the same beat doThroughARestart uses, for the same reason: a restart is
// tens of seconds and asking faster measures nothing.
//
// The `what` is what the line calls the thing being waited on, because "the
// node is not answering" says nothing about which of a script's waits printed
// it.
func waitOutRestart(err error, give time.Time, what string) bool {
	if !isDialRefused(err) || !time.Now().Before(give) {
		return false
	}
	fmt.Fprintf(os.Stderr, "the node is not answering - a restart is the usual reason, "+
		"and %s is still worth waiting for (%s left)\n", what, took(int(time.Until(give).Seconds())))
	time.Sleep(time.Second)
	return true
}

// rowInQueue finds the row a caller named, accepting the SHORT ID everybody
// actually writes.
//
// The match was `Items[i].ID == want`, exact - so `--row 01M0F7DJDM` never
// matched anything, fell through to the gone path, and reported "left the queue
// and this seat cannot read it" WITH EXIT 0, about a row that was sitting in
// the queue gating at that moment. Measured 2026-08-20 by two seats. A confident
// wrong answer under the success code is the worst shape available: a script
// reads 0 and carries on as though the branch had landed.
//
// Short ids are what this fleet writes. Every row reference in the room, in
// every commit message, and in the queue's own output is truncated - so an exact
// match is a rule the tool's own vocabulary breaks.
//
// AMBIGUITY IS REFUSED RATHER THAN RESOLVED. Two rows sharing a prefix is
// exactly when guessing is worst, and the refusal names both so the caller can
// lengthen the id rather than wonder which one it took.
func rowInQueue(items []mergeQueueItem, want string) (*mergeQueueItem, error) {
	var hit *mergeQueueItem
	var also []string
	for i := range items {
		if !strings.HasPrefix(items[i].ID, want) {
			continue
		}
		if items[i].ID == want {
			return &items[i], nil
		}
		if hit != nil {
			also = append(also, items[i].ID)
			continue
		}
		hit = &items[i]
	}
	if len(also) > 0 {
		return nil, fmt.Errorf("%q names %d rows in the queue - %s and %s: use more of the id",
			want, len(also)+1, hit.ID, strings.Join(also, ", "))
	}
	return hit, nil
}

// spentDeadline reports whether a failed read failed because the caller's own
// clock ran out.
//
// The context this verb builds is bounded by the remaining deadline, so the last
// request of every wait is cancelled by the wait itself. That cancellation is
// the QUIET DEADLINE - one of the two things a waiter is for - and reporting it
// as a broken node turns an ordinary outcome into a retryable error.
//
// It asks the clock as well as the error: a context that was cancelled with time
// still on it is something else's doing and belongs to the caller.
func spentDeadline(err error, give time.Time) bool {
	if err == nil {
		return false
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return false
	}
	return !time.Now().Before(give.Add(-time.Second))
}

// staleRed reports whether a red is about a tree the caller is not waiting for.
//
// With no --tip nothing is stale: a caller who states no tip is asking about the
// row as it stands, and the red IS the answer to that question. The flag is what
// turns "is this row red" into "is the tree I just pushed red", and those are
// different questions that used to share an exit code.
//
// The comparison is a prefix either way round, because one of the two is
// usually short and which one varies.
func staleRed(want, measured string) bool {
	want, measured = strings.TrimSpace(want), strings.TrimSpace(measured)
	if want == "" || measured == "" {
		return false
	}
	return !strings.HasPrefix(measured, want) && !strings.HasPrefix(want, measured)
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
		// UNREADABLE IS NOT LANDED, and it used to exit 0 saying so. A row this
		// seat cannot read might have landed, might have been closed, might
		// never have existed - and the one thing exit 0 means to a caller is
		// "the thing you were waiting for happened". This is the only outcome
		// here the verb does not actually know, so it is the one that goes back
		// as broken.
		return fmt.Errorf("%s is not in the queue and this seat cannot read it (%s): "+
			"it may have landed, been closed, or never existed - and this verb cannot tell",
			id, resp.Status)
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
	ctx context.Context, client *http.Client, base, token, target, project, since string, window int,
) (mergeQueueAnswer, error) {
	path := fmt.Sprintf("%s/api/merge-queue/wait?window=%d", base, window)
	if strings.TrimSpace(target) != "" {
		path += "&target=" + target
	}
	// STATED WHEN THE CALLER STATED IT, and left off otherwise so this verb
	// behaves exactly as it did. The door reads the landed tip per project and
	// falls back to an unkeyed row that nothing writes any more, so a caller who
	// can name their project gets an answer that moves. See wait.go's --project.
	if strings.TrimSpace(project) != "" {
		path += "&project=" + project
	}
	if since != "" {
		path += "&since=" + since
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return mergeQueueAnswer{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// THROUGH A RESTART, which is the one thing this verb hits more than any
	// other: it is a long blocking call by design, so a deploy inside its window
	// is not unlucky, it is expected. I learned that from my own first real use -
	// waiting on a row, the node deployed, and the verb exited 2 on "connection
	// refused" while the row it was watching landed fine.
	//
	// 2e2e13e made six verbs wait a refused dial out; this one wrote its own
	// request and so did not get it. A refused connection means nothing was sent,
	// which is what makes retrying it safe.
	resp, err := doThroughARestart(ctx, client, req, nil)
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
