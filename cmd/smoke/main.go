// Command smoke runs the live checks the gate asserts against a running node
// and a real database. Each subcommand is one check: it prints a one-line
// summary and exits zero, or explains itself and exits non-zero.
//
//	smoke healthz <url>   poll the node until /healthz reports ok, or give up
//	smoke ulid            10000 ids are unique and already sorted
//	smoke hlc             8 goroutines x 5000 readings, monotonic, no duplicates
//	smoke schema          the spine tables are all present
//	smoke roundtrip       user, agent, artifact and event insert and read back
//	smoke personal        a personal artifact (project NULL) inserts and reads back
//	smoke seed            two principals in two projects, printed as shell vars
//	smoke otlp-collector  stand in for an OTLP collector: write what is POSTed
//	                      to /v1/traces into a file, one payload per line
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/hlc"
	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: smoke <healthz|ulid|hlc|schema|roundtrip|personal|seed|otlp-collector> [args]")
	}

	var err error
	switch os.Args[1] {
	case "healthz":
		if len(os.Args) < 3 {
			fail("usage: smoke healthz <url>")
		}
		err = checkHealthz(os.Args[2])
	case "ulid":
		err = checkULID()
	case "hlc":
		err = checkHLC()
	case "schema":
		err = withDB(checkSchema)
	case "roundtrip":
		err = withDB(checkRoundTrip)
	case "personal":
		err = withDB(checkPersonal)
	case "seed":
		err = withDB(seedPrincipals)
	case "otlp-collector":
		if len(os.Args) < 4 {
			fail("usage: smoke otlp-collector <addr> <file>")
		}
		err = otlpCollector(os.Args[2], os.Args[3])
	default:
		fail("smoke: unknown check %q", os.Args[1])
	}

	if err != nil {
		fail("%v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func withDB(fn func(context.Context, *store.DB) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	db, err := store.Open(ctx, os.Getenv("DATABASE_URL"), "smoke")
	if err != nil {
		return err
	}
	defer db.Close()
	return fn(ctx, db)
}

// checkHealthz polls the node until /healthz answers with ok:true.
func checkHealthz(url string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(30 * time.Second)

	var last error
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		body, status, err := get(client, url)
		switch {
		case err != nil:
			last = err
		case status != http.StatusOK:
			last = fmt.Errorf("status %d: %s", status, body)
		default:
			var parsed struct {
				OK      bool   `json:"ok"`
				Node    string `json:"node"`
				DB      string `json:"db"`
				Version string `json:"version"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return fmt.Errorf("healthz returned %q, which is not JSON: %w", body, err)
			}
			if !parsed.OK {
				return fmt.Errorf("healthz reports not ok: %s", body)
			}
			if parsed.DB != "up" {
				return fmt.Errorf("healthz reports db %q: %s", parsed.DB, body)
			}
			fmt.Printf("healthz ok after %d attempt(s): node=%s version=%s db=%s\n",
				attempt, parsed.Node, parsed.Version, parsed.DB)
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("healthz never came up at %s: %v", url, last)
}

// otlpCollector is the smallest thing that can be called an OTLP collector: it
// answers POST /v1/traces with 200 and appends each body to a file, one payload
// per line.
//
// The gate needs it because "the exporter works" cannot be asserted from
// inside the node - the node's own copy of a span is written by the recorder,
// which is a different path. Something has to receive an HTTP request that
// really was made, and read the trace id out of the body it really was sent.
// It exits when it is killed; the gate kills it.
func otlpCollector(addr, path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/traces", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if _, err := file.Write(append(bytes.TrimSpace(body), '\n')); err != nil {
			http.Error(w, "write", http.StatusInternalServerError)
			return
		}
		if err := file.Sync(); err != nil {
			http.Error(w, "sync", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{}`)); err != nil {
			return
		}
	})

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	fmt.Printf("otlp collector on %s, writing to %s\n", ln.Addr(), path)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(ln)
}

func get(client *http.Client, url string) ([]byte, int, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// checkULID mints 10000 ids and asserts they are unique and already in sorted
// order: generation order and lexicographic order must agree.
func checkULID() error {
	const n = 10_000

	ids := make([]string, n)
	seen := make(map[string]int, n)
	for i := range ids {
		ids[i] = ulid.NewString()
		if prev, dup := seen[ids[i]]; dup {
			return fmt.Errorf("id %d duplicates id %d: %s", i, prev, ids[i])
		}
		seen[ids[i]] = i
		if len(ids[i]) != ulid.EncodedSize {
			return fmt.Errorf("id %d is %d chars, want %d: %q", i, len(ids[i]), ulid.EncodedSize, ids[i])
		}
	}

	for i := 1; i < n; i++ {
		if ids[i] <= ids[i-1] {
			return fmt.Errorf("id %d (%s) does not sort after id %d (%s)", i, ids[i], i-1, ids[i-1])
		}
	}

	generated := append([]string(nil), ids...)
	sort.Strings(ids)
	for i := range ids {
		if ids[i] != generated[i] {
			return fmt.Errorf("sorted order differs from generation order at %d: %s vs %s",
				i, ids[i], generated[i])
		}
	}

	// The id must still decode to the time it was minted.
	first, err := ulid.Parse(generated[0])
	if err != nil {
		return fmt.Errorf("parse %s: %w", generated[0], err)
	}
	if age := time.Since(first.Time()); age < 0 || age > time.Minute {
		return fmt.Errorf("id %s decodes to %s, which is %s away from now", generated[0], first.Time(), age)
	}

	fmt.Printf("%d ulids: unique, strictly increasing, sorted == generation order (first %s, last %s)\n",
		n, generated[0], generated[n-1])
	return nil
}

// checkHLC has 8 goroutines mint 5000 readings each from one clock and asserts
// that nothing repeats and nothing goes backwards.
func checkHLC() error {
	const goroutines, per = 8, 5_000

	c := hlc.New("smoke")
	var wg sync.WaitGroup
	batches := make([][]int64, goroutines)
	errs := make([]error, goroutines)

	for gi := 0; gi < goroutines; gi++ {
		wg.Add(1)
		go func(gi int) {
			defer wg.Done()
			batch := make([]int64, per)
			for i := range batch {
				at, err := c.Pack()
				if err != nil {
					errs[gi] = err
					return
				}
				batch[i] = at
			}
			batches[gi] = batch
		}(gi)
	}
	wg.Wait()
	for gi, err := range errs {
		if err != nil {
			return fmt.Errorf("goroutine %d could not take a reading: %w", gi, err)
		}
	}

	all := make([]int64, 0, goroutines*per)
	for gi, batch := range batches {
		for i := 1; i < len(batch); i++ {
			if batch[i] <= batch[i-1] {
				return fmt.Errorf("goroutine %d went backwards at %d: %d then %d",
					gi, i, batch[i-1], batch[i])
			}
		}
		all = append(all, batch...)
	}

	sort.Slice(all, func(a, b int) bool { return all[a] < all[b] })
	for i := 1; i < len(all); i++ {
		if all[i] == all[i-1] {
			wall, logical := hlc.Unpack(all[i])
			return fmt.Errorf("duplicate packed value %d (wall %d, logical %d)", all[i], wall, logical)
		}
	}

	// A merge from a peer that is far ahead must still land above everything
	// this clock has handed out.
	ahead := hlc.Pack(all[len(all)-1]>>hlc.LogicalBits+60_000, 9)
	merged := c.UpdatePacked(ahead, "peer")
	if merged <= ahead || merged <= all[len(all)-1] {
		return fmt.Errorf("merge of %d gave %d, which is not ahead of both clocks", ahead, merged)
	}

	fmt.Printf("%d hlc readings from %d goroutines: strictly increasing, %d distinct, merge lands ahead\n",
		len(all), goroutines, len(all))
	return nil
}

// checkSchema asserts that every spine table exists.
func checkSchema(ctx context.Context, db *store.DB) error {
	want := []string{"users", "agents", "tokens", "grants", "artifacts", "events", "tasks", "peers"}
	for _, table := range want {
		var n int
		err := db.SQL().QueryRowContext(ctx,
			`SELECT count(*) FROM information_schema.tables
			  WHERE table_schema = current_schema() AND table_name = $1`, table).Scan(&n)
		if err != nil {
			return fmt.Errorf("look up table %s: %w", table, err)
		}
		if n != 1 {
			return fmt.Errorf("table %s is missing", table)
		}
	}
	counts, err := db.Counts(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("schema: %d spine tables present, rows now %v\n", len(want), counts)
	return nil
}

// checkRoundTrip writes one of each spine row and reads them all back, with
// particular attention to parents surviving as a text[].
func checkRoundTrip(ctx context.Context, db *store.DB) error {
	user := &store.User{
		Handle:       "ik-" + ulid.NewString(),
		Display:      "Iliia K",
		AutoDelegate: true,
	}
	if err := db.InsertUser(ctx, user); err != nil {
		return err
	}

	agent := &store.Agent{UserID: user.ID, Kind: "claude", Project: "flowy"}
	if err := db.InsertAgent(ctx, agent); err != nil {
		return err
	}

	project := "flowy"
	artifact := &store.Artifact{
		Type:       "bug",
		Project:    &project,
		OwnerUser:  user.ID,
		Title:      "parents do not round-trip",
		Body:       "reported by the gate",
		Discovery:  "gate",
		Status:     "open",
		Severity:   "high",
		Tags:       []string{"phase0", "schema"},
		UserTags:   []string{"mine"},
		Related:    []string{},
		Visibility: "project",
		FilePath:   "internal/store/store.go",
		Fields:     json.RawMessage(`{"repro":"run the gate"}`),
	}
	if err := db.InsertArtifact(ctx, artifact); err != nil {
		return err
	}

	// The first event opens a thread and points at the artifact.
	opened := &store.Event{
		Type:     "artifact.created",
		Project:  &project,
		Room:     "flowy/bugs",
		Parents:  []string{artifact.ID},
		Actor:    user.ID,
		Artifact: artifact.ID,
		Body:     "opened",
		Meta:     json.RawMessage(`{"via":"smoke"}`),
	}
	if err := db.AppendEvent(ctx, opened); err != nil {
		return err
	}

	// The second continues that thread and merges two parents, which is the
	// shape the DAG has to carry.
	merged := &store.Event{
		Type:     "artifact.commented",
		Project:  &project,
		Room:     "flowy/bugs",
		Thread:   opened.Thread,
		Parents:  []string{opened.ID, artifact.ID},
		Actor:    agent.ID,
		Artifact: artifact.ID,
		Body:     "picked up",
	}
	if err := db.AppendEvent(ctx, merged); err != nil {
		return err
	}

	gotUser, err := db.GetUser(ctx, user.ID)
	if err != nil {
		return err
	}
	if gotUser.Handle != user.Handle || !gotUser.AutoDelegate || gotUser.HLC != user.HLC {
		return fmt.Errorf("user read back as %+v, want %+v", gotUser, user)
	}
	if gotUser.Node != "smoke" {
		return fmt.Errorf("user node = %q, want smoke", gotUser.Node)
	}

	gotAgent, err := db.GetAgent(ctx, agent.ID)
	if err != nil {
		return err
	}
	if gotAgent.UserID != user.ID || gotAgent.Kind != "claude" || gotAgent.Project != "flowy" {
		return fmt.Errorf("agent read back as %+v, want %+v", gotAgent, agent)
	}

	gotArtifact, err := db.GetArtifact(ctx, artifact.ID)
	if err != nil {
		return err
	}
	if gotArtifact.Project == nil || *gotArtifact.Project != "flowy" {
		return fmt.Errorf("artifact project read back as %v, want flowy", gotArtifact.Project)
	}
	if gotArtifact.Type != "bug" || gotArtifact.Severity != "high" || gotArtifact.Status != "open" {
		return fmt.Errorf("artifact read back as %+v", gotArtifact)
	}
	if err := sameStrings("artifact.tags", gotArtifact.Tags, artifact.Tags); err != nil {
		return err
	}
	if err := sameStrings("artifact.user_tags", gotArtifact.UserTags, artifact.UserTags); err != nil {
		return err
	}
	if string(gotArtifact.Fields) == "" {
		return fmt.Errorf("artifact fields came back empty")
	}
	if gotArtifact.Created.IsZero() || gotArtifact.Updated.IsZero() {
		return fmt.Errorf("artifact timestamps came back zero: %+v", gotArtifact)
	}

	gotOpened, err := db.GetEvent(ctx, opened.ID)
	if err != nil {
		return err
	}
	if err := sameStrings("event.parents", gotOpened.Parents, []string{artifact.ID}); err != nil {
		return err
	}
	if gotOpened.Thread != opened.ID {
		return fmt.Errorf("thread = %q, want the opening event id %q", gotOpened.Thread, opened.ID)
	}
	if gotOpened.SeqHLC != opened.SeqHLC || gotOpened.SeqHLC == 0 {
		return fmt.Errorf("seq_hlc read back as %d, want %d", gotOpened.SeqHLC, opened.SeqHLC)
	}

	gotMerged, err := db.GetEvent(ctx, merged.ID)
	if err != nil {
		return err
	}
	if err := sameStrings("merge event.parents", gotMerged.Parents, []string{opened.ID, artifact.ID}); err != nil {
		return err
	}
	if gotMerged.SeqHLC <= gotOpened.SeqHLC {
		return fmt.Errorf("second event seq_hlc %d did not advance past %d", gotMerged.SeqHLC, gotOpened.SeqHLC)
	}

	thread, err := db.ThreadEvents(ctx, opened.Thread)
	if err != nil {
		return err
	}
	if len(thread) != 2 || thread[0].ID != opened.ID || thread[1].ID != merged.ID {
		return fmt.Errorf("thread read back with %d events in the wrong order", len(thread))
	}

	fmt.Printf("round trip: user %s, agent %s, artifact %s, thread %s with %d events, parents intact\n",
		user.ID, agent.ID, artifact.ID, opened.Thread, len(thread))
	return nil
}

// checkPersonal writes an artifact with no project - the personal case, where
// project is NULL and visibility is personal - and reads it back.
func checkPersonal(ctx context.Context, db *store.DB) error {
	owner := &store.User{Handle: "solo-" + ulid.NewString(), Display: "Solo"}
	if err := db.InsertUser(ctx, owner); err != nil {
		return err
	}

	note := &store.Artifact{
		Type:       "note",
		Project:    nil,
		OwnerUser:  owner.ID,
		Title:      "personal note",
		Body:       "not for the project",
		Visibility: "personal",
		Tags:       []string{"private"},
	}
	if err := db.InsertArtifact(ctx, note); err != nil {
		return err
	}

	got, err := db.GetArtifact(ctx, note.ID)
	if err != nil {
		return err
	}
	if got.Project != nil {
		return fmt.Errorf("personal artifact came back with project %q, want NULL", *got.Project)
	}
	if got.Visibility != "personal" {
		return fmt.Errorf("visibility = %q, want personal", got.Visibility)
	}
	if got.OwnerUser != owner.ID {
		return fmt.Errorf("owner = %q, want %q", got.OwnerUser, owner.ID)
	}
	if err := sameStrings("personal artifact.tags", got.Tags, []string{"private"}); err != nil {
		return err
	}

	// A NULL project has to be selectable as such, which is how the personal
	// view will be built later.
	var n int
	err = db.SQL().QueryRowContext(ctx,
		`SELECT count(*) FROM artifacts
		  WHERE project IS NULL AND visibility = 'personal' AND id = $1`, note.ID).Scan(&n)
	if err != nil {
		return fmt.Errorf("select personal artifacts: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("personal artifact %s is not selectable by project IS NULL", note.ID)
	}

	fmt.Printf("personal artifact %s: project NULL, visibility personal, owner %s\n", note.ID, owner.ID)
	return nil
}

// seedPrincipals creates the two people, their agents and their bearer tokens
// that the permission checks are run as: user A with an agent in project pa,
// user B with an agent in project pb, and an operator who is the local node's
// own principal. It prints the ids as shell assignments, so the gate can eval
// them and then drive the HTTP API as either of them.
//
// No grants are seeded. Every cross-project read has to be earned by a grant
// the gate issues itself, over the API, mid-run.
func seedPrincipals(ctx context.Context, db *store.DB) error {
	type principal struct {
		prefix  string // shell variable prefix
		handle  string
		project string
		kind    string
	}
	people := []principal{
		{prefix: "A", handle: "alice", project: "pa", kind: "claude"},
		{prefix: "B", handle: "bob", project: "pb", kind: "opencode"},
		{prefix: "OP", handle: "operator", project: "pa", kind: "claude"},
	}

	var out []string
	// agentTokens maps an agent's token to the user and project it must resolve
	// to, so the seed can prove the inheritance rather than assume it.
	agentTokens := map[string][2]string{}
	// userIDs remembers the minted ids by prefix, for the extra tokens below.
	userIDs := map[string]string{}
	for _, person := range people {
		// Handles are unique in the schema, so they carry a suffix: seeding is
		// something a gate run does to whatever database it was pointed at, and
		// it must not depend on that database being empty.
		user := &store.User{
			Handle:       person.handle + "-" + ulid.NewString(),
			Display:      person.handle,
			AutoDelegate: true,
		}
		if err := db.InsertUser(ctx, user); err != nil {
			return err
		}
		agent := &store.Agent{UserID: user.ID, Kind: person.kind, Project: person.project}
		if err := db.InsertAgent(ctx, agent); err != nil {
			return err
		}

		// The user's own token, and a token for the agent acting on their
		// behalf. The agent token names no user of its own: it inherits one,
		// which is what lets an agent read its user's personal artifacts.
		userToken := "t" + person.prefix + "-" + ulid.NewString()
		agentToken := "t" + person.prefix + "agent-" + ulid.NewString()
		for _, tok := range []*store.Principal{
			{Token: userToken, UserID: user.ID, Project: person.project},
			{Token: agentToken, AgentID: agent.ID},
		} {
			if err := db.InsertToken(ctx, tok); err != nil {
				return err
			}
		}

		agentTokens[agentToken] = [2]string{user.ID, person.project}
		userIDs[person.prefix] = user.ID
		out = append(out,
			fmt.Sprintf("USER_%s=%s", person.prefix, user.ID),
			fmt.Sprintf("AGENT_%s=%s", person.prefix, agent.ID),
			fmt.Sprintf("PROJECT_%s=%s", person.prefix, person.project),
			fmt.Sprintf("TOKEN_%s=%s", person.prefix, userToken),
			fmt.Sprintf("TOKEN_%s_AGENT=%s", person.prefix, agentToken))
	}

	// A second token for user A, working in a third project. A principal is a
	// (user, agent, project) triple, so the same person in another project is
	// another principal - which is exactly what the per-artifact share needs to
	// be tested against: a project nobody has a project-wide grant into.
	pcToken := "tApc-" + ulid.NewString()
	if err := db.InsertToken(ctx, &store.Principal{
		Token: pcToken, UserID: userIDs["A"], Project: "pc",
	}); err != nil {
		return err
	}
	out = append(out, "PROJECT_C=pc", "TOKEN_A_PC="+pcToken)

	// Sanity: an agent token has to resolve to its user and its project, or
	// every check that runs as an agent is testing the wrong principal.
	for token, want := range agentTokens {
		p, err := db.PrincipalForToken(ctx, token)
		if err != nil {
			return fmt.Errorf("resolve agent token: %w", err)
		}
		if p.UserID != want[0] || p.Project != want[1] {
			return fmt.Errorf("agent token resolved to user %q project %q, want %q and %q",
				p.UserID, p.Project, want[0], want[1])
		}
	}

	// An unknown token has to resolve to nothing at all.
	if _, err := db.PrincipalForToken(ctx, "no-such-token"); !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("an unknown token resolved with error %v, want ErrNotFound", err)
	}

	fmt.Println(strings.Join(out, "\n"))
	return nil
}

func sameStrings(what string, got, want []string) error {
	if len(got) != len(want) {
		return fmt.Errorf("%s read back as %v, want %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Errorf("%s read back as %v, want %v", what, got, want)
		}
	}
	return nil
}
