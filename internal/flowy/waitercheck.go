package flowy

// `flowy waiter check --as NAME` - is that seat still hearing the room, and if
// not, WHICH FACT SAYS SO.
//
// COUNTED, from one operator's own scrollback on 2026-08-19/20: they retyped a
// thirty-line prompt roughly every fifteen minutes, for a day, to answer one
// question. The prompt carried the whole procedure - the python one-liner
// against /api/presence, which fields to read, that "unknown" is not deafness,
// that "forked" is, how stale is too stale, and a warning not to touch the
// other four seats' waiters. That is a program somebody was executing by hand,
// and this is that program.
//
// IT SAYS WHICH CLAUSE DECIDED, always, healthy or not. A verb that answered
// only "healthy" would be the readers pane that said "polling 4s ago" for
// twenty-eight minutes while the session behind it was deaf: true, checkable,
// and not an answer to the question being asked.
//
// IT DOES NOT KILL ANYTHING. It prints the pid the row's claim names and stops.
// The repair this replaces was `pkill -9 -f 'flowy inbox --as NAME'`, which
// killed the shell that ran it twice in one night because the pattern matched
// the process evaluating the pattern; a repair verb that picked its own target
// would be that failure with better manners. Naming the process and letting a
// person or a prompt act is the whole difference between a pid and a pattern.
//
// THE EXIT CODES ARE THE ANSWER, so a nag can branch without reading English:
//
//	0  healthy - a row exists and its last poll is fresh. NOT "a poll is in
//	   flight": a waiter loop polls, returns and polls again, so between-polls
//	   is the normal state of a healthy seat for part of every cycle
//	1  broken  - one of the clauses below fired, and it is named on stdout
//	2  could not ask - no node, no token, a refusal. NOT the same as broken:
//	   a seat whose health is unknown must not be restarted on that basis, and
//	   folding this into 1 would have a blinking node restart the whole fleet
//
// UNKNOWN IS NOT DEAF. MEASURED on 2026-08-17: waiter_kind reads "unknown" for
// a monitor-run listener that is demonstrably delivering - two seats read
// unknown while receiving messages in real time, because the node cannot
// classify a reader that never says what it is. Treating that as broken
// restarted a healthy listener. Only "forked" is the deaf signature: it holds
// the reader, consumes, advances the cursor and wakes nobody.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

const waiterUsage = `flowy waiter check - is this seat still hearing the room

usage:
  flowy waiter check --as NAME [--stale 10m]

  --as NAME     the reader label to ask about. Required: this verb answers
                about one seat, and a default would answer about a seat the
                caller did not name
  --stale D     how old a poll may be before the seat counts as broken
                (default 10m). A waiter holds a long poll, so a fresh row means
                a process is in there right now
  --url URL     node to ask (default $FLOWY_ADDR, then http://127.0.0.1:8787)
  --token T     bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)
  --agent NAME  the seat asking, whose token is ~/.config/flowy/agents/NAME

  Prints one line naming the clause that decided, and the process the waiter
  claims, so a repair can kill a number instead of matching a pattern.

  Exit 0 healthy, 1 broken, 2 could not ask. "Could not ask" is deliberately
  not "broken": a seat whose health is unknown must not be restarted on that
  basis.
`

// errWaiterBroken is the seat being deaf, as distinct from this verb being
// unable to find out - which is exit 2 and every other error here.
var errWaiterBroken = errors.New("the waiter is broken")

// waiterCmd is `flowy waiter ...`.
func waiterCmd(args []string) error {
	if len(args) == 0 {
		fmt.Print(waiterUsage)
		return errors.New("say what to do: check")
	}
	switch args[0] {
	case "check":
		return waiterCheckCmd(args[1:])
	case "help", "--help", "-h":
		fmt.Print(waiterUsage)
		return nil
	default:
		fmt.Print(waiterUsage)
		return fmt.Errorf("unknown waiter command %q", args[0])
	}
}

func waiterCheckCmd(args []string) error {
	fs := flag.NewFlagSet("waiter check", flag.ContinueOnError)
	as := fs.String("as", "", "the reader label to ask about")
	stale := fs.Duration("stale", 10*time.Minute, "how old a poll may be before the seat counts as broken")
	urlFlag := fs.String("url", "", "node to ask (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token := fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)")
	agent := fs.String("agent", "", agentFlagHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := strings.TrimSpace(*as)
	if name == "" {
		return errors.New("--as NAME: this verb answers about one seat\n\n" + waiterUsage)
	}
	if *stale <= 0 {
		return errors.New("--stale must be positive: a window of zero calls every seat broken")
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
	client := &http.Client{Timeout: 30 * time.Second}

	var answer struct {
		Listeners []*store.PresenceRow `json:"listeners"`
	}
	if err := peerRequest(ctx, client, http.MethodGet, base+"/api/presence", bearer, nil, &answer); err != nil {
		return err
	}

	verdict, ok, clause := judgeWaiter(answer.Listeners, name, *stale, time.Now())

	// A FORKED READING IS RE-SAMPLED BEFORE IT IS BELIEVED.
	//
	// MEASURED by another seat on 2026-08-20: one read said pid 1731273, kind
	// forked; three reads seconds later said pid 1732292, kind tracked. It had
	// caught the handover mid-flight - a delivery forks a successor, and that
	// successor holds the claim for the moment before the loop re-arms a
	// tracked waiter. A single sample would have restarted a seat that was
	// working, which is the same class of mistake as reading "unknown" as deaf.
	//
	// Only this clause is re-sampled. Stale is a statement about a timestamp
	// and a second look cannot change it; a missing row cannot appear in two
	// seconds; and re-reading a healthy seat would only add a way to fail.
	if clause == "forked" {
		select {
		case <-ctx.Done():
		case <-time.After(forkedSettle):
		}
		var again struct {
			Listeners []*store.PresenceRow `json:"listeners"`
		}
		if err := peerRequest(ctx, client, http.MethodGet, base+"/api/presence", bearer, nil, &again); err != nil {
			return err
		}
		second, secondOK, secondClause := judgeWaiter(again.Listeners, name, *stale, time.Now())
		if secondClause != "forked" {
			// Say both readings. A seat that read forked a moment ago and
			// reads otherwise now is a handover, and hiding the first reading
			// would make this line disagree with whatever the caller saw.
			fmt.Printf("%s (read forked %s earlier - a delivery's successor held the reader while the loop re-armed)\n",
				second, forkedSettle)
			if !secondOK {
				return errWaiterBroken
			}
			return nil
		}
		verdict = second + fmt.Sprintf(" - still forked %s later, so it is not a handover", forkedSettle)
		ok = secondOK
	}

	fmt.Println(verdict)
	if !ok {
		return errWaiterBroken
	}
	return nil
}

// forkedSettle is how long a forked reading is given to turn out to be a
// handover. Long enough to outlast the gap between a successor being forked and
// the loop re-arming - measured at well under a second on this fleet - and
// short enough that a genuinely deaf seat is reported promptly.
const forkedSettle = 2 * time.Second

// judgeWaiter is the whole procedure, separated from the fetching so it can be
// tested against rows rather than against a live node - which is the half that
// was being executed by hand, and the half that has to be right.
//
// THE CLAUSES ARE ORDERED BY WHAT A READER WOULD DO ABOUT THEM. A missing row
// means nothing was ever armed; a detached row means whatever was armed has
// left; a forked one is present and deaf; a stale one is present and gone
// quiet. They are four different repairs, so they are four different sentences.
func judgeWaiter(rows []*store.PresenceRow, name string, stale time.Duration, now time.Time) (line string, ok bool, clause string) {
	var row *store.PresenceRow
	for _, r := range rows {
		if r != nil && r.Reader == name {
			// The newest poll wins when a label is worn twice. Two rows under
			// one name is a doubled waiter and it is said out loud below
			// rather than tidied away here: they share a cursor, so each hears
			// part of the room while both look healthy.
			if row == nil || newerPoll(r, row) {
				row = r
			}
		}
	}
	if row == nil {
		return fmt.Sprintf("broken %s: no reader row - nothing has ever polled under that name", name), false, "no-row"
	}

	doubled := 0
	for _, r := range rows {
		if r != nil && r.Reader == name {
			doubled++
		}
	}
	// A doubled waiter is not by itself broken - both rows may be polling - but
	// it is never what somebody meant to arm, and it is invisible from every
	// other surface.
	extra := ""
	if doubled > 1 {
		extra = fmt.Sprintf(", and %d rows wear this name - they share a cursor, so each hears part of the room", doubled)
	}

	where := waiterWhere(row)
	switch {
	case row.Kind == store.WaiterForked:
		// The one kind that is deafness. It polls, it is attached, its last
		// poll is seconds old, and every one of those is true while nothing it
		// hears reaches anybody.
		return fmt.Sprintf("broken %s: kind forked - it holds the reader, advances the cursor and wakes nobody%s%s",
			name, where, extra), false, "forked"
	case row.LastPoll == nil:
		// Declared and never polled. Something armed a reader here and nothing
		// has ever called the inbox under it - which is a different repair from
		// a seat that polled and stopped, and reads identically from every
		// other surface.
		return fmt.Sprintf("broken %s: declared and never polled - state %s%s",
			name, orUnknown(row.State), where), false, "never-polled"
	case now.Sub(*row.LastPoll) > stale:
		return fmt.Sprintf("broken %s: last poll %s ago, older than %s%s%s",
			name, roundedAge(now.Sub(*row.LastPoll)), stale, where, extra), false, "stale"
	}

	// Healthy, and it says why - the same facts it was decided on, so a person
	// reading a green line can check it rather than trust it. The kind is
	// printed even when it is "unknown", because unknown is what a monitor-run
	// waiter reads as while delivering, and a line that hid it would invite the
	// restart that reading cost once already.
	//
	// FRESHNESS DECIDES, NOT ATTACHMENT, and this is the correction the gate
	// made to the first cut. A waiter loop polls, returns, and polls again -
	// claude-host's sleeps three seconds between cycles - so "no poll in
	// flight" is the normal state of a healthy seat for part of every cycle,
	// and a rule that called it broken would restart a seat for breathing. The
	// in-flight fact is still worth printing: a fresh row with no poll in it is
	// a seat between polls, and a fresh row with one is a seat inside a wait.
	holding := "between polls"
	if row.Attached {
		holding = "poll in flight"
	}
	return fmt.Sprintf("healthy %s: polled %s ago, %s, kind %s%s%s",
		name, roundedAge(now.Sub(*row.LastPoll)), holding, orUnknown(row.Kind), where, extra), true, "healthy"
}

// waiterWhere names the process the waiter claims, or says plainly that it
// claimed none.
//
// NOT SAID IS SAID. A waiter that predates the claim and one whose claim was
// incomplete are the same fact - this one cannot be named - and the repair for
// it is the old one: find it by hand, knowing a pattern matches the searcher.
// Printing nothing at all would read as "no repair needed".
func waiterWhere(row *store.PresenceRow) string {
	if !row.Process.Complete() {
		return ", process unnamed (nothing to kill by number)"
	}
	return fmt.Sprintf(", pid %d on %s since %s",
		row.Process.Pid, row.Process.Host, row.Process.Since.UTC().Format(time.RFC3339))
}

// newerPoll reports whether a has polled more recently than b. A row that has
// never polled loses to one that has, and two that never polled tie.
func newerPoll(a, b *store.PresenceRow) bool {
	if a.LastPoll == nil {
		return false
	}
	if b.LastPoll == nil {
		return true
	}
	return a.LastPoll.After(*b.LastPoll)
}

// roundedAge is an age a person reads, in the units they would have used.
func roundedAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return d.Round(time.Second).String()
	case d < time.Hour:
		return d.Round(time.Minute).String()
	default:
		return d.Round(time.Minute).String()
	}
}

// agoOrNever dates a poll, or says there has never been one - which is a
// different fact from a poll long ago and sends somebody to a different place.
func agoOrNever(at *time.Time, now time.Time) string {
	if at == nil {
		return "never"
	}
	return roundedAge(now.Sub(*at)) + " ago"
}

// orUnknown keeps an empty field from printing as a gap. A blank where a kind
// or a state goes reads as a rendering failure rather than as a node that has
// not classified this reader.
func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}
