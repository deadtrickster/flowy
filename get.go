package main

// `flowy get <path>` - one door's answer, on stdout, through the client that
// already exists.
//
// 01M0EVR0X3. Counted from two seats' transcripts in one night: 414 hand-built
// curls to the node, with the bearer header assembled by hand each time and the
// token re-read from ~/.config/flowy/agents 1485 times between them, plus 532
// `python3 -c` invocations whose only job was to pull one field out of the
// answer.
//
// THIS IS THE LOOP THAT COSTS CORRECTNESS RATHER THAN TIME, which is why it
// was worth a verb. 414 chances to get the host or the auth wrong, and one was
// taken: a seat probed 127.0.0.1:8787 for the live node, got zero doors, and
// nearly filed a stall on the strength of it. A hand-written curl gets none of
// what every other verb here has - the same token resolution and its warning
// when nothing named who is speaking, the same base URL, the same wait through
// a deploy window.
//
// A PATH THAT IS NOT A DOOR IS REFUSED HERE, not 404'd by the node. A shell
// script cannot tell 404-the-row from 404-the-typo, and the second is the one
// that reads as "the row is gone" and sends somebody looking for it. apiRoutes
// is the list the node itself advertises, so this cannot drift from what exists
// - the same list TestEveryRegisteredAPIRouteIsAdvertised holds to the mux.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const getUsage = `usage: flowy get <path> [--jq EXPR] [--url URL] [--token T] [--agent NAME]

  flowy get /api/merge-queue
  flowy get /api/artifact/01M0... --jq .title

Prints the door's answer on stdout. Exit 0 when the node answered 2xx, 1 when it
refused, 2 when the request could not be made at all.`

// getArgs is the parsing, separated from the request so it can be tested
// without a node.
//
// The first version of this test drove getCmd itself with an empty token, which
// still reached the network and took forty seconds against the deploy-window
// retry. A test that waits for a real socket to prove an argument was read is
// measuring the wrong thing slowly.
type getArgs struct {
	path, jq, url, token, agent string
}

func parseGetArgs(args []string) (getArgs, error) {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	jqExpr := fs.String("jq", "", "pull one field out with jq, instead of a second json language in the caller")
	urlFlag := fs.String("url", "", "node to talk to (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token := fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)")
	agent := fs.String("agent", "", agentFlagHelp)

	// FLAGS ON EITHER SIDE OF THE PATH, and neither order silently ignored.
	//
	// Go's flag package stops at the first non-flag argument, so
	// `flowy get /api/x --jq .y` parses nothing: --jq is dropped and the caller
	// gets the whole document with no sign the filter was lost. Measured on
	// this verb's first live run - it printed a correct-looking answer to the
	// wrong question.
	//
	// The first fix pulled out the first argument not starting with a dash,
	// which broke the other order: in `get --jq .project /api/x` that argument
	// is `.project`, the VALUE of --jq. A pre-scan cannot know which flags take
	// values; the flag package can. So it parses, takes the first positional,
	// and parses again from there.
	if err := fs.Parse(args); err != nil {
		return getArgs{}, err
	}
	var path string
	if rest := fs.Args(); len(rest) > 0 {
		path = rest[0]
		if err := fs.Parse(rest[1:]); err != nil {
			return getArgs{}, err
		}
	}
	if path == "" {
		return getArgs{}, fmt.Errorf("no path\n\n%s", getUsage)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if err := knownDoor(path); err != nil {
		return getArgs{}, err
	}
	return getArgs{path: path, jq: *jqExpr, url: *urlFlag, token: *token, agent: *agent}, nil
}

func getCmd(args []string) error {
	a, err := parseGetArgs(args)
	if err != nil {
		return err
	}
	base := resolveURL(a.url, os.Getenv("FLOWY_ADDR"))
	bearer, err := resolveToken(a.token, os.Getenv("FLOWY_TOKEN"), a.agent, os.Getenv("FLOWY_AGENT"))
	if err != nil {
		return err
	}
	if bearer == "" {
		return errNoToken()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+a.path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := doThroughARestartFrom(ctx, &http.Client{Timeout: 60 * time.Second}, req, nil,
		addressWasNamed(a.url, os.Getenv("FLOWY_ADDR")))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	out := body
	if a.jq != "" {
		out, err = throughJQ(a.jq, body)
		if err != nil {
			return err
		}
	}
	os.Stdout.Write(out)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		fmt.Println()
	}
	// THE STATUS IS THE ANSWER AND THE EXIT CODE IS TOO. A caller that has to
	// parse the printed body to learn whether the door refused is the caller
	// this verb exists to stop writing.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s answered %d", a.path, resp.StatusCode)
	}
	return nil
}

// knownDoor refuses a path the node does not advertise.
//
// MATCHED AGAINST THE PATTERN, not the literal, because a door is
// "/api/artifact/{id}" and a caller types an id. Segment counts and the fixed
// segments have to line up; a {placeholder} accepts anything that is not empty.
func knownDoor(path string) error {
	asked := strings.Split(strings.SplitN(strings.TrimPrefix(path, "/"), "?", 2)[0], "/")
	for _, route := range apiRoutes {
		method, pattern, ok := strings.Cut(route, " ")
		if !ok || method != http.MethodGet {
			continue
		}
		if matchesDoor(strings.Split(strings.TrimPrefix(pattern, "/"), "/"), asked) {
			return nil
		}
	}
	return fmt.Errorf("%s is not a door this node advertises - "+
		"a typo answers 404 the same way a missing row does, and this refuses rather than "+
		"leaving you to tell them apart\n\n%s", path, getUsage)
}

func matchesDoor(pattern, asked []string) bool {
	if len(pattern) != len(asked) {
		return false
	}
	for i, seg := range pattern {
		if strings.HasPrefix(seg, "{") {
			if asked[i] == "" {
				return false
			}
			continue
		}
		if seg != asked[i] {
			return false
		}
	}
	return true
}

// throughJQ shells out rather than embedding a second JSON language.
//
// Argued for in the row and worth restating: the alternative measured tonight
// is 532 `python3 -c` calls in two transcripts, each one a small program in a
// language the caller had to hold in their head beside the shell. A flag that
// runs the jq everybody already uses is smaller than that.
func throughJQ(expr string, body []byte) ([]byte, error) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		return nil, fmt.Errorf("--jq needs jq on PATH: %w", err)
	}
	cmd := exec.Command(jq, "-r", expr)
	cmd.Stdin = strings.NewReader(string(body))
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("jq %q: %w", expr, err)
	}
	return out, nil
}
