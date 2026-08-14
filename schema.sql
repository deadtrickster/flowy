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
    tombstone    boolean DEFAULT false
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
    hlc        bigint,
    node       text,
    tombstone  boolean DEFAULT false,
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
    created  timestamptz DEFAULT now()
);

-- Handoffs. state: open|accepted|done|dropped.
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
    node           text
);

-- Replication bookmarks, one row per peer node.
CREATE TABLE IF NOT EXISTS peers (
    peer           text PRIMARY KEY,
    pull_cursor    bigint DEFAULT 0,
    pushed_cursor  bigint DEFAULT 0,
    last_seen      timestamptz
);

CREATE INDEX IF NOT EXISTS artifacts_project_type_idx ON artifacts (project, type);
CREATE INDEX IF NOT EXISTS artifacts_owner_idx        ON artifacts (owner_user);
CREATE INDEX IF NOT EXISTS artifacts_hlc_idx          ON artifacts (hlc);
CREATE INDEX IF NOT EXISTS artifacts_updated_idx      ON artifacts (updated);

-- Phase 2 stores shared memory as artifacts of type 'memory', narrowed by kind.
-- The column is in the CREATE TABLE above; the ALTER is here so a database that
-- was created by an earlier phase picks it up on the next load.
ALTER TABLE artifacts ADD COLUMN IF NOT EXISTS kind text;

CREATE INDEX IF NOT EXISTS artifacts_type_kind_idx    ON artifacts (type, kind);

CREATE INDEX IF NOT EXISTS events_thread_idx          ON events (thread);
CREATE INDEX IF NOT EXISTS events_seq_hlc_idx         ON events (seq_hlc);
CREATE INDEX IF NOT EXISTS events_project_type_idx    ON events (project, type);
CREATE INDEX IF NOT EXISTS events_artifact_idx        ON events (artifact);

CREATE INDEX IF NOT EXISTS agents_user_idx            ON agents (user_id);
CREATE INDEX IF NOT EXISTS grants_to_project_idx      ON grants (to_project);
CREATE INDEX IF NOT EXISTS grants_from_project_idx    ON grants (from_project);
CREATE INDEX IF NOT EXISTS tasks_to_user_state_idx    ON tasks (to_user, state);
CREATE INDEX IF NOT EXISTS tasks_artifact_idx         ON tasks (artifact);

-- The permission filter asks two questions of grants on every read: is there a
-- project-wide grant along this edge, and is this one artifact shared with this
-- one user. One index each.
CREATE INDEX IF NOT EXISTS grants_edge_idx            ON grants (from_project, to_project);
CREATE INDEX IF NOT EXISTS grants_artifact_subject_idx ON grants (artifact, subject);

CREATE INDEX IF NOT EXISTS tokens_user_idx            ON tokens (user_id);
CREATE INDEX IF NOT EXISTS tokens_agent_idx           ON tokens (agent_id);

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
