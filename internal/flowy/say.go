package flowy

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
	fmt.Fprintln(os.Stderr, saidWhere(&said))
	return nil
}

// saidWhere is the line a person reads after a message lands: where it went and
// who said it, taken FROM THE NODE'S ANSWER rather than from what this process
// intended.
//
// MEASURED, and it is the whole reason this is not `"said in #" + *room`. Two
// messages were sent from this machine without a token in the environment. The
// CLI fell back to ~/.config/flowy/token, which is an operator credential in
// project "pa", and both printed `said in #general` and exited 0. They were
// written as the operator into pa's #general. Every seat reads flowy's
// #general, so nobody saw them - including the sender, who spent ten minutes
// looking for them in the wrong room.
//
// A ROOM NAME IS NOT AN ADDRESS once there is more than one project, and there
// are five with rows on this node. `#general` exists in every one of them. So
// the line names the project as well, and it names the speaker, because those
// are the two things that were wrong and neither was visible.
//
// FROM THE ANSWER, not from the request: `*room` is what was asked for and
// `said.Room` is what was written. A line built from the request can only ever
// describe the attempt, which is exactly how a message that went somewhere else
// reads as one that arrived.
func saidWhere(said *store.Event) string {
	where := "#" + said.Room
	if said.Project != nil && *said.Project != "" {
		where = *said.Project + "/#" + said.Room
	}
	if who := speakerOf(said); who != "" {
		return "said in " + where + " as " + who
	}
	return "said in " + where
}

// speakerOf is the name the node recorded for whoever wrote the event, falling
// back to the actor id.
//
// The id is a poor thing to read and it is still worth printing: it is what the
// node says the speaker IS, and an unfamiliar id in that line is precisely the
// signal that the credential was not the one the caller meant. A blank there
// would hide it.
func speakerOf(said *store.Event) string {
	var meta struct {
		ActorName string `json:"actor_name"`
	}
	if len(said.Meta) > 0 {
		if err := json.Unmarshal(said.Meta, &meta); err == nil &&
			strings.TrimSpace(meta.ActorName) != "" {
			return meta.ActorName
		}
	}
	return said.Actor
}

// sayBody takes the message from the arguments, or from stdin when there are
// none. Reading stdin only in that case matters: a `flowy say "text"` inside a
// pipeline must not block waiting for input nobody is going to send.
func sayBody(rest []string) (string, error) { return bodyOrStdin(rest, "say", sayUsage) }

// bodyOrStdin is sayBody with the verb's own words on the empty case. Three
// verbs read a body this way now, and a caller who typed `flowy merge open`
// and gave it nothing was being handed `flowy say`'s usage - the right rule
// with the wrong verb's help under it, which reads as the tool being confused
// about what was asked.
func bodyOrStdin(rest []string, verb, usage string) (string, error) {
	// A LONE `-` MEANS STDIN, which is what it means to every tool that takes a
	// body from a pipe: cat -, git apply -, kubectl apply -f -.
	//
	// MEASURED, and it is the reason this exists rather than a nicety. Four
	// doors read a body this way - say, todo file, todo note, merge open - and
	// `flowy say --url U - <<EOF` posted the literal string "-" as the message,
	// exit 0, with the same success line and the same shape of id a real
	// message gets. The heredoc on stdin was never read, because an argument
	// was given and the argument wins.
	//
	// The damage on one evening, counted by reading the bodies back rather than
	// by trusting the success line: six rows filed with a body of "-", two of
	// them MERGE rows sitting in the queue carrying no evidence at all, every
	// note written across four hours, and about ten messages in a room -
	// including two corrections and a ruling. The operator spotted it before
	// any of the four agents did, and asked twice.
	//
	// It survived because the failure is silent in BOTH directions: the writer
	// sees success, and the reader sees a row that exists, has a title, and
	// appears filed properly - a dash reads as a formatting artefact rather
	// than as a missing body.
	//
	// ONLY A LONE DASH. `flowy say - hello` still says "- hello", because a
	// dash among other words is a word. And a body that is genuinely one dash
	// is still sayable, on stdin: printf -- '-' | flowy say.
	if len(rest) == 1 && rest[0] == "-" {
		rest = nil
	}
	if len(rest) > 0 {
		return strings.Join(rest, " "), nil
	}
	stat, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if stat.Mode()&os.ModeCharDevice != 0 {
		return "", errors.New("nothing to " + verb + ": pass the text as an argument or on stdin\n\n" + usage)
	}
	text, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(text), "\n"), nil
}
