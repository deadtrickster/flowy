package flowy

// The project surface: the registry over HTTP, and the two commands an
// operator drives it with.
//
// What this is for is one sentence: a project was a free string on a token, so
// a day of real shared memory was filed into `pa` - the smoke seeder's fixture
// project - and no surface said so. The registry is the referent that makes a
// write into an undeclared project a refusal, and the indicator is what makes a
// write into a fixture visible at the moment it is made.
//
// The indicator matters more than the enumeration, and the enumeration is the
// part that looks like a feature. A dropdown filters what a token can already
// see; an indicator tells you the thing nobody knew. So `flowy projects` leads
// with which project this token writes into and whether that project is a
// fixture, and the list is underneath it.
//
// Nothing here is a permission. Declaring a project grants nothing and reveals
// nothing: a principal still writes only into its own project (see
// handleCreateArtifact's "you write where you are"), and still reads exactly
// what grants and scope allowed before this file existed. The one thing that is
// operator-only is changing a project that is already declared - its name, its
// origin, its fixture flag - because that is the row other nodes decide a
// collision against.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/store"
)

// ------------------------------------------------------------- the API

// projectRequest is the declaration body. Fixture is a pointer so that leaving
// it out and saying false are different: an update that does not mention the
// flag must not clear it.
type projectRequest struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Origin string `json:"origin"`
	// Pin is the operator settling a name collision by hand: this name, on this
	// node, means this origin. It is a separate word from a declaration because
	// it is a stronger statement - nothing arriving over sync overwrites a
	// pinned row - and a stronger statement should have to be asked for.
	Pin     bool  `json:"pin"`
	Fixture *bool `json:"fixture"`
}

// projectsResponse is the enumeration, and it leads with the caller's own
// project rather than only listing the rows: "which of these am I writing
// into" is the question the list is usually a roundabout way of asking.
type projectsResponse struct {
	Count    int              `json:"count"`
	Current  string           `json:"current,omitempty"`
	Fixture  bool             `json:"current_is_fixture"`
	Projects []*store.Project `json:"projects"`
	// Reads is the subset of those names whose rows this principal can actually
	// reach - see store.ReadableProjects. It is a second list rather than a flag
	// on the row because a Project is a signed registry row and this is a fact
	// about the reader, not about the project: two principals asking get the same
	// rows back and different Reads.
	//
	// The distinction is load-bearing for anything that says how far a list
	// reaches. Projects is names, and is deliberately wider - a grant edge either
	// way puts a name here. Reads is one-directional, because reading is.
	Reads []string `json:"reads"`
}

// handleListProjects enumerates the projects the principal may be shown.
//
// GET /api/projects
func (s *server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	// THROUGH THE GATE, not straight off the query string. scopeAll() exists to
	// answer this and says so in its own comment - "the answer is only ever yes
	// for this node's operator; for anyone else the parameter is simply not
	// there as far as the store is concerned" - and this handler read the raw
	// parameter instead, so ProjectFilterSQL returned TRUE for anybody who
	// typed it.
	//
	// MEASURED 2026-08-28 with an ordinary worker token: GET /api/projects
	// answered 4, GET /api/projects?scope=all answered 8. whoami on the same
	// token reports no operator flag. So every project name on the node,
	// including ones the caller reaches by no grant and belongs to by no
	// membership, was one query parameter away from any principal that could
	// authenticate.
	//
	// Names only - this door lists a registry, it does not open anything - but a
	// permission that a caller can grant themselves by typing is not one.
	all := scopeAll(r, p)
	list, err := s.db.ListProjects(r.Context(), p, all)
	if err != nil {
		serverError(w, r, err)
		return
	}
	reads, err := s.db.ReadableProjects(r.Context(), p, all)
	if err != nil {
		serverError(w, r, err)
		return
	}
	out := projectsResponse{Count: len(list), Current: p.Project, Projects: list, Reads: reads}
	for _, project := range list {
		if project.ID == p.Project {
			out.Fixture = project.Fixture
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateProject declares a project.
//
// Anybody may declare one that is not here yet, and that widens nothing: a
// project nobody holds a token for is a name and no more, because writes land
// in the principal's own project and reads are the grants they always were.
// Changing one that IS here is the operator's, because the origin on that row
// is what every other node decides a name collision against, and the fixture
// flag is what tells the next agent it is writing into demo data.
//
// POST /api/projects
func (s *server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req projectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	if req.Fixture != nil && *req.Fixture && !p.Operator {
		writeJSON(w, http.StatusForbidden,
			errorBody("only this node's operator marks a project as a fixture"))
		return
	}
	if req.Pin && !p.Operator {
		writeJSON(w, http.StatusForbidden,
			errorBody("a pin is how this node's operator settles a name collision, "+
				"and you are not the operator"))
		return
	}
	if req.Pin && strings.TrimSpace(req.Origin) == "" {
		writeJSON(w, http.StatusBadRequest,
			errorBody("a pin says which origin this name means here: origin is required"))
		return
	}

	held, err := s.db.Project(r.Context(), strings.TrimSpace(req.ID))
	switch {
	case errors.Is(err, store.ErrNotFound):
		held = nil
	case err != nil:
		serverError(w, r, err)
		return
	}
	if held != nil && !p.Operator {
		// Already declared, and the caller is not the one who may change it.
		// It answers with the row as it stands rather than with a refusal: a
		// declaration is idempotent, and the caller's intent - "let this
		// project exist" - is already true.
		writeJSON(w, http.StatusOK, map[string]any{"declared": false, "project": held})
		return
	}

	project := &store.Project{ID: req.ID, Name: req.Name, Origin: req.Origin, CreatedBy: p.UserID}
	if req.Pin {
		project.Provenance = store.ProvenancePinned
	}
	if held != nil {
		project.Fixture = held.Fixture
	}
	if req.Fixture != nil {
		project.Fixture = *req.Fixture
	}
	if err := s.db.DeclareProject(r.Context(), project); err != nil {
		if errors.Is(err, store.ErrBadProjectName) {
			writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
			return
		}
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"declared": true, "project": project})
}

// whoamiResponse is the principal plus what this node knows about the project
// it writes into.
//
// The three extra fields are the indicator, and they are here rather than in a
// second call because the moment a client asks who it is, is the moment it can
// be told where its writes land. A client that only reads `project` still works
// exactly as it did.
type whoamiResponse struct {
	*store.Principal
	// Declared says the project this token writes into has a registry row. A
	// false here on a live token means the registry and the tokens table have
	// come apart, which the foreign key should make impossible.
	Declared bool `json:"project_declared"`
	// Fixture says that project is demo seed data. It refuses nothing - a
	// fixture is a legitimate writable project - and it is exactly the sentence
	// nobody was shown when a day of real work went into `pa`.
	Fixture bool   `json:"project_fixture"`
	Origin  string `json:"project_origin,omitempty"`
	// WHERE THIS PERSON MAY WORK, which is not the same question as what they
	// may read.
	//
	// A person reads more than they are a member of - a grant points at a
	// project they have never joined - so this is the list a switcher offers
	// and `reads` on /api/projects is the list a reader may look into. Writing
	// where you can read would put work in places nobody is looking.
	//
	// NOT omitempty, and it matters: a person who belongs to nothing must send
	// [] rather than nothing at all, because "no memberships" and "this node
	// does not report memberships" are different facts and a client cannot tell
	// them apart from an absent key. That collapse has cost this fleet six
	// separate defects in two days.
	Memberships []string `json:"memberships"`
	// WHICH MECHANISM CARRIES THIS PRINCIPAL'S REACH, so an absent Memberships
	// says which kind of absent it is.
	//
	// Memberships alone cannot carry three facts. A person who belongs to
	// nothing is [], a list this node could not read is null, and an agent -
	// for whom the question does not apply at all, because a seat's reach is
	// minted into its token - was ALSO answering []. The field above says in
	// its own comment that collapsing two of those has cost this fleet six
	// defects; the third one was being collapsed a line below it.
	//
	// So it names the mechanism positively rather than leaving a reader to
	// infer it from a missing value:
	//
	//   "memberships" + a list   this person works in these
	//   "memberships" + []       this person belongs to nothing yet
	//   "memberships" + null     this node could not read them, and says so
	//   "token"       + null     an agent: not a question about this principal
	//
	// IT IS reach_from AND NOT reach, BECAUSE "reach" READ AS A SET.
	//
	// `"reach": "memberships"` sitting beside `"memberships": ["pa","flowy"]`
	// says the mechanism and looks like the list's label, and a reader who
	// takes it that way concludes the session reaches BOTH projects at once. I
	// am that reader: on 2026-09-03 I read it as an equality, called the
	// enforcement a defect against it, and told the operator twice that
	// widening session reach was a decision they owed us. It was not - the
	// enforcement was right and this word was doing two jobs.
	//
	// WHAT A SESSION ACTUALLY REACHES AT ANY MOMENT IS `project`, the field the
	// embedded Principal carries. memberships is the set a switcher may move
	// that project AMONG, not a set readable together - measured in
	// 01M1M526DX8EAY8TWAPYX4RTKC, where two ids swap 200/404 across a switch
	// while memberships stays 2. reach_from says which of the two mechanisms
	// chose `project`, and nothing more.
	//
	// omitempty because an unauthenticated request has no principal and so no
	// mechanism either - and an empty string would be a fourth thing to guess
	// at.
	ReachFrom string `json:"reach_from,omitempty"`
}

// The two mechanisms a principal's reach can come FROM - not the reach itself.
// A person's is project_members and can be changed by an owner; an agent's is
// minted into its token and cannot be changed at all, which is why "join a
// project" is not an act available to a seat.
//
// Either way the reach in force is ONE project - the principal's `project` -
// and these name how it was chosen. See the field's own comment for what
// happened when the distinction was left to the reader.
const (
	reachMemberships = "memberships"
	reachToken       = "token"
)

// handleEnterProject puts THIS SESSION into one of this person's projects.
//
// POST /api/projects/{project}/enter
//
// The operator, on 2026-08-20: "i as a user dont care about per project tokens
// - i want a human thing - my own projects without logging out/in." Until this
// existed a cookie session carried no project at all, so a person's writes had
// nowhere to land and the only way to change project was to paste a different
// agent's token - which is the machine's mechanism worn by a person.
//
// A SESSION ACT, NOT A CREDENTIAL ACT. Nothing about who you are changes; where
// you are working does. That is why it needs no re-auth and why it is safe as a
// control rather than as a login screen - and why an agent cannot call it: a
// bearer token has no session to be put anywhere, and its reach is
// token_projects, a different mechanism for a different kind of principal.
//
// THE ANSWER SAYS WHERE YOU NOW WRITE, not "ok". The rule that made this whole
// area findable is `flowy say` printing the project it wrote into rather than
// the one it was told to: anything that changes where writes land says so.
func (s *server) handleEnterProject(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if p == nil || p.UserID == "" {
		writeJSON(w, http.StatusForbidden, errorBody("only a person has projects to work in"))
		return
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || strings.TrimSpace(c.Value) == "" {
		// A bearer token reaches here with a real principal and no session.
		// Saying "log in" would be wrong for an agent, so this names the actual
		// difference rather than the symptom.
		writeJSON(w, http.StatusForbidden, errorBody(
			"this is a session act: a bearer token's projects are minted into it, "+
				"and it has no session to put into one"))
		return
	}

	project := strings.TrimSpace(r.PathValue("project"))
	if err := s.db.EnterProject(r.Context(), c.Value, p.UserID, project); err != nil {
		// The store's own sentence: it already tells "no such project" from
		// "you are not a member of it", which are two different people to go
		// and talk to.
		writeJSON(w, http.StatusForbidden, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":    project,
		"writing_in": project,
	})
}

// handleJoinProject makes somebody a member of one.
//
// POST /api/projects/{project}/members  {"user": "<id or handle>"}
//
// THE OPERATOR'S ACT. Membership decides where a person's work lands, so a
// person who could grant it to themselves could put work anywhere - the same
// reason a token cannot widen its own reach. It is idempotent: "make sure they
// are in it" is what an operator means, and a second call is not a failure.
func (s *server) handleJoinProject(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if p == nil || p.UserID == "" {
		writeJSON(w, http.StatusForbidden, errorBody("only a person invites people into a project"))
		return
	}
	project := strings.TrimSpace(r.PathValue("project"))
	// WHO MAY INVITE: the people who own the project, and this node's operator.
	//
	// The operator asked for exactly this - "normal ownership and collaboration
	// - i will invite other humans to projects" - so it is not the node
	// operator's act alone. Being a MEMBER is not enough: working somewhere and
	// deciding who else works there are different powers.
	if !p.Operator {
		may, err := s.db.MayInvite(r.Context(), p.UserID, project)
		if err != nil {
			serverError(w, r, err)
			return
		}
		if !may {
			writeJSON(w, http.StatusForbidden, errorBody(
				"you do not own "+strconv.Quote(project)+", so you cannot say who works in it - "+
					"its owner or this node's operator can"))
			return
		}
	}
	var req struct {
		User string `json:"user"`
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	// BY ID OR BY HANDLE, because an operator types a handle and a script has
	// an id, and refusing one of them makes the door usable by half its
	// callers. A handle that resolves to nobody is named in the refusal rather
	// than answered as "bad request": the operator's next question is always
	// which of the two they got wrong.
	name := strings.TrimSpace(req.User)
	user, err := s.db.GetUser(r.Context(), name)
	if err != nil {
		user, err = s.db.UserByHandle(r.Context(), name)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest,
			errorBody("no user called "+strconv.Quote(name)+" on this node, by id or by handle"))
		return
	}
	if err := s.db.JoinProject(r.Context(), user.ID, project, req.Role); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	mine, err := s.db.ProjectsOfUser(r.Context(), user.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	// WHAT THEY CAN REACH AFTERWARDS, rather than "ok": the fact an operator
	// wants is the new set, and a door that answers success makes them ask a
	// second question to learn what they just did.
	writeJSON(w, http.StatusOK, map[string]any{"user": user.ID, "memberships": mine})
}

// projectFacts is the registry's view of one project name, for a surface that
// is about to show it. A name with no row is not an error here: the caller is
// reporting, not writing.
func (s *server) projectFacts(ctx context.Context, name string) (bool, *store.Project) {
	if name == "" {
		return false, nil
	}
	project, err := s.db.Project(ctx, name)
	if err != nil {
		return false, nil
	}
	return true, project
}

// ------------------------------------------------------------- the command

const projectsUsage = `flowy projects - which project this token writes to, and what else is here

usage:
  flowy projects [list]                     what this token writes to, then the registry
  flowy projects declare --project NAME [--origin REMOTE] [--fixture]
  flowy projects pin --project NAME --origin REMOTE

  --url URL     node to ask (default $FLOWY_ADDR, then http://127.0.0.1:8787)
  --token T     bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)
  --agent NAME  the seat speaking, whose token is ~/.config/flowy/agents/NAME
                (default $FLOWY_AGENT). ~/.config/flowy/token is the OPERATOR'S
                own, so falling through to it warns; --agent me is the operator
                saying it was meant, and stops the warning

An origin is a git remote and is canonicalised before it is stored, so
git@github.com:x/y.git and https://github.com/x/y are one project. Left out on a
declare inside a git work tree, the remote is read from there; with no repository
at all the project gets a derived identity, which is a first-class case.

A pin is how a name collision is settled: two nodes' "flowy" with two different
remotes are two projects, the merge refuses to fold them together, and the pin
says which one this node means.
`

// projectsCmd is `flowy projects`.
//
// It speaks HTTP rather than opening the database, like `flowy tui` and unlike
// `flowy mcp`: the question it answers is about a token, and a token means
// something to a node rather than to a DSN. That is also what makes it usable
// from the machine an agent is actually running on.
func projectsCmd(args []string) error {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}

	fs := flag.NewFlagSet("projects", flag.ContinueOnError)
	url := fs.String("url", "", "node to talk to (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token := fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)")
	agent := fs.String("agent", "", agentFlagHelp)
	name := fs.String("project", "", "the project's name")
	origin := fs.String("origin", "", "the project's git remote")
	fixture := fs.Bool("fixture", false, "mark it as demo seed data rather than real work")
	if err := fs.Parse(args); err != nil {
		return err
	}
	base := resolveURL(*url, os.Getenv("FLOWY_ADDR"))
	bearer, err := resolveToken(*token, os.Getenv("FLOWY_TOKEN"), *agent, os.Getenv("FLOWY_AGENT"))
	if err != nil {
		return err
	}
	if bearer == "" {
		return errNoToken()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch sub {
	case "list":
		return listProjects(ctx, base, bearer)
	case "declare":
		remote := *origin
		if remote == "" {
			remote = gitRemote()
		}
		return declareProject(ctx, base, bearer, projectRequest{
			ID: *name, Origin: remote, Fixture: fixture,
		})
	case "pin":
		if *origin == "" {
			return errors.New("a pin says which origin this name means here: pass --origin")
		}
		return declareProject(ctx, base, bearer, projectRequest{
			ID: *name, Origin: *origin, Pin: true,
		})
	case "help", "--help", "-h":
		fmt.Print(projectsUsage)
		return nil
	}
	return fmt.Errorf("unknown subcommand %q\n\n%s", sub, projectsUsage)
}

// listProjects prints the indicator first and the registry second, because the
// indicator is the part that was missing.
func listProjects(ctx context.Context, base, token string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	var who whoamiResponse
	if err := peerRequest(ctx, client, http.MethodGet, base+"/api/whoami", token, nil, &who); err != nil {
		return err
	}
	var list projectsResponse
	// scope=all so that the caller sees the whole registry rather than the
	// corner their own token is in. It is the whole table for everybody, not
	// only for the operator: the registry is a list of names, and this command
	// is how somebody finds the name of a project that was declared while they
	// were not looking. What they may read in those projects is a different
	// question, and Reads on the response answers it.
	if err := peerRequest(ctx, client, http.MethodGet, base+"/api/projects?scope=all", token,
		nil, &list); err != nil {
		return err
	}

	switch {
	case who.Principal == nil || who.Project == "":
		fmt.Println("this token writes to no project: what it writes is personal to its own user")
	case who.Fixture:
		fmt.Printf("this token writes to %s - A FIXTURE PROJECT (demo seed data, not real work)\n",
			who.Project)
	default:
		fmt.Printf("this token writes to %s\n", who.Project)
	}
	if who.Principal != nil && who.Project != "" && !who.Declared {
		fmt.Printf("  and %s has no registry row, which should not be possible - "+
			"reload schema.sql\n", who.Project)
	}

	sort.Slice(list.Projects, func(i, j int) bool { return list.Projects[i].ID < list.Projects[j].ID })
	fmt.Printf("\n%d project(s) you can see:\n", len(list.Projects))
	for _, project := range list.Projects {
		here := "  "
		if who.Principal != nil && project.ID == who.Project {
			here = "* "
		}
		marks := project.Provenance
		if project.Fixture {
			marks += ", fixture"
		}
		fmt.Printf("%s%-24s %-40s (%s)\n", here, project.ID, project.Origin, marks)
	}
	return nil
}

// declareProject posts a declaration and says what the node did with it.
func declareProject(ctx context.Context, base, token string, req projectRequest) error {
	if strings.TrimSpace(req.ID) == "" {
		return errors.New("which project: pass --project")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	var out struct {
		Declared bool           `json:"declared"`
		Project  *store.Project `json:"project"`
		Error    string         `json:"error"`
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if err := peerRequest(ctx, client, http.MethodPost, base+"/api/projects", token,
		body, &out); err != nil {
		return err
	}
	if out.Project == nil {
		return fmt.Errorf("the node answered without a project: %s", out.Error)
	}
	what := "already declared"
	if out.Declared {
		what = "declared"
	}
	fmt.Printf("%s %s as %s (%s)\n", what, out.Project.ID, out.Project.Origin, out.Project.Provenance)
	for _, was := range out.Project.Superseded {
		fmt.Printf("  superseding %s\n", was)
	}
	return nil
}

// gitRemote is the origin remote of the work tree this command was run in, or
// empty when there is not one.
//
// It shells out to git rather than reading .git by hand, for the reason the
// forge bridge shells out to `gh`: the tool that owns the format is the tool
// that should read it, and a repository can be a worktree, a submodule or a
// symlink. It is best-effort by design - a project with no repository is a
// first-class case and gets a derived identity - so every failure here is
// silence rather than an error.
func gitRemote() string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
