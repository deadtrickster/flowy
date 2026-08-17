package main

// The worklog: what the last few seats did, and where they stopped.
//
// THIS FILE IS THE ONE WAY IN. Everything that writes an entry - the MCP tool,
// POST /api/worklog, `flowy worklog append` - goes through appendWorklogEntry
// below, and the reference check is written once, here. That is not tidiness. An
// entry checks every artifact id it references against the writer's own read
// filter, so an entry cannot point at work its author could not see; a second
// implementation of the write is a second place for that check to be missing,
// and the check is the whole of what the surface is for.
//
// WHY THERE ARE TWO DOORS AND NOT ONE. The worklog was MCP-only, and the agents
// doing the work were exactly the ones that could not record it: a spawned VM
// agent is given no MCP server, deliberately - one that could reach the spawn
// server would start VMs of its own and the concurrency cap would stop meaning
// anything. So the fleet's memory across sessions had two entries ever and one
// in the twelve hours in which ten runs drained a queue, against 311 chat
// messages in the same window. A log that the working agents cannot write is not
// a log. HTTP and a CLI verb are doors those agents already have.
//
// What it is NOT is a kind on POST /api/activity, and readableKinds in
// activity.go says why: the refs are this verb's argument and not a column on an
// event, so the generic door cannot make the check and a kind accepted there
// would be an entrance that skips it. A dedicated verb is the opposite move -
// the same argument list, the same refusals, one implementation.
//
// Three decisions, in the order they matter:
//
//   - it is events, not a new artifact type. An append-only per-project stream
//     is what the event DAG already is: two seats appending at once produce two
//     rows and no conflict, and the log's cursor, its permission filter and its
//     replication carry the worklog with no second copy of any of them. A
//     worklog artifact would be one document that concurrent seats edit, which
//     is the two-doors problem the reports surface already refused once.
//   - every entry carries an actor, and the actor is the token's. Which seat
//     WROTE it is the first thing the next one asks, so it is stamped from the
//     credential exactly as a chat message's is, and there is no actor argument.
//     An entry that could name somebody else as its author is an entry nobody
//     can trust.
//   - VOUCHED IS NOT AUTHORED. An entry may be written by one seat ABOUT
//     another's work, and it says so on the row - see worklogSubject. That is
//     the whole of the second half of this surface, and the reason it exists is
//     below.
//
// The read shape is recent-N, newest first: what happened lately, which is the
// handoff read. Search of the whole history is mem_search and report_search's
// job, and the timeline's q; a worklog that grew a query language would be a
// second corpus rather than the front page of one.
//
// It is not memory, and the two must not be merged. A memory item is a durable
// fact that gets revised - one row per fact, edited in place as it changes. An
// entry here is a moment: what changed, what is next, and what it was true of,
// never edited afterwards, because a chronology that can be rewritten is not a
// chronology. Same store, same permission filter, two read shapes.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// worklogEventType is what an entry is in the log, and worklogRoom is where the
// entries sit - a room of their own, so the timeline and the chat views can
// tell a seat's log from a conversation without reading each row's type.
const (
	worklogEventType = "worklog"
	worklogRoom      = "worklog"
)

// maxWorklogField is the ceiling on what an entry may say in one field. It is
// small on purpose: an entry is an index into the fabric, so a wall of text
// here is a document that belongs in a report, or a fact that belongs in
// memory, with the entry pointing at it by id. The refusal says which.
const maxWorklogField = 4_000

// maxWorklogRefs is how many artifacts one entry may reference. An entry that
// points at fifty things is not an index into what happened, it is a table of
// contents for the project.
const maxWorklogRefs = 50

// maxWorklogBranch is the ceiling on the branch or worktree an entry names. It
// is a git ref or a directory, not a sentence: something longer than this is a
// description of where the work was, and a description belongs in what. The run
// id and the verify status are held to the same ceiling for the same reason -
// each of them is a label, and a paragraph in one of them is a paragraph nobody
// will read there.
const maxWorklogBranch = 200

// defaultWorklogRead is how many entries a read hands back when the caller asks
// for no particular number - enough for a fresh seat to see the last few shifts
// and short enough to be read rather than skimmed.
const defaultWorklogRead = 20

// The meta keys an entry's own fields ride in. They are named here rather than
// spelled inline at each end, because the write, the read and speakerStripped
// all have to mean the same set - see worklogStampedKeys.
const (
	worklogMetaWhat    = "what"
	worklogMetaNext    = "next"
	worklogMetaAsOf    = "as_of"
	worklogMetaBranch  = "branch"
	worklogMetaRefs    = "refs"
	worklogMetaSubject = "subject"
	worklogMetaRun     = "run"
	worklogMetaVerify  = "verify"
)

// worklogStampedKeys are the entry fields this node stamps and a client may
// never hand in, in the sense the actor keys, the resolved mentions and a
// citation are already stamped - see speakerStripped in api.go, which drops
// them off any meta that arrives from outside.
//
// Which keys are on this list is decided by one question: does the field make a
// claim the node has checked? refs is checked against the writer's own read
// filter, and subject against the principals that exist here, so a client that
// could write either would be making the claim without the check - an entry
// pointing at work its author cannot see, or vouching for a seat that does not
// exist. run and verify are on it because they are the reason a vouched entry is
// worth anything: "the gate passed" is what a reader acts on, and a marker a
// client can paint onto somebody else's chronology through the generic event
// door is not a marker.
//
// what, next, as_of and branch are deliberately NOT on it. They are the entry's
// own prose about its own shift, they claim nothing about anybody else, and the
// body beside them was always a client's to write.
var worklogStampedKeys = map[string]bool{
	worklogMetaRefs:    true,
	worklogMetaSubject: true,
	worklogMetaRun:     true,
	worklogMetaVerify:  true,
}

// worklogAppendArgs is one entry as a writer asks for it. It is the argument
// list of all three doors: the MCP tool decodes JSON into it, POST /api/worklog
// takes it as its body, and the CLI fills it from flags.
type worklogAppendArgs struct {
	What   string   `json:"what"`
	Next   string   `json:"next"`
	AsOf   string   `json:"as_of"`
	Branch string   `json:"branch"`
	Refs   []string `json:"refs"`

	// Subject is whose work this entry is about, when that is not the writer.
	// See worklogSubject for the whole of what it means and why it is here.
	Subject string `json:"subject"`
	// Run is the run the work was done in, and Verify is what the gate said
	// about it. Both are what makes a vouched entry worth reading rather than
	// worth doubting: a harness writing on a run's behalf knows both and cannot
	// invent either, and a reader deciding whether to trust the entry is
	// deciding about those two facts.
	Run    string `json:"run"`
	Verify string `json:"verify"`
}

// worklogEntry is one entry as a reader gets it back: the stream's own fields,
// plus where in the log it sits and who wrote it.
type worklogEntry struct {
	ID      string   `json:"id"`
	Actor   string   `json:"actor"`
	Project *string  `json:"project"`
	What    string   `json:"what"`
	Next    string   `json:"next,omitempty"`
	AsOf    string   `json:"as_of,omitempty"`
	Branch  string   `json:"branch,omitempty"`
	Refs    []string `json:"refs"`

	// Subject is whose work the entry is about, and Vouched says it is not the
	// author's own. Vouched is derived from the two ids rather than stored: two
	// columns that can disagree about the same fact is one too many, and the
	// reader's question - "is this seat's own account, or somebody else's report
	// of it?" - is exactly subject != actor.
	Subject string `json:"subject,omitempty"`
	Vouched bool   `json:"vouched,omitempty"`
	Run     string `json:"run,omitempty"`
	Verify  string `json:"verify,omitempty"`

	SeqHLC  int64  `json:"seq_hlc"`
	Node    string `json:"node"`
	Created string `json:"created"`
}

// appendWorklogEntry writes one entry, and is the only thing in this program
// that does.
//
// It appends and never updates: there is no id argument, because an entry is a
// statement about a moment and a moment that can be edited afterwards is not
// one. Something that turned out to be wrong is corrected by the next entry
// saying so, which is what a chronology is.
//
// It takes the store rather than a server or an MCP server so that both doors
// can reach it without one of them importing the other. Every refusal below is
// therefore the same refusal, in the same words, whichever door asked - which is
// the property the gate asserts rather than the code path.
func appendWorklogEntry(ctx context.Context, db *store.DB, p *store.Principal,
	a worklogAppendArgs,
) (*store.Event, error) {
	// Every entry carries an actor, so a token that resolves to nobody cannot
	// write one. This is the same bar reportWrite keeps for ownership, and here
	// it is the invariant itself rather than a consequence of one.
	if p == nil || p.UserID == "" {
		return nil, errors.New("this token resolves to no user, so it cannot write a worklog entry")
	}
	// The ceilings before the two lookups: a field that is over one is refused
	// without asking the database anything.
	if err := worklogFieldFault(a); err != nil {
		return nil, err
	}

	refs, err := worklogRefs(ctx, db, p, a.Refs)
	if err != nil {
		return nil, err
	}
	// The actor is the token's, always - see chatActor. The subject is checked
	// against it, because vouching for yourself is authoring.
	actor, _ := chatActor(p)
	subject, err := worklogSubject(ctx, db, actor, a.Subject)
	if err != nil {
		return nil, err
	}

	e, err := worklogEvent(p, a, refs, subject)
	if err != nil {
		return nil, err
	}
	if err := db.AppendEvent(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

// worklogFieldFault answers why an entry's own fields are not writable, or nil
// when they are. It asks the database nothing, so the cheap refusals happen
// before the two lookups the refs and the subject need.
func worklogFieldFault(a worklogAppendArgs) error {
	what := strings.TrimSpace(a.What)
	if what == "" {
		return errors.New("an entry says what changed: what is required")
	}
	for _, field := range []struct{ name, value string }{
		{"what", what}, {"next", strings.TrimSpace(a.Next)},
	} {
		if len(field.value) > maxWorklogField {
			return fmt.Errorf("%s is %d bytes, over the %d ceiling - an entry indexes "+
				"what happened; write the document with report_write or the fact with "+
				"mem_write and reference it from refs",
				field.name, len(field.value), maxWorklogField)
		}
	}
	// The labels. Each is optional and stays optional - an entry written off a
	// branch is still an entry, and a reader narrowing by branch is narrowing
	// rather than filing - and each is a label rather than a sentence, so a
	// paragraph in one of them is a paragraph nobody will read there.
	for _, field := range []struct{ name, value, instead string }{
		{"branch", strings.TrimSpace(a.Branch),
			"name the ref or the worktree, and put the story in what"},
		{"run", strings.TrimSpace(a.Run), "name the run, and put what happened in what"},
		{"verify", strings.TrimSpace(a.Verify),
			"say what the gate said, and put the detail in what"},
	} {
		if len(field.value) > maxWorklogBranch {
			return fmt.Errorf("%s is %d bytes, over the %d ceiling - %s",
				field.name, len(field.value), maxWorklogBranch, field.instead)
		}
	}
	return nil
}

// worklogEvent builds the row an entry is, with the refs and the subject the
// checks above already settled.
//
// It touches nothing outside its arguments, which is what lets a test ask the
// question that matters about the vouched marker: whether it is inside the bytes
// this node signs. See worklog_test.go - the marker rides in meta, meta is
// folded into the event signature as its sha256, and a marker a relay could
// strip would be believed as authorship.
func worklogEvent(p *store.Principal, a worklogAppendArgs, refs []string,
	subject string,
) (*store.Event, error) {
	what, next := strings.TrimSpace(a.What), strings.TrimSpace(a.Next)
	// The actor, and the kind of thing it is, the way chat stamps it: a console
	// or a TUI reading the timeline tells a person from the agent working for
	// them without a join per row.
	actor, actorKind := chatActor(p)
	if refs == nil {
		refs = []string{}
	}
	meta, err := json.Marshal(map[string]any{
		"actor_kind":       actorKind,
		"actor_user":       p.UserID,
		worklogMetaWhat:    what,
		worklogMetaNext:    next,
		worklogMetaAsOf:    strings.TrimSpace(a.AsOf),
		worklogMetaBranch:  strings.TrimSpace(a.Branch),
		worklogMetaRefs:    refs,
		worklogMetaSubject: subject,
		worklogMetaRun:     strings.TrimSpace(a.Run),
		worklogMetaVerify:  strings.TrimSpace(a.Verify),
	})
	if err != nil {
		return nil, err
	}

	// An entry lands in the principal's home project, like every other write, so
	// the worklog is per-project the same way a room is. A token with no project
	// writes an entry of its own that only it can read, which is what a
	// principal with no project has for everything else here.
	var project *string
	if p.Project != "" {
		home := p.Project
		project = &home
	}
	return &store.Event{
		Type:    worklogEventType,
		Project: project,
		Room:    worklogRoom,
		Actor:   actor,
		Body:    worklogBody(what, next, subject),
		Meta:    meta,
	}, nil
}

// worklogRefs checks the artifact ids an entry references, and returns them
// cleaned up: blanks dropped, duplicates collapsed, the writer's order kept.
//
// A ref the caller cannot read is refused rather than stored, and the refusal
// names it. That is the invariant the surface exists to keep - an entry
// referencing work its author could not see is not an index into the fabric,
// it is an assertion about somebody else's - and it is the same check
// mayNameParents makes of an edge in the DAG, for the same reason: an id is a
// guess anybody can make.
//
// This function is the reason there is no second write path. Every door calls
// it, so "the HTTP door refuses what the MCP tool refuses" is not a promise two
// implementations keep in step - it is one implementation.
func worklogRefs(ctx context.Context, db *store.DB, p *store.Principal, asked []string) ([]string, error) {
	refs := make([]string, 0, len(asked))
	seen := map[string]bool{}
	for _, ref := range asked {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	if len(refs) > maxWorklogRefs {
		return nil, fmt.Errorf("an entry references %d artifacts, over the %d ceiling - "+
			"name the ones this shift was about", len(refs), maxWorklogRefs)
	}
	if len(refs) == 0 {
		return refs, nil
	}

	unreadable, err := db.UnreadableArtifacts(ctx, p, refs)
	if err != nil {
		return nil, err
	}
	if len(unreadable) > 0 {
		return nil, fmt.Errorf("ref %s is not an artifact you can read; a worklog entry "+
			"points at work that is in front of you, by id", unreadable[0])
	}
	return refs, nil
}

// worklogSubject resolves whose work an entry is about, and answers "" when the
// entry is the author's own account of their own shift.
//
// VOUCHED IS NOT AUTHORED, and this is where the difference is written down.
// The case that forces it is the drainer: a harness that runs agents in VMs
// knows the run id, the verify status and the diff, and it cannot lie about
// whether the gate passed, so it is the right author for an entry about a run
// that has ended. But an entry written BY the harness ABOUT an agent must never
// read as that agent's own word. That is the impersonation shape this fabric
// refuses everywhere else - a citation carrying its own copy of the quote, a
// meta naming a speaker the token is not - and the fix is the same one: say
// which of the two it is, on the row, rather than leave a reader to assume the
// friendlier reading.
//
// So the actor stays the token's, always, and the subject is a separate field.
// Both are on the row, and a reader can tell an account from a report of one.
//
// The subject has to name a principal that exists here, checked the way an
// addressee is - see mayAddress. A name nothing answers to is the worst
// available failure: the entry is written, it reads as a report about a seat,
// and no surface anywhere says the seat was a typo. It tells the caller nothing
// they could not already read, since every event in every room they can see
// carries an actor.
//
// A subject that resolves to the author is dropped rather than stored, because
// then there is nothing to vouch for: an entry about your own shift is your own
// account of it, which is what an entry with no subject already is. Absent and
// self are one state for the same reason an absent addressee and an empty one
// are one row.
func worklogSubject(ctx context.Context, db *store.DB, actor, asked string) (string, error) {
	subject := strings.TrimSpace(asked)
	if subject == "" || subject == actor {
		return "", nil
	}
	if len(subject) > maxWorklogBranch {
		return "", fmt.Errorf("subject is %d bytes, over the %d ceiling - it is the id of the "+
			"seat whose work this is, not a description of them", len(subject), maxWorklogBranch)
	}
	if _, err := db.GetUser(ctx, subject); err == nil {
		return subject, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if _, err := db.GetAgent(ctx, subject); err == nil {
		return subject, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	return "", errors.New("no principal called " + subject + " here: the subject of an entry " +
		"is the user or the agent whose work it is, and an entry with none is your own account " +
		"of your own shift")
}

// entryOf renders one event as the entry it is.
//
// What the entry said is read off meta, where the write put it, and the body is
// the fallback: an event of this type that arrived without meta - from a peer
// running a build that predates this surface, say - is still an entry and still
// says something, and dropping it from the read would be a gap in a chronology
// with nothing to say there was one.
func entryOf(e *store.Event) worklogEntry {
	entry := worklogEntry{
		ID: e.ID, Actor: e.Actor, Project: e.Project, What: e.Body,
		Refs: []string{}, SeqHLC: e.SeqHLC, Node: e.Node,
		Created: e.Created.UTC().Format("2006-01-02T15:04:05.999999Z07:00"),
	}
	var fields map[string]json.RawMessage
	if len(e.Meta) == 0 || json.Unmarshal(e.Meta, &fields) != nil {
		return entry
	}
	if what := metaString(fields, worklogMetaWhat); what != "" {
		entry.What = what
	}
	entry.Next, entry.AsOf = metaString(fields, worklogMetaNext), metaString(fields, worklogMetaAsOf)
	entry.Branch = metaString(fields, worklogMetaBranch)
	entry.Subject = metaString(fields, worklogMetaSubject)
	entry.Run, entry.Verify = metaString(fields, worklogMetaRun), metaString(fields, worklogMetaVerify)
	// Derived, not stored - see worklogEntry. A subject equal to the actor is a
	// seat's own account however it got written, including on a row some other
	// build wrote, so the read decides it rather than trusting a flag.
	entry.Vouched = entry.Subject != "" && entry.Subject != entry.Actor
	if raw, ok := fields[worklogMetaRefs]; ok {
		var refs []string
		if err := json.Unmarshal(raw, &refs); err == nil && refs != nil {
			entry.Refs = refs
		}
	}
	return entry
}

// worklogBody is what the entry reads as on every surface that renders an event
// body and knows nothing about this one - the timeline, the console's activity
// view, the TUI. What changed comes first on an authored entry, because that is
// the line those views show.
//
// A vouched entry says so ahead of it, and that is not decoration. The
// body is what a surface with no idea what a worklog is renders, and it is
// inside the row signature; an entry whose vouching lived only in a field that
// such a surface does not read would appear there as the author's own account of
// somebody else's shift, which is the one reading this design exists to prevent.
// So the marker travels with the sentence.
func worklogBody(what, next, subject string) string {
	body := what
	if subject != "" {
		body = "vouched for " + subject + "\n\n" + what
	}
	if next == "" {
		return body
	}
	return body + "\n\nnext: " + next
}

// ------------------------------------------------------------------- the API

// handleWorklogAppend appends one entry over HTTP.
//
// This is the door the agents that do the work actually have. It is a thin
// wrapper on purpose: every refusal, including the reference check and the
// subject resolution, is appendWorklogEntry's, so this endpoint cannot drift
// away from the MCP tool by being edited without it.
//
// A refusal is a 400 rather than a 403: what is wrong with the entry is a
// property of the entry - an id that is not readable, a subject nobody answers
// to, a field over its ceiling - and the caller's credential was fine. The
// message is the store's own words, because the writer is an agent and the
// words are what it has to act on.
//
// POST /api/worklog  {what, next?, as_of?, branch?, refs?, subject?, run?, verify?}
func (s *server) handleWorklogAppend(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req worklogAppendArgs
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	e, err := appendWorklogEntry(r.Context(), s.db, p, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	// The entry, and the fixture line when this seat is writing its chronology
	// into demo seed data - the same warning the MCP tool answers with, because
	// it is the same write. See mcp_projects.go.
	answer := map[string]any{"entry": entryOf(e)}
	if warning := fixtureWarning(r.Context(), s.db, p); warning != "" {
		answer["warning"] = warning
	}
	writeJSON(w, http.StatusOK, answer)
}

// --------------------------------------------------------------- the command

const worklogUsage = `flowy worklog - the chronology: what the last few seats did

usage:
  flowy worklog read [--limit N]
  flowy worklog append "what changed" [flags]
  echo "what changed" | flowy worklog append [flags]

  read is what to do when you pick up a seat: the most recent entries you may
  read, newest first. append is what to do before you stop.

  append flags:
  --next N       what the next seat should pick up, and what is in the way of it
  --as-of A      the commit, version or run id the entry is true of
  --branch B     the branch or worktree this shift worked in
  --ref ID       an artifact this entry is about, repeatable. An id you cannot
                 read is REFUSED - the worklog is an index into what is here
  --subject WHO  the seat whose work this is, when it is not yours. The entry is
                 then VOUCHED rather than authored: you stay its writer and it
                 says whose shift it reports on. Use it when you are a harness
                 writing on a run's behalf, and never to sign as somebody else
  --run ID       the run the work was done in
  --verify S     what the gate said about it

  common flags:
  --url URL      node to tell (default $FLOWY_ADDR, then http://127.0.0.1:8787)
  --token T      bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)
  --agent NAME   the seat speaking, whose token is ~/.config/flowy/agents/NAME
                 (default $FLOWY_AGENT). ~/.config/flowy/token is the OPERATOR'S
                 own, so falling through to it warns; --agent me is the operator
                 saying it was meant, and stops the warning

  The text of an entry is one argument, or stdin when there is none.

  Exit 0 when the node accepted it, 2 when it did not. A refusal is a failure
  here rather than a JSON body to remember to read, for the reason flowy say
  exits the same way: an entry nobody recorded must not look like one that was.
`

// worklogCmd is `flowy worklog`.
//
// The whole reason this command exists: a spawned agent gets no MCP server, by
// design, so the seats doing the work were the only ones that could not write
// the log about it. This speaks HTTP for the reason `flowy say` does - what an
// agent in a VM has is a token and a node, not a DSN.
func worklogCmd(args []string) error {
	if len(args) > 0 && args[0] == "help" {
		fmt.Print(worklogUsage)
		return nil
	}
	// read and append are named rather than inferred. A verb that guessed from
	// the flags would guess wrong in the direction that costs something: a typo
	// in an append silently reading the log looks like an entry that was written.
	sub := "read"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, rest = args[0], args[1:]
	}

	fs := flag.NewFlagSet("worklog", flag.ContinueOnError)
	limit := fs.Int("limit", 0, "how many entries to read (default 20)")
	next := fs.String("next", "", "what the next seat should pick up")
	asOf := fs.String("as-of", "", "the commit, version or run id this is true of")
	branch := fs.String("branch", "", "the branch or worktree this shift worked in")
	subject := fs.String("subject", "", "the seat whose work this is, when it is not yours")
	run := fs.String("run", "", "the run the work was done in")
	verify := fs.String("verify", "", "what the gate said about it")
	urlFlag := fs.String("url", "", "node to talk to (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token := fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)")
	agent := fs.String("agent", "", agentFlagHelp)
	var refs stringList
	fs.Var(&refs, "ref", "an artifact this entry is about, repeatable")
	if err := fs.Parse(rest); err != nil {
		return err
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

	switch sub {
	case "read":
		return readWorklog(ctx, client, base, bearer, *limit)
	case "append":
		what, err := sayBody(fs.Args())
		if err != nil {
			return err
		}
		if strings.TrimSpace(what) == "" {
			return errors.New("an entry says what changed: pass it as an argument or on stdin\n\n" +
				worklogUsage)
		}
		return appendWorklog(ctx, client, base, bearer, worklogAppendArgs{
			What: what, Next: *next, AsOf: *asOf, Branch: *branch, Refs: refs,
			Subject: *subject, Run: *run, Verify: *verify,
		})
	}
	return fmt.Errorf("unknown worklog command %q\n\n%s", sub, worklogUsage)
}

// stringList is a flag that may be given more than once - `--ref a --ref b`.
// Repetition rather than a comma-separated list, because an artifact id is
// opaque and a separator inside one would be a ref silently cut in half.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

// appendWorklog posts one entry and reports what the node did with it.
func appendWorklog(ctx context.Context, client *http.Client, base, token string,
	a worklogAppendArgs,
) error {
	payload, err := json.Marshal(a)
	if err != nil {
		return err
	}
	var answer struct {
		Entry   worklogEntry `json:"entry"`
		Warning string       `json:"warning"`
	}
	if err := peerRequest(ctx, client, http.MethodPost, base+"/api/worklog",
		token, payload, &answer); err != nil {
		return err
	}

	// The id on stdout, so a script can hand it on; everything a person reads
	// goes to stderr, which is how `flowy say` keeps stdout parseable.
	fmt.Println(answer.Entry.ID)
	if answer.Entry.Vouched {
		fmt.Fprintf(os.Stderr, "appended, VOUCHED for %s - it says you wrote it about their work\n",
			answer.Entry.Subject)
	} else {
		fmt.Fprintln(os.Stderr, "appended to the worklog")
	}
	if answer.Warning != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", answer.Warning)
	}
	return nil
}

// readWorklog prints the recent entries, newest first.
//
// It reads through GET /api/activity narrowed to the kind, which is where the
// permission filter already is, rather than through a worklog read endpoint of
// its own. There is deliberately no second read door: narrowing an existing read
// opens nothing, and a new one would be a second place for that filter to be
// missing. The write needed a verb of its own because it has arguments the
// generic door cannot check; the read has none.
func readWorklog(ctx context.Context, client *http.Client, base, token string, limit int) error {
	if limit <= 0 {
		limit = defaultWorklogRead
	}
	var page struct {
		Items []activityItem `json:"items"`
	}
	url := base + "/api/activity?kind=" + worklogEventType + "&order=recent&limit=" +
		strconv.Itoa(limit)
	if err := peerRequest(ctx, client, http.MethodGet, url, token, nil, &page); err != nil {
		return err
	}
	if len(page.Items) == 0 {
		// Not "nothing happened". A chronology that answers an empty read with
		// silence is a chronology that lies about the one thing it is for.
		fmt.Fprintln(os.Stderr, "no entries you can read - which is not the same as "+
			"nothing having happened")
		return nil
	}
	for _, item := range page.Items {
		printWorklogItem(item)
	}
	fmt.Fprintf(os.Stderr, "%d entr%s, newest first\n", len(page.Items),
		plural(len(page.Items), "y", "ies"))
	return nil
}

// printWorklogItem writes one entry for a person to read.
//
// A vouched entry is drawn as vouched here as well, because this is one of the
// places somebody reads the worklog: an entry the harness wrote about a run
// printed as that run's own account of itself would be the marker buying
// nothing. The author and the subject are both on the line, in that order,
// which is the order the reader's question comes in.
func printWorklogItem(item activityItem) {
	entry := entryOf(&store.Event{
		ID: item.ID, Actor: item.Actor, Body: item.Body, Meta: item.Meta,
	})
	who := entry.Actor
	if entry.Vouched {
		who = entry.Actor + " VOUCHING FOR " + entry.Subject
	}
	fmt.Printf("%s  %s\n", item.Created, who)
	for _, label := range []struct{ name, value string }{
		{"branch", entry.Branch}, {"as of", entry.AsOf},
		{"run", entry.Run}, {"verify", entry.Verify},
	} {
		if label.value != "" {
			fmt.Printf("  %s: %s\n", label.name, label.value)
		}
	}
	fmt.Printf("  %s\n", entry.What)
	if entry.Next != "" {
		fmt.Printf("  next: %s\n", entry.Next)
	}
	if len(entry.Refs) > 0 {
		fmt.Printf("  about: %s\n", strings.Join(entry.Refs, " "))
	}
	fmt.Println()
}

// plural picks the ending for a count, so a report of one entry does not say
// "1 entries".
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
