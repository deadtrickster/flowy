package flowy

// The verbs an agent reaches for and did not have, each bought by a count.
//
// Measured on lab2x1 on 2026-09-14, over one seat's transcripts (7341 tool
// calls, 856 of them flowy): 163 hand-built curls to doors this client had no
// verb for, 52 reads of `--help` (25 of them the top-level menu, which did not
// list `get`, `dm`, `waiter` or `nag` at all), three guessed verbs that do not
// exist (`read`, `roster`, `artifact`), six guessed JSON shapes for the
// attachment door, and 19 fetches of a node-instructions row the seat cannot
// read. By door, what the curls were for:
//
//	76  GET /api/artifacts, /api/artifact/{id}   listing and reading rows, half of them kind=skill
//	21  GET /api/metrics*                        (a dashboard's business, left alone here)
//	12  GET /api/chat/general                    the last few messages - a mention's antecedents
//	10  GET /api/search
//	 8  GET/POST /api/attachment
//	 3  GET /api/presence                        "who is listening"
//
// `flowy get` already answers every one of those generically, and the count
// above was taken on a box whose binary predates it. What a generic door does
// not give is a NAME an agent can guess: `flowy read` and `flowy roster` were
// both tried, verbatim, before the curl. So the four most-guessed intents get
// verbs, thin over the same client `get` uses, and the menu lists everything.
//
// `flowy instructions` is the one that costs the most per miss. The seat brief
// says to fetch a node-instructions artifact by id at the start of every
// session and after every compaction; the row is in another project, the
// fetch 404s, and the 404 reads as "somebody gave me a stale id". The node has
// GET /api/instructions, an ORDERED composition of node, project and seat
// rules for the caller's own principal, and nothing on the client spelled it.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// agentClient resolves the node and the token the way every verb does, and
// hands back a request function. One place, so a new verb cannot get the
// token resolution and its operator warning wrong.
func agentClient(urlFlag, tokenFlag, agentFlag string) (func(ctx context.Context, method, path string, body []byte, into any) error, error) {
	base := resolveURL(urlFlag, os.Getenv("FLOWY_ADDR"))
	bearer, err := resolveToken(tokenFlag, os.Getenv("FLOWY_TOKEN"), agentFlag, os.Getenv("FLOWY_AGENT"))
	if err != nil {
		return nil, err
	}
	if bearer == "" {
		return nil, errNoToken()
	}
	client := &http.Client{Timeout: 60 * time.Second}
	return func(ctx context.Context, method, path string, body []byte, into any) error {
		return peerRequest(ctx, client, method, base+path, bearer, body, into)
	}, nil
}

// eventLine is one message as a person reads it: when, who, to whom, the id
// and the thread, then the body indented. The same shape `inbox` prints in its
// stderr summary, so a message read here and a message delivered there look
// alike.
func eventLine(e *store.Event) string {
	var meta map[string]any
	_ = json.Unmarshal(e.Meta, &meta)
	who := e.Actor
	if n, _ := meta["actor_name"].(string); n != "" {
		who = n
	}
	if k, _ := meta["actor_kind"].(string); k == "user" {
		who += " (person)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s", e.Created.UTC().Format("2006-01-02 15:04"), who)
	if e.AddresseeName != "" {
		fmt.Fprintf(&b, " -> %s", e.AddresseeName)
	} else if e.Addressee != "" {
		fmt.Fprintf(&b, " -> %s", e.Addressee)
	}
	fmt.Fprintf(&b, "  id %s", e.ID)
	if e.Thread != "" {
		fmt.Fprintf(&b, "  thread %s", e.Thread)
	}
	b.WriteString("\n")
	for _, line := range strings.Split(strings.TrimRight(e.Body, "\n"), "\n") {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// ------------------------------------------------------------- read

const readUsage = `flowy read - the last few messages in a room, oldest first

usage:
  flowy read [--room R] [--last N] [--thread ID] [--json]

  --room R      default general
  --last N      how many, default 20
  --thread ID   only this thread
  --json        the node's events, one JSON object per line, instead of prose

The antecedents of a mention: a delivery hands you one message, and this is
how you see what it answers. It moves no cursor and wakes nobody.
`

func readCmd(args []string) error {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	room := fs.String("room", "general", "the room to read")
	last := fs.Int("last", 20, "how many of the most recent messages")
	thread := fs.String("thread", "", "only this thread")
	asJSON := fs.Bool("json", false, "one JSON object per line instead of prose")
	urlFlag := fs.String("url", "", "node to talk to")
	token := fs.String("token", "", "bearer token")
	agent := fs.String("agent", "", agentFlagHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 && fs.Args()[0] == "help" {
		fmt.Print(readUsage)
		return nil
	}
	if *last <= 0 {
		return errors.New("--last is a positive count")
	}
	call, err := agentClient(*urlFlag, *token, *agent)
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("limit", fmt.Sprint(*last))
	q.Set("order", "recent")
	if *thread != "" {
		q.Set("thread", *thread)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var page struct {
		Events []*store.Event `json:"events"`
	}
	if err := call(ctx, http.MethodGet, "/api/chat/"+url.PathEscape(*room)+"?"+q.Encode(), nil, &page); err != nil {
		return err
	}
	// `recent` answers newest first; a person reads a conversation the other
	// way, and so does an agent handed it as context.
	for i, j := 0, len(page.Events)-1; i < j; i, j = i+1, j-1 {
		page.Events[i], page.Events[j] = page.Events[j], page.Events[i]
	}
	enc := json.NewEncoder(os.Stdout)
	for _, e := range page.Events {
		if *asJSON {
			if err := enc.Encode(e); err != nil {
				return err
			}
			continue
		}
		fmt.Print(eventLine(e))
	}
	if len(page.Events) == 0 {
		fmt.Fprintf(os.Stderr, "nothing in #%s that this token can read\n", *room)
	}
	return nil
}

// ------------------------------------------------------------- skills

const skillsUsage = `flowy skills - the skills on the shelf: rows of kind=skill, readable from here

usage:
  flowy skills                     list: id, visibility, title
  flowy skills show ID             the body of one, on stdout
  flowy skills file --title T [--scope S] [body]
                                   put one on the shelf: the body on stdin or
                                   as the argument; scope personal, project or
                                   shared (default project - a skill is for
                                   the people who work here)
  flowy skills [--project P]       another project's shelf, if the token reaches it

A skill is an artifact with kind=skill (not type=skill - that filter answers
nothing, and was tried). Reading one costs the row's body and nothing else.
`

func skillsCmd(args []string) error {
	// The subcommand comes first and the flags after it - `flowy skills file
	// --title T` - and Go's flag parser stops at the first bare word, so the
	// word is taken off before the flags are read.
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("skills", flag.ContinueOnError)
	project := fs.String("project", "", "another project's shelf")
	title := fs.String("title", "", "for `file`: one line naming the skill")
	scope := fs.String("scope", "project", "for `file`: personal, project or shared")
	urlFlag := fs.String("url", "", "node to talk to")
	token := fs.String("token", "", "bearer token")
	agent := fs.String("agent", "", agentFlagHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if sub == "help" || (len(rest) > 0 && rest[0] == "help") {
		fmt.Print(skillsUsage)
		return nil
	}
	call, err := agentClient(*urlFlag, *token, *agent)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if sub == "file" {
		if strings.TrimSpace(*title) == "" {
			return errors.New("a skill has a title: flowy skills file --title T\n\n" + skillsUsage)
		}
		body, err := bodyOrStdin(rest, "skills file", skillsUsage)
		if err != nil {
			return err
		}
		visibility, err := scopeVisibility(*scope)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{
			"type": store.MemoryType, "kind": "skill",
			"title": *title, "body": body, "visibility": visibility,
		})
		if err != nil {
			return err
		}
		var answer struct {
			ID string `json:"id"`
		}
		if err := call(ctx, http.MethodPost, "/api/artifacts", payload, &answer); err != nil {
			return err
		}
		fmt.Println(answer.ID)
		fmt.Fprintf(os.Stderr, "filed skill %s at scope %s; `flowy skills show %s` reads it back\n",
			answer.ID, *scope, answer.ID)
		return nil
	}
	if sub == "show" {
		if len(rest) < 1 || strings.TrimSpace(rest[0]) == "" {
			return errors.New("which one: flowy skills show ID\n\n" + skillsUsage)
		}
		var art store.Artifact
		if err := call(ctx, http.MethodGet, "/api/artifact/"+url.PathEscape(rest[0]), nil, &art); err != nil {
			return err
		}
		if art.Kind != "skill" {
			fmt.Fprintf(os.Stderr, "note: %s is kind=%q, not a skill; printing it anyway\n", art.ID, art.Kind)
		}
		fmt.Fprintf(os.Stderr, "%s  %s  (%s, updated %s)\n", art.ID, art.Title, art.Visibility,
			art.Updated.UTC().Format(time.RFC3339))
		fmt.Print(art.Body)
		if !strings.HasSuffix(art.Body, "\n") {
			fmt.Println()
		}
		return nil
	}
	if sub != "" || len(rest) > 0 {
		return fmt.Errorf("flowy skills takes `show ID` or `file --title T`, got %q\n\n%s", sub+strings.Join(rest, " "), skillsUsage)
	}
	q := url.Values{}
	q.Set("kind", "skill")
	q.Set("limit", "200")
	if *project != "" {
		q.Set("project", *project)
	}
	var page struct {
		Artifacts []*store.Artifact `json:"artifacts"`
	}
	if err := call(ctx, http.MethodGet, "/api/artifacts?"+q.Encode(), nil, &page); err != nil {
		return err
	}
	if len(page.Artifacts) == 0 {
		fmt.Fprintln(os.Stderr, "no skills this token can read")
		return nil
	}
	for _, a := range page.Artifacts {
		fmt.Printf("%s  %-8s  %s\n", a.ID, a.Visibility, a.Title)
	}
	fmt.Fprintf(os.Stderr, "%d skill(s); `flowy skills show ID` for one\n", len(page.Artifacts))
	return nil
}

// ------------------------------------------------------------- attach

const attachUsage = `flowy attach - put a file on the node, as an attachment row

usage:
  flowy attach [--title T] [--type MIME] [--body TEXT] [--room R] [--message ID] FILE

  FILE          LAST, after the flags. Go's flag parser stops at the first
                argument that is not a flag, so FILE first leaves every flag
                after it unparsed and this verb refuses with "which file"
                while holding the file you named.
  --title T     default the file's name
  --type MIME   default from the extension, else application/octet-stream
  --body TEXT   a note beside it
  --room R      the room this belongs to
  --message ID  the ID of a message it hangs from, not its text. An id you
                cannot read is refused by the node, which reads as a
                permission problem and is a type error.

Prints the attachment's id. The node's body shape is content_base64 plus
filename plus content_type; six other shapes were tried by hand before this
verb existed, and every one was refused by field name.

A file attached to a message does NOT make the message carry it: the ids a
message carries ride inside its signature, so they are written when it is said
and cannot be added afterwards. This verb attaches a file TO a conversation;
saying a message WITH a file is a different operation.
`

func attachCmd(args []string) error {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	title := fs.String("title", "", "title, default the file's name")
	ctype := fs.String("type", "", "content type, default from the extension")
	body := fs.String("body", "", "a note beside the file")
	room := fs.String("room", "", "also say it in this room")
	// THE ID OF A MESSAGE, NOT ITS TEXT. This read "the room message, with
	// --room", and two seats passed prose to it: the node resolves the value
	// through readableMessage, so a sentence comes back as "message <that
	// sentence> is not one you can read", which reads as a permission problem
	// and is a type error. The MCP surface for the same field has always said
	// id; this line was the outlier.
	message := fs.String("message", "", "id of the message this hangs from, with --room")
	urlFlag := fs.String("url", "", "node to talk to")
	token := fs.String("token", "", "bearer token")
	agent := fs.String("agent", "", agentFlagHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) > 0 && rest[0] == "help" {
		fmt.Print(attachUsage)
		return nil
	}
	// "WHICH FILE" IS THE WRONG ANSWER WHEN A FILE WAS NAMED. Go's flag parser
	// stops at the first non-flag argument, so `attach FILE --title x` leaves
	// [FILE --title x] in fs.Args() and the count test refuses with a question
	// the caller already answered - while the usage line above printed FILE
	// first, which is the order that cannot work. The order is fixed there; this
	// says so for anybody who typed it the old way, and names the file it is
	// holding so there is no doubt it was received.
	if len(rest) > 1 {
		for _, a := range rest[1:] {
			if strings.HasPrefix(a, "-") {
				return errors.New("the flags come before FILE: got " + rest[0] +
					" and then " + a + ", which this could not parse as a flag " +
					"because the file name ended the flags\n\n" +
					"  flowy attach " + strings.Join(rest[1:], " ") + " " + rest[0] + "\n")
			}
		}
	}
	if len(rest) != 1 {
		return errors.New("which file: flowy attach [flags] FILE\n\n" + attachUsage)
	}
	raw, err := os.ReadFile(rest[0])
	if err != nil {
		return err
	}
	name := filepath.Base(rest[0])
	if *title == "" {
		*title = name
	}
	if *ctype == "" {
		*ctype = mimeByExtension(name)
	}
	call, err := agentClient(*urlFlag, *token, *agent)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(attachmentWriteArgs{
		Title:    *title,
		Content:  base64.StdEncoding.EncodeToString(raw),
		Type:     *ctype,
		Filename: name,
		Body:     *body,
		Room:     *room,
		Message:  *message,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	var out struct {
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
		Size   int64  `json:"size_bytes"`
		Digest string `json:"digest_sha256"`
	}
	if err := call(ctx, http.MethodPost, "/api/attachment", payload, &out); err != nil {
		return err
	}
	fmt.Println(out.Item.ID)
	fmt.Fprintf(os.Stderr, "attached %s (%d bytes, sha256 %s) as %s\n", name, out.Size, out.Digest, *ctype)
	return nil
}

func mimeByExtension(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".txt", ".log":
		return "text/plain"
	case ".md":
		return "text/markdown"
	case ".json", ".jsonl":
		return "application/json"
	case ".html":
		return "text/html"
	case ".pdf":
		return "application/pdf"
	case ".csv":
		return "text/csv"
	}
	return "application/octet-stream"
}

// ------------------------------------------------------------- roster

const rosterUsage = `flowy roster - who is listening, according to the node

usage:
  flowy roster [--json]

One line per listener: seat, state, waiter kind, the process behind it and
where it is, when it last polled. "attached" is the node's reading of the poll,
not the seat's claim about itself.
`

func rosterCmd(args []string) error {
	fs := flag.NewFlagSet("roster", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "the node's answer, verbatim")
	urlFlag := fs.String("url", "", "node to talk to")
	token := fs.String("token", "", "bearer token")
	agent := fs.String("agent", "", agentFlagHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 && fs.Args()[0] == "help" {
		fmt.Print(rosterUsage)
		return nil
	}
	call, err := agentClient(*urlFlag, *token, *agent)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var raw json.RawMessage
	if err := call(ctx, http.MethodGet, "/api/presence", nil, &raw); err != nil {
		return err
	}
	if *asJSON {
		os.Stdout.Write(raw)
		fmt.Println()
		return nil
	}
	var answer struct {
		Listeners []struct {
			Reader   string `json:"reader"`
			Project  string `json:"project"`
			Attached bool   `json:"attached"`
			Kind     string `json:"waiter_kind"`
			State    string `json:"state"`
			LastPoll string `json:"last_poll_at"`
			Process  *struct {
				Pid  int    `json:"waiter_pid"`
				Host string `json:"waiter_host"`
			} `json:"process"`
		} `json:"listeners"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return fmt.Errorf("the node's presence answer was not the shape expected: %w", err)
	}
	for _, l := range answer.Listeners {
		where := "-"
		if l.Process != nil && l.Process.Pid != 0 {
			where = fmt.Sprintf("pid %d on %s", l.Process.Pid, l.Process.Host)
		}
		state := l.State
		if !l.Attached {
			state = "NOT attached"
		}
		fmt.Printf("%-20s %-6s %-14s %-8s %-40s last poll %s\n",
			l.Reader, l.Project, state, l.Kind, where, l.LastPoll)
	}
	if len(answer.Listeners) == 0 {
		fmt.Fprintln(os.Stderr, "nobody is listening in any project this token reaches")
	}
	return nil
}

// ------------------------------------------------------------- instructions

const instructionsUsage = `flowy instructions - the rules that apply to this seat, in the order they bind

usage:
  flowy instructions [--json]

Node rules, then the project's, then the seat's own - GET /api/instructions,
composed for the calling principal. This is what to read at the start of a
session and after a compaction, instead of fetching a row by id: a row lives
in one project and a token reads what it reaches, so an id from a brief
answers 404 to a seat in another project and reads as a stale id.

An empty answer is "nobody has filed instructions for this seat yet", and it
is said so, because an empty list and a wrong door look alike.
`

func instructionsCmd(args []string) error {
	fs := flag.NewFlagSet("instructions", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "the node's answer, verbatim")
	urlFlag := fs.String("url", "", "node to talk to")
	token := fs.String("token", "", "bearer token")
	agent := fs.String("agent", "", agentFlagHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 && fs.Args()[0] == "help" {
		fmt.Print(instructionsUsage)
		return nil
	}
	call, err := agentClient(*urlFlag, *token, *agent)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var raw json.RawMessage
	if err := call(ctx, http.MethodGet, "/api/instructions", nil, &raw); err != nil {
		return err
	}
	if *asJSON {
		os.Stdout.Write(raw)
		fmt.Println()
		return nil
	}
	var answer struct {
		Order        []string `json:"order"`
		Instructions []struct {
			ID    string `json:"id"`
			Scope string `json:"scope"`
			Seat  string `json:"seat"`
			Title string `json:"title"`
			Body  string `json:"body"`
		} `json:"instructions"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return fmt.Errorf("the node's instructions answer was not the shape expected: %w", err)
	}
	if len(answer.Instructions) == 0 {
		fmt.Fprintf(os.Stderr, "no instructions are filed for this seat (order would be %s). "+
			"The seat brief on disk stands until somebody files them - "+
			"`flowy instructions` is a door, not a claim that rules exist.\n",
			strings.Join(answer.Order, " > "))
		return nil
	}
	for i, a := range answer.Instructions {
		scope := a.Scope
		if a.Seat != "" {
			scope += " " + a.Seat
		}
		fmt.Printf("## %d. %s  (%s, %s)\n\n%s\n\n", i+1, a.Title, scope, a.ID, strings.TrimRight(a.Body, "\n"))
	}
	return nil
}

// nodeBuild is the node's version string, or false when nothing answers within
// a few seconds. No token: GET /api/node is open, and `flowy version` must not
// start warning about credentials.
func nodeBuild() (string, bool) {
	base := resolveURL("", os.Getenv("FLOWY_ADDR"))
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(base + "/api/node")
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var answer struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil || answer.Version == "" {
		return "", false
	}
	return answer.Version, true
}

const retireUsage = `flowy retire - tombstone a row this seat superseded

usage:
  flowy retire <id> [--dry-run] [--json]
  flowy retire [--dry-run] <id>

The id goes first or last. Between flags it is not found - that would need
knowing which flags take values.

Tombstones an artifact: it stops appearing in listings, and its id answers
410 Gone rather than 404 - a stale reference reads as superseded rather than
mistyped. Consolidating a shift into one report leaves the rows it replaced
behind, and until this verb they were retired with a hand-built curl.

Only the owner may retire a row. A row filed by another seat answers 403 and
says who filed it - that is an authorisation question, not a spelling one.

--dry-run resolves the id and prints what would go, writing nothing.
`

// retireCmd tombstones one artifact.
//
// 01M2GVZMSC1V4KK2PPCT2ANVQG. The door existed - POST /api/artifact/<id>/delete
// - and no verb reached it, so lubuntu3-glm probed DELETE /api/artifacts/<id>,
// got 404 from the wrong spelling, and filed a node feature ask for something
// already built. A wrong path and a missing feature are the same 404.
// retireArgs is what `retire` was asked to do, separated from doing it so the
// argument handling can be asserted without a node. The defect it exists for -
// flags after the id being silently ignored - is invisible to a test that only
// checks refusals, because a refusal is what both the broken and the fixed
// version produce for a bad call. What distinguishes them is an ACCEPTED call
// whose flags took effect.
type retireArgs struct {
	id     string
	dryRun bool
	asJSON bool
	url    string
	token  string
	agent  string
	help   bool
}

func parseRetireArgs(args []string) (retireArgs, error) {
	var out retireArgs
	fs := flag.NewFlagSet("retire", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dryRun := fs.Bool("dry-run", false, "resolve and print, write nothing")
	asJSON := fs.Bool("json", false, "the node's answer, verbatim")
	urlFlag := fs.String("url", "", "node to talk to")
	token := fs.String("token", "", "bearer token")
	agent := fs.String("agent", "", agentFlagHelp)
	// THE ID BEFORE ITS FLAGS, which is how anybody types it. Go's flag package
	// stops at the first non-flag argument, so `retire ID --dry-run` parses
	// zero flags and --dry-run lands in Args() - a dry run that writes. Same
	// defect as 2c729ff on skills; found by running the verb, not reading it.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		out.id, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return out, err
	}
	rest := fs.Args()
	if out.id == "" && len(rest) > 0 {
		out.id, rest = rest[0], rest[1:]
	}
	out.dryRun, out.asJSON = *dryRun, *asJSON
	out.url, out.token, out.agent = *urlFlag, *token, *agent
	if out.id == "help" {
		out.help = true
		return out, nil
	}
	out.id = strings.TrimSpace(out.id)
	if out.id == "" || len(rest) > 0 {
		return out, fmt.Errorf("one id, and only one: retiring the wrong row is not undone by running it again")
	}
	return out, nil
}

func retireCmd(args []string) error {
	a, err := parseRetireArgs(args)
	if err != nil {
		fmt.Print(retireUsage)
		return err
	}
	if a.help {
		fmt.Print(retireUsage)
		return nil
	}
	id, dryRun, asJSON := a.id, &a.dryRun, &a.asJSON

	call, err := agentClient(a.url, a.token, a.agent)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// READ BEFORE WRITING, always - and it is the whole of --dry-run. The id
	// is typed by hand from a listing, and a mistyped ULID that happens to
	// exist names somebody else's row.
	var before json.RawMessage
	if err := call(ctx, http.MethodGet, "/api/artifact/"+id, nil, &before); err != nil {
		return err
	}
	var row struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Kind   string `json:"kind"`
		Author string `json:"author"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(before, &row); err != nil {
		return fmt.Errorf("the node's artifact answer was not the shape expected: %w", err)
	}

	if *dryRun {
		fmt.Printf("would retire %s  %s  %s\n  %s\n", row.ID, row.Kind, row.Status, row.Title)
		fmt.Printf("  filed by %s - nothing was written\n", row.Author)
		return nil
	}

	var after json.RawMessage
	if err := call(ctx, http.MethodPost, "/api/artifact/"+id+"/delete", nil, &after); err != nil {
		return err
	}
	if *asJSON {
		os.Stdout.Write(after)
		fmt.Println()
		return nil
	}
	fmt.Printf("retired %s  %s\n  %s\n", row.ID, row.Kind, row.Title)
	// 410, not 404, and the difference is the point: a retired row is
	// distinguishable from one that never existed, so a stale reference to it
	// reads as "superseded" rather than "you mistyped the id".
	fmt.Printf("  tombstoned: gone from listings; the id answers 410 Gone, not 404\n")
	return nil
}
