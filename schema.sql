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

-- Agents acting for a user. kind: claude|glm|opencode.
CREATE TABLE IF NOT EXISTS agents (
    id      text PRIMARY KEY,
    user_id text REFERENCES users (id),
    kind    text,
    project text,
    hlc     bigint,
    node    text
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
-- type: transcript|memory|chat|bug|feature|note
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
    id       text PRIMARY KEY,
    type     text,
    project  text,
    room     text,
    thread   text,
    parents  text[],
    actor    text,
    artifact text,
    seq_hlc  bigint,
    node     text,
    body     text,
    meta     jsonb,
    sig      bytea,
    created  timestamptz DEFAULT now()
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

CREATE INDEX IF NOT EXISTS events_thread_idx          ON events (thread);
CREATE INDEX IF NOT EXISTS events_seq_hlc_idx         ON events (seq_hlc);
CREATE INDEX IF NOT EXISTS events_project_type_idx    ON events (project, type);
CREATE INDEX IF NOT EXISTS events_artifact_idx        ON events (artifact);

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
