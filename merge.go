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
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

const mergeUsage = `flowy merge - file a branch for the queue to land

usage:
  flowy merge open --branch B [--target master] [--title T] [--assignee A]
                   [--room R] [--scope S] [body]

The body is the argument, or stdin, and it is what the next reader has instead
of you: what changed, what was measured, what is deliberately not in it.

  --branch    the branch this would land. Required - a merge request that does
              not say one cannot be rebased, gated or fast-forwarded.
  --target    where it lands (default master).
  --title     one line for the queue (default "land <branch>").
  --assignee  who is carrying it. An unassigned row with a branch already
              written is how two people build the same thing.
  --room      the room its landing is announced in (default general). A merge
              row filed without one lands SILENTLY: LandMerge writes the entry
              with the room on it, so no room means no entry anybody sees.
              Measured 2026-08-19 - four of the six rows that landed that day
              carried no room, and the landing announcement covered two of six,
              which is worse than uniformly silent.
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
// heldBy answers WHERE a branch is checked out, and tells that apart from not
// knowing.
//
// Three answers, not two:
//
//	path, true   the branch is attached to a worktree at path
//	"", true     it is not attached anywhere, measured
//	"", false    THIS TREE CANNOT ANSWER - not a git repo, or a repo that does
//	             not carry the branch, so its worktree list is about some other
//	             history entirely
//
// The third is the whole point of the signature. A caller that collapses it
// into "not held" gets a confident clean from a directory that has never heard
// of the branch, which is the same defect as a nil slice serialising to null:
// a failure to know rendered as a successful no. Twelve of those were found in
// this codebase in one day.
func heldBy(branch string) (string, bool) {
	// WHICH REPOSITORY TO ASK, and it is not always the one you are standing in.
	//
	// Measured after the first version of this guard landed: every seat files
	// with `cd ~/Projects/flowy-dogfood && ./flowy merge open`, and that
	// directory is the DEPLOY directory - it holds the built binary and is not
	// a git repository at all. `git rev-parse --git-dir` there is a fatal. So
	// the guard answered cannot-know, proceeded, and was inert on the only path
	// anybody uses. It was correct and it caught nothing.
	//
	// $FLOWY_REPO names the tree that owns the branches, which is the tree the
	// drainer rebases in. Unset, this falls back to the working directory, which
	// is right for anybody running from their own worktree.
	git := func(args ...string) *exec.Cmd {
		cmd := exec.Command("git", args...)
		if repo := strings.TrimSpace(os.Getenv("FLOWY_REPO")); repo != "" {
			cmd.Dir = repo
		}
		return cmd
	}
	// Does that directory own the branch at all? `git worktree list` in an
	// unrelated clone happily lists that clone's worktrees and finds nothing,
	// which reads exactly like a clean answer about the branch in question.
	if err := git("rev-parse", "--verify", "--quiet",
		"refs/heads/"+branch).Run(); err != nil {
		return "", false
	}
	out, err := git("worktree", "list", "--porcelain").Output()
	if err != nil {
		return "", false
	}
	// --porcelain is stable and line-oriented on purpose: the human format
	// aligns columns and abbreviates, and a path with a space in it is
	// unparseable from it. Records are blank-line separated, "worktree <path>"
	// then optionally "branch refs/heads/<name>".
	at := ""
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			at = strings.TrimPrefix(line, "worktree ")
		case line == "branch refs/heads/"+branch:
			return at, true
		}
	}
	return "", true
}

// repoHint says which directory was asked, when one was. It exists so the note
// above names a PLACE rather than a condition: "no git repository here" sends
// somebody looking at their shell, "no git repository at /x/y" tells them their
// FLOWY_REPO is wrong, and those are different fixes.
func repoHint() string {
	if repo := strings.TrimSpace(os.Getenv("FLOWY_REPO")); repo != "" {
		return " at " + repo + " (from $FLOWY_REPO)"
	}
	if wd, err := os.Getwd(); err == nil {
		return " at " + wd
	}
	return ""
}

func mergeOpen(args []string) error {
	fs := flag.NewFlagSet("merge open", flag.ContinueOnError)
	branch := fs.String("branch", "", "the branch this would land")
	target := fs.String("target", "", "where it lands (default master)")
	title := fs.String("title", "", `one line for the queue (default "land <branch>")`)
	assignee := fs.String("assignee", "", "who is carrying it")
	room := fs.String("room", "", "the room its landing is announced in (default "+mergeRoomDefault+")")
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
	// REFUSED AT THE DOOR, because fifteen minutes later it costs a measurement.
	//
	// A branch checked out in any worktree cannot be rebased, so the drainer
	// picks the row up, records a block and moves on. From outside the row looks
	// stalled rather than fixable, and by then whoever filed it has gone on to
	// something else. FIVE TIMES on 2026-08-20, across three agents - the worst
	// cost an hour on an unrelated flake because the row also carried a red, and
	// mine sat blocked for 25 minutes while I reported that I was waiting for
	// the gate. Here the person is still at the keyboard and the fix is one
	// command.
	//
	// BEFORE THE NETWORK, deliberately: no token is resolved, no row is written,
	// nothing to withdraw. A refusal that has already filed something is a
	// second thing to clean up.
	//
	// AND IT SAYS WHERE. "that branch is checked out" sends somebody to
	// `git worktree list`; the path plus the command is the whole fix, and the
	// drainer's own block message already names it, so the string was proven
	// useful before it was put here.
	at, known := heldBy(name)
	// CANNOT-KNOW IS SAID OUT LOUD, not swallowed.
	//
	// A silent proceed is what made the first version of this guard useless: it
	// could not check, said nothing, and every reader assumed the check had run
	// and passed. That is the same defect the three-valued return exists to
	// avoid, reappearing one layer up - the distinction was kept in heldBy and
	// then thrown away by the caller.
	//
	// A NOTE, NOT A REFUSAL. Nobody should be blocked from filing because their
	// shell is in the wrong directory, and there are honest callers with no repo
	// anywhere near them. But "I did not check" has to reach the person who
	// would otherwise read silence as a pass.
	if !known {
		fmt.Fprintf(os.Stderr,
			"note: could not check whether %s is checked out somewhere - "+
				"no git repository here%s. Set FLOWY_REPO to the tree that owns the "+
				"branch to enable the check. Filing anyway.\n",
			name, repoHint())
	}
	if known && at != "" {
		return fmt.Errorf("%s is checked out in %s, so the drainer cannot rebase it - "+
			"detach that worktree first:\n\n"+
			"    git -C %s checkout --detach\n\n"+
			"then file this again. Filed as it stands, the row would be picked up, "+
			"blocked, and left looking stalled", name, at, at)
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
	// THE ROOM, DEFAULTED AND SAID OUT LOUD.
	//
	// A merge row with no room lands silently. store.LandMerge writes the entry
	// with the room on it, so a row that carries none produces an entry with
	// nowhere to be - and until today this verb had no --room flag at all, so it
	// was not that nobody typed one, it was that nobody could. Measured
	// 2026-08-19: four of the six rows that landed that day carry no room, and
	// the landing announcement therefore covered two of six. A room that reports
	// a third of the landings is worse than one that reports none, because the
	// silence about the other four reads as "nothing landed".
	//
	// SO IT DEFAULTS RATHER THAN REFUSING. The --scope comment below argues the
	// opposite for scope and it is right there and right here: an unstated scope
	// is a row nobody can READ, which is unrecoverable from the outside, while
	// an unstated room is a landing announced in the wrong place, which somebody
	// can see and move. Requiring it would break every caller in flight for a
	// defect that has a good default.
	//
	// The default announces itself on stderr, which is the difference between
	// this and the silent default that put three rows at personal scope: a
	// caller who did not mean general finds out at the moment they could still
	// say otherwise.
	where := strings.TrimSpace(*room)
	if where == "" {
		where = mergeRoomDefault
	}
	fields[store.RoomField] = where

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
	// The room is said too, and for the same reason as the scope: it is a
	// property of this row that decides whether anybody hears about it, and the
	// caller can still change their mind while the words are on their screen.
	if strings.TrimSpace(*room) == "" {
		fmt.Fprintf(os.Stderr, "its landing will be announced in #%s - say --room to change that\n",
			where)
	}
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

// mergeRoomDefault is where a landing is announced when the caller says nothing.
// #general is where this fleet decides what to pick up, which makes it the room
// a landing is least likely to be missed in.
const mergeRoomDefault = "general"
