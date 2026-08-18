// Package store is the node's Postgres-wire persistence layer.
//
// It speaks stock Postgres over the wire and nothing more: the deployment
// target is a SereneDB node, so the SQL here avoids anything that is a property
// of Postgres the storage engine rather than Postgres the protocol.
//
// Rows are stamped on the way in - a ULID for the primary key when the caller
// left it empty, and a packed hybrid logical clock plus the node name for the
// hlc/seq_hlc columns - so a later phase can merge two nodes' writes.
package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/hlc"
	"github.com/deadtrickster/flowy/internal/otel"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// DB is a handle on the node's database plus the clocks that stamp its rows.
type DB struct {
	sql   *sql.DB
	clock *hlc.Clock
	node  string

	// keyMu guards priv, which is this node's ed25519 private key: read from
	// node_identity the first time something signs, minted there if the table
	// has no row for this node yet, and then held for the life of the process.
	// It never leaves this struct - nothing serialises it, and no query that
	// replication can reach selects the column.
	keyMu sync.Mutex
	priv  ed25519.PrivateKey

	// authorMu guards authors, which is the principal signing keys this node
	// holds: the private half of a keypair belonging to a person or an agent
	// rather than to this machine, read from principal_identity the first time
	// that principal writes here and kept for the life of the process. Only the
	// keys that were FOUND are kept - see principalSigner - because a key
	// provisioned against a running node has to take effect on the next write
	// and not on the next restart. It never leaves this struct, for the same
	// reason priv does not.
	authorMu sync.Mutex
	authors  map[string]ed25519.PrivateKey

	// requirePinned refuses a replicated row from any node whose key this
	// node's operator has not pinned by hand, which is FLOWY_REQUIRE_PINNED_
	// PEERS. It is a deployment's choice, so it is configuration and not a row.
	requirePinned bool
}

// Open dials dsn and verifies the connection. node names this node in every row
// it writes.
func Open(ctx context.Context, dsn, node string) (*DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("store: empty DSN (set DATABASE_URL)")
	}
	sqlDB, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	sqlDB.SetMaxOpenConns(16)
	sqlDB.SetMaxIdleConns(4)
	sqlDB.SetConnMaxLifetime(time.Hour)

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &DB{
		sql: sqlDB, clock: hlc.New(node), node: node,
		requirePinned: requirePinnedFromEnv(),
	}, nil
}

// Close releases the pool.
func (d *DB) Close() error { return d.sql.Close() }

// SQL exposes the underlying pool for callers that need raw access.
func (d *DB) SQL() *sql.DB { return d.sql }

// Node returns the node name stamped onto rows.
func (d *DB) Node() string { return d.node }

// Clock returns the node's hybrid logical clock.
func (d *DB) Clock() *hlc.Clock { return d.clock }

// Ping reports whether the database is reachable.
func (d *DB) Ping(ctx context.Context) error { return d.sql.PingContext(ctx) }

// execer is what both the pool and a transaction satisfy, so one statement
// serves a write made on its own and the same write made as part of a sequence
// that has to land whole.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// inTx runs fn in one transaction and commits it, rolling back on any error.
//
// It is what the multi-row operations are written against. An assignment is
// three rows - the share, the task and the message that opens its thread - and
// a status move is two, and until they were one transaction a node that failed
// halfway left the half behind: a share nothing points at, a task about an
// artifact the assignee cannot read, a status the trail has no entry for.
// Nothing backfills that. Worse, the half replicates on its own, because the
// rows carry their own readings and a peer merges whatever is there.
func (d *DB) inTx(ctx context.Context, what string, fn func(tx *sql.Tx) error) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	defer tx.Rollback() //nolint:errcheck // rolled back only when Commit did not happen
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	return nil
}

// stamp fills in an id, an hlc reading and the node name where the caller left
// them empty.
//
// It can fail, because taking a reading can: a clock with nothing left above
// the last value it handed out has no reading to give, and a row stamped with
// the previous one is a row that shares a reading with another - which the
// merge resolves by dropping one of them. So the write does not happen.
func (d *DB) stamp(id *string, clockVal *int64, node *string) error {
	if *id == "" {
		*id = ulid.NewString()
	}
	if *clockVal == 0 {
		at, err := d.clock.Pack()
		if err != nil {
			return fmt.Errorf("store: %w", err)
		}
		*clockVal = at
	}
	if *node == "" {
		*node = d.node
	}
	return nil
}

// User is a person on this node.
type User struct {
	ID           string `json:"id"`
	Handle       string `json:"handle"`
	Display      string `json:"display"`
	AutoDelegate bool   `json:"auto_delegate"`
	HLC          int64  `json:"hlc"`
	Node         string `json:"node"`
}

// InsertUser writes a user, stamping id/hlc/node when unset.
func (d *DB) InsertUser(ctx context.Context, u *User) error {
	if err := d.stamp(&u.ID, &u.HLC, &u.Node); err != nil {
		return err
	}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO users (id, handle, display, auto_delegate, hlc, node)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		u.ID, u.Handle, u.Display, u.AutoDelegate, u.HLC, u.Node)
	if err != nil {
		return fmt.Errorf("store: insert user: %w", err)
	}
	return nil
}

// GetUser reads a user by id.
func (d *DB) GetUser(ctx context.Context, id string) (*User, error) {
	var (
		u                        User
		handle, display, nodeCol sql.NullString
		auto                     sql.NullBool
		clockVal                 sql.NullInt64
	)
	err := d.sql.QueryRowContext(ctx,
		`SELECT id, handle, display, auto_delegate, hlc, node FROM users WHERE id = $1`, id).
		Scan(&u.ID, &handle, &display, &auto, &clockVal, &nodeCol)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get user %s: %w", id, err)
	}
	u.Handle, u.Display, u.Node = handle.String, display.String, nodeCol.String
	u.AutoDelegate, u.HLC = auto.Bool, clockVal.Int64
	return &u, nil
}

// The agent kinds: what an agent is for, as opposed to what it runs.
//
// Only AgentKindSystem and AgentKindMonitor may post a federation-scope
// announcement. Everything else about an agent is the same whichever of these
// it is - the kind is a capability and not a role hierarchy, and a reviewer is
// no more privileged than a worker.
const (
	AgentKindWorker   = "worker"
	AgentKindReviewer = "reviewer"
	AgentKindSystem   = "system"
	AgentKindMonitor  = "monitor"
)

// agentKinds is the closed set. An agent record carrying anything else is
// refused on the way in rather than coalesced to worker: a kind nothing
// implements is a typo, and a typo that silently becomes the default is a
// system agent somebody thinks they created and did not.
var agentKinds = map[string]bool{
	AgentKindWorker:   true,
	AgentKindReviewer: true,
	AgentKindSystem:   true,
	AgentKindMonitor:  true,
}

// AgentKindOK reports whether kind is one this node implements. Empty is one:
// it is what a row written before the column existed reads back as, and the
// coalesce below makes it a worker.
func AgentKindOK(kind string) bool { return kind == "" || agentKinds[kind] }

// MayAnnounceFederation reports whether an agent of this kind may post an
// announcement that travels the whole fabric.
func MayAnnounceFederation(kind string) bool {
	return kind == AgentKindSystem || kind == AgentKindMonitor
}

// Agent is a coding agent acting for a user. Kind is the runtime -
// claude|glm|opencode - and AgentKind is what the agent is for:
// worker|reviewer|system|monitor, defaulting to worker.
type Agent struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	Kind      string `json:"kind"`
	AgentKind string `json:"agent_kind"`
	Project   string `json:"project"`
	HLC       int64  `json:"hlc"`
	Node      string `json:"node"`
}

// ErrBadAgentKind is an agent record naming a kind this node does not implement.
var ErrBadAgentKind = errors.New("store: agent_kind must be worker, reviewer, system or monitor")

// InsertAgent writes an agent, stamping id/hlc/node when unset. An agent with
// no kind is a worker, which is what makes every existing seed and every row
// written before the column existed still valid.
func (d *DB) InsertAgent(ctx context.Context, a *Agent) error {
	if err := d.stamp(&a.ID, &a.HLC, &a.Node); err != nil {
		return err
	}
	if !AgentKindOK(a.AgentKind) {
		return ErrBadAgentKind
	}
	// An agent's home project is a project that was declared. It is also a
	// foreign key on the column, and this is the same rule said where it can
	// name the project rather than the constraint.
	if err := requireProject(ctx, d.sql, a.Project); err != nil {
		return err
	}
	if a.AgentKind == "" {
		a.AgentKind = AgentKindWorker
	}
	_, err := d.sql.ExecContext(ctx,
		// project is NULL rather than '' when an agent has no home: they mean
		// the same thing to every reader and different things to the foreign
		// key, which would go looking for a project called ''.
		`INSERT INTO agents (id, user_id, kind, agent_kind, project, hlc, node)
		 VALUES ($1, $2, $3, $4, nullif($5, ''), $6, $7)`,
		a.ID, a.UserID, a.Kind, a.AgentKind, a.Project, a.HLC, a.Node)
	if err != nil {
		return fmt.Errorf("store: insert agent: %w", err)
	}
	return nil
}

// GetAgent reads an agent by id.
func (d *DB) GetAgent(ctx context.Context, id string) (*Agent, error) {
	var (
		a                                         Agent
		userID, kind, agentKind, project, nodeCol sql.NullString
		clockVal                                  sql.NullInt64
	)
	err := d.sql.QueryRowContext(ctx,
		`SELECT id, user_id, kind, coalesce(agent_kind, 'worker'), project, hlc, node
		   FROM agents WHERE id = $1`, id).
		Scan(&a.ID, &userID, &kind, &agentKind, &project, &clockVal, &nodeCol)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get agent %s: %w", id, err)
	}
	a.UserID, a.Kind, a.Project, a.Node = userID.String, kind.String, project.String, nodeCol.String
	a.AgentKind, a.HLC = agentKind.String, clockVal.Int64
	return &a, nil
}

// Artifact is anything the node holds that is worth naming: a transcript, a
// memory, a bug, a note. A nil Project means the artifact is personal to
// OwnerUser.
//
// Kind narrows Type without multiplying it: a memory item is an artifact of
// type 'memory' whose kind is note|todo|feature|handoff, so one table, one
// permission filter and one search index serve all of them.
type Artifact struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Kind       string          `json:"kind,omitempty"`
	Project    *string         `json:"project"`
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
	Fields     json.RawMessage `json:"fields,omitempty"`
	// ReplacedBy is the id of the artifact that supersedes this one, and it is
	// derived at read time rather than stored: see SupersedesField and
	// (*DB).replacedBy. There is no column for it, it is not in the signature
	// and it does not replicate - the read paths that carry the permission
	// filter fill it, and every other way of getting an Artifact leaves it
	// empty, because the answer depends on who is asking.
	//
	// Unlike SupersedesField, THIS value is not stored or replicated, so
	// nothing federation-shaped forces it to stay a bare id - it is only a
	// bare id today for wire compatibility with clients already reading it as
	// one. New Go code holding an *Artifact whose ReplacedBy is set should not
	// assume the replacement shares this row's project or type: read the
	// replacement row (replacedBy already has it, permission-checked, before
	// it narrows to just the id) and get its address with RefOf.
	ReplacedBy string `json:"replaced_by,omitempty"`
	// ReplacedByRef is WHERE that replacement lives: project/type/id, the same
	// three segments in the same order as the console's route, filled beside
	// ReplacedBy off the replacement's own row.
	//
	// It exists because an id on its own is not an address. The console had to
	// build a link to the replacement out of something, and the only pieces it
	// had were the ones on the row in its hand, so it reused THAT row's project
	// and type - which is right only while a replacement stays in the same
	// project and keeps the same type, and nothing makes it. Every other reader
	// of a bare id is in the same position, and none of them can tell they
	// guessed wrong.
	//
	// Empty when the replacement is personal to its author: with no project
	// there are no three segments, and half a reference that renders as a link
	// is worse than none. ReplacedBy still names it, which is all the truth
	// there is to tell.
	//
	// ReplacedBy stays a bare id beside this rather than being widened, because
	// clients already read it as one. Derived at read time, same as ReplacedBy,
	// so it is not stored, not signed and does not replicate.
	ReplacedByRef string `json:"replaced_by_ref,omitempty"`
	// Assignee is who is carrying the work, put where Status already is so ONE
	// read answers both. It is derived at read time by AssigneeOf - the fields
	// key first, the body's legacy OWNER line second - and filled in the same
	// three read paths as ReplacedBy, never in scanArtifact, so it does not ride
	// the sync paths or the signature.
	//
	// It is here because its absence cost three agents an afternoon. Status is
	// top level, assignee was one level down in fields, and neither is
	// discoverable from the other: one agent filtered status inside .fields and
	// called 23 finished rows open; another parsed the owner out of the body with
	// the wrong prefix, got twelve honest blanks, and reassigned three rows that
	// were already claimed. Both reads succeeded. Both were about the wrong
	// population. A queue whose two most-read facts live in two different shapes
	// invites exactly that, and the fix is not to document where they are.
	Assignee string `json:"assignee,omitempty"`
	// Category is WHAT KIND OF WORK this is, out of a closed set - see
	// TodoCategories. It is put where Status and Assignee already are so that ONE
	// read answers all three, which is the whole of e891944's finding and is not
	// worth learning twice; it is derived at read time by CategoryOf and filled in
	// the same three read paths as ReplacedBy, never in scanArtifact, so it does
	// not ride the sync paths or the signature.
	//
	// It is called category and not kind because Kind is directly above it and
	// means "this row is a todo". The console labels it "Kind" for the operator,
	// who never sees this name. Empty is a todo nobody has classified, which is
	// legal and is most of the queue.
	Category string `json:"category,omitempty"`
	// Raiser is WHO THE WORK CAME FROM, put beside Assignee for the reason
	// Category is put beside Status: the queue is read by these facts together,
	// and one of them living a level down in fields is how a client ends up
	// answering a question about the wrong population. Derived at read time by
	// RaiserOf and filled in the same three read paths as ReplacedBy, never in
	// scanArtifact, so it does not ride the sync paths or the signature.
	//
	// Raised by X, carried by Y are two facts and neither is owner_user, which
	// is the seat whose token wrote the row and is a column above. Empty is a
	// row that does not say where its work came from - every queue item written
	// before this field, and nothing here guesses one. See RaiserField.
	Raiser string `json:"raiser,omitempty"`
	// Started is when this row FIRST went active, and LastWorked is when
	// something that counts as work last touched it. Both are columns, stamped
	// by setArtifactStatus and appendEvent, and both are pointers because ABSENT
	// IS A REAL ANSWER: a row nobody has started has no start, and a zero time
	// would read as 1970 - the worst case - for the ordinary case of a fresh
	// row. See workEvidence for what moves LastWorked and why the list is short.
	//
	// They are separate from Updated, which moves on ANY write. A rename looked
	// exactly like progress, which is what left six-hour-old claims reading as
	// work in flight, and is the whole reason these two exist.
	//
	// Unlike Assignee and Category above, these ARE scanned: they are columns
	// rather than derivations, and a reader that cannot see them has to ask the
	// database directly - which is the state these shipped in.
	Started    *time.Time `json:"started,omitempty"`
	LastWorked *time.Time `json:"last_worked,omitempty"`
	// Notes is what has been LEARNED about this row since it was filed, oldest
	// first: measurements, the fix shape somebody worked out, what it turned out
	// to be blocked on. Each one is attributed to the seat that wrote it and
	// nothing already on the row changes when one is added - see
	// internal/store/todonote.go, which is where an append differs from an edit
	// and why.
	//
	// It is here rather than only behind GET /api/todo/{id}/notes for the reason
	// Assignee, Category and Raiser are here: a queue is read by these facts
	// together, and a client that has to make a second call for one of them is a
	// client that mostly does not. Unlike those three this one is a QUERY, so it
	// is filled on the single-row read paths only - a list of 200 rows carrying
	// every note on each of them is a different endpoint's answer.
	//
	// Derived at read time and permission-filtered on the entries themselves, so
	// like ReplacedBy it is not stored, not signed and does not replicate. Empty
	// is a row nobody has learned anything about yet, which is most of them.
	Notes []NoteEntry `json:"notes,omitempty"`
	// Reported and External are the forge link: whether this artifact has been
	// filed as an issue somewhere, and where. Both are written only by the
	// forge endpoints - see SetArtifactExternal - so an ordinary update of the
	// artifact carries them forward untouched.
	Reported  bool         `json:"reported"`
	External  *ExternalRef `json:"external,omitempty"`
	HLC       int64        `json:"hlc"`
	Node      string       `json:"node"`
	Tombstone bool         `json:"tombstone"`
	// Sig is the signature of the node named in Node over this row's
	// authenticated fields - see internal/sign. It travels with the row and is
	// what the merge on the far side checks before it looks at anything else.
	Sig []byte `json:"sig,omitempty"`
	// SigForm is WHICH CANONICAL FORM Sig was made over.
	//
	// Every signature already begins with a domain string - "flowy.artifact.v1"
	// - so the version has always been in the bytes and nothing read it. A
	// verifier had to assume the form, which is what made adding a signed
	// column a choice between breaking every signature ever written and putting
	// the value outside the signature where a relay can rewrite it.
	//
	// Empty means v1: every row written before this existed was signed under
	// that domain. It is NOT itself signed, because it selects the verifier
	// rather than asserting anything - a wrong value fails verification, which
	// is the same answer tampering gets - and an unknown value is REFUSED
	// rather than defaulted, so a peer cannot name a form this node has never
	// heard of and have its row judged by the weakest one available.
	SigForm string `json:"sig_form,omitempty"`
	// AuthorSig is the OWNER's own signature over the fields only an owner
	// writes - see sign.CanonicalArtifactAuthorship. It is a different claim
	// from Sig, made with a different key: Sig says which node wrote these
	// bytes, this says who the words are from. A row can carry both, one, or
	// neither.
	AuthorSig []byte `json:"author_sig,omitempty"`
	// Authorship is what THIS node can say about that claim: authored when a
	// principal signature over the row verified here, attributed otherwise. It
	// is decided on the way in - at the local write, or at the merge - and never
	// taken from the wire, because it is this node's own finding and not
	// something a peer gets to assert about itself.
	Authorship string    `json:"authorship,omitempty"`
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
}

// InsertArtifact writes an artifact, stamping id/hlc/node when unset.
func (d *DB) InsertArtifact(ctx context.Context, a *Artifact) error {
	if err := d.stamp(&a.ID, &a.HLC, &a.Node); err != nil {
		return err
	}
	if a.Visibility == "" {
		a.Visibility = "project"
	}
	if err := requireProjectPtr(ctx, d.sql, a.Project); err != nil {
		return err
	}
	// The date is minted here and passed in rather than left to the column's
	// default, because it is inside the signature - see createdNow.
	a.Created = createdNow()
	if err := d.signArtifact(ctx, d.sql, a); err != nil {
		return err
	}
	var fields any
	if len(a.Fields) > 0 {
		fields = []byte(a.Fields)
	}
	err := d.sql.QueryRowContext(ctx,
		`INSERT INTO artifacts (id, type, kind, project, owner_user, title, body, discovery,
		                        status, severity, tags, user_tags, related, visibility,
		                        file_path, fields, hlc, node, tombstone, search, sig, created,
		                        author_sig, authorship, sig_form, started)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
		         $19, `+fmt.Sprintf(artifactSearchSQL, 20)+`, $21, $22, $23, $24, $25,
		         -- Born active, same rule as the other two inserts. This door is
		         -- reached only by cmd/smoke, so no test in internal/store covers
		         -- this line - it is here for consistency, and a mutation of it
		         -- would go unnoticed by that package. Said out loud rather than
		         -- left for somebody to discover by trusting the coverage.
		         CASE WHEN $9 = '`+ActiveStatus+`' THEN now() END)
		 RETURNING created, updated`,
		a.ID, a.Type, a.Kind, a.Project, a.OwnerUser, a.Title, a.Body, a.Discovery,
		a.Status, a.Severity, pq.Array(a.Tags), pq.Array(a.UserTags), pq.Array(a.Related),
		a.Visibility, a.FilePath, fields, a.HLC, a.Node, a.Tombstone, searchText(a), a.Sig,
		a.Created, a.AuthorSig, authorshipOr(a.Authorship), a.SigForm).
		Scan(&a.Created, &a.Updated)
	if err != nil {
		return fmt.Errorf("store: insert artifact: %w", err)
	}
	return nil
}

// GetArtifact reads an artifact by id, without asking who wants it. Handlers
// use ReadArtifact instead; this is for the paths that have already decided,
// and for the tests.
func (d *DB) GetArtifact(ctx context.Context, id string) (*Artifact, error) {
	a, err := scanArtifact(d.sql.QueryRowContext(ctx,
		`SELECT `+artifactColumns+` FROM artifacts ar WHERE ar.id = $1`, id), nil)
	if err != nil {
		return nil, fmt.Errorf("store: get artifact %s: %w", id, err)
	}
	return a, nil
}

// Event is one entry in the append-only log. Parents carries the thread DAG:
// empty starts a thread, one continues it, several merge branches.
type Event struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Project  *string         `json:"project"`
	Room     string          `json:"room"`
	Thread   string          `json:"thread"`
	Parents  []string        `json:"parents"`
	Actor    string          `json:"actor"`
	Artifact string          `json:"artifact"`
	SeqHLC   int64           `json:"seq_hlc"`
	Node     string          `json:"node"`
	Body     string          `json:"body"`
	Meta     json.RawMessage `json:"meta,omitempty"`
	// Addressee is who a message is directed at - a user id or an agent id -
	// and is empty when it is directed at the room.
	//
	// IN A ROOM it is not a permission axis, and that is the whole of what it
	// means there. An addressed room message is a room message: the same people
	// read it that read the room before, and nothing about the column narrows or
	// widens a read. What it changes is what a reader is told - a client can say
	// "this one is for you" instead of leaving every reader to infer it from the
	// prose - which is a rendering decision and a wake-up decision, never an
	// access one.
	//
	// WITH NO PROJECT AND NO ROOM it is the other party to a direct message, and
	// EventFilterSQL does read it: one clause in the projectless branch, which
	// is the branch that already restricts a row to its author. It widens that
	// to the author and the one principal named here, and nothing else. See
	// privateEventSQL, and IsDirectMessage for the same rule in Go.
	Addressee string `json:"addressee,omitempty"`
	// Private says this event is a direct message: the addressee is a party to
	// it rather than somebody it was pointed at, and only the two of them read
	// it. It is DERIVED and never stored - there is no private column, the
	// signature does not cover it, and appendEvent does not write it. scanEvent
	// works it out from the row's own project, type, room and addressee, which
	// is IsDirectMessage, which is privateEventSQL in Go.
	//
	// So it is a rendering fact and never a permission one. A client that
	// received it over the wire from a peer is holding a claim; what decided
	// whether the row reached that client at all is EventFilterSQL, in the
	// database, on the columns. Nothing may ever read this to answer "may they
	// see it" - by the time it is set, that question has been answered.
	Private bool `json:"private,omitempty"`
	// Sig is the writing node's signature over the event - see Artifact.Sig.
	Sig []byte `json:"sig,omitempty"`
	// AuthorSig is the ACTOR's own signature over the whole event, and
	// Authorship is what this node can say about it - see Artifact.AuthorSig
	// and Artifact.Authorship. An event is the row where the two claims come
	// apart most sharply: the actor column is the whole of what a message
	// means, and a node signature says nothing about it.
	AuthorSig  []byte    `json:"author_sig,omitempty"`
	Authorship string    `json:"authorship,omitempty"`
	Created    time.Time `json:"created"`
	// Citation is the message this one says it is about, resolved for whoever
	// is reading - see citations.go. It is derived at read time rather than
	// stored, exactly as Artifact.ReplacedBy is: what the row holds is a
	// pointer and a span in meta, there is no column for this, it is not in the
	// signature and it does not replicate. The read paths that carry the
	// permission filter fill it, and every other way of getting an Event leaves
	// it nil, because both the words and whether there are any depend on who is
	// asking.
	Citation *Citation `json:"citation,omitempty"`
}

// AppendEvent writes an event, stamping id/seq_hlc/node when unset.
// workEvidence says which events count as somebody working a row.
//
// Deliberately a short list rather than "any event". A read, a delivery or a
// presence poll is not work, and counting them would make last_worked move
// whenever anybody LOOKED at a row - which is how `updated` became useless for
// this question in the first place.
func workEvidence(kind string) bool {
	switch kind {
	case EventMergeGate, EventMergeLand, EventMergeAbandon,
		EventTodoNote, EventTodoStatus, EventTodoAssign:
		return true
		// EventTodoEdit is DELIBERATELY ABSENT. It records an edit of a queue
		// item's WORDS - a retitling, a reworded body - and a rename is exactly
		// what made `updated` useless for this question. Counting it would widen
		// "evidence of work" back to "any write" one case at a time, which is how
		// the column being replaced got that way.
		//
		// Changing what a row SAYS is not progress on what it asks for.
	}
	return false
}

func (d *DB) AppendEvent(ctx context.Context, e *Event) error {
	ctx, span := otel.Start(ctx, otel.KindIngest, "event.append")
	defer span.End()
	span.SetAttr("event.type", e.Type)
	return d.appendEvent(ctx, d.sql, e)
}

// appendEvent is AppendEvent against whatever is in hand: the pool for a
// message on its own, a transaction for one that is part of an operation.
func (d *DB) appendEvent(ctx context.Context, q execer, e *Event) error {
	if err := d.stamp(&e.ID, &e.SeqHLC, &e.Node); err != nil {
		return err
	}
	if e.Thread == "" {
		// A thread with no explicit head is named after its first event.
		e.Thread = e.ID
	}
	// An event lands in a declared project or not at all, exactly as an
	// artifact does: a chat room, a worklog entry and a status trail all carry
	// a project, and a project nothing declared is a room nobody can find.
	if err := requireProjectPtr(ctx, q, e.Project); err != nil {
		return err
	}
	// The date is minted here and passed in rather than left to the column's
	// default, because it is inside the signature - see createdNow.
	e.Created = createdNow()
	if err := d.signEvent(ctx, q, e); err != nil {
		return err
	}
	var meta any
	if len(e.Meta) > 0 {
		meta = []byte(e.Meta)
	}
	// The addressee is written through nullif so that "directed at the room" is
	// one value in the column rather than two: the read reads NULL back as the
	// empty string, and the signature is over the empty string either way.
	err := q.QueryRowContext(ctx,
		`INSERT INTO events (id, type, project, room, thread, parents, actor, artifact,
		                     seq_hlc, node, body, meta, addressee, sig, author_sig, authorship,
		                     created)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, nullif($13, ''), $14, $15,
		         $16, $17)
		 RETURNING created`,
		e.ID, e.Type, e.Project, e.Room, e.Thread, pq.Array(e.Parents), e.Actor,
		e.Artifact, e.SeqHLC, e.Node, e.Body, meta, e.Addressee, e.Sig, e.AuthorSig,
		authorshipOr(e.Authorship), e.Created).
		Scan(&e.Created)
	if err != nil {
		return fmt.Errorf("store: append event: %w", err)
	}
	// AN EVENT ABOUT A ROW IS EVIDENCE SOMEBODY TOUCHED IT.
	//
	// last_worked answers "when did this work last leave a trace", and the
	// traces are these: a gate declared or recorded, a land, a note, a change
	// of hands. Moving it here rather than at each verb means a new verb that
	// writes an event cannot forget - and forgetting is what a per-door rule
	// costs, six times over, on this codebase today.
	//
	// Best effort on purpose: a clock that failed to move must never fail the
	// write it was describing. The worst case is a row that looks staler than
	// it is, which is the direction a nag should err in anyway.
	if e.Artifact != "" && workEvidence(e.Type) {
		_, _ = q.ExecContext(ctx,
			`UPDATE artifacts SET last_worked = now() WHERE id = $1`, e.Artifact)
	}

	// The handlers answer with the event they just wrote rather than reading it
	// back, so the derived flag is set here for the same reason scanEvent sets
	// it: whatever a caller was handed, it was worked out from the columns.
	e.Private = IsDirectMessage(e)
	return nil
}

// GetEvent reads an event by id, without asking who wants it.
func (d *DB) GetEvent(ctx context.Context, id string) (*Event, error) {
	e, err := scanEvent(d.sql.QueryRowContext(ctx,
		`SELECT `+eventColumns+` FROM events e WHERE e.id = $1`, id))
	if err != nil {
		return nil, fmt.Errorf("store: get event %s: %w", id, err)
	}
	return e, nil
}

// ThreadEvents reads one thread in log order.
//
// One statement, like ListEvents: it used to read the thread's ids and then
// fetch each event by id, which is a query per message. The console's thread
// pane and the forge's reviewer loop both walk whole threads, so that was the
// length of the conversation in round trips every time either of them ran, and
// the rows in between could move under it.
//
// It asks who wants it no more than GetEvent does - the callers that need the
// filter use ListEvents with a thread.
func (d *DB) ThreadEvents(ctx context.Context, thread string) ([]*Event, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+eventColumns+` FROM events e WHERE e.thread = $1 ORDER BY e.seq_hlc, e.id`,
		thread)
	if err != nil {
		return nil, fmt.Errorf("store: thread %s: %w", thread, err)
	}
	defer rows.Close()

	out := []*Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("store: thread %s: %w", thread, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: thread %s: %w", thread, err)
	}
	return out, nil
}

// Counts returns the row count of each spine table, which is what /healthz
// reports when it is asked for detail.
func (d *DB) Counts(ctx context.Context) (map[string]int64, error) {
	tables := []string{"users", "agents", "tokens", "grants", "artifacts", "events", "tasks",
		"peers", "projects"}
	out := make(map[string]int64, len(tables))
	for _, t := range tables {
		var n int64
		// t comes from the fixed list above, never from a caller.
		if err := d.sql.QueryRowContext(ctx, `SELECT count(*) FROM `+t).Scan(&n); err != nil {
			return nil, fmt.Errorf("store: count %s: %w", t, err)
		}
		out[t] = n
	}
	return out, nil
}
