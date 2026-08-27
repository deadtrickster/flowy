package flowy

// `flowy todo` and `flowy note` - the memory door from a shell.
//
// WHY: an agent with MCP files a row with one tool call. A SEAT WITH A SHELL
// had no verb at all, so it spawns `flowy mcp` over stdio and hand-builds the
// initialize / initialized / tools-call sequence as JSON-RPC, then unwraps
// content[0].text as JSON to find the id. I have written that shim from memory
// perhaps thirty times in one session - about 300 tokens of protocol re-authored
// into context each time, against roughly 30 for the line below - and it is
// what filed three rows at a scope nobody could read, because a shim knows the
// wire format and nothing about what a row is for.
//
// A pipeline retyped from memory is a verb that does not exist yet. This is
// that verb, for the four writes a seat actually makes: file one, note on one,
// take one, close one.
//
// Reading is NOT here. `flowy queue` is the queue's own reader and the console
// is the board; two verbs over one list is the duplication this file exists to
// remove.

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

const todoUsage = `flowy todo - the queue, from a shell

usage:
  flowy todo file  --title T [--room R] [--category C] [--assignee A] [--scope S] [body]
  flowy todo note  --id ID [text]
  flowy todo claim --id ID [--as WHO] [--expect WHO]
  flowy todo waiting-on --id ID --of WHO [what you asked]
  flowy todo done  --id ID [--status S] [--note "what was measured"] [--duplicate-of ID]

The body, and a note's text, is the argument or stdin.

  --room      file it into a room, so the row arrives where the work was agreed
              rather than only on the board. Without it the row is filed
              straight onto the board.
  --category  bug, feature, chore or question - the closed set the queue counts.
  --scope     who may read it (default project). A queue row only its author can
              read is not on the queue.
  --as        on claim: who takes it, defaulting to this token's own handle.
              "" or "nobody" puts the row back on the unowned pile, which is how
              work is handed back rather than handed to somebody.
  --expect    on claim: who you read as carrying it just before you decided to
              take it, "" for a row nobody held. The write is then refused,
              naming whoever got there first, if the row moved in between - so
              of two agents claiming one row, exactly one comes away with it.
  --of        on waiting-on: who owes the next move. It does NOT change who is
              carrying the row - that is the whole point, and claim is the
              verb that hands work over. "" takes the question back.
              "me" resolves to this token's own handle rather than being stored
              as a word no roster can answer for.
  --status    on done: todo, active or done (default done).

Ids go to stdout and everything a person reads goes to stderr.
`

const noteKindUsage = `flowy note - a memory, from a shell

usage:
  flowy note write --title T [--scope S] [body]

A note is one agent's own record by default - scope personal - which is what
separates it from a queue row. Say --scope project to write one the room can
read.
`

// todoCmd is `flowy todo ...`.
func todoCmd(args []string) error {
	sub := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "file", "raise":
		return cliTodoFile(args)
	case "note":
		return cliTodoNote(args)
	case "claim", "take":
		return cliTodoClaim(args)
	case "waiting-on", "waiting", "ask":
		return cliTodoWaitingOn(args)
	case "done", "close", "status":
		return cliTodoDone(args)
	case "help", "-h", "--help":
		fmt.Print(todoUsage)
		return nil
	case "":
		return errors.New("flowy todo takes a command\n\n" + todoUsage)
	default:
		return fmt.Errorf("unknown todo command %q\n\n%s", sub, todoUsage)
	}
}

// noteCmd is `flowy note ...`.
func noteCmd(args []string) error {
	sub := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "write", "file", "":
		return cliNoteWrite(args)
	case "help", "-h", "--help":
		fmt.Print(noteKindUsage)
		return nil
	default:
		return fmt.Errorf("unknown note command %q\n\n%s", sub, noteKindUsage)
	}
}

// nodeFor resolves the node and the credential the same way every other verb
// here does, and refuses rather than guessing.
func nodeFor(urlFlag, token, agent string) (base, bearer string, err error) {
	base = resolveURL(urlFlag, os.Getenv("FLOWY_ADDR"))
	bearer, err = resolveToken(token, os.Getenv("FLOWY_TOKEN"), agent, os.Getenv("FLOWY_AGENT"))
	if err != nil {
		return "", "", err
	}
	if bearer == "" {
		return "", "", errNoToken()
	}
	return base, bearer, nil
}

// doorFlags are the three every one of these verbs takes.
func doorFlags(fs *flag.FlagSet) (url, token, agent *string) {
	url = fs.String("url", "", "node to talk to (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token = fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)")
	agent = fs.String("agent", "", agentFlagHelp)
	return url, token, agent
}

// scopeVisibility turns a scope word into the visibility it means, refusing
// anything outside the three. The membership test is explicit because
// store.VisibilityForScope hands an unknown word straight back - it is a lookup
// with a passthrough, not a validator.
func scopeVisibility(scope string) (string, error) {
	if !slices.Contains(store.MemScopes, scope) {
		return "", fmt.Errorf("scope %q is not one of %s", scope, strings.Join(store.MemScopes, ", "))
	}
	return store.VisibilityForScope(scope), nil
}

// call is one JSON request against the node, with the timeout every verb here
// uses.
// A nil `into` means the caller does not read the answer, and it is turned into
// somewhere to put it rather than passed on: peerRequest unmarshals into what
// it is given, and a nil there fails the request AFTER the write has already
// happened - which reads as "the write was refused" about a write that landed.
// Measured, on the first run of `flowy todo file --category`: the row was
// filed, the category was set, and the verb reported the category refused.
func call(method, url, bearer string, payload []byte, into any) error {
	if into == nil {
		var discard json.RawMessage
		into = &discard
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	return peerRequest(ctx, client, method, url, bearer, payload, into)
}

// cliTodoFile files one queue row.
//
// TWO DOORS, because they write different rows and the difference matters: a
// row filed into a room carries who raised it and says so where the work was
// agreed, and a row filed onto the board is nobody's ask but the filer's. The
// flag chooses, rather than this file deciding for everybody.
func cliTodoFile(args []string) error {
	fs := flag.NewFlagSet("todo file", flag.ContinueOnError)
	title := fs.String("title", "", "one line, which is what the board shows")
	room := fs.String("room", "", "file it into this room, so it arrives where the work was agreed")
	category := fs.String("category", "", "bug, feature, chore or question")
	assignee := fs.String("assignee", "", "who is carrying it")
	scope := fs.String("scope", "project", "who may read it: "+strings.Join(store.MemScopes, ", "))
	url, token, agent := doorFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*title) == "" {
		return errors.New("a row needs a title: it is the whole of what the board shows\n\n" + todoUsage)
	}
	body, err := bodyOrStdin(fs.Args(), "file", todoUsage)
	if err != nil {
		return err
	}
	visibility, err := scopeVisibility(*scope)
	if err != nil {
		return err
	}
	base, bearer, err := nodeFor(*url, *token, *agent)
	if err != nil {
		return err
	}

	// EVERY FIELD THROUGH THE DOOR THAT CHECKS IT. The create takes what its
	// own door takes - the room door has a category, the board door has none -
	// and anything left over is applied by the verb that owns it afterwards. A
	// field this side posted into a body the handler does not read would be a
	// write that succeeded and changed nothing, which is the shape of half the
	// defects this fleet found today.
	var id string
	if name := strings.TrimSpace(*room); name != "" {
		payload, err := json.Marshal(map[string]string{
			"title": *title, "body": body, "category": *category,
		})
		if err != nil {
			return err
		}
		var answer struct {
			Item struct {
				ID string `json:"id"`
				// The project the node filed it into, for the line below. It
				// is a pointer because a personal row has none.
				Project *string `json:"project"`
			} `json:"item"`
		}
		if err := call(http.MethodPost, base+"/api/chat/"+name+"/todo", bearer, payload, &answer); err != nil {
			return err
		}
		id = answer.Item.ID
		// WHERE IT WENT, from the node's answer rather than from the argument -
		// see saidWhere in say.go for the evening this comes from. A room name
		// is unique within a project and this node holds five, so "#general"
		// names a room in every one of them and says nothing about which
		// credential just wrote.
		where := "#" + name
		if answer.Item.Project != nil && *answer.Item.Project != "" {
			where = *answer.Item.Project + "/#" + name
		}
		defer fmt.Fprintf(os.Stderr, "raised %s in %s\n", id, where)
	} else {
		payload, err := json.Marshal(map[string]any{
			"type": store.MemoryType, "kind": "todo",
			"title": *title, "body": body, "visibility": visibility,
		})
		if err != nil {
			return err
		}
		var answer struct {
			ID string `json:"id"`
		}
		if err := call(http.MethodPost, base+"/api/artifacts", bearer, payload, &answer); err != nil {
			return err
		}
		id = answer.ID
		defer fmt.Fprintf(os.Stderr, "filed %s at scope %s\n", id, *scope)
		// The category has a door of its own, which is where the closed set is
		// checked. A word this door took and nothing counted would be the
		// silent write the vocabulary exists to prevent.
		if c := strings.TrimSpace(*category); c != "" {
			body, err := json.Marshal(map[string]string{"category": c})
			if err != nil {
				return err
			}
			if err := call(http.MethodPost, base+"/api/todo/"+id+"/category", bearer, body, nil); err != nil {
				return fmt.Errorf("filed %s, and its category was refused: %w", id, err)
			}
		}
	}

	// Unguarded, because filing work and naming who does it is not a race -
	// see assigneeRequest.Expect, which is what a CLAIM sends and what
	// `flowy todo claim` is for.
	if who := strings.TrimSpace(*assignee); who != "" {
		body, err := json.Marshal(map[string]string{"assignee": who})
		if err != nil {
			return err
		}
		if err := call(http.MethodPost, base+"/api/todo/"+id+"/assignee", bearer, body, nil); err != nil {
			return fmt.Errorf("filed %s, and naming %s on it was refused: %w", id, who, err)
		}
	}
	fmt.Println(id)
	return nil
}

// cliTodoNote appends what has been learned about a row.
func cliTodoNote(args []string) error {
	fs := flag.NewFlagSet("todo note", flag.ContinueOnError)
	id := fs.String("id", "", "the row this is about")
	url, token, agent := doorFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return errors.New("a note is about a row: pass --id\n\n" + todoUsage)
	}
	text, err := bodyOrStdin(fs.Args(), "note", todoUsage)
	if err != nil {
		return err
	}
	base, bearer, err := nodeFor(*url, *token, *agent)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"note": text})
	if err != nil {
		return err
	}
	var answer struct {
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
		Notes []struct {
			Note string `json:"note"`
		} `json:"notes"`
	}
	if err := call(http.MethodPost, base+"/api/todo/"+*id+"/note", bearer, payload, &answer); err != nil {
		return err
	}
	fmt.Println(*id)
	fmt.Fprintf(os.Stderr, "noted on %s, %d note(s) on the row now\n", *id, len(answer.Notes))
	return nil
}

// cliTodoWaitingOn says a row is waiting on somebody, or takes that back.
//
// IT IS NOT AN ASSIGNMENT AND MUST NOT LOOK LIKE ONE. The row it implements was
// raised because the only two ways to say "I asked you" were handing the row
// over - which tells the board they are CARRYING work they are only answering,
// and put four rows on the operator in one evening - or writing a note, which
// nothing counts. So this sets one field and the carrier does not move.
//
// EVERY RULE IS THE STORE'S. Resolving "me" to a handle, refusing a caller that
// resolves to nobody, deleting the keys rather than storing empties: all of it
// lives in internal/store/todowaiting.go, and this sends what it was given. A
// client that normalised a name here would be a second idea of what waiting
// means, which is priority.go's argument and the reason that file is thin.
//
// THE QUESTION IS THE BODY, like a note's text, because it is prose - and
// because the alternative is a --asked flag holding a sentence, which is the
// shape that ate two phrases to a backtick this week.
func cliTodoWaitingOn(args []string) error {
	fs := flag.NewFlagSet("todo waiting-on", flag.ContinueOnError)
	id := fs.String("id", "", "the row this is about")
	// A POINTER, so "not passed" and "passed empty" stay different. Empty is how
	// a question is withdrawn and it has to be sayable; absent is a caller who
	// forgot the flag, and answering those the same way would make `waiting-on
	// --id X` silently clear a question somebody is waiting on.
	of := fs.String("of", "\x00", "who owes the next move, \"\" to take it back")
	url, token, agent := doorFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return errors.New("waiting-on is about a row: pass --id\n\n" + todoUsage)
	}
	if *of == "\x00" {
		return errors.New("waiting-on needs --of WHO, or --of \"\" to take the question back\n\n" +
			todoUsage)
	}
	asked := strings.TrimSpace(strings.Join(fs.Args(), " "))
	base, bearer, err := nodeFor(*url, *token, *agent)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"waiting_on": *of, "asked": asked})
	if err != nil {
		return err
	}
	// READ BACK OFF THE ANSWER, not echoed from the request: the store resolves
	// a self-name, so "waiting on me" and "waiting on flowy-claude" are the same
	// call and only one of them is what was stored. Printing the argument would
	// report a name the board does not have.
	var answer struct {
		WaitingOn string `json:"waiting_on"`
		Asked     string `json:"asked"`
	}
	if err := call(http.MethodPost, base+"/api/todo/"+*id+"/waiting-on", bearer, payload, &answer); err != nil {
		return err
	}
	fmt.Println(*id)
	switch {
	case answer.WaitingOn == "":
		fmt.Fprintf(os.Stderr, "%s is waiting on nobody now\n", *id)
	case answer.Asked != "":
		fmt.Fprintf(os.Stderr, "%s is waiting on %s: %s\n", *id, answer.WaitingOn, answer.Asked)
	default:
		fmt.Fprintf(os.Stderr, "%s is waiting on %s\n", *id, answer.WaitingOn)
	}
	return nil
}

// cliTodoClaim takes a row, or hands one over.
//
// --expect is what makes it a CLAIM rather than an assignment: of two agents
// reaching for one row, exactly one comes away holding it and the other is
// refused naming the winner. It is a pointer because absent and empty are
// different questions - empty means "I read this as nobody's", absent means "I
// am not racing anybody".
func cliTodoClaim(args []string) error {
	fs := flag.NewFlagSet("todo claim", flag.ContinueOnError)
	id := fs.String("id", "", "the row to take")
	// A SENTINEL, for the same reason --expect has one: absent and empty are
	// different instructions here. Absent means "I am taking it" and resolves to
	// the token's own handle. --as "" means nobody carries this.
	//
	// RELEASING WAS ALREADY POSSIBLE and this is not the feature - `--as nobody`
	// has always worked, because NormalizeAssignee collapses every one of
	// store.nobodyWords ("nobody", "none", "unowned", "-", "?", "tbd", "n/a",
	// "unassigned") to the empty assignee. What was wrong is what the EMPTY
	// STRING did: it was the flag's default, so `--as ""` was indistinguishable
	// from not passing --as at all, and the door quietly asked the node who this
	// token was and claimed the row FOR THE CALLER. The opposite of what was
	// typed, with no refusal to read. Measured on a row I was carrying and wanted
	// to hand back: it answered "flowy-claude is carrying" and left it mine.
	//
	// An argument that silently means its own opposite is worse than one that is
	// not accepted, so the empty string now says what a reader would expect and
	// the word stays as the readable spelling of it.
	as := fs.String("as", "\x00", `who is taking it (default the token's own handle), "" for nobody`)
	expect := fs.String("expect", "\x00", `who you read as carrying it, "" for nobody`)
	url, token, agent := doorFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return errors.New("a claim names a row: pass --id\n\n" + todoUsage)
	}
	base, bearer, err := nodeFor(*url, *token, *agent)
	if err != nil {
		return err
	}
	// An EMPTY --as is a release and goes to the node as it was typed. Only an
	// absent one asks the node who this token is.
	who := strings.TrimSpace(*as)
	if *as == "\x00" {
		// The token's own name, ASKED OF THE NODE rather than assembled here.
		// A handle this side reconstructed is a claim about a seat nobody
		// read, and the board is a place where the name is the whole of who
		// holds the work. /api/me because /api/whoami answers ids: an id in the
		// assignee field is a name no room recognises.
		var me struct {
			User struct {
				Handle string `json:"handle"`
			} `json:"user"`
		}
		if err := call(http.MethodGet, base+"/api/me", bearer, nil, &me); err != nil {
			return fmt.Errorf("could not ask the node who this token is, so there is "+
				"no name to claim with - pass --as: %w", err)
		}
		who = strings.TrimSpace(me.User.Handle)
		if who == "" {
			return errors.New("this token resolves to no handle, so there is nothing to " +
				"claim with - pass --as")
		}
	}
	req := map[string]string{"assignee": who}
	if *expect != "\x00" {
		req["expect"] = *expect
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	var answer struct {
		Item struct {
			ID     string `json:"id"`
			Fields struct {
				Assignee string `json:"assignee"`
			} `json:"fields"`
		} `json:"item"`
	}
	if err := call(http.MethodPost, base+"/api/todo/"+*id+"/assignee", bearer, payload, &answer); err != nil {
		return err
	}
	fmt.Println(*id)
	// A RELEASED ROW SAYS SO. The node answers an empty assignee and the old
	// line printed " is carrying <id>" - a sentence with a hole where the name
	// goes, which reads as an answer that failed rather than as the row being
	// free.
	if strings.TrimSpace(answer.Item.Fields.Assignee) == "" {
		fmt.Fprintf(os.Stderr, "nobody is carrying %s\n", *id)
		return nil
	}
	fmt.Fprintf(os.Stderr, "%s is carrying %s\n", answer.Item.Fields.Assignee, *id)
	return nil
}

// cliTodoDone moves a row's status, and for a close says what was measured.
//
// --note is one flag on this verb rather than a second command before it. Every
// row closed on the live node took two calls, `todo note` and then this, and
// the node now refuses the second one when nothing has been said: the note is
// what makes a row worth reading in a week and the status is bookkeeping, so
// splitting them made the valuable half the optional one. See
// store.SetTodoStatus.
func cliTodoDone(args []string) error {
	fs := flag.NewFlagSet("todo done", flag.ContinueOnError)
	id := fs.String("id", "", "the row to move")
	status := fs.String("status", "done", "todo, active or done")
	note := fs.String("note", "", "what was measured, written with the close")
	dup := fs.String("duplicate-of", "", "the row that survives, when this close is a duplicate")
	url, token, agent := doorFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return errors.New("a status move names a row: pass --id\n\n" + todoUsage)
	}
	base, bearer, err := nodeFor(*url, *token, *agent)
	if err != nil {
		return err
	}
	body := map[string]string{"status": *status}
	if strings.TrimSpace(*note) != "" {
		body["note"] = *note
	}
	// A CLOSE THAT NAMES A SURVIVOR. The node writes the same edge a
	// superseding report writes and refuses the pair that closes into each
	// other - see store.CloseAsDuplicate. The note is optional here for once,
	// because naming the survivor has already said the measurement: the work is
	// not gone, it is over there.
	if strings.TrimSpace(*dup) != "" {
		body["replaced_by"] = *dup
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if err := call(http.MethodPost, base+"/api/artifact/"+*id+"/status", bearer, payload, nil); err != nil {
		return err
	}
	fmt.Println(*id)
	fmt.Fprintf(os.Stderr, "%s is %s\n", *id, *status)
	return nil
}

// cliNoteWrite files a memory.
func cliNoteWrite(args []string) error {
	fs := flag.NewFlagSet("note write", flag.ContinueOnError)
	title := fs.String("title", "", "one line naming what this is about")
	scope := fs.String("scope", "personal", "who may read it: "+strings.Join(store.MemScopes, ", "))
	url, token, agent := doorFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*title) == "" {
		return errors.New("a note needs a title\n\n" + noteKindUsage)
	}
	body, err := bodyOrStdin(fs.Args(), "write", noteKindUsage)
	if err != nil {
		return err
	}
	visibility, err := scopeVisibility(*scope)
	if err != nil {
		return err
	}
	base, bearer, err := nodeFor(*url, *token, *agent)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"type": store.MemoryType, "kind": "note",
		"title": *title, "body": body, "visibility": visibility,
	})
	if err != nil {
		return err
	}
	var answer struct {
		ID string `json:"id"`
	}
	if err := call(http.MethodPost, base+"/api/artifacts", bearer, payload, &answer); err != nil {
		return err
	}
	fmt.Println(answer.ID)
	fmt.Fprintf(os.Stderr, "wrote %s at scope %s\n", answer.ID, *scope)
	return nil
}
