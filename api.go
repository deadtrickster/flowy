package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// maxBody caps a request body. Artifacts hold transcripts, so it is generous,
// but it is not unbounded.
const maxBody = 8 << 20

// artifactRequest is the create/update body. Project is raw JSON rather than a
// *string because absent and null mean different things here: absent means "my
// home project", null means "no project at all", which is what personal is.
type artifactRequest struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Kind       string          `json:"kind"`
	Project    json.RawMessage `json:"project"`
	OwnerUser  string          `json:"owner_user"`
	Title      string          `json:"title"`
	Body       string          `json:"body"`
	Discovery  string          `json:"discovery"`
	Status     string          `json:"status"`
	Severity   string          `json:"severity"`
	Tags       []string        `json:"tags"`
	UserTags   []string        `json:"user_tags"`
	Related    []string        `json:"related"`
	Visibility string          `json:"visibility"`
	FilePath   string          `json:"file_path"`
	Fields     json.RawMessage `json:"fields"`
}

// decodeJSON reads a capped, strict JSON body. Unknown fields are an error:
// silently dropping a misspelled `visibilty` is how an artifact ends up
// readable by a project that was never meant to see it.
func decodeJSON(r *http.Request, into any) error {
	return decodeJSONLimit(r, into, maxBody)
}

// decodeJSONLimit is decodeJSON with a different cap, for the one endpoint that
// takes a page of rows rather than a single one.
//
// The body is one JSON value and nothing else. A decoder stops at the end of
// the first value and says nothing about what follows, so
// `{"type":"bug"}{"visibility":"personal"}` decoded as the first object and
// dropped the second on the floor - and DisallowUnknownFields, which is the
// whole strict-input guarantee, only ever looks inside the value it decoded.
// Silently dropped input is how a row gets written at a visibility nobody
// asked for, so anything after the first value is the same 400 a malformed
// body gets.
func decodeJSONLimit(r *http.Request, into any, limit int64) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}

// handleCreateArtifact creates an artifact, or replaces one the principal owns.
//
// POST /api/artifacts
// defaultWorkStatus is the rule above, as a function, so the test exercises the
// code the door runs rather than a copy of it that cannot notice the door
// changing underneath it.
func defaultWorkStatus(status, kind string, update bool) string {
	if status == "" && !update && isWorkKind(kind) {
		return store.TodoStatus
	}
	return status
}

// mergeRowSentAsAType refuses a merge request sent as a TYPE, and says what to
// send instead. Empty means the request is fine.
//
// A merge request is type "memory" with kind "merge". Send "merge" as the TYPE
// instead - the obvious guess, and the word every queue answer prints - and
// this door used to take it: 200, signed, and a row the merge queue cannot see,
// because MergeAdmissible asks a.Kind and the kind is empty. Its status came
// back empty too, and the status door then refused "todo" with the ARTIFACT
// vocabulary, because IsQueueItem is false for it and the row never reaches the
// queue's lifecycle at all.
//
// Nothing said a word. The row looked filed and was not.
//
// ONLY MERGE, and the narrowing is measured rather than cautious. The first cut
// of this refused every work kind sent as a type, and the gate caught it: the
// suite creates a top-level `feature` and moves it open -> triaged -> wont-fix,
// which is a legitimate artifact with the artifact lifecycle. todo, note,
// report, diagram and feature all live on both sides of that fence - 636 rows
// measured on the live node, one top-level todo among them - so refusing the
// general case refuses shapes this fabric supports.
//
// Merge is the one with no other reading: nothing consumes a top-level merge
// artifact, no lifecycle path claims it, and there are zero of them in 636 rows.
// The general ambiguity is not a door's problem to fix - it is the migration
// under the ruling on 01M0ANFYWY, and a refusal is not a substitute for it.
//
// A function of two strings rather than a block in the handler, so it can be
// exercised without a database or a request.
func mergeRowSentAsAType(typ, kind string) string {
	if kind != "" || typ != store.MergeKind {
		return ""
	}
	return fmt.Sprintf(
		"a merge request is type %q with kind %q - sent as a type with no kind it is on no "+
			"queue, and the status door speaks a different vocabulary to it",
		store.MemoryType, store.MergeKind)
}

func (s *server) handleCreateArtifact(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req artifactRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	if req.Type == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("type is required"))
		return
	}
	// An announcement is an artifact, and this is the endpoint that writes
	// artifacts - which would make the capability check on POST /api/announcements
	// a suggestion. The scope lives in fields, and fields is a blob this handler
	// takes as it is given, so a worker agent could have posted a federation
	// announcement here by typing the type out. Announcements have their own
	// door, and this is the sign on this one.
	if req.Type == store.AnnouncementType {
		writeJSON(w, http.StatusForbidden, errorBody(
			"an announcement is posted through POST /api/announcements, "+
				"which is where its scope is checked"))
		return
	}

	if why := mergeRowSentAsAType(req.Type, req.Kind); why != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(why))
		return
	}

	project, err := req.project(p)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}

	// An artifact is owned by whoever wrote it. The field is accepted so a
	// client can be explicit, not so it can put somebody else's name on a row -
	// that would let one user mint another user's personal artifacts.
	owner := req.OwnerUser
	if owner == "" {
		owner = p.UserID
	}
	if owner != p.UserID {
		writeJSON(w, http.StatusForbidden, errorBody("owner_user must be the calling principal"))
		return
	}

	// An update is a replacement of a row that is already there, so it has to
	// clear the same bar twice: the principal must be able to see the artifact
	// at all, and must own it. Unreadable is 404 rather than 403, the same as a
	// plain read, so a probe cannot learn an id exists by trying to write it.
	//
	// update says which of the two writes below runs. It is false for an id
	// this caller cannot read, and that is the whole of the fix: a read that
	// finds nothing means "nothing you may write to", not "nothing there". The
	// two came apart in the one case that matters - the caller's own artifact,
	// in another project, held with a token for this one - and taking the
	// update branch on it moved the row into this project, wiped every field
	// the request left out and brought a deleted one back. The store settles
	// what "not there" really was: a create that lands on a taken id writes
	// nothing and comes back ErrTaken, which is answered as a 404 like the read.
	update := false
	if req.ID != "" {
		existing, err := s.db.ReadArtifact(r.Context(), p, req.ID, false)
		switch {
		case errors.Is(err, store.ErrNotFound):
			// Not there, or not ours to see, or deleted. This is a create with
			// a caller-chosen id, which is allowed - ids are minted anywhere -
			// and it is a create even if the id turns out to be taken.
		case err != nil:
			serverError(w, r, err)
			return
		case existing.OwnerUser != p.UserID:
			writeJSON(w, http.StatusForbidden, errorBody("not the owner of "+req.ID))
			return
		default:
			// Fields the caller left out keep the value they had, so an update
			// does not have to restate the whole artifact.
			update = true
			req.fillFrom(existing)
			if project, err = req.project(p); err != nil {
				writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
				return
			}
			// A row with no project is its owner's and nobody else's, whatever
			// the visibility column says - the read filter's first branch, and
			// the personal floor's whole promise. Giving it one is not an edit
			// of a field, it is handing the row to a project, so an update
			// does not do it: the same refusal an assignment of a personal
			// artifact gets. Say personal and it stays personal, or write it
			// in the project it belongs to.
			if existing.Project == nil && project != nil &&
				req.Visibility != store.VisibilityPersonal {
				writeJSON(w, http.StatusBadRequest,
					errorBody("artifact "+req.ID+" has no project and is its owner's alone; "+
						"an update cannot move it into "+*project+
						" as "+req.Visibility+" - create it there instead"))
				return
			}
		}
	}

	// You write where you are. A principal is a (user, agent, project) triple
	// and the project half of it is the only project it may put a row into:
	// writing into another one would produce an artifact its own author cannot
	// read back, because reads go by project and not by authorship. The same
	// person working in another project is another principal, with another
	// token.
	if project != nil && *project != p.Project {
		writeJSON(w, http.StatusForbidden,
			errorBody("cannot write into project "+*project+" as a principal of "+p.Project))
		return
	}

	art := &store.Artifact{
		ID:         req.ID,
		Type:       req.Type,
		Kind:       req.Kind,
		Project:    project,
		OwnerUser:  owner,
		Title:      req.Title,
		Body:       req.Body,
		Discovery:  req.Discovery,
		Status:     req.Status,
		Severity:   req.Severity,
		Tags:       req.Tags,
		UserTags:   req.UserTags,
		Related:    req.Related,
		Visibility: req.Visibility,
		FilePath:   req.FilePath,
		Fields:     req.Fields,
	}
	// A work item raised here with NO status starts at the beginning of the
	// lifecycle, the same as one raised through MCP or through
	// POST /api/chat/{room}/todo.
	//
	// mcp_tools.go carries the story of this exact bug being found and fixed -
	// "THE DOOR NEVER SET ONE" - and it was fixed in that door alone. This one
	// kept writing "", which is not a state any reader can compare: it is not
	// todo, so a board filtering for outstanding work skips the row, and it is
	// not done either. Measured on the live node before this change: 179
	// kind=todo rows, ?status=todo returned 29, and six rows filed through this
	// door in one afternoon were invisible to every board query and to the nag
	// that reads them.
	//
	// So the fix is the same fix, in the door the console actually uses. Two
	// ways in that default differently are two boards.
	//
	// Create only, and only when nothing was stated. An update that restates
	// nothing keeps what the row has, including empty, because healing a stale
	// status on an unrelated edit moves other people's work behind their backs.
	art.Status = defaultWorkStatus(art.Status, art.Kind, update)

	// AND WHO RAISED IT, for the same reason and with the same rule.
	//
	// raiser is stamped by POST /api/chat/{room}/todo and by the MCP raise tool
	// and by nothing else - so a row written through THIS door, which is what
	// the console's /new page and every one of our scripts uses, carries no
	// reporter at all. Measured on the live node: the six most recent todos,
	// raiser on zero of them, and the operator noticing that todos do not show
	// who reported them.
	//
	// The caller is the raiser when nothing else said. That is not a guess: a
	// direct create IS somebody stating the work, and the principal making the
	// request is who stated it. What raiser.go refuses to guess is a raiser for
	// a row written BEFORE the key existed, where nobody stated anything and
	// owner_user would be this node inventing a fact - those rows still answer
	// "" and the console should show that rather than a name.
	//
	// NO RAISER IS STAMPED HERE, and the empty answer is the honest one.
	//
	// This door used to write the CALLER's handle into raiser on any work row
	// that carried none (f5f5802). That contradicted a rule the room door
	// already had, written down at todos.go:141: the raiser is WHO THE WORK
	// CAME FROM, taken from the message a todo was raised out of, and left
	// empty otherwise because "owner_user already records the seat that typed
	// it, and inventing a second copy of that answer would be inventing the
	// fact this field exists to keep honest."
	//
	// MEASURED, 2026-08-19: claude-host filed two rows in one hour from one
	// seat through two doors - `todo file --room general` left the raiser
	// empty, `merge open` stamped claude-host. Same field, two meanings: one
	// says nobody asked for this, the other says I typed it.
	//
	// The older rule wins. A row filed by the seat that wanted it has no raiser
	// and does not need one - owner_user is that answer, and the surface that
	// wants to show a reporter reads it there.

	write := s.db.CreateArtifact
	if update {
		write = s.db.UpsertArtifact
	}
	if err := write(r.Context(), art); err != nil {
		if errors.Is(err, store.ErrUndeclaredProject) {
			// A project nobody declared is not a target. It is 400 rather than
			// 404 because there is nothing to hide here - a project's existence
			// is not a secret, every row that names one says the name - and the
			// caller's next move is to declare it or fix the spelling.
			writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
			return
		}
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrTaken) {
			// The id names a row this principal may not write: somebody else's,
			// or one that has been deleted, or - on a create - simply one that
			// is already there. The store refused it and wrote nothing, and the
			// answer is the one a read of that row would give: a caller must
			// not learn an id exists by writing to it.
			writeJSON(w, http.StatusNotFound, errorBody("no such artifact"))
			return
		}
		var refusal store.DepRefusal
		if errors.As(err, &refusal) {
			// A row the store will not hold, and the caller can fix it: a queue
			// item written active with nobody carrying it is the one this door
			// can produce - it takes a status and has never taken an assignee.
			// It is the caller's mistake, so it is 400 rather than the node
			// reporting itself as broken. See store.ActiveUnownedError.
			writeJSON(w, http.StatusBadRequest, errorBody(refusal.Error()))
			return
		}
		serverError(w, r, err)
		return
	}
	// A CORRECTION IS HEARD WHERE THE CLAIM WAS MADE.
	//
	// The supersede edge has existed for reports and instructions since before
	// this, and it works for a finding today - measured on the live node: file
	// a finding carrying fields.supersedes and the older row comes back with
	// replaced_by and a resolved address. Nobody used it for corrections.
	//
	// So item 3 of 01M0B93BNH - "correct the row when the measurement changes"
	// - was never a missing mechanism. It was a mechanism nobody met: a
	// correction is written as prose in a note, and a reader has to notice it.
	// On 2026-08-19 alone there were five - a wrong cause for a red, a 404 read
	// as a typed door, a ruling built on that read and withdrawn, a sleeping
	// drainer called dead, and two suites read as a lock failure when one was a
	// child of the other.
	//
	// The edge is silent by construction: it is derived at READ time, so the
	// only way to learn a claim was superseded is to read that claim again.
	// Nobody re-reads a finding they have already acted on. So the node says it
	// where the claim was made, once, in the room the superseded row lives in.
	if said := s.supersedeHeardIn(r, art); said != nil {
		if err := s.db.AppendEvent(r.Context(), said); err != nil {
			// The row is written and the edge holds; only the announcement
			// failed. Saying nothing is the same failure this exists to fix, so
			// it is logged rather than swallowed - and it is not an error the
			// caller can act on, so their write still answers 200.
			log.Printf("supersede: could not announce %s -> %s: %v",
				art.ID, store.SupersedesOf(art), err)
		}
	}
	// AND A BRANCH FILED FOR THE QUEUE IS HEARD WHERE IT WAS FILED - see
	// filedHeardIn. Only on a create: a row edited later has already been
	// announced, and saying it twice is how a room learns to skip the line.
	if !update {
		if said := s.filedHeardIn(r, art); said != nil {
			if err := s.db.AppendEvent(r.Context(), said); err != nil {
				log.Printf("merge: could not announce %s in its room: %v", art.ID, err)
			}
		}
	}
	writeJSON(w, http.StatusOK, art)
}

// supersedeHeardIn is the message the room reads when a row replaces another,
// or nil when this row replaces nothing.
//
// IT SPEAKS IN THE SUPERSEDED ROW'S ROOM, not the new row's. A correction
// belongs to the conversation where the claim was made: the people who read
// "the deps door is typed to todos" are the people who need to hear that it was
// an id prefix, and they are not necessarily reading wherever the correction
// happens to be filed.
//
// A row that replaces nothing, or replaces something this caller cannot read,
// says nothing. The second is not a leak to close: replacedBy is
// permission-filtered on the read side, so a caller can already only be told
// about a replacement they may see - this keeps the announcement to the same
// rule rather than inventing a second one.

// filedHeardIn is the message the room reads when a BRANCH is filed for the
// queue, or nil when the row names no room to say it in.
//
// WHY IT EXISTS. Measured 2026-08-19 by watching what five verbs actually put
// in a room: `todo file --room` announces "raised a todo ...", `todo claim`
// announces "gave ... to ...", and `merge open --room general` takes the room,
// writes it onto the row, and says NOTHING in it. So a room hears about a new
// todo and not about a new branch - and the branch is the louder of the two,
// because somebody may be about to gate it, land it, or file the same work.
//
// It is the rule 01M0BNW8XD already settled, at the other end of the queue: a
// claim is not a state change somebody looks up later, it is a message to the
// other seats, and the log is not a place anybody watches. That row fixed the
// handover; this is the filing. A landing already announces itself since the
// same afternoon, so of the three moments a merge row has - filed, landed,
// abandoned - only the first was silent.
//
// A ROW FILED INTO NO ROOM STILL SAYS NOTHING, deliberately: there is no
// conversation it belongs to, and inventing one puts a branch in front of
// people who never saw the work. Same rule claimHeardIn follows.
//
// A FAILURE TO BUILD THE MESSAGE IS NOT A FAILURE TO FILE. The row is what the
// caller asked for; the announcement is what the room gets. Returning nil here
// leaves the filing intact and quiet, which is what this door did for everybody
// until today.
func (s *server) filedHeardIn(r *http.Request, art *store.Artifact) *store.Event {
	room, body, ok := filedSaidFor(art)
	if !ok {
		return nil
	}
	p := principalOf(r)
	actor, kind := chatActor(p)
	meta, err := json.Marshal(speakerMeta(p, kind, s.speakerName(r.Context(), p)))
	if err != nil {
		return nil
	}
	return &store.Event{
		Type:   chatEventType,
		Room:   room,
		Thread: art.ID,
		Actor:  actor,
		Body:   body,
		Meta:   withTrace(json.RawMessage(meta), traceIDOf(r)),
	}
}

// filedSaidFor is WHETHER to say anything and WHAT, with no principal and no
// database in it - which is what makes the four refusals testable without a
// node. The speaker's name and the trace ride on top in filedHeardIn.
func filedSaidFor(art *store.Artifact) (room, body string, ok bool) {
	if art == nil || art.Kind != store.MergeKind {
		return "", "", false
	}
	fields, err := store.ArtifactFields(art)
	if err != nil {
		return "", "", false
	}
	room, _ = fields[store.RoomField].(string)
	room = strings.TrimSpace(room)
	if room == "" {
		return "", "", false
	}
	branch := strings.TrimSpace(store.BranchOf(art))
	if branch == "" {
		return "", "", false
	}
	// THE ID IS WHOLE AND FOLLOWS A WORD A READER CAN COPY FROM, which is
	// todos.go's rule and it was paid for: two agents took a THREAD id out of a
	// raise notification, called the row doors with it, and read the 404 as a
	// row that had gone away. The ids differed in their last character.
	if target := strings.TrimSpace(store.TargetOf(art)); target != "" {
		return room, "filed " + branch + " to land on " + target + ": " + art.ID, true
	}
	return room, "filed " + branch + " for the queue: " + art.ID, true
}

func (s *server) supersedeHeardIn(r *http.Request, art *store.Artifact) *store.Event {
	older := store.SupersedesOf(art)
	if older == "" {
		return nil
	}
	p := principalOf(r)
	was, err := s.db.ReadArtifact(r.Context(), p, older, false)
	if err != nil {
		return nil
	}
	fields, err := store.ArtifactFields(was)
	if err != nil {
		return nil
	}
	room, _ := fields[store.RoomField].(string)
	if strings.TrimSpace(room) == "" {
		return nil
	}
	actor, kind := chatActor(p)
	meta, err := json.Marshal(speakerMeta(p, kind, s.speakerName(r.Context(), p)))
	if err != nil {
		return nil
	}
	return &store.Event{
		Type:   chatEventType,
		Room:   room,
		Thread: was.ID,
		Actor:  actor,
		Body:   "superseded " + firstLineOf(was.Title) + " - " + firstLineOf(art.Title),
		Meta:   withTrace(json.RawMessage(meta), traceIDOf(r)),
	}
}

// project resolves the three states of the project field: absent means the
// principal's home project, null means none, a string means that project.
func (req *artifactRequest) project(p *store.Principal) (*string, error) {
	raw := strings.TrimSpace(string(req.Project))
	switch {
	case raw == "":
		if req.Visibility == "personal" || p.Project == "" {
			return nil, nil
		}
		home := p.Project
		return &home, nil
	case raw == "null":
		return nil, nil
	}
	var name string
	if err := json.Unmarshal(req.Project, &name); err != nil {
		return nil, errors.New("project must be a string or null")
	}
	if name == "" {
		return nil, nil
	}
	return &name, nil
}

// fillFrom carries forward the parts of an existing artifact the update left
// unset.
func (req *artifactRequest) fillFrom(old *store.Artifact) {
	setIfEmpty := func(dst *string, old string) {
		if *dst == "" {
			*dst = old
		}
	}
	setIfEmpty(&req.Type, old.Type)
	setIfEmpty(&req.Kind, old.Kind)
	setIfEmpty(&req.Title, old.Title)
	setIfEmpty(&req.Body, old.Body)
	setIfEmpty(&req.Discovery, old.Discovery)
	setIfEmpty(&req.Status, old.Status)
	setIfEmpty(&req.Severity, old.Severity)
	setIfEmpty(&req.Visibility, old.Visibility)
	setIfEmpty(&req.FilePath, old.FilePath)
	if req.Tags == nil {
		req.Tags = old.Tags
	}
	if req.UserTags == nil {
		req.UserTags = old.UserTags
	}
	if req.Related == nil {
		req.Related = old.Related
	}
	if len(req.Fields) == 0 {
		req.Fields = old.Fields
	}
	// None is a value too, and it is carried forward like any other. Absent
	// means "the principal's home project", so an update that said nothing
	// about the project - a bare {id, type} - used to move a row nobody but its
	// owner could read into the caller's project, on a request that said
	// nothing about scope at all.
	if len(req.Project) == 0 {
		if old.Project != nil {
			req.Project, _ = json.Marshal(*old.Project)
		} else {
			req.Project = json.RawMessage("null")
		}
	}
}

// listParams are the query parameters this list honours, and the whole of them.
//
// It is a list rather than a comment because of what the alternative did: a
// parameter the handler did not read was DROPPED, and the answer came back 200
// with every row in it. `?type=finding&tag=ragflow` returned all 40 findings on
// a node where 16 carried that tag, and nothing in the response said the filter
// had not run. That is worse than a refusal and worse than an empty list -
// both of those are answers a caller can act on, while an unapplied filter is a
// wrong answer in the shape of a right one, and no client can detect it.
//
// So a parameter that is not here is refused by name. Adding one to this map is
// the second half of implementing it, and forgetting to is a 400 rather than a
// lie.
var listParams = map[string]bool{
	"type":     true,
	"kind":     true,
	"assignee": true,
	"project":  true,
	"status":   true,
	"room":     true,
	"category": true,
	"tag":      true,
	"limit":    true,
	"scope":    true,
}

// refuseUnknownParams turns any parameter outside known into a sentence naming
// it, or "" when there is nothing to refuse.
//
// Naming it is most of the value: "bad request" leaves the caller re-reading
// their own URL for the typo, and the typo this was filed for - `tags` for
// `tag` - is one letter.
func refuseUnknownParams(q url.Values, known map[string]bool) string {
	unknown := []string{}
	for name := range q {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return ""
	}
	sort.Strings(unknown)
	accepted := make([]string, 0, len(known))
	for name := range known {
		accepted = append(accepted, name)
	}
	sort.Strings(accepted)
	what := "query parameter"
	if len(unknown) > 1 {
		what = "query parameters"
	}
	return "this list does not honour the " + what + " " +
		strings.Join(quoted(unknown), ", ") +
		", and a filter that is not applied would answer with more than was asked for. " +
		"It takes " + strings.Join(quoted(accepted), ", ")
}

// quoted puts each name in double quotes, so a refusal reads as a list of
// parameters rather than as a sentence with words in it.
func quoted(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, strconv.Quote(name))
	}
	return out
}

// tagsArg is the tags a list is narrowed by: every ?tag= on the URL, trimmed,
// with the empty ones dropped.
//
// Empty means absent here, as it does for every other parameter on this door -
// `?tag=` is a filter nobody typed, usually a console rendering a cleared box,
// and narrowing to the artifacts labelled with the empty string would answer
// nothing for a query that asked for no narrowing.
func tagsArg(values []string) []string {
	out := make([]string, 0, len(values))
	for _, tag := range values {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// handleListArtifacts lists what the principal may read.
//
// GET /api/artifacts?type=&kind=&project=&status=&room=&category=&tag=&limit=
//
// tag narrows to the artifacts carrying that label, in the query rather than
// over the page - so it composes with limit, and a short page still means "that
// was all of them". Repeated, it means AND: ?tag=a&tag=b is the artifacts
// carrying both. See store.ArtifactQuery.Tags for which columns it reads and
// why AND is the reading a console's stacked filters need.
//
// Anything outside listParams is refused by name rather than dropped.
//
// room narrows to what was raised in one chat room, and is a narrowing like
// type and kind beside it: the permission filter is the same clause it always
// was, and a list with no room in it is every artifact the caller may read,
// including the ones that carry no room at all.
//
// category is the same kind of narrowing over what kind of work a queue item is
// - and it is the reason that set is closed, because "give me the bugs" is only
// a question with an answer if there is one word for bugs.
func (s *server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()

	// Before anything is read, because a request this door cannot honour must
	// not be answered as though it had been.
	if why := refuseUnknownParams(q, listParams); why != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(why))
		return
	}
	room, err := roomArg(q.Get("room"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	// What kind of work, out of the closed set. It goes through the same door
	// every write of one goes through, so asking for a category that is not in
	// the vocabulary is a refusal naming the vocabulary rather than an empty page
	// that reads exactly like "there are no bugs".
	category, err := store.NormalizeTodoCategory(q.Get("category"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	// WHO IS CARRYING IT, off the generated column rather than out of the fields
	// blob - see ArtifactQuery.Assignee. The store has answered this since the
	// column landed; this door never passed it, so every client that wanted
	// "my rows" pulled three hundred and filtered them itself, which is the
	// json extraction the column exists to remove happening one layer further
	// out.
	//
	// `nobody` is a STATE and it goes through the queue's own vocabulary rather
	// than a second list here: ?assignee=none, unassigned, unowned and the rest
	// all ask the same question, because two words for one state read as two
	// states. See store.NobodyName.
	assignee, unassigned, err := assigneeArg(q.Get("assignee"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	list, err := s.db.ListArtifacts(r.Context(), p, store.ArtifactQuery{
		Type:       q.Get("type"),
		Kind:       q.Get("kind"),
		Project:    q.Get("project"),
		Status:     q.Get("status"),
		Assignee:   assignee,
		Unassigned: unassigned,
		Room:       room,
		Category:   category,
		Tags:       tagsArg(q["tag"]),
		ScopeAll:   scopeAll(r, p),
		Limit:      intParam(q.Get("limit")),
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	// What this node refused to hand over, and why. A queue read that drops the
	// rows this node would not take and returns a shorter list is lying by
	// omission: the reader cannot tell "there is no more work" from "there is
	// work I would not carry". So the count travels with the answer, in the same
	// shape the console already distinguishes its other kinds of empty in - see
	// store.WithheldAuthorship. It is absent, not zero, when there is nothing to
	// say.
	withheld, err := s.db.WithheldAuthorship(r.Context(), p, scopeAll(r, p))
	if err != nil {
		serverError(w, r, err)
		return
	}
	// And what it refused for good, which is a different statement about a
	// different set. withheld is what is missing right now; this is what this
	// node has decided it will not take however the rule changes afterwards -
	// see store.RefusedAuthorship. Both or neither would be wrong: a claim can
	// be terminally refused while the row itself has since arrived under the
	// author's own signature, and then there is nothing withheld and something
	// refused. Absent, not zero, for the same reason.
	refused, err := s.db.RefusedAuthorship(r.Context(), p, scopeAll(r, p))
	if err != nil {
		serverError(w, r, err)
		return
	}
	// AND WHICH OF THEM THEIR AUTHOR HAS TAKEN BACK. One read for the whole
	// page - see store.FillDisowned - so every row on it is judged against one
	// state of the repudiations, which is the property a per-row lookup would
	// lose.
	if err := s.db.FillDisowned(r.Context(), list, nil); err != nil {
		log.Printf("disowned: could not resolve repudiations for an artifact page: %v", err)
	}
	body := stampScope(map[string]any{"artifacts": list}, answerScopeOf(r, p))
	if withheld != nil {
		body["withheld"] = withheld
	}
	if refused != nil {
		body["refused"] = refused
	}
	writeJSON(w, http.StatusOK, body)
}

// handleGetArtifact returns one artifact, or 404 when it is missing or out of
// reach - the caller cannot tell which, which is the point.
//
// GET /api/artifact/{id}
func (s *server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	art, err := s.db.ReadArtifact(r.Context(), p, r.PathValue("id"), scopeAll(r, p))
	if errors.Is(err, store.ErrNotFound) {
		// Absent, or withdrawn? 404 answers three different truths - never
		// existed, taken back, and out of your reach - and twenty minutes went
		// into that ambiguity tonight over rows that turned out to be personal.
		//
		// So a row this reader COULD have read, and which was withdrawn, gets
		// 410 and says who took it back and when. Everything else stays 404,
		// INCLUDING a row that exists and is not for this reader: saying "it
		// exists but not for you" is an existence oracle, and ids are guessable.
		// ReadWithdrawn runs the same permission filter for that reason.
		if wd, wErr := s.db.ReadWithdrawn(r.Context(), p, r.PathValue("id"), scopeAll(r, p)); wErr == nil {
			writeJSON(w, http.StatusGone, map[string]any{
				"error":     "artifact withdrawn",
				"withdrawn": wd,
			})
			return
		}
		// And the fourth truth 404 was answering: the id is real and is not an
		// artifact id at all. See misreadIDNote.
		writeJSON(w, http.StatusNotFound,
			errorBody("no such artifact"+s.misreadIDNote(r, r.PathValue("id"))))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	// AND WHETHER ITS AUTHOR HAS TAKEN IT BACK. One row, one read of the
	// every repudiation this node holds - see store.FillDisowned, which carries
	// why the mark is a third reading rather than a replacement.
	//
	// A failure here does not fail the read: the row is what was asked for, and
	// answering 500 because the mark could not be resolved would hide a row
	// over an annotation on it. It is logged by the store call's own path and
	// the field is simply absent, which is what every surface shows today.
	if fillErr := s.db.FillDisowned(r.Context(), []*store.Artifact{art}, nil); fillErr != nil {
		log.Printf("disowned: could not resolve repudiations for %s: %v", art.ID, fillErr)
	}
	writeJSON(w, http.StatusOK, art)
}

// misreadIDNote is what a not-found answer adds when the id the caller used
// names something real in another id space: a chat message, or a chat thread.
// It is empty when the id names nothing this reader can reach, which leaves the
// refusal exactly as it was.
//
// THE REFUSAL CARRIES THE DIAGNOSIS, which is the pattern the merge queue
// already keeps. A 404 that says only "no such artifact" is true and useless
// against a thread id: the reader goes looking for a deleted row, finds nothing,
// and concludes the row is gone - when the row is there and they are holding the
// wrong end of it. Two agents did exactly that within five minutes on
// 2026-08-18, over two ids that differed in their last character.
//
// The id is an argument rather than read off the path here, because a door that
// names two rows - an edge has a todo and a blocker - cannot tell which of them
// missed, and a note that guessed would be this same defect wearing a label.
// Only a caller that knows which id failed may ask for the sentence.
//
// A diagnosis that cannot be produced is not an error the caller should see:
// the answer to their request is 404 either way, and turning a failed
// explanation into a 500 would replace a usable refusal with an unusable one.
// So a store failure here costs the sentence and nothing else.
func (s *server) misreadIDNote(r *http.Request, id string) string {
	m, err := s.db.MisreadArtifactID(r.Context(), principalOf(r), id)
	if err != nil || m == nil {
		return ""
	}
	what := "a chat message"
	names := "; the row it is about is "
	if m.Space == store.IDSpaceThread {
		what = "a chat thread"
		names = "; the row raised in it is "
	}
	note := " - that id names " + what + ", not a row"
	if m.Artifact != "" {
		note += names + m.Artifact
	}
	return note
}

// handleDeleteArtifact tombstones an artifact: the row stays, marked, with a
// fresh clock reading, so the delete can replicate as a fact rather than as an
// absence.
//
// POST /api/artifact/{id}/delete
func (s *server) handleDeleteArtifact(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	id := r.PathValue("id")

	art, err := s.db.ReadArtifact(r.Context(), p, id, false)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such artifact"))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	if art.OwnerUser != p.UserID {
		writeJSON(w, http.StatusForbidden, errorBody("not the owner of "+id))
		return
	}

	art, err = s.db.TombstoneArtifact(r.Context(), p, id)
	if errors.Is(err, store.ErrNotFound) {
		// The row changed hands between the read and the delete - a merge
		// landing mid-request - so the delete found nothing of the caller's.
		writeJSON(w, http.StatusNotFound, errorBody("no such artifact"))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, art)
}

// handleSearch ranks the artifacts the principal may read against a free text
// query. The match covers title, body, discovery and tags, so a word an agent
// only ever wrote in the discovery still finds the artifact.
//
// GET /api/search?q=&type=&project=
func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()
	query := q.Get("q")

	hits, err := s.db.SearchArtifacts(r.Context(), p, store.ArtifactQuery{
		Query:    query,
		Type:     q.Get("type"),
		Kind:     q.Get("kind"),
		Project:  q.Get("project"),
		Status:   q.Get("status"),
		ScopeAll: scopeAll(r, p),
		Limit:    intParam(q.Get("limit")),
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, stampScope(
		map[string]any{"query": query, "artifacts": hits}, answerScopeOf(r, principalOf(r))))
}

// eventRequest is the append body. There is no project field: an event lands in
// the principal's home project, because that is the only project it is entitled
// to write into.
type eventRequest struct {
	Type    string   `json:"type"`
	Room    string   `json:"room"`
	Thread  string   `json:"thread"`
	Parents []string `json:"parents"`
	// Actor is accepted and ignored. Who wrote an event is decided by the
	// token, here and everywhere else.
	Actor    string          `json:"actor"`
	Artifact string          `json:"artifact"`
	Body     string          `json:"body"`
	Meta     json.RawMessage `json:"meta"`
}

// mintedTypes are the event types a handler of this node writes and a client
// may not. Each of them is a claim the node itself makes - a lifecycle move, a
// handoff, something the forge bridge did - and the trail is only worth reading
// if the only way to get one is to actually do the thing. chat is not in here:
// it carries no authority beyond what POST /api/chat/{room}/say already gives
// the same principal.
var mintedTypes = map[string]bool{
	statusEventType: true,
	taskEventType:   true,
	forgeEventType:  true,
	// The quiesce log. An ack is what releases a resource and lets a
	// maintenance change proceed, so an ack anybody can type is a gate anybody
	// can open - and a hold anybody can type is a way to stop somebody else's
	// release by claiming to depend on it.
	store.EventAnnouncement:   true,
	store.EventQuiesceHold:    true,
	store.EventQuiesceRelease: true,
	store.EventQuiesceAck:     true,
	// A pin, and the entry that takes it down. Written by the verb because the
	// refusals that make a room's strip trustworthy are there: the message
	// exists, this reader can see it, and it was said in this room.
	store.EventPinAdd:    true,
	store.EventPinRemove: true,
	// A vote, and the closure that stops them. Both are written by the verb
	// that does the thing, because both refusals that make the record worth
	// reading live there - see store.CastVote.
	store.EventProposalVote:  true,
	store.EventProposalClose: true,
	// An edge in the queue and the entry that takes one back. Every refusal that
	// makes the graph safe to drain is on the verb - see store.writeDep - so an
	// edge written by hand is an edge with none of them asked, read by a machine
	// deciding whether to start work.
	store.EventDepAdd:    true,
	store.EventDepRemove: true,
	// An assignment. The refusal that makes it worth reading is on the verb - the
	// writer has to be able to read the todo - and the value it is the record of
	// is written in the same transaction, so an entry handed in here would be a
	// handover that never happened, claimed about work the writer may not be able
	// to see. See store.AssignTodo.
	store.EventTodoAssign: true,
	// And a queue move, which is the same argument about the other half of the
	// metadata: "this is done" is the claim the whole queue is drained on, and one
	// typed by hand would be a closure with none of the verb's refusals asked and
	// nothing on the row to match it. See store.SetTodoStatus.
	store.EventTodoStatus: true,
	// And a classification. The closed set is the whole value of that field, and
	// the only thing holding it closed is the verb - an entry typed in here would
	// be "somebody filed this as a defect" recorded as a decision, with no such
	// category on the row and nothing able to count either. See
	// store.SetTodoCategory.
	store.EventTodoCategory: true,
	store.EventTodoSteal:    true,
	// A repro run's verdict. The refusal that makes the log worth reading -
	// the finding has a project, so the run stays readable by whoever can
	// read the finding rather than only by whoever reported it - is on the
	// verb. See store.RecordFindingRun.
	store.EventFindingRun: true,
	// And where a finding stands upstream. The refusals that make the record a
	// fact rather than a word - a state in the vocabulary, an issue number
	// behind it, and no second filing over one that still stands - are all on
	// the verb, and the filing is written on the row in the same transaction. An
	// entry typed in here would say a finding was filed as an issue that nobody
	// opened. See store.SetFindingUpstream.
	store.EventFindingUpstream: true,
	// And how strong the evidence is. The refusal that makes that word a fact -
	// `verified` names the commit the reproduction was run against - is on the
	// verb, and the claim is written on the row in the same transaction. An entry
	// typed in here would say a finding was verified against a build nobody ran.
	// See store.SetFindingEvidence.
	store.EventFindingEvidence: true,
	// And an edit of a todo's words, which is the sharpest case on this list:
	// the entry's whole content is "this was written against a row that had not
	// moved", and the compare-and-set on the verb is the only thing that makes
	// that sentence true. One typed by hand would be a lost update carrying a
	// record that says it was not one. See store.EditTodo.
	store.EventTodoEdit: true,
	// And a note on a row. The entry is its own content here rather than the
	// record of a value written beside it, so one typed in by hand is a
	// paragraph attributed to a seat that never wrote it, rendered under the
	// author's body as what somebody learned about the work. See
	// store.AppendTodoNote.
	store.EventTodoNote: true,
}

// handleAppendEvent appends to the log. The log is append-only: there is no
// update and no delete, and the DAG is carried in parents rather than in the
// order rows happen to land.
//
// The actor is the token's, always. It used to be whatever the body said, which
// made the log worth nothing: anybody holding any token could write an entry
// under somebody else's name, and the history that the lifecycle, the inbox and
// the forge bridge all read back would have been signed by a stranger.
//
// POST /api/events
func (s *server) handleAppendEvent(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req eventRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	if req.Type == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("type is required"))
		return
	}
	if mintedTypes[req.Type] {
		writeJSON(w, http.StatusForbidden,
			errorBody("a "+req.Type+" event is written by the endpoint that does the thing, "+
				"not by hand"))
		return
	}

	// A thread the caller named is a thread the caller has to be able to read.
	// The tasks clause in the event filter shows a thread to the parties to the
	// task that names it, so appending to one that is closed to you is a way to
	// put words in front of people whose conversation you cannot see.
	if req.Thread != "" && !s.mayWriteThread(w, r, req.Thread) {
		return
	}
	if req.Parents == nil {
		req.Parents = []string{}
	}
	// And the edges it claims are edges to events it can see. This path took the
	// whole list on trust, which is how an event came to descend from ids that
	// are not here.
	if !s.mayNameParents(w, r, req.Parents) {
		return
	}
	// And the artifact it says it is about is one it can read. Same reason: the
	// column is a claim, the event filter carries the events about an artifact
	// to everybody it is shared with, and this one was the last of the four
	// still going in on trust.
	if !s.mayNameArtifact(w, r, req.Artifact) {
		return
	}

	// An agent acting on its own behalf is the actor; otherwise the user is.
	// req.Actor is read and dropped: it is accepted so an older client is not
	// broken by a 400, and it decides nothing.
	actor, _ := chatActor(p)
	var project *string
	if p.Project != "" {
		home := p.Project
		project = &home
	}

	e := &store.Event{
		Type:     req.Type,
		Project:  project,
		Room:     req.Room,
		Thread:   req.Thread,
		Parents:  req.Parents,
		Actor:    actor,
		Artifact: req.Artifact,
		Body:     req.Body,
		Meta:     withTrace(speakerStripped(req.Meta), traceIDOf(r)),
	}
	if err := s.db.AppendEvent(r.Context(), e); err != nil {
		// A body the store refuses is the caller asking for something they may
		// not have, not this node being broken. Without this the ceiling on a
		// reaction arrived as a 500, which tells a client to retry the one
		// thing that will never work.
		var refused store.ReactionError
		if errors.As(err, &refused) {
			writeJSON(w, http.StatusBadRequest, errorBody(refused.Why))
			return
		}
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// actorMetaPrefix is what the handlers that mint an event write the speaker
// under: actor_kind says whether a person or their agent said it, actor_user
// says which person, and actor_name says what that person was called at the
// time. The console renders them, and the gate reads them back to tell an
// agent's message from its user's.
//
// It is the store's constant rather than a second copy of the string: the merge
// reads the same keys to decide whether a replicated event is claiming a
// speaker, and two spellings of "actor_" would be two doors with different
// ideas about what attribution is.
const actorMetaPrefix = store.ActorMetaPrefix

// speakerStripped drops the speaker keys - and the trace key - out of meta a
// client handed over.
//
// The actor column has been the token's since the forgery in it was fixed, but
// meta rode in verbatim, and every reader of an event that cares who is
// speaking reads meta - so `{"actor_kind":"agent","actor_user":"somebody"}` on
// a hand-appended event was the same forgery through a second door: the row is
// correctly signed and reads, everywhere it is rendered, as somebody else.
// Meta is still carried, because it is where a client puts what an event is
// about; it is simply not a channel for saying who is talking.
//
// The mentions key is the same rule for addressing. It says which words in a
// body the node resolved to people, and the console draws those words in that
// person's colour and rings the reader's own - so a client that could write it
// would be putting somebody's name, or the reader's, on a message that named
// nobody. The node stamps it where it does the resolving, in mentions.go.
//
// The trace key is the same rule for correlation rather than for attribution.
// A trace id in meta is what carries a handoff across the node boundary, and
// the far node reads it back off the thread and continues that trace - so a
// client that could write one could join its own events to somebody else's
// trace, or scatter a trace across events that had nothing to do with it. The
// node stamps it, on the writes it makes, and nowhere else - see withTrace.
//
// The cite key is the sharpest version of the first rule rather than a new one.
// It says which message - or which words of which message - this one is
// answering, and the console draws that as a quotation, under the quoted
// principal's name and in their colour. A client that could write its own would
// be putting words in somebody else's mouth on a row that is correctly signed
// and correctly actored, which is the same forgery the actor keys are stripped
// to prevent. The node stamps it where it has checked that the source is
// readable and the span is inside it - see mayCite in chat.go.
//
// The worklog's stamped keys are the same rule for a claim about whose work
// something was. A worklog entry is not a minted type - it has to replicate, and
// a minted one does not - so POST /api/events will write an event of that type,
// and every one of these fields is a claim the worklog verb CHECKS before it
// stamps it: refs against the writer's own read filter, subject against the
// principals that exist here, run and verify as the evidence a reader decides on.
// Handed in through this door they would be the claim without the check - an
// entry pointing at work its author cannot see, or one vouching for a seat, on a
// row that is correctly signed and correctly actored. So they are the node's to
// write, in worklog.go, and here they are dropped. What an entry says about its
// own shift - what, next, as_of, branch - is not on the list: it claims nothing
// about anybody else, and the body beside it was always a client's to write.
//
// Anything that is not a JSON object is dropped whole: there is nothing to
// strip out of it and nothing that needs it through.
func speakerStripped(meta json.RawMessage) json.RawMessage {
	if len(meta) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(meta, &fields); err != nil {
		return nil
	}
	for k := range fields {
		if strings.HasPrefix(k, actorMetaPrefix) ||
			k == store.TraceMetaKey || k == store.MentionsMetaKey ||
			k == store.CiteMetaKey || worklogStampedKeys[k] {
			delete(fields, k)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	clean, err := json.Marshal(fields)
	if err != nil {
		return nil
	}
	return clean
}

// handleListEvents reads the log in order, filtered to what the principal may
// see.
//
// GET /api/events?thread=&since=
func (s *server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()

	since, err := strconv.ParseInt(orZero(q.Get("since")), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("since must be a packed hlc integer"))
		return
	}

	list, err := s.db.ListEvents(r.Context(), p, store.EventQuery{
		Thread:   q.Get("thread"),
		Room:     q.Get("room"),
		Type:     q.Get("type"),
		Since:    since,
		ScopeAll: scopeAll(r, p),
		Limit:    intParam(q.Get("limit")),
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := s.db.FillDisowned(r.Context(), nil, list); err != nil {
		log.Printf("disowned: could not resolve repudiations for an event page: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": list})
}

// grantRequest is the body of a grant. Leave artifact empty for a project-wide
// grant, or name one artifact - with the user it is shared to - for a share.
type grantRequest struct {
	FromProject string `json:"from_project"`
	ToProject   string `json:"to_project"`
	Subject     string `json:"subject"`
	Artifact    string `json:"artifact"`
	Cap         string `json:"cap"`
}

// handleCreateGrant issues a capability.
//
// You can only give away what is yours: a project-wide grant has to be written
// by a principal of the project being opened up (to_project), and a share has
// to be written by the artifact's owner. Personal artifacts cannot be shared at
// all - the floor holds in the store regardless, so this is only here to fail
// loudly rather than quietly.
//
// POST /api/grants
func (s *server) handleCreateGrant(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req grantRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	// A cap nothing reads is still a cap that is stored, signed and replicated.
	// Every read rule here treats a live grant as a read and never looks at the
	// column, so `write` would be accepted, travel to every peer and describe a
	// reach nobody granted - waiting for the first reader that does look. The
	// set is what is implemented, and it is one entry today.
	if !store.GrantCapOK(req.Cap) {
		writeJSON(w, http.StatusBadRequest,
			errorBody("cap must be "+store.CapRead+" or left out; this node implements no other"))
		return
	}

	if req.Artifact != "" {
		art, err := s.db.ReadArtifact(r.Context(), p, req.Artifact, false)
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorBody("no such artifact"))
			return
		}
		if err != nil {
			serverError(w, r, err)
			return
		}
		if art.OwnerUser != p.UserID {
			writeJSON(w, http.StatusForbidden, errorBody("not the owner of "+req.Artifact))
			return
		}
		if art.Visibility == "personal" || art.Project == nil {
			writeJSON(w, http.StatusBadRequest,
				errorBody("a personal artifact cannot be shared; give it a project first"))
			return
		}
		if req.Subject == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("subject is required for a share"))
			return
		}
		// A share is about one artifact, and both ends of the edge it writes
		// follow from that: it lands where the artifact lives, and it comes
		// from where the owner handing it over is. Neither is the caller's to
		// say, and to_project was only defaulted while from_project was never
		// looked at at all - so a share carried whatever edge its body claimed,
		// and a grants table read by project rather than by subject would have
		// believed it.
		req.ToProject = *art.Project
		req.FromProject = p.Project
	} else {
		if req.FromProject == "" || req.ToProject == "" {
			writeJSON(w, http.StatusBadRequest,
				errorBody("from_project and to_project are required for a project grant"))
			return
		}
		if req.ToProject != p.Project {
			writeJSON(w, http.StatusForbidden,
				errorBody("only a principal of "+req.ToProject+" can open it up"))
			return
		}
	}

	g := &store.Grant{
		FromProject: req.FromProject,
		ToProject:   req.ToProject,
		Subject:     req.Subject,
		Artifact:    req.Artifact,
		Cap:         req.Cap,
		GrantedBy:   p.UserID,
	}
	if err := s.db.InsertGrant(r.Context(), g); err != nil {
		if errors.Is(err, store.ErrUndeclaredProject) {
			// Both ends of a grant name a declared project. A capability into a
			// project nobody declared opens nothing - it is a typo that would
			// replicate - and the caller is told which end is the problem.
			writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
			return
		}
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// handleWhoami echoes the principal a token resolved to, which is the quickest
// way to find out why a read came back empty.
//
// It answers where this token's writes land as well as who it is, because the
// two are one question and only one of them was ever answered. A token is a
// (user, agent, project) triple and the project half decides which project
// every artifact, memory item and worklog entry written with it lands in - so
// "you are alice" without "and everything you write goes into pa, which is a
// fixture" is the shape of answer that let a day of real work sit in demo seed
// data. See projects.go.
//
// GET /api/whoami
func (s *server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	out := whoamiResponse{Principal: p}
	if p != nil {
		declared, project := s.projectFacts(r.Context(), p.Project)
		out.Declared = declared
		if project != nil {
			out.Fixture, out.Origin = project.Fixture, project.Origin
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// intParam parses an optional positive integer parameter, treating anything
// unparseable as absent.
func intParam(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// assigneeArg reads ?assignee=. It answers a name to match, or that the caller
// asked for the rows nobody is carrying, or a refusal.
//
// The order matters: NormalizeAssignee turns every nobody-word into the empty
// string, and an empty Assignee means "do not filter". Asking that question
// first is what keeps ?assignee=none from quietly answering with the whole
// queue - which is the failure this door has made twice in other shapes, an
// answer that is wrong and looks exactly like a right one.
func assigneeArg(raw string) (name string, unassigned bool, err error) {
	if strings.TrimSpace(raw) == "" {
		return "", false, nil
	}
	if store.NobodyName(raw) {
		return "", true, nil
	}
	name, err = store.NormalizeAssignee(raw)
	if err != nil {
		return "", false, err
	}
	return name, false, nil
}
