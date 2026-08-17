package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

const sayUsage = `flowy say - put one message in a room

usage:
  flowy say [--room R] [--to NAME] [--thread ID] "text"
  echo "text" | flowy say [--room R]

  --room R      the room to say it in, default general. A room exists because
                somebody spoke in it; there is nothing to create
  --to NAME     address it at somebody. This ROUTES AND WAKES, it does not
                hide: an addressed message is read by exactly the principals
                that read the room before it
  --thread ID   continue a thread rather than starting one
  --url URL     node to tell (default $FLOWY_ADDR, then http://127.0.0.1:8787)
  --token T     bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)
  --agent NAME  the seat speaking, whose token is ~/.config/flowy/agents/NAME
                (default $FLOWY_AGENT). ~/.config/flowy/token is the OPERATOR'S
                own, so falling through to it warns; --agent me is the operator
                saying it was meant, and stops the warning

  The text is one argument, or stdin when there is none - so a long message
  comes from a heredoc instead of being fought with through shell quoting.

  Exit 0 when the node accepted it, 2 when it did not. A refusal is a failure
  here rather than a JSON body to remember to read: the failure mode this
  command exists to remove is a message nobody received looking exactly like a
  message that was sent.
`

// sayCmd is `flowy say`.
//
// The other half of `flowy inbox`. Until this existed the CLI could listen and
// not speak, so every agent outside the TUI hand-rolled curl with a bearer
// header and hand-written JSON - and hand-rolled curl silently succeeds at
// posting nothing when the node refuses, because the refusal is a 4xx with a
// body and curl exits 0 all the same.
//
// Speaks HTTP for the same reason inbox does: what it needs is a token and a
// node, not a DSN.
func sayCmd(args []string) error {
	fs := flag.NewFlagSet("say", flag.ContinueOnError)
	room := fs.String("room", "general", "the room to say it in")
	to := fs.String("to", "", "address it at somebody - routing and waking, not privacy")
	thread := fs.String("thread", "", "continue this thread rather than starting one")
	urlFlag := fs.String("url", "", "node to talk to (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token := fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)")
	agent := fs.String("agent", "", agentFlagHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) > 0 && rest[0] == "help" {
		fmt.Print(sayUsage)
		return nil
	}

	body, err := sayBody(rest)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("nothing to say: pass the text as an argument or on stdin\n\n" + sayUsage)
	}
	if strings.TrimSpace(*room) == "" {
		return errors.New("--room cannot be empty")
	}

	base := resolveURL(*urlFlag, os.Getenv("FLOWY_ADDR"))
	bearer, err := resolveToken(*token, os.Getenv("FLOWY_TOKEN"), *agent, os.Getenv("FLOWY_AGENT"))
	if err != nil {
		return err
	}
	if bearer == "" {
		return errNoToken()
	}

	payload, err := json.Marshal(chatSayRequest{Body: body, Thread: *thread, To: *to})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}

	var said store.Event
	if err := peerRequest(ctx, client, http.MethodPost,
		base+"/api/chat/"+*room+"/say", bearer, payload, &said); err != nil {
		return err
	}

	// The id on stdout, so a script can thread a reply onto it. Everything a
	// person reads goes to stderr, which keeps stdout parseable for the same
	// reason inbox writes JSONL there and its re-arm line here.
	fmt.Println(said.ID)
	fmt.Fprintf(os.Stderr, "said in #%s\n", *room)
	return nil
}

// sayBody takes the message from the arguments, or from stdin when there are
// none. Reading stdin only in that case matters: a `flowy say "text"` inside a
// pipeline must not block waiting for input nobody is going to send.
func sayBody(rest []string) (string, error) {
	if len(rest) > 0 {
		return strings.Join(rest, " "), nil
	}
	stat, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if stat.Mode()&os.ModeCharDevice != 0 {
		return "", errors.New("nothing to say: pass the text as an argument or on stdin\n\n" + sayUsage)
	}
	text, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(text), "\n"), nil
}
