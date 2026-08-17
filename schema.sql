-- Handoff Fabric node - Phase 0 schema spine.
--
-- Every mutable row carries hlc bigint + node text so a later phase can merge
-- concurrent writes from two nodes without a coordinator. Primary keys are text
-- ULIDs: lexicographically sortable, minted anywhere, never colliding.
--
-- events is append-only and carries the thread DAG in parents text[]; branch and
-- merge are expressed there rather than in a separate table.
--
-- Postgres wire portable only. The deployment target is SereneDB, so nothing in
-- here may depend on a stock-Postgres storage feature: no extensions, no
-- partitioning, no SERIAL/identity (ids come from the node), no triggers, no
-- stored procedures.
--
-- The one exception is search, and it is deliberately quarantined at the bottom
-- of this file: artifacts.search is a tsvector with a GIN index, which is
-- Postgres full text and nothing else. Phase 1 needs ranked text search now;
-- SereneDB brings vectors, and when it does the whole of the SEARCH section
-- goes away - one column and one index - without touching a row of the spine.
-- The column is filled by the node on write rather than by a generated column
-- or a trigger, so nothing but the type and the index method is engine-specific
-- (array_to_string is STABLE, so a generated column is not even possible, and a
-- trigger would drag PL/pgSQL into a schema that promises none).

BEGIN;

-- People. auto_delegate decides whether inbound work may be handed to an agent
-- without asking first.
CREATE TABLE IF NOT EXISTS users (
    id            text PRIMARY KEY,
    handle        text UNIQUE,
    display       text,
    auto_delegate boolean DEFAULT true,
    hlc           bigint,
    node          text
);

-- Agents acting for a user.
--
-- Two different questions, two columns, because they are not the same question
-- and one of them is a permission:
--
--   kind       - which runtime this is: claude|glm|opencode. It says what is on
--                the other end of the token and nothing about what it may do.
--   agent_kind - what the agent is for: worker|reviewer|system|monitor. Only
--                system and monitor may post a federation-scope announcement,
--                which is the one thing on this node that one agent says and
--                every node in the fabric then shows to everybody.
--
-- agent_kind defaults to 'worker' and is written that way in the CREATE TABLE
-- as well as in the ALTER below. The default is the point: every agent that
-- existed before this column did is a worker, every seed that does not mention
-- it still writes a valid row, and the narrow capability is the one you have to
-- ask for rather than the one you have to remember to drop.
CREATE TABLE IF NOT EXISTS agents (
    id         text PRIMARY KEY,
    user_id    text REFERENCES users (id),
    kind       text,
    agent_kind text DEFAULT 'worker',
    project    text,
    hlc        bigint,
    node       text
);

-- Bearer tokens. A token resolves to a principal: the (user, agent, project)
-- triple every request is authorised as. project is the principal's home
-- project - the one it may read without a grant. Either user_id or agent_id may
-- be empty; a token that names only an agent inherits that agent's user.
--
-- Tokens are local credentials, not fabric state: they carry no hlc/node and
-- Phase 3 will not replicate this table.
CREATE TABLE IF NOT EXISTS tokens (
    token    text PRIMARY KEY,
    user_id  text,
    agent_id text,
    project  text
);

-- Where a waiter got to in the log. One row is one thing that blocks on the
-- inbox and returns: `flowy inbox --as NAME` is the caller, and NAME is the
-- label.
--
-- The cursor is here rather than in a file beside the client, and that is the
-- whole point of the table. Every harness that carried its own cursor file
-- reread what it had already answered the first time two of them ran, or the
-- first time one was restarted from a different directory; a position in a
-- shared log belongs to the log.
--
-- The key is the principal AND the label, and it needs both. Several agents in
-- a fleet run under one token, so a cursor keyed on the principal alone means
-- one agent's read consumes another's wake-up - not a rare race, every message.
-- And one session can speak under more than one name over its life, so the unit
-- that has a position is the process that blocks and returns rather than the
-- person behind it. The label is what you would restart.
--
-- A label is created explicitly (see store.DeclareInboxReader) and an unknown
-- one is refused. A typo that silently became a new reader starting from now
-- would be an inbox that is permanently empty and never says why, which is the
-- failure this table is meant to end rather than a new one to introduce.
--
-- principal is the (user, agent, project) triple a token resolves to, joined
-- the way sync_pending joins it - store.pendingKey, unit separators, so no
-- principal can be forged out of two others by choosing an id with a separator
-- in it. The same person in two projects is two principals reading two
-- different slices of the log, so they are two rows here.
--
-- It is local, like tokens: no hlc, no node, no signature, and nothing
-- replicates it. A cursor is a fact about a process on this machine, and a
-- replicated one would mean a peer's read consuming a wake-up here.
-- acked_delivery and acked_quiet are why the mark last moved: because messages
-- were handed over, or because a poll expired with nothing to hand over and the
-- mark still had to pass the reader's own messages. Both advance the same
-- column, so without the two counters a lost acknowledgement and a quiet night
-- are the same row - and "messages that never arrived" is exactly the question
-- somebody asks of this table when it has already happened.
CREATE TABLE IF NOT EXISTS inbox_readers (
    principal      text NOT NULL,
    reader         text NOT NULL,
    read_cursor    bigint NOT NULL DEFAULT 0,
    acked_delivery bigint NOT NULL DEFAULT 0,
    acked_quiet    bigint NOT NULL DEFAULT 0,
    created        timestamptz DEFAULT now(),
    updated        timestamptz DEFAULT now(),
    PRIMARY KEY (principal, reader)
);

-- Presence. A waiter acks only when the cursor moves, so last-ack conflates
-- room activity with listener health: a busy room makes a dead listener look
-- fresh, a quiet room makes a healthy one look gone. The poll is the signal
-- the server actually has - last_poll_at moves on every /api/inbox/wait,
-- whatever the room is doing, and polls_in_flight is > 0 exactly while a
-- waiter is attached. Neither is liveness of a process on somebody's machine;
-- they are the honest ceiling of what the node can see, and the views say so.
ALTER TABLE inbox_readers ADD COLUMN IF NOT EXISTS last_poll_at timestamptz;
ALTER TABLE inbox_readers ADD COLUMN IF NOT EXISTS polls_in_flight int NOT NULL DEFAULT 0;

-- And WHAT KIND of listener that poll came from, which is a different question
-- from whether anybody is polling - they came apart on the night this column
-- was added, for 28 minutes, with every surface reporting healthy.
--
-- A waiter forks a successor before it returns so the room stays heard while
-- the agent reads (see inboxhandover.go). That successor is detached, so it is
-- nobody's harness task: it HEARS EVERYTHING AND CAN WAKE NOBODY, because only
-- a tracked waiter exiting produces a notification. last_poll_at was seconds
-- old and polls_in_flight was 1 the whole time, both of them true, and the
-- answer to the question anybody was actually asking was no.
--
-- 'unknown' is the default rather than 'tracked' because a row written before
-- this column existed, or by a client that does not send its kind, is evidence
-- of nothing - and the reading that costs 28 minutes is the optimistic one.
-- Only the three words ever land here; store.WaiterKindOf is what makes that
-- true, so a client cannot invent a fourth state for the roster to render.
ALTER TABLE inbox_readers ADD COLUMN IF NOT EXISTS waiter_kind text NOT NULL DEFAULT 'unknown';

-- Phase 10. The project registry: the row every project column points at.
--
-- A project used to be a free string. Nothing declared one and nothing checked
-- one, so a project came into existence the moment somebody wrote it and the
-- only way to ask what projects there were was a UNION of DISTINCT project over
-- the tables that carry it - which cannot see a project that has no rows yet,
-- and cannot tell a typo from a place. A day of real work was filed into `pa`
-- that way, which is the smoke seeder's fixture project.
--
-- So this is a referent and not a convenience list. A token's scope, an
-- artifact's project and a grant's endpoints name a row here, and a local write
-- into a project with no row is refused - see store.ErrUndeclaredProject.
--
--   id         - the referent itself: exactly the string the other tables carry
--                and that is already inside their signatures. It is the primary
--                key rather than a ULID with a UNIQUE name beside it, because
--                the merge key has to be the thing that is referenced: two
--                nodes declaring `flowy` independently would otherwise mint two
--                ids for one name and collide on a column the merge does not
--                key on. Here they are one row and one ordinary merge.
--   name       - the label a person reads. Free to differ from the id.
--   created_by - the user that declared it, when a person did.
--   provenance - how this row came to exist: declared|seed|backfill|observed|
--                pinned. It is about the ROW.
--   fixture    - what the project is FOR: demo seed data rather than real work.
--                A different question from provenance, which is why it is a
--                different column. It does not refuse anything: a fixture is a
--                legitimate writable project, and the flag exists so that
--                writing real work into one is visible at the moment it is done
--                rather than six hours later.
--   origin     - where the project came from, and the only externally checkable
--                thing this row says: the canonicalised git remote when the
--                project has one (`git:github.com/owner/name`), and a locally
--                derived identity when it does not (`derived:<node>/<name>`).
--                It is what makes a name collision decidable instead of a
--                judgement call - two nodes on one repo arrive at one origin
--                without any reconcile protocol, and two different projects
--                both called `flowy` have different origins and are refused
--                rather than silently merged. See store.CanonicalOrigin.
--   superseded - the origins this one replaced, oldest first. A project that
--                had no repo and then got one, or whose remote was renamed or
--                transferred, SUBSTITUTES its origin - and substitution is an
--                alias, never a rewrite: no row's project column is touched,
--                because `project` is inside the signed payload and rewriting
--                it would forge every row that named it. Rows keep pointing at
--                the name, which never changes; the chain is what lets a peer
--                still holding the old origin be recognised as the same
--                project. `supersedes` on a report is the same shape.
--   origin_at  - when origin last changed, so a substitution has a date.
--
-- Replicated and signed like every other fabric row, because `project` is
-- already inside the signed payload of everything that carries it: a node-local
-- registry would leave the referent local while every reference to it is
-- federated.
--
-- There is no tombstone column, and that is deliberate rather than an omission.
-- Every other replicated table has one because a row can be deleted; a project
-- cannot, because deleting it would orphan every row that names it - the
-- registry adapts to the data and never the other way round - and because a
-- revocable referent is a referent a peer can revoke: a tombstone arriving from
-- a peer would stop this node writing into its own project.
CREATE TABLE IF NOT EXISTS projects (
    id         text PRIMARY KEY,
    name       text,
    created_by text,
    provenance text DEFAULT 'declared',
    fixture    boolean DEFAULT false,
    origin     text,
    superseded text[],
    origin_at  timestamptz,
    hlc        bigint,
    node       text,
    sig        bytea,
    created    timestamptz DEFAULT now()
);

-- Cross-project sharing. A grant is a capability from one project to another,
-- optionally narrowed to a subject or a single artifact. Deletes are tombstones
-- so the row can still merge after it is gone.
CREATE TABLE IF NOT EXISTS grants (
    id           text PRIMARY KEY,
    from_project text,
    to_project   text,
    subject      text,
    artifact     text,
    cap          text DEFAULT 'read',
    granted_by   text,
    hlc          bigint,
    node         text,
    tombstone    boolean DEFAULT false,
    -- Phase 6.5: the grantor node's signature over this row. See the ALTERs
    -- below and internal/sign.
    sig          bytea
);

-- Everything a node holds that is worth naming.
-- type: transcript|memory|chat|bug|feature|note|report
-- kind narrows a type: a memory is a note|todo|feature|handoff.
-- visibility: personal|project|shared
-- project NULL means the artifact is personal to owner_user.
CREATE TABLE IF NOT EXISTS artifacts (
    id         text PRIMARY KEY,
    type       text,
    kind       text,
    project    text,
    owner_user text,
    title      text,
    body       text,
    discovery  text,
    status     text,
    severity   text,
    tags       text[],
    user_tags  text[],
    related    text[],
    visibility text DEFAULT 'project',
    file_path  text,
    fields     jsonb,
    -- Phase 6. external is the link to an issue on a forge -
    -- {forge, repo, number, url, state} plus the two sync cursors - and
    -- reported says the artifact has been filed there. Both are written only by
    -- the forge endpoints: an ordinary update of an artifact does not touch
    -- them, so editing a bug cannot silently unfile it.
    reported   boolean DEFAULT false,
    external   jsonb,
    hlc        bigint,
    node       text,
    tombstone  boolean DEFAULT false,
    sig        bytea,
    created    timestamptz DEFAULT now(),
    updated    timestamptz DEFAULT now()
);

-- Append-only log. parents is the branch/merge DAG: one parent continues a
-- thread, several merge them, none starts one. seq_hlc is the packed hybrid
-- logical clock and is what peers page through.
CREATE TABLE IF NOT EXISTS events (
    id        text PRIMARY KEY,
    type      text,
    project   text,
    room      text,
    thread    text,
    parents   text[],
    actor     text,
    artifact  text,
    seq_hlc   bigint,
    node      text,
    body      text,
    meta      jsonb,
    -- Who a message is directed at, NULL when it is directed at the room. It is
    -- a user id or an agent id, the same two things actor holds, and it is
    -- nullable because most messages are addressed to nobody in particular.
    --
    -- It is not a permission column and nothing in the permission filter reads
    -- it: an addressed message is read by exactly the principals that could
    -- read the room without it. What it carries is what a reader is TOLD.
    addressee text,
    sig       bytea,
    created   timestamptz DEFAULT now()
);

-- Handoffs. One row is one assignment: this artifact, from this person to that
-- one, with the chat thread it opened and where it got to.
--
-- state: open|delegated|done.
--   open      - handed over, waiting for the person
--   delegated - handed on to that person's agent (assignee_agent), which is what
--               users.auto_delegate does at assignment time
--   done      - finished, by either side
--
-- The share that lets to_user read the artifact is a grants row written by the
-- same operation and carrying the same hlc reading; the conversation is the
-- events whose thread is this row's. A task names both and owns neither, so
-- each keeps the permission filter it already had.
CREATE TABLE IF NOT EXISTS tasks (
    id             text PRIMARY KEY,
    artifact       text,
    from_user      text,
    to_user        text,
    project        text,
    state          text DEFAULT 'open',
    assignee_agent text,
    thread         text,
    hlc            bigint,
    node           text,
    sig            bytea
);

-- Who a node is. One row per node this one has ever had to believe, keyed by
-- the node name that every replicated row carries in its node column.
--
-- The local node's row is the only one that holds private_key: it is the seed
-- half of an ed25519 keypair, it never leaves this machine, and nothing that
-- replicates ever selects it. Every other row is a public key and how this node
-- came to hold it:
--
--   pinned = true  - the operator put it there, out of band, naming the node
--                    and its key (`flowy identity pin`, or FLOWY_PEER_KEYS).
--                    This is the authoritative kind.
--   pinned = false - it arrived over the wire and this node had never heard of
--                    that node before, so it was taken on trust the first time
--                    and is held to ever after. A second, different key for a
--                    node already in this table is refused rather than applied:
--                    there is no key rotation over the wire, because a rotation
--                    a peer can serve is an impersonation a peer can serve.
--
-- The row is self-signed - sig is the node's own signature over its name and
-- its public key - so an identity can travel through a relay that holds neither
-- key without that relay being able to alter it. That is what lets A verify C's
-- rows on a page it pulled from B.
CREATE TABLE IF NOT EXISTS node_identity (
    node_id     text PRIMARY KEY,
    public_key  bytea NOT NULL,
    private_key bytea,
    pinned      boolean NOT NULL DEFAULT false,
    created_hlc bigint,
    sig         bytea
);

-- Replication bookmarks, one row per peer node.
CREATE TABLE IF NOT EXISTS peers (
    peer           text PRIMARY KEY,
    pull_cursor    bigint DEFAULT 0,
    pushed_cursor  bigint DEFAULT 0,
    last_seen      timestamptz
);

-- What a page of a delta could not carry, per reader.
--
-- A grant can make an artifact that is older than the reader's cursor readable
-- for the first time, so a pull rescans below the cursor for the rows the
-- grants in that page just opened. That rescan is a page like any other, and
-- what does not fit in it cannot be found again by paging forward - the grant
-- is under the cursor by then and nothing else about those rows ever moved.
-- So the overflow is written down here instead, and every later pull by the
-- same reader drains it.
--
-- principal is the reader the rows are owed to - the (user, agent, project)
-- triple a token resolves to - rather than a peer node, because a pull knows
-- which principal is asking and not which machine it is asking from.
-- sent_hwm is the high water mark a row was last handed over under: when the
-- reader comes back with a cursor at or above it, the row has certainly been
-- applied and the debt is settled.
CREATE TABLE IF NOT EXISTS sync_pending (
    principal text,
    artifact  text,
    sent_hwm  bigint DEFAULT 0,
    PRIMARY KEY (principal, artifact)
);

-- Phase 7. What a file written into the FUSE mount says should happen to the
-- store, written down before it happens.
--
-- A close(2) on a file in the mount cannot wait for a signed, indexed,
-- transactional write and then report a database error to an agent that has
-- already moved on. So the mount does not do the write in the callback: it
-- records the intent here - the path, the bytes and their hash - and answers.
-- A drainer applies the intents afterwards, in one transaction each, and only
-- then marks the row applied.
--
-- That ordering is the whole point. The row is committed before the callback
-- returns, so a node that dies between the close and the store write comes back
-- with the intent still pending and replays it: at-least-once delivery, with
-- the artifact, its event and this row's applied stamp in one transaction, so
-- a crash in the middle leaves neither half. Replaying is safe because the
-- drainer compares hash against the last intent it applied for the same
-- artifact and skips a write the store already has, which is what turns
-- at-least-once into exactly one write.
--
-- Local, like tokens and sync_pending: no hlc, no node signature, never
-- replicated. It is this node's queue of work on its own store, and a peer has
-- no business knowing what a file here was called or when it was closed. The
-- artifact the drainer writes out of it is the fabric row, and that one is
-- stamped and signed like every other.
CREATE TABLE IF NOT EXISTS fs_intents (
    id         text PRIMARY KEY,
    node       text,
    -- The mount-relative path the write came in on: <project>/<user>/<type>/<name>.
    -- Kept whole so a pending queue can be read by a person.
    path       text,
    -- The row it is a write of. Minted when the file was new, so a replay after
    -- a crash writes the same id rather than a second artifact.
    artifact   text,
    owner_user text,
    -- Who the event the drainer writes will name: the agent when the mount was
    -- opened with an agent's token, the user otherwise. It is on the row so
    -- that a reconcile after a restart writes the same attribution the close
    -- would have, without needing the token that is long gone.
    actor      text,
    -- NULL is the personal floor, exactly as it is on artifacts.
    project    text,
    type       text,
    visibility text,
    -- The name the file has in the mount, which is what artifacts.file_path
    -- holds for a row that came in through here.
    name       text,
    -- sha256 of content, hex. The dedup key.
    hash       text,
    content    text,
    applied    timestamptz,
    created    timestamptz DEFAULT now()
);

-- Phase 8. What the node saw itself do.
--
-- A span is one operation - an MCP call, a permission check, a query, an
-- ingest, a leg of a sync - with the trace it belongs to and the span above it.
-- The trace id is what makes a handoff followable end to end: it is minted on
-- the node the work was assigned on, it rides that assignment's opening event
-- in meta (which is inside the event's signature, so a relay cannot rewrite
-- it), and the node the work is delivered to continues the same trace rather
-- than starting one of its own.
--
-- Local, like tokens and fs_intents: no hlc, no signature, never replicated.
-- A span is this node's account of what this node did, and the collector -
-- `flowy traces --peer` - is what puts two nodes' accounts of one trace back
-- together. Replicating them would mean merging somebody else's telemetry into
-- the fabric under the fabric's own rules, which is not what telemetry is.
--
-- span_id is the primary key rather than a serial, and the spans that stand for
-- a replicated row derive it from that row's id, so applying the same delta
-- twice records one span rather than two - see store.DeliverSpanID.
CREATE TABLE IF NOT EXISTS spans (
    span_id    text PRIMARY KEY,
    trace_id   text NOT NULL,
    parent_id  text,
    name       text,
    kind       text,
    node       text,
    -- Who the work was for. The scope filter reads these three: a span is its
    -- principal's, and after that it is the project's - and then only when the
    -- artifact it names is one the reader may read.
    actor      text,
    user_id    text,
    project    text,
    artifact   text,
    status     text,
    started    timestamptz,
    ended      timestamptz,
    duration_us bigint,
    attrs      jsonb
);

CREATE INDEX IF NOT EXISTS spans_trace_idx   ON spans (trace_id, started);
CREATE INDEX IF NOT EXISTS spans_started_idx ON spans (started);
CREATE INDEX IF NOT EXISTS spans_user_idx    ON spans (user_id);

-- What a metric read measured, so that "unusual" can be a statement about this
-- node's own history rather than about a number somebody picked.
--
-- One row per series per read, keyed by the scope the read was made in: two
-- principals see different corpora, so their histories are different series and
-- must not be averaged together. The anomaly pass refuses a verdict below
-- metricMinSamples samples rather than calling three points a baseline.
CREATE TABLE IF NOT EXISTS metric_samples (
    id     text PRIMARY KEY,
    scope  text,
    series text,
    value  double precision,
    at     timestamptz DEFAULT now()
);

CREATE INDEX IF NOT EXISTS metric_samples_series_idx ON metric_samples (scope, series, at);

-- Refusals, counted. A 401 or a 403 under /api/ is written here as it happens,
-- with the principal it was refused to when there was one.
--
-- It is the audit half of the permissions group, and it is scope-filtered like
-- everything else: you see the refusals you were given, the operator sees the
-- node's. Nothing about the row that was refused is stored - not its id, not
-- its project - because a denial log that records what somebody could not read
-- is a way to read it.
CREATE TABLE IF NOT EXISTS access_denials (
    id      text PRIMARY KEY,
    at      timestamptz DEFAULT now(),
    user_id text,
    agent   text,
    status  integer,
    method  text,
    route   text,
    reason  text
);

CREATE INDEX IF NOT EXISTS access_denials_at_idx   ON access_denials (at);
CREATE INDEX IF NOT EXISTS access_denials_user_idx ON access_denials (user_id, at);

-- What a merge did, per peer, so the sync group can report conflicts rather
-- than guess at them. A conflict is a row that arrived, was authentic and
-- allowed, and lost its merge to a row already here: last-writer-wins keeps no
-- loser, so if the count is not taken as it happens it cannot be taken at all.
CREATE TABLE IF NOT EXISTS sync_stats (
    peer      text PRIMARY KEY,
    conflicts bigint DEFAULT 0,
    refused   bigint DEFAULT 0,
    applied   bigint DEFAULT 0,
    at        timestamptz
);

-- The bytes of an attachment, and nothing else about it.
--
-- An attachment is an artifact of type 'attachment' - title, project, scope,
-- owner, the same permission filter as everything else - and this table holds
-- the one thing an artifact row has no column for. What the bytes are (size,
-- sha256, the claimed content type and the sniffed one) rides the artifact's
-- fields, so it is inside the row signature; only the payload is out here.
--
-- It is a table of its own for two reasons, and neither is about size alone:
--
--   - events is append-only and every reader of the log pays for every column
--     of every row it pages. A megabyte in there is a megabyte through every
--     sync page, every timeline read and every peer's merge, forever.
--   - artifacts.body is text, feeds the search vector, and is returned by every
--     list. Postgres text cannot hold a NUL byte at all, so a captured binary
--     would not round-trip through it - it would be refused by the database or
--     mangled on the way in, which is the failure this surface exists to avoid.
--
-- bytea, not text, for that last reason. One row per artifact, so the join in
-- store.ReadAttachment is on the primary key.
--
-- No foreign key onto artifacts, like tasks.artifact and events.artifact: rows
-- arrive from peers in reading order rather than in dependency order, and a
-- constraint here would make that order load-bearing. The artifact is what
-- decides whether these bytes may be read - the read joins the two and applies
-- ArtifactFilterSQL to the artifact - so an orphan here is unreachable rather
-- than exposed.
--
-- Not replicated yet. The artifact row travels as it always did, the bytes stay
-- on the node they were written to, and a peer that pulled the row is told the
-- content is not here rather than handed an empty file. The digest that would
-- let a peer check bytes it fetched later is already inside the signature, so
-- carrying them across is a sync change and not a schema one.
CREATE TABLE IF NOT EXISTS attachment_bytes (
    artifact text PRIMARY KEY,
    content  bytea NOT NULL,
    created  timestamptz DEFAULT now()
);

CREATE INDEX IF NOT EXISTS artifacts_project_type_idx ON artifacts (project, type);
CREATE INDEX IF NOT EXISTS artifacts_owner_idx        ON artifacts (owner_user);
CREATE INDEX IF NOT EXISTS artifacts_hlc_idx          ON artifacts (hlc);
CREATE INDEX IF NOT EXISTS artifacts_updated_idx      ON artifacts (updated);
-- Growth is "how many of these landed in the last day", which is a read by date.
CREATE INDEX IF NOT EXISTS artifacts_created_idx      ON artifacts (created);

-- Phase 2 stores shared memory as artifacts of type 'memory', narrowed by kind.
-- The column is in the CREATE TABLE above; the ALTER is here so a database that
-- was created by an earlier phase picks it up on the next load.
ALTER TABLE artifacts ADD COLUMN IF NOT EXISTS kind text;

CREATE INDEX IF NOT EXISTS artifacts_type_kind_idx    ON artifacts (type, kind);

-- Phase 6 links an artifact to an issue on a forge. The columns are in the
-- CREATE TABLE above; the ALTERs are here so a database created by an earlier
-- phase picks them up on the next load.
ALTER TABLE artifacts ADD COLUMN IF NOT EXISTS reported boolean DEFAULT false;
ALTER TABLE artifacts ADD COLUMN IF NOT EXISTS external jsonb;

-- "what have I filed" is a read of the reported flag, and it is a small
-- minority of the rows.
CREATE INDEX IF NOT EXISTS artifacts_reported_idx      ON artifacts (reported);

-- Phase 6.5. Every replicated row carries the signature of the node that wrote
-- it, over the canonical encoding of its authenticated fields - see
-- internal/sign. The column is nullable, because the column is not where the
-- rule lives: the merge requires a signature that verifies under the key of the
-- node named on the row, and refuses the row when there is none. Making it NOT
-- NULL would say the same thing in a place that cannot say why a row was
-- refused, and would stop a node loading a schema over a store written before
-- the column existed.
ALTER TABLE artifacts ADD COLUMN IF NOT EXISTS sig bytea;
ALTER TABLE events    ADD COLUMN IF NOT EXISTS sig bytea;
ALTER TABLE tasks     ADD COLUMN IF NOT EXISTS sig bytea;
ALTER TABLE grants    ADD COLUMN IF NOT EXISTS sig bytea;

-- Chat addressing. A message can be directed at one principal while still being
-- a message in the room, and the column is in the CREATE TABLE above; the ALTER
-- is here so a database created by an earlier phase picks it up on the next
-- load. Nullable, with no backfill: every message written before this existed
-- was directed at the room, which is what NULL already says.
ALTER TABLE events    ADD COLUMN IF NOT EXISTS addressee text;

CREATE INDEX IF NOT EXISTS events_thread_idx          ON events (thread);
CREATE INDEX IF NOT EXISTS events_seq_hlc_idx         ON events (seq_hlc);
CREATE INDEX IF NOT EXISTS events_project_type_idx    ON events (project, type);
CREATE INDEX IF NOT EXISTS events_artifact_idx        ON events (artifact);

-- Phase 9. What an agent is for, as opposed to what it runs. The column is in
-- the CREATE TABLE above; the ALTER is here so a database created by an earlier
-- phase picks it up on the next load - and it carries the same default, so the
-- agents already in that database come back as workers rather than as NULL.
ALTER TABLE agents ADD COLUMN IF NOT EXISTS agent_kind text DEFAULT 'worker';
UPDATE agents SET agent_kind = 'worker' WHERE agent_kind IS NULL;

-- Announcements are artifacts of type 'announcement' - one table, one
-- permission filter, one signature, one merge, exactly as a memory item is an
-- artifact of type 'memory'. What makes one an announcement rather than a note
-- is fields->>'scope' (node|project|federation), and that is read on every pull
-- and every push, because a node-scope announcement must not leave the node.
-- The severity is severity and the window is status ('active' until it is
-- 'resolved') plus fields->>'resolved_at'.
CREATE INDEX IF NOT EXISTS artifacts_announcement_idx ON artifacts (type, status);

CREATE INDEX IF NOT EXISTS agents_user_idx            ON agents (user_id);
CREATE INDEX IF NOT EXISTS grants_to_project_idx      ON grants (to_project);
CREATE INDEX IF NOT EXISTS grants_from_project_idx    ON grants (from_project);
CREATE INDEX IF NOT EXISTS tasks_to_user_state_idx    ON tasks (to_user, state);
CREATE INDEX IF NOT EXISTS tasks_artifact_idx         ON tasks (artifact);

-- The event filter asks one more question since Phase 4: is this event's thread
-- a thread some task of mine opened. That is a lookup by thread on every read of
-- the log, so it gets its own index.
CREATE INDEX IF NOT EXISTS tasks_thread_idx           ON tasks (thread);
CREATE INDEX IF NOT EXISTS tasks_from_user_idx        ON tasks (from_user);
CREATE INDEX IF NOT EXISTS tasks_assignee_agent_idx   ON tasks (assignee_agent);

-- A status trail is read by artifact and by type: "every status move on this
-- bug, in order".
CREATE INDEX IF NOT EXISTS events_artifact_type_idx   ON events (artifact, type);

-- The permission filter asks two questions of grants on every read: is there a
-- project-wide grant along this edge, and is this one artifact shared with this
-- one user. One index each.
CREATE INDEX IF NOT EXISTS grants_edge_idx            ON grants (from_project, to_project);
CREATE INDEX IF NOT EXISTS grants_artifact_subject_idx ON grants (artifact, subject);

CREATE INDEX IF NOT EXISTS tokens_user_idx            ON tokens (user_id);
CREATE INDEX IF NOT EXISTS tokens_agent_idx           ON tokens (agent_id);

-- The drainer asks two questions and nothing else: what is still pending, in
-- the order it was written, and what was the last thing applied for this
-- artifact. One index each. No partial index on applied IS NULL: a predicate
-- index is a property of the planner rather than of the wire, and the queue is
-- short enough that the plain one is the same answer.
CREATE INDEX IF NOT EXISTS fs_intents_applied_idx     ON fs_intents (applied, created);
CREATE INDEX IF NOT EXISTS fs_intents_artifact_idx    ON fs_intents (artifact, applied);

-- Phase 10. The registry adapts to the data, and never the other way round.
--
-- Rows written before this table existed already name pa, pb, pc and flowy, and
-- those rows stay valid: `project` is inside the signed payload, so rewriting a
-- project column to fit a registry would produce rows whose signatures no longer
-- verify - forged rows, by this node's own definition. So the back-fill reads
-- the names the data already carries and declares them.
--
-- It runs here rather than only in Go because the foreign key below cannot be
-- added to a table whose rows point at projects that have no row yet. The rows
-- it writes carry no signature and no reading: they are a local statement about
-- what is already here, and `flowy serve` adopts them on startup - stamping,
-- signing and dating each one under this node's key, which is what makes them
-- replicable. See store.BackfillProjects.
--
-- The fixture flag is not set here. Which names are the smoke seeder's fixtures
-- is one list, and it lives in Go beside the seeder that writes them
-- (store.FixtureProjects); a second copy of it in SQL is a second copy to
-- forget.
INSERT INTO projects (id, name, provenance)
SELECT name, name, 'backfill' FROM (
    SELECT DISTINCT project      AS name FROM artifacts   WHERE project      IS NOT NULL
    UNION SELECT DISTINCT project        FROM events      WHERE project      IS NOT NULL
    UNION SELECT DISTINCT project        FROM tasks       WHERE project      IS NOT NULL
    UNION SELECT DISTINCT project        FROM tokens      WHERE project      IS NOT NULL
    UNION SELECT DISTINCT project        FROM agents      WHERE project      IS NOT NULL
    UNION SELECT DISTINCT project        FROM fs_intents  WHERE project      IS NOT NULL
    UNION SELECT DISTINCT from_project   FROM grants      WHERE from_project IS NOT NULL
    UNION SELECT DISTINCT to_project     FROM grants      WHERE to_project   IS NOT NULL
) AS named
WHERE name <> '' AND name NOT IN (SELECT id FROM projects);

-- An absent project is NULL and not '', on the two local tables that carry one.
-- They mean the same thing to every reader - a principal with no home project -
-- and they do not mean the same thing to a foreign key, which would ask the
-- registry for a project called ''. Written before the key is added, because a
-- row like that is what would refuse it.
UPDATE tokens SET project = NULL WHERE project = '';
UPDATE agents SET project = NULL WHERE project = '';

-- The referential integrity, on the two tables that can carry it.
--
-- tokens and agents are local: they are this node's credentials, they carry no
-- hlc and no signature, and nothing replicates them. So a foreign key here is a
-- promise the database can keep - a token cannot be scoped to a project that
-- was never declared, which is the exact shape of the mistake this table
-- exists for.
--
-- artifacts, events, tasks and grants deliberately do NOT get one, and the
-- reason is federation rather than tidiness. Those rows arrive from peers, in
-- pages, in whatever order a page happens to carry, and a page can legitimately
-- hold an artifact whose project row the puller was never handed - a grant lets
-- a principal read into a project without being of it. A foreign key there
-- would turn that into a constraint error that fails the whole transaction,
-- rather than the counted, explained refusal the merge already has. The rule on
-- those tables is enforced where the writing happens instead: a LOCAL write
-- into an undeclared project is refused by the store, and a replicated row
-- naming a project this node has never heard of records that project as
-- `observed` rather than being dropped. See internal/store/projects.go.
--
-- DROP then ADD, because Postgres has no ADD CONSTRAINT IF NOT EXISTS and this
-- file is loaded again over a database that already has it. Both statements are
-- plain SQL: a DO block would be PL/pgSQL, which the header of this file
-- promises none of.
ALTER TABLE tokens DROP CONSTRAINT IF EXISTS tokens_project_fkey;
ALTER TABLE tokens ADD  CONSTRAINT tokens_project_fkey
    FOREIGN KEY (project) REFERENCES projects (id);
ALTER TABLE agents DROP CONSTRAINT IF EXISTS agents_project_fkey;
ALTER TABLE agents ADD  CONSTRAINT agents_project_fkey
    FOREIGN KEY (project) REFERENCES projects (id);

-- A database whose projects table predates the origin columns picks them up
-- here, like every other added column in this file. They are nullable and the
-- adoption in store.BackfillProjects fills them: a row with no origin is a
-- project this node has not yet said where it came from, and the derived form
-- is what it gets.
ALTER TABLE projects ADD COLUMN IF NOT EXISTS origin     text;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS superseded text[];
ALTER TABLE projects ADD COLUMN IF NOT EXISTS origin_at  timestamptz;

-- Enumeration is "what is here", by name; the fixture read is "and which of
-- these is demo data"; the origin read is the collision check, which asks
-- "who else claims this origin" on the way in.
CREATE INDEX IF NOT EXISTS projects_hlc_idx     ON projects (hlc);
CREATE INDEX IF NOT EXISTS projects_fixture_idx ON projects (fixture);
CREATE INDEX IF NOT EXISTS projects_origin_idx  ON projects (origin);

-- ------------------------------------------------------------------- SEARCH
-- Everything below this line is Postgres full text and is expected to be
-- deleted when the store moves to SereneDB and search becomes vector search.
-- Nothing above it depends on anything below it.
--
-- search covers title, body, discovery and tags, so a word that appears only in
-- the discovery of a bug still finds it. The node writes the column in the same
-- statement that writes the row (see internal/store.artifactSearchSQL); there
-- is no generated column and no trigger.
ALTER TABLE artifacts ADD COLUMN IF NOT EXISTS search tsvector;

CREATE INDEX IF NOT EXISTS artifacts_search_idx ON artifacts USING gin (search);

COMMIT;
