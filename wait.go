package main

// `flowy wait` - block until a named thing has happened, once, in one place.
//
// THE RULING THIS IMPLEMENTS, from three seats counting their own transcripts
// on 2026-08-19/20 rather than remembering (row 01M0EVQCMY):
//
//	flowy-claude  591 of 6450 bash calls are waiting - 9% of everything typed,
//	              including one queue-watch loop written in 76 spellings
//	claude-host   the worktree/gate/file cycle 63 times
//	orchestrator  the gate-lock retry loop written 8 separate times
//
// The fleet has no wait verb a PERSON calls. `flowy inbox` blocks and nothing
// else does, so every seat writes the polling loop once per row, badly and
// differently. What the hand-written ones get wrong is never the same thing
// twice: a cap that abandons a gate after forty minutes and says nothing, an
// interval picked by feel rather than from how fast the watched thing moves,
// and - every single time - no word at all while it blocks, so a wait and a
// hang are indistinguishable until one of them never ends.
//
// THREE PROPERTIES, and they are the whole value:
//
//  1. IT SAYS WHAT IT IS WAITING FOR, on stderr, before it blocks and again
//     whenever what it can see changes. A waiter that prints nothing is
//     indistinguishable from a hung one, and the usual response to that is to
//     kill it and start again - which is how a caller ends up polling by hand
//     inside a tool written to stop them polling by hand.
//  2. ONE CAP, and when it expires it says how long it waited and what it last
//     saw. A silent exit is the failure that cost a gate pass: forty iterations
//     of sixty seconds, then nothing, and the next reader had no way to tell an
//     abandoned wait from a finished one.
//  3. IT USES THE DOORS THAT BLOCK where they exist. /api/merge-queue/wait
//     holds a request open; polling it on a timer would be the same defect one
//     layer up. Where no such door exists - the deploy case - it polls and SAYS
//     it is polling, because a caller that thinks it is being pushed to will
//     read a stale answer as a current one.
//
// WHAT IS NOT HERE, deliberately. `flowy queue wait --row` already does the row
// case and does it better than a second attempt would: four outcomes rather
// than two, with red separate from broken because a caller sanely retries a
// blinked node and would retry forever on a red. So --row DELEGATES to it
// rather than reimplementing it. Two spellings of one wait is exactly the shape
// this verb exists to remove, and adding one here while removing seventy-six
// elsewhere would be a poor trade.
//
// EXIT CODES are the ones `flowy queue wait` and `flowy inbox` already use,
// because a shell has to tell these apart without parsing anything:
//
//	0  the thing happened, and the line says what it was
//	1  the deadline passed quietly - not a failure, it is one of the two things
//	   a waiter is for
//	2  broken: the node did not answer, or the token was refused
//	3  a red verdict arrived (--row only, from `queue wait`)
//
// A NODE THAT NEVER ANSWERED IS 2 AND NOT 1, and the difference is what the
// caller learned. A quiet deadline means the question was asked and the answer
// was "not yet"; a node that was never there means it was never asked, and a
// script that treated those alike would report "the tip did not move" about a
// tip it never read. --deploy is the deliberate exception: waiting for a node
// to come back IS its subject, so a node that is down for the whole deadline is
// a quiet deadline there rather than a failure.

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

const waitUsage = `flowy wait - block until a named thing has happened

usage:
  flowy wait --row ID [--deadline 3600]
  flowy wait --tip [TARGET] [--sha SHA] [--deadline 3600]
  flowy wait --deploy [--sha SHA] [--deadline 900]

Exactly one subject. A wait with no subject is a sleep, and a wait with two is
a question about which one answered.

  --row       a merge row: block until it lands, leaves the queue, or goes red.
              Handed to ` + "`flowy queue wait`" + `, which is where that case lives.
  --tip       a landing target, "master" by default: block until its tip moves
              off what it is now, or reaches --sha if one is named.
  --deploy    block until the node is answering from a different build than the
              one it is answering from now, or from --sha if one is named.
  --sha       the specific commit to wait FOR, rather than any change. A prefix
              is enough - it is compared as one.
  --deadline  seconds before giving up. It then says how long it waited and what
              it last saw, rather than exiting quietly.

It says what it is waiting for on stderr while it blocks, so that a wait and a
hang are not the same picture.

exits 0 when it happened, 1 when the deadline passed quietly, 2 when something
broke, 3 on a red verdict.
`

// waitSaid prints the running commentary. stderr, because the ANSWER goes to
// stdout and a caller that captures the answer must not capture the narration -
// that is the same rule every verb in this binary keeps.
func waitSaid(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "flowy wait: "+format+"\n", args...)
}

func cliWait(args []string) error {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	row := fs.String("row", "", "a merge row to watch")
	tip := fs.String("tip", "", "a landing target to watch (\"master\" if given no value)")
	deploy := fs.Bool("deploy", false, "wait for the node to answer from a different build")
	sha := fs.String("sha", "", "the commit to wait for, rather than any change")
	// WHICH PROJECT'S TARGET, and it is passed on rather than left off.
	// Measured on the live node 2026-08-20: /api/merge-queue with no project
	// answered target_tip bbb3c16 while ?project=flowy and git both said
	// 8d611b8, four landings later. LandedTipOf (mergelock.go:395) tries the
	// caller's project and then "" - so a caller who states none reads the OLD
	// UNKEYED row, which nothing has written to since lands became project-keyed
	// and which is therefore stale forever rather than merely out of date.
	//
	// So this verb can be right today by being told, and the door being fixed
	// makes the flag unnecessary rather than wrong. Waiting on a value that
	// cannot move is the worst failure a waiter has: it is indistinguishable
	// from nothing happening.
	project := fs.String("project", "", "which project's target to watch, when the node holds more than one")
	deadline := fs.Int("deadline", 0, "seconds before giving up (default 3600, 900 for --deploy)")
	urlFlag := fs.String("url", "", "node to talk to (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token := fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)")
	agent := fs.String("agent", "", agentFlagHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}

	// EXACTLY ONE SUBJECT, counted rather than checked in an if-chain, so that
	// adding a fourth subject cannot quietly leave the rule behind. Two subjects
	// is refused rather than resolved in some order: "which of these did it
	// answer about" is a question the caller should not have to ask afterwards.
	subjects := 0
	for _, on := range []bool{strings.TrimSpace(*row) != "", *tip != "", *deploy} {
		if on {
			subjects++
		}
	}
	switch {
	case subjects == 0:
		return errors.New("a wait with no subject is a sleep: pass --row, --tip or --deploy\n\n" + waitUsage)
	case subjects > 1:
		return errors.New("a wait with two subjects cannot say which one answered: pass one of " +
			"--row, --tip or --deploy\n\n" + waitUsage)
	}

	if strings.TrimSpace(*row) != "" {
		if strings.TrimSpace(*sha) != "" {
			return errors.New("--sha is a commit and --row is a row: `flowy queue wait --row` " +
				"answers with the sha it landed as\n\n" + waitUsage)
		}
		// THE WHOLE ROW CASE, handed over unchanged. Its flags are its own, so
		// what this verb was given is passed through rather than re-derived -
		// a second parse of the same arguments is a second set of defaults.
		return queueWaitCmd(args)
	}

	base := resolveURL(*urlFlag, os.Getenv("FLOWY_ADDR"))
	bearer, err := resolveToken(*token, os.Getenv("FLOWY_TOKEN"), *agent, os.Getenv("FLOWY_AGENT"))
	if err != nil {
		return err
	}
	if bearer == "" {
		return errNoToken()
	}

	want := strings.TrimSpace(*sha)
	if *deploy {
		return waitForDeploy(base, bearer, want, secondsOr(*deadline, 900))
	}
	target := strings.TrimSpace(*tip)
	if target == "" || target == "true" {
		target = "master"
	}
	return waitForTip(base, bearer, target, strings.TrimSpace(*project), want, secondsOr(*deadline, 3600))
}

// secondsOr is the deadline the caller asked for, or this subject's own default.
//
// A flag default cannot do this: the two subjects want different numbers - an
// hour for a gate pass, fifteen minutes for a deploy - and one shared default
// would be wrong for whichever it was not chosen for. 0 means "not stated"
// rather than "wait for no time", because a deadline of zero is a verb that
// answers before it has looked.
func secondsOr(asked, fallback int) int {
	if asked > 0 {
		return asked
	}
	return fallback
}

// sameCommit compares a wanted sha against one the node reported, either way
// round, because one of them is usually short and which one varies.
//
// It is a prefix comparison and it is deliberately not a validation: a caller
// who passes eight characters gets the answer they meant, and a caller who
// passes something that is not a sha at all waits out their deadline and is
// told what it actually saw, which is a better failure than a refusal that
// guesses at what a commit looks like.
func sameCommit(want, got string) bool {
	want, got = strings.TrimSpace(want), strings.TrimSpace(got)
	if want == "" || got == "" {
		return false
	}
	return strings.HasPrefix(got, want) || strings.HasPrefix(want, got)
}

// waitForTip blocks until a landing target moves.
//
// ON THE WAIT DOOR, not on a timer. /api/merge-queue/wait holds the request
// open and answers when the queue or the target changes, so this learns of a
// landing within a second of it happening rather than within an interval
// somebody picked. That door is also what makes the "says something while it
// waits" property cheap: every return is a real change, so a line per return is
// a line per event rather than a line per tick.
func waitForTip(base, token, target, project, want string, deadline int) error {
	const window = 60
	client := &http.Client{Timeout: time.Duration(window+15) * time.Second}
	started := time.Now()
	give := started.Add(time.Duration(deadline) * time.Second)

	// THE OPENING READ IS NOT SPECIAL, and it must not be the one place this
	// verb still dies during a restart: a caller who runs `flowy wait --tip`
	// the instant after a land is running it at the exact moment the node goes
	// away to deploy that land.
	var first mergeQueueAnswer
	for {
		var err error
		first, err = queueLook(client, base, token, target, project, "", window, give)
		if err == nil {
			break
		}
		if !waitOutRestart(err, give, target) {
			return err
		}
	}
	was := strings.TrimSpace(first.TargetTip)
	if want != "" && sameCommit(want, was) {
		// ALREADY TRUE IS A REAL ANSWER, and answering it immediately is what
		// makes this safe to put in a script before the thing that would move
		// the tip. A waiter that blocks on a condition already met is a race
		// the caller cannot see and cannot fix.
		fmt.Printf("%s is %s\n", target, shortSHA(was))
		return nil
	}
	if want != "" {
		waitSaid("%s is at %s, waiting for %s, up to %s", target, shortSHA(was), want, took(deadline))
	} else {
		waitSaid("%s is at %s, waiting for it to move, up to %s", target, shortSHA(was), took(deadline))
	}

	cursor := first.Cursor
	for {
		// THE WINDOW IS CAPPED BY WHAT IS LEFT OF THE DEADLINE, and this is not
		// a nicety. The deadline used to be checked only BETWEEN blocking reads,
		// so a --deadline of 12 seconds returned after 25: the door held the
		// request open for its own full window and the check could not run until
		// it came back. A cap that overshoots by up to a minute is a cap the
		// caller cannot use to bound anything, which is most of why it exists.
		left := int(time.Until(give).Seconds())
		if left <= 0 {
			waitSaid("gave up after %s - %s is still %s", took(int(time.Since(started).Seconds())),
				target, shortSHA(was))
			return errWaitedOut
		}
		hold := window
		if left < hold {
			hold = left
		}
		answer, err := queueLook(client, base, token, target, project, cursor, hold, give)
		if err != nil {
			// A DEPLOY IS THE EXPECTED INTERRUPTION HERE, for the reason
			// written out in queuewait.go: what this is waiting for is a
			// landing, and a landing is followed by the node restarting. The
			// caller's deadline is the only cap - doThroughARestart's twenty
			// seconds is sized for a one-shot call, not for an hour-long wait.
			if !waitOutRestart(err, give, target) {
				return err
			}
			continue
		}
		cursor = answer.Cursor
		now := strings.TrimSpace(answer.TargetTip)
		if now == was || now == "" {
			continue
		}
		// It moved. Whether that is the answer depends on whether a particular
		// commit was named - a tip that moved to somebody else's landing is a
		// change and is not what a caller waiting for their own sha asked for.
		if want == "" || sameCommit(want, now) {
			fmt.Printf("%s moved %s -> %s\n", target, shortSHA(was), shortSHA(now))
			return nil
		}
		waitSaid("%s moved to %s, which is not %s - still waiting", target, shortSHA(now), want)
		was = now
	}
}

// queueLook is one blocking read of the queue, with the restart tolerance the
// row waiter already has: a long call is a call a deploy lands inside of.
// queueLook is one blocking read, bounded by the CALLER'S deadline as well as
// by the window.
//
// give is passed in rather than left to the window, because the request is not
// the only thing that can outlast a short wait: doThroughARestart rides out a
// refused dial for its own twenty seconds before it returns anything at all, so
// `--deadline 8` against a node that is not there took twenty. Measured. The
// context is what stops it - the retry loop selects on ctx.Done() - so bounding
// the context bounds every layer under it at once, which is the only way a cap
// chosen once stays the only cap.
func queueLook(
	client *http.Client, base, token, target, project, since string, window int, give time.Time,
) (mergeQueueAnswer, error) {
	hold := time.Duration(window+10) * time.Second
	if left := time.Until(give); left < hold {
		hold = left
	}
	if hold <= 0 {
		hold = time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), hold)
	defer cancel()
	return waitOnQueue(ctx, client, base, token, target, project, since, window)
}

// waitForDeploy blocks until the node answers from a different build.
//
// THIS ONE POLLS, AND SAYS SO. There is no door that holds a request open until
// the binary changes, and there cannot usefully be one: the thing a caller is
// waiting for here is the old process going away, so the answer arrives on a
// connection the old process cannot serve. Polling is the honest mechanism, and
// naming it in the first line is the point - a caller who believes they are
// being pushed to reads a stale answer as a current one.
//
// The interval is derived from what is being watched rather than picked: a
// deploy is a build, a restart and a health check, which is tens of seconds at
// best, so asking every three is already asking faster than the thing can move.
func waitForDeploy(base, token, want string, deadline int) error {
	const every = 3 * time.Second
	client := &http.Client{Timeout: 10 * time.Second}
	started := time.Now()
	give := started.Add(time.Duration(deadline) * time.Second)

	// A FIRST READ THAT FAILS IS NOT A FAILURE OF THE WAIT. Measured against the
	// live node while writing this: started during a real deploy, the opening
	// healthz was refused and the verb exited 2 - at the exact moment somebody
	// runs it. "Wait for the node to come back" is the whole use, and a waiter
	// that requires the node to be up before it will wait for the node to be up
	// is a waiter for the case nobody has.
	//
	// So a failed first read means the baseline is UNKNOWN rather than absent,
	// and the answer is then the first build that answers at all - which is
	// exactly what a caller who started mid-restart meant.
	was, err := healthOf(client, base, token)
	known := err == nil
	switch {
	case !known:
		waitSaid("%s is not answering, so there is no build to compare against - "+
			"waiting for whatever comes back, polling every %s, up to %s",
			hostOf(base), every, took(deadline))
	case want != "" && sameCommit(want, buildOf(was.Version)):
		fmt.Printf("%s is running %s\n", was.Node, was.Version)
		return nil
	case want != "":
		waitSaid("%s is running %s, polling every %s for %s, up to %s",
			was.Node, was.Version, every, want, took(deadline))
	default:
		waitSaid("%s is running %s, polling every %s for a different build, up to %s",
			was.Node, was.Version, every, took(deadline))
	}

	// A NODE THAT HAS GONE AWAY IS THE EXPECTED MIDDLE OF THIS WAIT, not a
	// failure of it: between the old process stopping and the new one answering
	// there is a window where nothing is listening, and that window is precisely
	// what a deploy is. So a read that fails is reported once and waited out,
	// and only the DEADLINE ends this - which is the same rule the escape hatch
	// keeps, for the same reason.
	down := !known
	for {
		time.Sleep(every)
		if !time.Now().Before(give) {
			waitSaid("gave up after %s - last saw %s", took(int(time.Since(started).Seconds())),
				lastSeen(was, known, down))
			return errWaitedOut
		}
		now, err := healthOf(client, base, token)
		if err != nil {
			if !down {
				waitSaid("the node stopped answering, which is what a restart looks like - waiting")
				down = true
			}
			continue
		}
		if down {
			waitSaid("the node is answering again, on %s", now.Version)
			down = false
		}
		// WITH NO BASELINE, the first build that answers is the answer. With
		// one, only a DIFFERENT build is.
		if known && now.Version == was.Version {
			continue
		}
		if want == "" || sameCommit(want, buildOf(now.Version)) {
			if known {
				fmt.Printf("%s is running %s (was %s)\n", now.Node, now.Version, was.Version)
			} else {
				fmt.Printf("%s is running %s\n", now.Node, now.Version)
			}
			return nil
		}
		waitSaid("%s is running %s, which is not %s - still waiting", now.Node, now.Version, want)
		was, known = now, true
	}
}

// lastSeen is what to say about a wait that expired: the build, or the fact
// that nothing was answering at the end. "last saw 0.8.0+abc" and "last saw
// nothing answering" send a reader to different places.
func lastSeen(was healthzResponse, known, down bool) string {
	if down || !known {
		return "nothing answering"
	}
	return was.Version
}

// hostOf is the node as a person names it, for the lines printed before there
// is an answer to read a node name out of.
func hostOf(base string) string {
	return strings.TrimPrefix(strings.TrimPrefix(strings.TrimRight(base, "/"), "http://"), "https://")
}

// buildOf is the commit out of a version string like "0.8.0+e6f1121".
//
// A build with no + is its own answer rather than an error: a binary built
// outside the release path reports "0.8.0+src", and a --sha wait against that
// should simply never match rather than refuse to run.
func buildOf(version string) string {
	if _, build, ok := strings.Cut(version, "+"); ok {
		return build
	}
	return version
}

// healthOf reads /healthz. It is an open door and takes no token, but one is
// sent when there is one so that a node which starts requiring it does not
// break this verb silently.
func healthOf(client *http.Client, base, token string) (healthzResponse, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/healthz", nil)
	if err != nil {
		return healthzResponse{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return healthzResponse{}, err
	}
	defer resp.Body.Close()
	var answer healthzResponse
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return healthzResponse{}, err
	}
	// A 503 with a body is a node that is up and cannot reach its store, which
	// is a real reading and not a failure to read: it still says which build is
	// running, which is the only thing this verb is asking.
	if answer.Version == "" {
		return healthzResponse{}, fmt.Errorf("the node answered %s with no version", resp.Status)
	}
	return answer, nil
}

// took is a duration a person reads, from seconds. Minutes past ninety seconds,
// because "3600s" is a number somebody has to convert and "60m" is not.
func took(seconds int) string {
	if seconds < 90 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm", (seconds+30)/60)
}
