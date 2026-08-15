package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/store"
)

// Federation, the node half.
//
// A node is one flowy instance with one database and one name, and every row it
// writes carries that name and a clock reading. Two nodes hold each other's
// rows by exchanging deltas: a pull reads what the peer has that we have not,
// a push hands the peer what we have that it has not, and both sides run the
// same merge. The mechanism is symmetric - there is no primary - even though
// the deployments it is built for are a laptop and a server talking to each
// other, which is a hub with one spoke.
//
// Two things make it safe to run repeatedly and to interrupt:
//
//   - it is permission-filtered. A peer authenticates as a principal, exactly
//     like an agent, and pulls what that principal may read. The cross-project
//     grant that lets somebody in pb read an artifact in pa is the same grant
//     that lets a node holding pb's work replicate it, and a personal artifact
//     replicates to nobody at all. Replication is not a back door around the
//     permission model; it is a client of it.
//   - the cursors live in the peers table, so a sync is resumable and idempotent.
//     Being offline is being behind, and being behind is a cursor that has not
//     moved yet.
//
// The merge itself - append-only events, last-writer-wins by hlc for everything
// else - is in internal/store/sync.go, because both the push endpoint and the
// driver go through it.

// maxSyncBody caps a pushed delta. It is larger than maxBody because a page of
// artifacts is many bodies, and each of those may be a transcript.
const maxSyncBody = 64 << 20

// syncTimeout bounds one request to a peer.
const syncTimeout = 60 * time.Second

// syncPages caps how many pages one run will move in each direction, so a sync
// against a node with a very old cursor ends rather than running until it is
// killed. What it does not finish, the next run resumes.
const syncPages = 100

// pullResponse is what GET /api/sync/pull answers with: a delta plus the cursor
// the caller may store once it has applied it.
type pullResponse struct {
	*store.SyncSet
	Node string `json:"node"`
}

// handleSyncPull hands a peer everything the requesting principal may read that
// is newer than ?since. The principal is resolved from the bearer token by the
// same middleware every other endpoint is behind, and the rows are narrowed by
// the same permission filter every other read uses.
//
// GET /api/sync/pull?since=<hlc>&limit=<n>
func (s *server) handleSyncPull(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()

	since, err := strconv.ParseInt(orZero(q.Get("since")), 10, 64)
	if err != nil || since < 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("since must be a packed hlc integer"))
		return
	}

	set, err := s.db.SyncPull(r.Context(), p, store.SyncQuery{
		Since: since,
		Limit: intParam(q.Get("limit")),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, pullResponse{SyncSet: set, Node: s.node})
}

// pushResponse reports what a push carried and what it changed. They differ
// whenever a row lost its merge - an older artifact, an event that was already
// here - which is what makes pushing the same delta twice a no-op you can see.
//
// Refused is the third number: rows this node would not take from this
// principal at all. A peer that is quietly having half its delta dropped should
// be able to see that it is, and why.
type pushResponse struct {
	Node     string         `json:"node"`
	Received map[string]int `json:"received"`
	Applied  map[string]int `json:"applied"`
	Refused  map[string]int `json:"refused"`
	Reasons  []string       `json:"reasons,omitempty"`
	HWM      int64          `json:"hwm"`
}

// isPeer reports whether p may push a delta at this node.
//
// Pushing is not reading. A pull hands a token holder what that token may
// already read one row at a time; a push writes rows of the caller's choosing
// into this database and merges them last-writer-wins. So it is not open to
// every token: it is the operator's, and whoever the operator named in
// FLOWY_PEERS. A share subject - somebody who holds one grant on one artifact -
// is exactly the principal this keeps out.
func (s *server) isPeer(p *store.Principal) bool {
	if p == nil {
		return false
	}
	return p.Operator || (p.UserID != "" && s.peers[p.UserID])
}

// handleSyncPush merges a peer's delta into this node. It is an upsert by id
// and it is idempotent: the same body applied twice changes nothing the second
// time.
//
// Two gates, and both of them are here because a push used to have neither: the
// caller has to be a peer this node's operator named, and every row it carries
// has to be one that caller could have written over the API - see SyncApplyAs.
//
// POST /api/sync/push
func (s *server) handleSyncPush(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if !s.isPeer(p) {
		writeJSON(w, http.StatusForbidden,
			errorBody("replication is pushed by a peer this node names, not by any token; "+
				"pull instead, or ask the operator to add you to FLOWY_PEERS"))
		return
	}

	var in store.SyncSet
	if err := decodeJSONLimit(r, &in, maxSyncBody); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}

	res, err := s.db.SyncApplyAs(r.Context(), p, &in)
	if errors.Is(err, store.ErrBadReading) {
		// Nothing was merged and the clock was not touched. A reading like this
		// is not a row to skip: it is a delta to refuse.
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, pushResponse{
		Node:     s.node,
		Received: in.Counts(),
		Applied:  res.Applied,
		Refused:  res.Refused,
		Reasons:  res.Reasons,
		HWM:      in.HWM,
	})
}

// handleListPeers reports this node's replication bookmarks: who it syncs with
// and how far each cursor has got. It is the operator's view of federation, so
// it answers only for the operator - a peer's cursor is not something one
// principal's token should reveal to another.
//
// GET /api/peers
func (s *server) handleListPeers(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if p == nil || !p.Operator {
		writeJSON(w, http.StatusForbidden, errorBody("peers are the operator's view of this node"))
		return
	}
	peers, err := s.db.ListPeers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"node": s.node, "peers": peers})
}

// ---------------------------------------------------------------- the driver

// syncReport is what `flowy sync` prints: one JSON object per run, so a cron
// entry or a shell can read it back.
type syncReport struct {
	Peer         string         `json:"peer"`
	Node         string         `json:"node"`
	PeerNode     string         `json:"peer_node,omitempty"`
	As           string         `json:"as"`
	Pulled       map[string]int `json:"pulled"`
	Applied      map[string]int `json:"applied"`
	Refused      map[string]int `json:"refused"`
	Pushed       map[string]int `json:"pushed"`
	PeerApplied  map[string]int `json:"peer_applied"`
	PeerRefused  map[string]int `json:"peer_refused"`
	Reasons      []string       `json:"reasons,omitempty"`
	PullCursor   int64          `json:"pull_cursor"`
	PushedCursor int64          `json:"pushed_cursor"`
}

// syncCmd is `flowy sync --peer <url> --token <t>`: pull the peer's delta since
// our pull cursor and apply it, then hand the peer ours since its pushed
// cursor. Both cursors live in the peers table, so running it twice in a row
// moves nothing the second time and running it after a week moves a week.
//
// The token is the identity replication runs as, on both sides. It authenticates
// us to the peer, and it also has to resolve here - because what we push is what
// that principal may read on this node, which is the same rule the peer applies
// to what it hands us. Neither node ever ships a row the peer's principal could
// not have read one at a time over the API.
func syncCmd(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	peer := fs.String("peer", "", "base URL of the peer node, e.g. https://box.local:8787")
	token := fs.String("token", "", "bearer token to authenticate to the peer as")
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres-wire DSN (default $DATABASE_URL)")
	node := fs.String("node", envOr("FLOWY_NODE", defaultNode()), "name of this node")
	limit := fs.Int("limit", 0, "rows per table per page (default 500)")
	pull := fs.Bool("pull", true, "pull the peer's rows")
	push := fs.Bool("push", true, "push ours")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *peer == "" {
		return errors.New("no peer: pass --peer <url>")
	}
	if *token == "" {
		if *token = os.Getenv("FLOWY_TOKEN"); *token == "" {
			return errors.New("no token: pass --token <t> or set FLOWY_TOKEN")
		}
	}
	base, err := peerBase(*peer)
	if err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("no DSN: set DATABASE_URL or pass -dsn")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	db, err := store.Open(ctx, *dsn, *node)
	if err != nil {
		return err
	}
	defer db.Close()

	// A node that applied a peer's rows and was restarted must not mint a
	// reading that loses to a row it already holds.
	if _, err := db.SeedClock(ctx); err != nil {
		return err
	}

	// What we may push is what this token's principal may read here.
	principal, err := db.PrincipalForToken(ctx, *token)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("token does not resolve on this node: replication runs as a principal, "+
			"and %s has to name one here as well as on the peer", short(*token))
	}
	if err != nil {
		return err
	}

	if err := db.RegisterPeer(ctx, base); err != nil {
		return err
	}
	bookmark, err := db.GetPeer(ctx, base)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: syncTimeout}
	report := syncReport{
		Peer: base, Node: *node, As: principal.UserID,
		Pulled: zeroCounts(), Applied: zeroCounts(), Refused: zeroCounts(),
		Pushed: zeroCounts(), PeerApplied: zeroCounts(), PeerRefused: zeroCounts(),
		PullCursor: bookmark.PullCursor, PushedCursor: bookmark.PushedCursor,
	}

	if *pull {
		if err := pullFromPeer(ctx, db, client, base, *token, *limit, principal, &report); err != nil {
			return err
		}
	}
	if *push {
		if err := pushToPeer(ctx, db, client, base, *token, *limit, principal, &report); err != nil {
			return err
		}
	}
	if err := db.TouchPeer(ctx, base); err != nil {
		return err
	}

	out, err := json.Marshal(report)
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

// pullFromPeer reads pages from the peer and applies each one before asking for
// the next, advancing the cursor as it goes: interrupt it and what it has
// already applied stays applied.
//
// Each page is merged as the principal the replication token resolves to here,
// which is the same principal the peer filtered the page for. It used to be
// merged as nobody at all, and that made being willing to read from a peer the
// same as being willing to let it write anything: one forged grant in a page
// this node asked for, and the project it names is readable by whoever the peer
// says - which the next pull then carries out of the door. A peer that answers
// with what it should is unaffected; anything else is refused and counted.
func pullFromPeer(ctx context.Context, db *store.DB, client *http.Client,
	base, token string, limit int, principal *store.Principal, report *syncReport,
) error {
	cursor := report.PullCursor
	for page := 0; page < syncPages; page++ {
		url := fmt.Sprintf("%s/api/sync/pull?since=%d", base, cursor)
		if limit > 0 {
			url += fmt.Sprintf("&limit=%d", limit)
		}

		var got pullResponse
		if err := peerRequest(ctx, client, http.MethodGet, url, token, nil, &got); err != nil {
			return err
		}
		if got.SyncSet == nil || got.SyncSet.Len() == 0 {
			break
		}
		report.PeerNode = got.Node

		res, err := db.SyncApplyFrom(ctx, principal, got.SyncSet)
		if err != nil {
			return err
		}
		addCounts(report.Pulled, got.SyncSet.Counts())
		addCounts(report.Applied, res.Applied)
		addCounts(report.Refused, res.Refused)
		report.Reasons = append(report.Reasons, res.Reasons...)

		// A page that carries rows but no higher cursor cannot be paged past;
		// stop rather than ask for it again forever.
		if got.SyncSet.HWM <= cursor {
			break
		}
		cursor = got.SyncSet.HWM
		if err := db.AdvancePullCursor(ctx, base, cursor); err != nil {
			return err
		}
		report.PullCursor = cursor
	}
	return nil
}

// pushToPeer hands the peer our rows since its pushed cursor, a page at a time,
// filtered to what the replication principal may read here.
func pushToPeer(ctx context.Context, db *store.DB, client *http.Client,
	base, token string, limit int, principal *store.Principal, report *syncReport,
) error {
	cursor := report.PushedCursor
	for page := 0; page < syncPages; page++ {
		set, err := db.SyncPull(ctx, principal, store.SyncQuery{Since: cursor, Limit: limit})
		if err != nil {
			return err
		}
		if set.Len() == 0 {
			break
		}

		body, err := json.Marshal(set)
		if err != nil {
			return err
		}
		var got pushResponse
		if err := peerRequest(ctx, client, http.MethodPost, base+"/api/sync/push", token, body, &got); err != nil {
			return err
		}
		report.PeerNode = got.Node
		addCounts(report.Pushed, set.Counts())
		addCounts(report.PeerApplied, got.Applied)
		addCounts(report.PeerRefused, got.Refused)
		report.Reasons = append(report.Reasons, got.Reasons...)

		// The peer would not take part of what it was handed. The cursor is a
		// promise that everything below it has been offered and dealt with, and
		// a refused row was not: moving past it is how those rows are never
		// sent again and the two nodes quietly differ. So the bookmark stays
		// where it is and this run stops pushing. The next one hands the same
		// page over - which clears a refusal that was only about the order
		// things arrived in, and leaves a real one in the report where somebody
		// can read it, rather than dropping the row on the floor.
		//
		// A row the peer already holds is not a refusal: it loses its merge and
		// is ignored, so a page coming back at the node that wrote it does not
		// wedge the cursor here.
		if refused := count(got.Refused); refused > 0 {
			break
		}

		if set.HWM <= cursor {
			break
		}
		cursor = set.HWM
		if err := db.AdvancePushedCursor(ctx, base, cursor); err != nil {
			return err
		}
		report.PushedCursor = cursor
	}
	return nil
}

// peerRequest makes one authenticated request to a peer and decodes its answer.
func peerRequest(ctx context.Context, client *http.Client, method, url, token string,
	body []byte, into any,
) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close()

	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxSyncBody))
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, url, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: peer answered %d: %s", method, url, resp.StatusCode,
			strings.TrimSpace(string(answer)))
	}
	if err := json.Unmarshal(answer, into); err != nil {
		return fmt.Errorf("%s %s: peer answered with %q, which is not the expected JSON: %w",
			method, url, short(string(answer)), err)
	}
	return nil
}

// peerBase normalises a peer URL to a scheme and host with no trailing slash,
// so the same peer is one row in the peers table however it was typed.
func peerBase(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("peer %q is not a URL: %w", raw, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("peer %q names no host", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("peer %q: only http and https are peers", raw)
	}
	return strings.TrimSuffix(u.Scheme+"://"+u.Host+u.Path, "/"), nil
}

func zeroCounts() map[string]int {
	return map[string]int{"artifacts": 0, "events": 0, "tasks": 0, "grants": 0}
}

// count adds a report's tables up, which is how a caller asks "did anything at
// all happen" of a per-table count.
func count(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func addCounts(into, from map[string]int) {
	for k, v := range from {
		into[k] += v
	}
}

// short truncates a string for an error message, so a failure quotes a peer's
// answer rather than reprinting it.
func short(s string) string {
	const max = 120
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
