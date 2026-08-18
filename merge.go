package main

// `flowy merge open` - filing a merge request from the shell.
//
// WHY THIS EXISTS: four seats file merge requests and none of them had a verb
// for it. I drove a Python shim that speaks MCP over stdio to call mem_write;
// the others curl. That is four copies of what a merge row is made of, kept in
// four sessions and none of them in this repository, and it has already gone
// wrong twice tonight - three rows filed at personal scope where the drainer
// could not see them, and a rule about branches that only one door enforced.
//
// WHAT IS DELIBERATELY NOT HERE:
//
//   - gate, land and abandon. Landing is the drainer's, by the operator's
//     instruction, and a hand-landing path with a nicer name is still a hand
//     landing. This verb files work and reads nothing back but the id.
//   - a queue listing. `flowy queue` is where that belongs, and two verbs
//     reading one list is the duplication this file is here to remove.
//
// It posts to /api/artifacts, which is the same door the console writes
// through, so the row it files is the row a person would have filed: the store
// decides the branch rule, the project comes from the token, and the default
// visibility is the door's.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

const mergeUsage = `flowy merge - file a branch for the queue to land

usage:
  flowy merge open --branch B [--target master] [--title T] [--assignee A] [body]

The body is the argument, or stdin, and it is what the next reader has instead
of you: what changed, what was measured, what is deliberately not in it.

  --branch    the branch this would land. Required - a merge request that does
              not say one cannot be rebased, gated or fast-forwarded.
  --target    where it lands (default master).
  --title     one line for the queue (default "land <branch>").
  --assignee  who is carrying it. An unassigned row with a branch already
              written is how two people build the same thing.
  --scope     who may read it (default project). It is SENT rather than left
              to the door: three rows sat invisible for hours because a default
              decided this and nobody typed it.

It prints the row id on stdout and everything a person reads on stderr, so a
script can hand the id straight on.

Gating and landing are NOT here. The drainer takes rows off this queue; filing
one is where a seat's part ends.
`

// mergeCmd is `flowy merge ...`.
func mergeCmd(args []string) error {
	sub := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "open", "file":
		return mergeOpen(args)
	case "help", "-h", "--help":
		fmt.Print(mergeUsage)
		return nil
	case "":
		return errors.New("flowy merge takes a command\n\n" + mergeUsage)
	default:
		return fmt.Errorf("unknown merge command %q\n\n%s", sub, mergeUsage)
	}
}

// mergeOpen files one merge request.
func mergeOpen(args []string) error {
	fs := flag.NewFlagSet("merge open", flag.ContinueOnError)
	branch := fs.String("branch", "", "the branch this would land")
	target := fs.String("target", "", "where it lands (default master)")
	title := fs.String("title", "", `one line for the queue (default "land <branch>")`)
	assignee := fs.String("assignee", "", "who is carrying it")
	scope := fs.String("scope", "project", "who may read it: "+strings.Join(store.MemScopes, ", "))
	urlFlag := fs.String("url", "", "node to talk to (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token := fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)")
	agent := fs.String("agent", "", agentFlagHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := strings.TrimSpace(*branch)
	if name == "" {
		return errors.New("a merge request says which branch it would land: pass --branch\n\n" +
			mergeUsage)
	}
	body, err := bodyOrStdin(fs.Args(), "file", mergeUsage)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("a merge request says what it changed and what was measured: " +
			"pass it as an argument or on stdin\n\n" + mergeUsage)
	}

	base := resolveURL(*urlFlag, os.Getenv("FLOWY_ADDR"))
	bearer, err := resolveToken(*token, os.Getenv("FLOWY_TOKEN"), *agent, os.Getenv("FLOWY_AGENT"))
	if err != nil {
		return err
	}
	if bearer == "" {
		return errNoToken()
	}

	fields := map[string]string{store.BranchField: name}
	if t := strings.TrimSpace(*target); t != "" {
		fields[store.TargetField] = t
	}
	if who := strings.TrimSpace(*assignee); who != "" {
		fields[store.AssigneeField] = who
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	// THE SCOPE IS STATED, NOT INHERITED. The door's default happens to be the
	// right one today, and that is exactly the argument against relying on it:
	// three merge rows sat at personal scope for hours because a default
	// decided who could read them and nobody had typed a word about it. A
	// caller saying what they want is not a second opinion about the rule - it
	// is the door being used for what it is for.
	//
	// The membership test is explicit because VisibilityForScope hands an
	// unknown word straight back - it is a lookup with a passthrough, not a
	// validator - and an unrecognised scope would otherwise be posted verbatim
	// as a visibility nothing reads.
	if !slices.Contains(store.MemScopes, *scope) {
		return fmt.Errorf("scope %q is not one of %s", *scope, strings.Join(store.MemScopes, ", "))
	}
	visibility := store.VisibilityForScope(*scope)

	line := strings.TrimSpace(*title)
	if line == "" {
		line = "land " + name
	}
	payload, err := json.Marshal(map[string]any{
		"type": store.MemoryType, "kind": store.MergeKind,
		"title": line, "body": body, "fields": json.RawMessage(raw),
		"visibility": visibility,
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	var answer struct {
		ID         string `json:"id"`
		Visibility string `json:"visibility"`
	}
	if err := peerRequest(ctx, client, http.MethodPost, base+"/api/artifacts",
		bearer, payload, &answer); err != nil {
		return err
	}

	fmt.Println(answer.ID)
	fmt.Fprintf(os.Stderr, "filed %s to land %s on %s\n",
		answer.ID, name, store.TargetOf(&store.Artifact{Fields: raw}))
	// The scope is said out loud because it is the one property of this row a
	// person cannot see from here and cannot afford to get wrong: a queue row
	// only its author can read is not on the queue, and that is exactly how
	// three rows sat invisible for hours.
	if answer.Visibility == store.VisibilityPersonal {
		fmt.Fprintln(os.Stderr, "WARNING: this row is personal - nobody but you can read it, "+
			"including whatever lands it")
	}
	return nil
}
