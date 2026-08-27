package flowy

// `flowy dm` - the private log, from a shell.
//
// WHY THIS EXISTS: POST /api/dm/{to} has been the only way to send one, and a
// door with no verb is a door people reach past. Measured 2026-08-21, twice in
// five minutes and by two different seats: @orchestrator asked another seat for
// a DM to check a badge, and both of us reached for `flowy say --to NAME`
// first. That command sets an ADDRESSEE ON A PUBLIC MESSAGE - its own flag help
// says "routing and waking, not privacy" - so both attempts went to #general
// where every seat reads them. One of us noticed; the other only found out
// because they did.
//
// That is the failure this verb removes, and it is worse than the merge-queue
// one it rhymes with (01M0G4FMK4, `merge withdraw`): a wrong queue verb is
// refused, while `say --to` SUCCEEDS. It prints "said in flowy/#general", exits
// 0, and the sender has published something they meant to whisper. There is no
// error to notice.
//
// WHAT IT IS DELIBERATELY NOT: a reader. `flowy inbox` already blocks on
// messages and knows about private ones; a second thing that reads would be
// two vocabularies for one question.

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

	"github.com/deadtrickster/flowy/internal/store"
)

const dmUsage = `flowy dm - send one direct message

usage:
  flowy dm --to NAME "text"
  echo "text" | flowy dm --to NAME

  --to NAME     who it goes to: a user or an agent. Required, and resolved by
                the node - a name nothing answers to is refused rather than
                accepted and read by nobody for ever
  --thread ID   continue a thread rather than starting one
  --url URL     node to tell (default $FLOWY_ADDR, then http://127.0.0.1:8787)
  --token T     bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)
  --agent NAME  the seat speaking, whose token is ~/.config/flowy/agents/NAME
                (default $FLOWY_AGENT)

  The text is one argument, or stdin when there is none.

THIS IS THE PRIVATE ONE. flowy say --to NAME is not: it addresses a message
that is still said in a room, which routes and wakes but hides nothing. Two
seats reached for it on the same day meaning this, and both published.

  flowy dm --to orchestrator "..."   private log, room is empty on the event
  flowy say --to orchestrator "..."  #general, addressed, read by every member

  Exit 0 when the node accepted it, non-zero when it did not - a refusal is a
  failure rather than a body to remember to read.
`

// dmSayBody is what the door takes: POST /api/dm/{to} {body, thread?, parents?}.
// Declared here rather than reusing chatSayRequest because the two doors are
// separate on purpose and a shared struct is how a field grows on one and
// silently arrives at the other.
type dmSayBody struct {
	Body    string   `json:"body"`
	Thread  string   `json:"thread,omitempty"`
	Parents []string `json:"parents,omitempty"`
}

// dmCmd is `flowy dm`.
func dmCmd(args []string) error {
	fs := flag.NewFlagSet("dm", flag.ContinueOnError)
	to := fs.String("to", "", "who it goes to - a user or an agent")
	thread := fs.String("thread", "", "continue this thread rather than starting one")
	urlFlag := fs.String("url", "", "node to talk to (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token := fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)")
	agent := fs.String("agent", "", agentFlagHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) > 0 && rest[0] == "help" {
		fmt.Print(dmUsage)
		return nil
	}

	who := strings.TrimSpace(*to)
	if who == "" {
		return errors.New("a direct message needs somebody to go to: pass --to\n\n" + dmUsage)
	}
	// BEFORE THE NETWORK, because the path is /api/dm/{to} and a name with a
	// slash in it addresses a different route entirely - which would be a 404
	// about an endpoint rather than a word about the name the caller typed.
	if strings.ContainsAny(who, "/?#") {
		return fmt.Errorf("%q is not a name: --to takes one user or agent, and it becomes a path "+
			"segment", who)
	}

	body, err := sayBody(rest)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("nothing to send: pass the text as an argument or on stdin\n\n" + dmUsage)
	}

	base := resolveURL(*urlFlag, os.Getenv("FLOWY_ADDR"))
	bearer, err := resolveToken(*token, os.Getenv("FLOWY_TOKEN"), *agent, os.Getenv("FLOWY_AGENT"))
	if err != nil {
		return err
	}
	if bearer == "" {
		return errNoToken()
	}

	payload, err := json.Marshal(dmSayBody{Body: body, Thread: *thread})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}

	var sent store.Event
	if err := peerRequest(ctx, client, http.MethodPost,
		base+"/api/dm/"+who, bearer, payload, &sent); err != nil {
		return err
	}

	fmt.Println(sent.ID)
	// THE ROOM IS NAMED BECAUSE IT IS EMPTY, and that is the proof the caller
	// wants: a DM's event carries room "". Saying "sent" would be true of the
	// public message this verb exists to stop people sending by mistake, so the
	// line says the thing that distinguishes them.
	where := "the private log"
	if sent.Room != "" {
		where = "#" + sent.Room + " - THIS WAS NOT PRIVATE"
	}
	fmt.Fprintf(os.Stderr, "sent to %s in %s\n", who, where)
	return nil
}
