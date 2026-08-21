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
  flowy merge withdraw --id ID --note "why"

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

On withdraw:

  --id        the merge row to take out of the queue. The queue filters on
              status, so a withdrawn row is a done row - and abandon is a
              different operation on a different thing: it gives back the
              target LOCK and leaves the request in the queue.
  --note      why. Required, and the only required note in this file: every
              other queue event leaves an artifact behind - a landing leaves a
              sha, a red leaves a verdict - and a withdrawal leaves an absence.

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
	case "withdraw":
		return mergeWithdraw(args)
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
// THE ADVICE DEPENDS ON WHICH TREE IS HOLDING IT, and getting that wrong froze
// the whole queue for a gate.
//
// This message used to say "detach that worktree first" and print
// `git checkout --detach`, whoever was holding the branch. 01M0HQKP0C: somebody
// built a branch in the SHARED CHECKOUT, was told by this refusal to detach it,
// did, filed - and the drainer then refused to land anything at all, because
// deploying from a detached shared checkout is refused one layer down:
// "/home/dead/Projects/flowy is on HEAD, not master". The advice created the
// next failure, and it was my advice.
//
// A LINKED WORKTREE MAY BE DETACHED; THE MAIN ONE MUST GO BACK TO MASTER.
// Nothing else in the fleet cares what a scratch worktree points at, and
// everything cares what the main one points at - the drainer lands there.
//
// git answers this exactly rather than by convention: in the main working tree
// the git-dir and the common git-dir are the same directory, and in a linked
// one the git-dir is <common>/worktrees/<name>. Both are asked as absolute
// paths so the comparison is not about how the question was phrased.
func isLinkedWorktree(at string) bool {
	dir, err := exec.Command("git", "-C", at, "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		return false
	}
	common, err := exec.Command("git", "-C", at,
		"rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(dir)) != strings.TrimSpace(string(common))
}

// freeItBy is the sentence, and freeItWith is the command. Two functions rather
// than one returning a pair, because they are read in two different places in
// the format string and a pair would put the wrong half in the wrong slot the
// first time somebody edits it.
//
// CANNOT-KNOW FALLS BACK TO THE SAFE ADVICE. isLinkedWorktree answers false
// when git cannot be asked, and false means "treat it as the main tree", so an
// unanswerable question produces "put it back on master" - which is harmless in
// a scratch worktree and is the only correct thing in the shared one. The
// dangerous advice is never the default.
func freeItBy(at string) string {
	if isLinkedWorktree(at) {
		return "detach that worktree first:"
	}
	return "that is a main working tree, so put it back on master rather than " +
		"detaching it - a detached checkout there is what the drainer lands from, " +
		"and it refuses:"
}

func freeItWith(at string) string {
	if isLinkedWorktree(at) {
		return fmt.Sprintf("git -C %s checkout --detach", at)
	}
	return fmt.Sprintf("git -C %s checkout master", at)
}

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

// whyCannotAsk says WHICH of the two cannot-know cases happened, in a sentence
// somebody can act on.
//
// heldBy answers three things and the note that reads it answered two, so
// "no git repository here at X" was printed for both of these:
//
//	X is not a repository at all          set FLOWY_REPO
//	X is a repository without that branch  set FLOWY_REPO TO THE RIGHT TREE
//
// The second is claude-host's case, filing flowy branches from
// ~/Projects/firecode - which IS a git repository, so the note was not merely
// vague, it was FALSE, and it sent them to look for a repository they were
// already standing in. Reported by them on 2026-08-21, the day after the note
// was written to prevent exactly this class of thing.
//
// Same collapse as everything else this fix has been about: a function kept the
// distinction carefully and its caller threw it away. Here the caller is the
// only place the distinction is visible to a person.
func whyCannotAsk(branch string) string {
	where, from := repoAsked()
	git := func(args ...string) *exec.Cmd {
		cmd := exec.Command("git", args...)
		if where != "" {
			cmd.Dir = where
		}
		return cmd
	}
	at := where
	if at == "" {
		at = "here"
	} else {
		at = at + from
	}
	// Is it a repository at all? This is the cheap half and it is the half that
	// decides which sentence to print.
	if err := git("rev-parse", "--git-dir").Run(); err != nil {
		return "there is no git repository at " + at
	}
	return at + " is a git repository but has no branch " + branch +
		" - that is probably not the tree that owns it"
}

// repoAsked is the directory heldBy consults and where that came from, so every
// message about it names the same place by the same rule.
func repoAsked() (where, from string) {
	if repo := strings.TrimSpace(os.Getenv("FLOWY_REPO")); repo != "" {
		return repo, " (from $FLOWY_REPO)"
	}
	if wd, err := os.Getwd(); err == nil {
		return wd, ""
	}
	return "", ""
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
			"note: could not check whether %s is checked out somewhere - %s. "+
				"Set FLOWY_REPO to the tree that owns the branch to enable the "+
				"check. Filing anyway.\n",
			name, whyCannotAsk(name))
	}
	if known && at != "" {
		return fmt.Errorf("%s is checked out in %s, so the drainer cannot rebase it - "+
			"%s\n\n"+
			"    %s\n\n"+
			"then file this again. Filed as it stands, the row would be picked up, "+
			"blocked, and left looking stalled", name, at, freeItBy(at), freeItWith(at))
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

// mergeWithdraw takes a row OUT of the queue, and is the verb this file did not
// have.
//
// WHY A VERB RATHER THAN A SENTENCE IN THE DOCS. Three withdrawals happened on
// the live node before this existed and none of them used the same words:
// `flowy todo done --id`, a raw `curl -X POST /api/artifact/{id}/status`, and an
// attempt at `/api/merge/{id}/abandon` that was refused - correctly, about a
// question the caller was not asking. Withdrawal is a normal part of running a
// queue, and a normal operation whose verb lives under a different noun is one
// every new seat rediscovers by being refused.
//
// THE TWO VERBS AND WHY THEY ARE NOT THE SAME ONE:
//
//	abandon    gives back the target LOCK and leaves the request at todo. The
//	           row stays in the queue and can be gated again.
//	withdraw   retires the ROW. The queue filters on status, so done is what
//	           takes it out - and the lock, if this row holds one, is NOT
//	           touched by that. Measured: nothing in the status path calls
//	           MergeLockOf.
//
// which is why this refuses rather than closing a row that holds its target.
// Closing it would take the row out of the queue and leave master reserved for
// the full BlockBelievedFor window with no row left to explain why - a stuck
// lock whose reason has been deleted, found by whoever declares next.
func mergeWithdraw(args []string) error {
	fs := flag.NewFlagSet("merge withdraw", flag.ContinueOnError)
	id := fs.String("id", "", "the merge row to take out of the queue")
	note := fs.String("note", "", "why it is being withdrawn")
	urlFlag := fs.String("url", "", "node to talk to (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token := fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)")
	agent := fs.String("agent", "", agentFlagHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}
	row := strings.TrimSpace(*id)
	if row == "" {
		return errors.New("a withdrawal names a row: pass --id\n\n" + mergeUsage)
	}
	// THE REASON IS REQUIRED, and this is the one place in this file that
	// demands one. A row leaving the queue is the only queue event with no
	// artifact of its own: a landing leaves a sha, a block leaves a reason, a
	// red leaves a verdict, and a withdrawal leaves an absence. Whoever wonders
	// next week why that branch never landed has the note or has nothing.
	if strings.TrimSpace(*note) == "" {
		return errors.New("a withdrawal says why - a row that vanishes from the queue " +
			"with nothing on it is indistinguishable from one that landed: pass --note\n\n" +
			mergeUsage)
	}

	base, bearer, err := nodeFor(*urlFlag, *token, *agent)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}

	// IS IT A MERGE ROW AT ALL. `todo done` and this write through the same
	// door, so without this check `merge withdraw` on a todo id closes the todo
	// and reports a withdrawal - the wrong noun accepted silently, which is how
	// the abandon confusion started.
	var art struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	if err := peerRequest(ctx, client, http.MethodGet, base+"/api/artifact/"+row,
		bearer, nil, &art); err != nil {
		return err
	}
	if art.Kind != store.MergeKind {
		return fmt.Errorf("%s is a %s row, not a merge request - `flowy todo done --id %s "+
			"--note \"...\"` is what closes one of those", row, art.Kind, row)
	}
	if art.Status == store.DoneStatus {
		// NOT AN ERROR TO REPEAT, but not a silent success either: a caller
		// that sees "withdrawn" for the second time has learnt nothing about
		// whether their first call worked.
		fmt.Println(row)
		fmt.Fprintf(os.Stderr, "%s was already out of the queue - nothing to withdraw\n", row)
		return nil
	}

	// DOES THIS ROW HOLD THE TARGET. The lock names the row it was taken for -
	// see mergeQueueLock.Item, and 01M0DZP4HS for why an identity rather than a
	// name. If it is this one, withdrawing would strand it.
	answer, _, err := readQueue(ctx, client, base, bearer, "")
	if err == nil && answer.Lock != nil && answer.Lock.Held && answer.Lock.Item == row {
		return fmt.Errorf("%s is holding %s until %s, so withdrawing it now would leave the "+
			"target reserved with no row left to say why - give the lock back first:\n\n"+
			"    curl -X POST -H \"Authorization: Bearer $FLOWY_TOKEN\" \\\n"+
			"      -d '{\"reason\":\"withdrawing the row\"}' \\\n"+
			"      %s/api/merge/%s/abandon\n\n"+
			"then withdraw it", row, answer.Target, answer.Lock.Until.Format(time.RFC3339),
			base, row)
	}
	// A QUEUE THAT COULD NOT BE READ IS NOT A QUEUE WITH NO LOCK. The read
	// above is a guard, not the operation, so a failure here must not refuse a
	// legitimate withdrawal - but it must not be reported as a clean check
	// either.
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: could not read the queue to check whether %s holds "+
			"the target (%v) - withdrawing anyway. If it was gating, give the lock back.\n",
			row, err)
	}

	payload, marshalErr := json.Marshal(map[string]string{
		"status": store.DoneStatus, "note": "withdrawn from the queue: " + strings.TrimSpace(*note),
	})
	if marshalErr != nil {
		return marshalErr
	}
	// Through call() rather than peerRequest directly: the status door answers
	// with a body this verb has no use for, and peerRequest given a nil
	// destination tries to decode into it anyway.
	if err := call(http.MethodPost, base+"/api/artifact/"+row+"/status",
		bearer, payload, nil); err != nil {
		return err
	}
	fmt.Println(row)
	fmt.Fprintf(os.Stderr, "withdrew %s - it is out of the queue and the note says why\n", row)
	return nil
}
