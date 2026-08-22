package main

// WHAT A ROLE MEANS AT A DOOR.
//
// The operator, 2026-08-20: "invite other users with different permissions of
// course", and "some will be like me some readonly some cant close or cant
// rause".
//
// THE RULE THIS FILE EXISTS TO KEEP: a role name lands WITH the check that
// enforces it, never before. A person labelled readonly who can still raise a
// row is worse than no roles at all - the label says one thing, the node does
// another, and only the node is real.
//
// WHY A TABLE RATHER THAN A CHECK IN EACH HANDLER. Measured from serve.go: 120
// registered routes, 61 of them POST, DELETE or PUT. Sixty-one checks written
// sixty-one times is sixty-one places to forget the sixty-second, and this
// fleet already measured what happens to promises like that - ninety doors take
// an id and not one of them resolves a short one.
//
// So the shape is paramguard's, deliberately, down to the test: every route
// says what it needs, the walk in the suite fails on a route that says nothing
// AND on an entry naming a route that does not exist, and the door itself has
// nothing to remember. Enforcement lives in the suite; the middleware only
// reads what the suite made sure is there.

import (
	"net/http"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// What a route needs from the principal that reaches it.
const (
	// needsWrite puts something into a project or changes something in it. A
	// reader must not reach these.
	needsWrite = "write"
	// needsNothing is a read, or a personal or session act that a reader MUST
	// still be able to do - acking their own inbox, declaring their own
	// reader, entering a project they belong to, logging in. If "readonly"
	// stopped these it would mean "cannot use the tool", which is not a role
	// anybody asked for.
	needsNothing = "nothing"
	// needsOperator is already refused by the handler's own rule; it is named
	// here so the walk can tell "guarded elsewhere" from "nobody has said".
	needsOperator = "operator"
)

// routeNeeds says what each registered route requires. A route missing from
// this table fails the walk in roleguard_test.go rather than being allowed:
// absent is a mistake, and needsNothing is a decision somebody made.
var routeNeeds = map[string]string{
	"DELETE /api/artifact/{id}/origins/{origin}": needsWrite,
	"DELETE /api/chat/{room}/pin/{id}":           needsWrite,
	// A schedule row changes what a reader receives, so writing one is a
	// write. Reading the table and resolving it are plain reads, gated by
	// the project reach every other read is gated by, and the FLEET scope
	// carries its own operator check inside the handler - the scope is in
	// the request rather than in the route, so a route-level needsOperator
	// would refuse the project and room cases too.
	"DELETE /api/schedules/{signal}":           needsWrite,
	"PUT /api/schedules":                       needsWrite,
	"DELETE /api/inbox/reader/{name}":          needsNothing,
	"DELETE /api/todo/{id}/deps/{blocker}":     needsWrite,
	"POST /api/agent/{id}/projects":            needsOperator,
	"POST /api/announcement/{id}/ack":          needsNothing,
	"POST /api/announcement/{id}/resolve":      needsOperator,
	"POST /api/announcements":                  needsOperator,
	"POST /api/artifact/{id}/delete":           needsWrite,
	"POST /api/artifact/{id}/origins":          needsWrite,
	"POST /api/artifact/{id}/status":           needsWrite,
	"POST /api/artifacts":                      needsWrite,
	"POST /api/assign":                         needsWrite,
	"POST /api/attachment":                     needsWrite,
	"POST /api/bookmark":                       needsWrite,
	"DELETE /api/bookmark/{id}":                needsWrite,
	"POST /api/chat/{room}/pin":                needsWrite,
	"POST /api/chat/{room}/react":              needsWrite,
	"POST /api/chat/{room}/say":                needsWrite,
	"POST /api/chat/{room}/todo":               needsWrite,
	"POST /api/chat/{room}/todo/{id}/assignee": needsWrite,
	"POST /api/dm/{to}":                        needsWrite,
	"POST /api/events":                         needsWrite,
	"POST /api/finding/{id}/evidence":          needsWrite,
	// Its twin beside it. Recording where a finding went is a change to the
	// project's own row, which is what needsWrite means - and it is deliberately
	// not needsOperator: somebody who filed the issue but could not say so would
	// have to ask the finding's author to say it for them.
	"POST /api/finding/{id}/upstream": needsWrite,
	"POST /api/forge/file":            needsWrite,
	"POST /api/forge/sync":            needsOperator,
	// The mock forge's own doors, registered only when the node runs one. They
	// drive a fixture rather than a project, and they are the node's to drive:
	// a person in a project has no business steering another project's forge
	// through it.
	"POST /api/forge/mock/comment":         needsOperator,
	"POST /api/forge/mock/fail":            needsOperator,
	"POST /api/forge/mock/login":           needsOperator,
	"POST /api/forge/mock/on-file":         needsOperator,
	"POST /api/forge/mock/state":           needsOperator,
	"POST /api/grants":                     needsWrite,
	"POST /api/inbox/ack":                  needsNothing,
	"POST /api/inbox/reader":               needsNothing,
	"POST /api/instructions":               needsOperator,
	"POST /api/join/{id}/approve":          needsOperator,
	"POST /api/join/{id}/refuse":           needsOperator,
	"POST /api/lock":                       needsWrite,
	"POST /api/lock/release":               needsWrite,
	"POST /api/merge/{id}/abandon":         needsWrite,
	"POST /api/merge/{id}/blocked":         needsWrite,
	"POST /api/merge/{id}/unblocked":       needsWrite,
	"POST /api/merge/{id}/gate":            needsWrite,
	"POST /api/merge/{id}/land":            needsWrite,
	"POST /api/merge/{id}/renew":           needsWrite,
	"POST /api/openspec":                   needsWrite,
	"POST /api/projects":                   needsWrite,
	"POST /api/projects/{project}/enter":   needsNothing,
	"POST /api/projects/{project}/members": needsOperator,
	"POST /api/quiesce/hold":               needsOperator,
	"POST /api/quiesce/release":            needsOperator,
	"POST /api/repro/run":                  needsWrite,
	"POST /api/rooms":                      needsWrite,
	"POST /api/rooms/{room}/invite":        needsWrite,
	"POST /api/rooms/{room}/leave":         needsNothing,
	"POST /api/sync/push":                  needsOperator,
	"POST /api/task/{id}/delegate":         needsWrite,
	"POST /api/task/{id}/state":            needsWrite,
	"POST /api/todo/{id}/assignee":         needsWrite,
	"POST /api/todo/{id}/category":         needsWrite,
	"POST /api/todo/{id}/priority":         needsWrite,
	"POST /api/todo/{id}/deps":             needsWrite,
	"POST /api/todo/{id}/edit":             needsWrite,
	"POST /api/todo/{id}/note":             needsWrite,
	"POST /api/user/{id}/role":             needsOperator,
	// The VM doors. Operator rather than write, deliberately: spawning starts
	// a process on the host with a copy of a project tree in it, and say and
	// down reach into one already running. A writer is somebody who may file
	// rows; that is a different thing from somebody who may run code on the
	// machine serving this node.
	"POST /api/vm/spawn":          needsOperator,
	"POST /api/vm/{name}/say":     needsOperator,
	"POST /api/vm/{name}/down":    needsOperator,
	"POST /api/work/{id}/claim":   needsWrite,
	"POST /api/work/{id}/done":    needsWrite,
	"POST /api/work/{id}/release": needsWrite,
	"POST /api/worklog":           needsWrite,
	"POST /api/activity":          needsWrite,
	// Your own handle and password. A reader must be able to change their own
	// password - a role in a project says what you may do IN THAT PROJECT, not
	// whether you may hold an account.
	"PUT /api/me":               needsNothing,
	"PUT /api/me/auto_delegate": needsNothing,
}

// roleGuard refuses a project write by somebody whose role in that project does
// not allow it.
//
// WHO THIS APPLIES TO: a principal that came from a LOGIN, working in a
// project. Not a bearer token - a token's reach is token_projects, minted into
// the credential, which is a different mechanism for a different kind of
// principal. See docs/identity-and-access.md.
//
// MEASURED, and this is the correction that cost a red gate: the first cut said
// "has a user and no agent", which is TRUE OF EVERY TOKEN ON THIS NODE. The
// gate's own TOKEN_A resolves to a user with no agent, so the guard refused 321
// checks - it was asking a session question of a credential. store.Principal
// carries ViaSession for exactly this, set in the cookie branch of principalFor
// and nowhere else.
//
// A person with no active project is not refused here either; they have nowhere
// to write and the doors below say so in their own terms.
//
// EVERY ROUTE IS READ FROM THE TABLE, and a route missing from it is refused
// rather than allowed - the direction that fails safe. A reader who is
// inconvenienced says so; a reader who writes is a permission failure nobody
// reports.
func (s *server) roleGuard(api *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principalOf(r)
		// Not a person, or not working anywhere: nothing here to decide. The
		// bearer path is untouched, which is the constraint that lets this land
		// while four agents and a drainer are using the node.
		if p == nil || !p.ViaSession || strings.TrimSpace(p.Project) == "" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		_, pattern := api.Handler(r)
		// The catch-all, as in paramGuard: a pattern with no method is the 404
		// handler, and judging it would answer about permissions when the
		// caller's problem is the path.
		if pattern == "" || !strings.Contains(pattern, " ") {
			next.ServeHTTP(w, r)
			return
		}
		if routeNeeds[pattern] != needsWrite {
			// Including a route that says nothing: it is not treated as a write
			// here, and the walk in the suite is what makes sure that case does
			// not exist. Refusing an undeclared route at runtime would break
			// the node the moment somebody adds a door and forgets the table -
			// the suite is where that failure belongs, before it ships.
			next.ServeHTTP(w, r)
			return
		}

		role, err := s.db.RoleInProject(r.Context(), p.UserID, p.Project)
		if err != nil {
			serverError(w, r, err)
			return
		}
		if store.RoleMayWrite(role) {
			next.ServeHTTP(w, r)
			return
		}
		// THE REFUSAL NAMES THE PROJECT, THE ROLE AND WHAT IT IS NOT. A person
		// told only "forbidden" cannot tell "I am a reader here" from "this row
		// is not mine" from "the node is broken", and those are three different
		// things to do next.
		writeJSON(w, http.StatusForbidden, errorBody(
			"you are "+store.RoleName(role)+" in "+p.Project+", so you can read it and not write in it - "+
				"an owner of that project can change your role"))
	})
}
