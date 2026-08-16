package main

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
}

// handleListProjects enumerates the projects the principal may be shown.
//
// GET /api/projects
func (s *server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	list, err := s.db.ListProjects(r.Context(), p, r.URL.Query().Get("scope") == "all")
	if err != nil {
		serverError(w, r, err)
		return
	}
	out := projectsResponse{Count: len(list), Current: p.Project, Projects: list}
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
	name := fs.String("project", "", "the project's name")
	origin := fs.String("origin", "", "the project's git remote")
	fixture := fs.Bool("fixture", false, "mark it as demo seed data rather than real work")
	if err := fs.Parse(args); err != nil {
		return err
	}
	base := resolveURL(*url, os.Getenv("FLOWY_ADDR"))
	bearer, err := resolveToken(*token, os.Getenv("FLOWY_TOKEN"))
	if err != nil {
		return err
	}
	if bearer == "" {
		return errors.New("no token: pass --token, set FLOWY_TOKEN, or write one to " +
			"~/.config/" + tokenFile)
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
	// scope=all so that an operator sees the whole registry rather than the
	// corner their own token is in. It does nothing at all for anybody else -
	// the node only obeys it for its operator - so asking for it always is one
	// request instead of two and a guess about which one is answering.
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
