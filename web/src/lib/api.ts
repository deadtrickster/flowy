/**
 * The node's HTTP API, as the console sees it.
 *
 * Every call carries the bearer token the node resolves to a principal, so the
 * console never decides what anybody may read: it asks, and the permission
 * filter on the other end answers. A room that comes back empty is a room this
 * token cannot see, and that is the correct thing to render.
 */

const TOKEN_KEY = "flowy.token";

/**
 * Citation is what a message says it is about, resolved by the node for the
 * token that read it: the message it cites, and - only when this reader may
 * read that message - who was quoted and the words themselves.
 *
 * The words are DERIVED, not stored. The row carries a pointer and a byte span
 * into the cited body, and the node cuts the quote out of the signed source on
 * the way out - so a citation cannot misquote, and the console may draw it as
 * the quoted person's own words rather than as the citing author's account of
 * them. See citations.go.
 *
 * `readable` is never absent, and it is the field the console has to draw
 * rather than assume: rooms are scoped by project and the log is not, so a
 * reply is often in front of somebody whose source message is not. Then there
 * is no text, no actor and no name - the node hands over none of it - and the
 * honest thing on screen is that this quotes something out of reach.
 */
export interface Citation {
  message: string;
  whole: boolean;
  start?: number;
  end?: number;
  readable: boolean;
  actor?: string;
  name?: string;
  text?: string;
  /** The quote was cut at the node's cap, so it ends in an ellipsis. */
  truncated?: boolean;
}

/** FlowyEvent is one row of the append-only log. A chat message is one of these. */
export interface FlowyEvent {
  id: string;
  type: string;
  project: string | null;
  room: string;
  thread: string;
  parents: string[];
  actor: string;
  artifact: string;
  seq_hlc: number;
  node: string;
  body: string;
  /**
   * addressee is who the message is directed at, absent for the room. It
   * changes how a message is drawn and nothing about who may read it: an
   * addressed message is a room message, read by exactly the principals that
   * could read the room without it.
   */
  addressee?: string;
  /**
   * private says this is a direct message: the actor and the addressee are the
   * only two principals who can read it, decided by one clause in the node's
   * event filter over the row's own columns - no project, no room, an
   * addressee.
   *
   * The node derives it and the console draws it. Setting it here makes
   * nothing private: what decided whether this row arrived at all happened in
   * the database before the response was written.
   */
  private?: boolean;
  /**
   * meta is where the node stamps who is speaking. actor_name is what they
   * were called when they said it, and it is optional in the strong sense:
   * every message said before the node recorded a name has none, so a reader
   * falls back to the actor id rather than drawing a gap.
   *
   * mentions is the @names in the body that meant somebody, as the node
   * resolved them when the message was said: "name:id" pairs, space separated,
   * in the order they were written. The first of them is also the addressee -
   * see mentions.go. Optional in the same strong sense: absent on every
   * message that named nobody, which is every message written before mentions
   * existed.
   *
   * cite is the citation as the row records it - "<id>" for a whole message,
   * "<id>:<start>:<end>" for a span of one. The console does not read it: the
   * resolved `citation` below is the same fact with the permission filter
   * already applied, and a client deriving a quote from the raw pointer would
   * be deriving it from a message it may not be able to read.
   */
  meta?: {
    actor_kind?: "user" | "agent";
    actor_user?: string;
    actor_name?: string;
    mentions?: string;
    cite?: string;
    /** attachment ids a message carries, space separated, in the order named. */
    attachments?: string;
  };
  /** What this message is answering, as the node resolved it for this reader. */
  citation?: Citation;
  /**
   * authorship is what THIS node can say about who wrote the message, and it is
   * the node's finding rather than a claim on the row: "authored" means a
   * signature made with the actor's own key verified here, "attributed" means
   * it did not and the message rests on the word of whichever node relayed it.
   *
   * A message carries two signatures. The node's says which machine wrote the
   * bytes; the principal's says who the words are from. Pinning a peer's node
   * key is agreeing to carry what it relays - which is what federation is - and
   * was being read as agreeing to whatever it said about who wrote what, so a
   * pinned peer could put words in anybody's mouth and every reader here saw
   * them as that person's own.
   *
   * Most rows are attributed today, and that is honest rather than alarming: it
   * is what a store holds until principals have keys.
   */
  authorship?: "authored" | "attributed";
  created: string;
}

/**
 * NodeInfo is what the node says about itself. `bundle` is the hashed console
 * asset this binary embeds - the fingerprint a running tab compares itself
 * against to find out a deploy has happened underneath it.
 */
export interface NodeInfo {
  node: string;
  version: string;
  console: boolean;
  bundle: string;
}

/** PinEntry is one line of the log behind a room's strip. */
export interface PinEntry {
  message: string;
  verb: string;
  actor: string;
  actor_kind?: string;
  at: string;
  event: string;
}

/**
 * PinsView is a room's strip: the ids that are up, and the log they were folded
 * out of. `pinned` is in the order each message was FIRST pinned, so re-pinning
 * an old decision does not reshuffle the strip under a reader.
 */
export interface PinsView {
  room: string;
  pinned: string[];
  log: PinEntry[];
}

/** ChatPage is what a room read or a long poll answers with. */
export interface ChatPage {
  room?: string;
  events: FlowyEvent[];
  since: number;
  cursor: number;
  /**
   * before is the other end of a window, and only a backwards read carries it -
   * see api.roomWindow. It is the reading of the OLDEST message in the page,
   * strictly exclusive, so handing it back asks for the page before this one.
   * Absent on a forward read, which has nothing older to offer.
   */
  before?: number;
}

/**
 * InboxReader is one reader's place in the log, as the node holds it: the
 * label, and the last reading it has acknowledged. `acked_delivery` and
 * `acked_quiet` say why the mark last moved and the console does not draw
 * them, but they are on the row and dropping them here would make this type
 * disagree with the endpoint.
 */
export interface InboxReader {
  reader: string;
  cursor: number;
  acked_delivery: number;
  acked_quiet: number;
  created: string;
  updated: string;
}

/**
 * Presence is the two rosters a room view wants. Members is who has spoken;
 * listeners is who holds a reader place. The node sees polling, not processes,
 * so a listener line says when a poll last started and whether one is in
 * flight - "polled 4s ago" is checkable, "online" would be a claim.
 *
 * waiter_kind is what the listener said it is: "tracked" wakes somebody when
 * it hears, "forked" hears and wakes nobody, "unknown" has not said. It is the
 * only field here that answers the question a roster is actually read for, and
 * the node reports "unknown" rather than assuming, so it is never absent.
 *
 * state is what the seat is DOING, which is not the same question: "listening"
 * polled inside the window, "starting" has never polled and was declared a
 * moment ago, and "lost" is holding a poll that never ended and is older than
 * any poll can be - something armed a waiter there and it stopped. A lost row
 * is deliberately still on this list: the panel's job is to say a seat has gone
 * deaf, and dropping the row would have deleted the only record that it had.
 */
export interface Presence {
  members: { actor: string; name: string; kind: string }[];
  listeners: {
    principal: string;
    project: string;
    reader: string;
    user_name: string;
    attached: boolean;
    waiter_kind: string;
    state: string;
    last_poll_at?: string | null;
    updated: string;
  }[];
}

export interface Whoami {
  user: string;
  agent?: string;
  project?: string;
  operator?: boolean;
  /**
   * Where this token's writes land, as the registry sees it. project_fixture
   * is the one a person has to act on: a fixture is demo seed data and is
   * perfectly writable, so nothing refuses a write into one - which is exactly
   * why the console has to say it rather than leave the project as a word.
   */
  project_declared?: boolean;
  project_fixture?: boolean;
  project_origin?: string;
}

/** Project is one row of the registry every project column points at. */
export interface Project {
  id: string;
  name: string;
  created_by?: string;
  provenance: string;
  fixture: boolean;
  origin?: string;
  superseded?: string[];
}

export interface ProjectsPage {
  count: number;
  current?: string;
  current_is_fixture: boolean;
  projects: Project[];
  /**
   * The subset of `projects` whose rows this token can reach. It is shorter than
   * `projects` whenever a grant points the other way: the registry shows a name
   * on an edge in either direction, and reading only travels along one of them.
   * Anything that states how far a list reaches has to use this one.
   */
  reads?: string[];
}

/**
 * What a read could NOT hand over, and why.
 *
 * A refusal nobody sees is indistinguishable from success. The node refuses a row
 * that names a principal it holds a signing key for, at or after that key's
 * epoch, with no signature of theirs that verifies - see FlowyEvent.authorship -
 * and until this existed the only party told was the peer that pushed it. On this
 * side the row simply was not there, and a queue read handed back a shorter list
 * that reads as "that is all the work there is".
 *
 * So it is the count AND the reason, together, and it is absent rather than zero
 * when there is nothing to say. A page with a `withheld` of 0 on it every day is
 * a page nobody reads the day it says 3.
 */
export interface Withheld {
  rows: number;
  reason: string;
}

/**
 * The authorship claims this node refused for GOOD, and why.
 *
 * `Withheld` above is what is missing right now, and it clears the moment the row
 * turns up properly signed. This is the decision behind it, and it outlives the
 * rule that made it: a claim the node refused is refused again on sight, without
 * being re-judged against whatever the rule has become since. Before that, a
 * refusal was only a delay - the peer went on offering the row, and the next
 * change that widened what this node takes let it in.
 *
 * Claims and not rows, deliberately. What is terminal is one unbacked assertion
 * that a named person wrote this, not the row and not the person: the same words
 * carrying that person's own signature are a different claim and they land. So a
 * row can be counted here and be present in the list beside it, and both numbers
 * are true.
 */
export interface Refused {
  claims: number;
  reason: string;
}

/**
 * One merge request as the node reports it, flat.
 *
 * Flat because every reader that has had to dig a value out of `fields` got it
 * wrong at least once - status read from the wrong level, an owner read from
 * body text. The node knows where these live; the page should not have to.
 *
 * `admissible` is absent, not false, when no tip was stated. Absent means "not
 * decided"; false means "decided, and no". Collapsing those two is how a page
 * ends up drawing a green light because nobody asked the question.
 */
export interface MergeRequest {
  id: string;
  title: string;
  project?: string;
  branch: string;
  target: string;
  gated_tip: string;
  gate_run: string;
  status: string;
  assignee?: string;
  admissible?: boolean;
  reason?: string;
  /** True while a run is measuring this branch and has not reported yet. */
  gating: boolean;
  /**
   * The row somebody already wrote about this refusal, when there is one.
   *
   * It arrives attached to the refusal because that is the whole point: the
   * reader is looking at a no they did not expect, and this is the moment they
   * would otherwise start diagnosing it from scratch. `ref` is project/type/id -
   * the console's own route - and is absent for a row personal to its author,
   * which no route reaches.
   */
  known_issue?: KnownIssue;
}

export interface KnownIssue {
  code: string;
  id: string;
  title: string;
  ref?: string;
}

/**
 * NoteEntry is one thing that was LEARNED about a row after it was filed: the
 * text, who wrote it and when.
 *
 * A note is not an edit and this type is where that shows up on the wire. The
 * words are somebody else's, sitting beside the author's body rather than
 * replacing it, and nothing here can be rewritten or deleted - the node has no
 * verb for either, so a note that turned out to be wrong is answered by a
 * further note saying so. See internal/store/todonote.go, which is where the
 * rule lives and is not repeated over here.
 *
 * actor_kind and actor_user are both carried because "the agent that did the
 * work measured this" and "the operator says this" are the two things a reader
 * of a note is telling apart, and the seat alone does not separate them.
 */
export interface NoteEntry {
  id: string;
  type: string;
  todo: string;
  note: string;
  actor: string;
  actor_kind?: string;
  actor_user?: string;
  seq_hlc: number;
  node: string;
  created: string;
}

export interface Artifact {
  id: string;
  type: string;
  kind?: string;
  project: string | null;
  owner_user: string;
  title: string;
  body: string;
  discovery: string;
  status: string;
  severity: string;
  tags: string[] | null;
  user_tags: string[] | null;
  related: string[] | null;
  visibility: string;
  file_path: string;
  hlc: number;
  node: string;
  tombstone: boolean;
  created: string;
  updated: string;
  /** fields is jsonb the node signs with the row; reports keep as_of and
   * supersedes there, announcements their scope. unknown because each type
   * owns its own shape - narrow at the use site. */
  fields?: unknown;
  /**
   * replaced_by is the newer artifact that supersedes this one. It is not
   * stored anywhere: supersedes points backwards from the replacement, and the
   * node turns that round on the way out, through the same permission filter
   * as the row itself. So it is here exactly when there is a replacement this
   * token may read - absent means either nothing replaced it or the thing that
   * did is out of reach, and the console cannot tell those apart on purpose.
   */
  replaced_by?: string;
  /**
   * replaced_by_ref is where that replacement lives - project/type/id, read off
   * the replacement's own row. It is here because replaced_by alone is not an
   * address: a replacement may sit in another project and may be another type,
   * and a console holding only the id has nothing to build a link out of but
   * the row it is already showing, which is the wrong row. Absent when the
   * replacement is personal to its author and so has no project - see refPath,
   * which is where that turns into no link rather than a broken one.
   */
  replaced_by_ref?: string;
  /**
   * authorship is what this node can say about the owner's claim to the words:
   * "authored" when a signature made with the owner's own key verified here,
   * "attributed" when it did not and the row rests on the word of whichever
   * node relayed it. See FlowyEvent.authorship - it is the same finding about
   * the other kind of row, and what an owner signs is what only an owner writes
   * (title, body, project, tags), so a party's status move does not disturb it.
   */
  authorship?: "authored" | "attributed";
  /**
   * notes is what has been learned about this row since it was filed, oldest
   * first, and it arrives from a SINGLE-ROW read only. The list reads do not
   * carry it on purpose - a queue of 200 rows with every note on each is a
   * different endpoint's answer - so absent here means "this read does not
   * carry notes" as often as it means "there are none". Only a page that read
   * one row may treat an absent list as an empty one.
   */
  notes?: NoteEntry[];
}

/**
 * refPath turns a reference the node sent - project/type/id - into the route
 * that shows it, and returns undefined for anything that is not one.
 *
 * Undefined rather than a best effort, because the caller's alternative to a
 * link is showing the id as text, and that is the better answer: a link built
 * out of two guessed segments looks exactly like a good one and lands on a row
 * that is not the one it named. Anything short of three non-empty segments is
 * not a route.
 *
 * The segments are encoded one at a time and joined after, not encoded whole:
 * a project name may hold a ? or a #, which the router would otherwise read as
 * the start of a query or a hash. It may never hold a /, which is what makes
 * splitting on one unambiguous.
 */
export function refPath(ref: string | undefined): string | undefined {
  const parts = (ref ?? "").split("/");
  if (parts.length !== 3 || parts.some((part) => part === "")) return undefined;
  return `/p/${parts.map(encodeURIComponent).join("/")}`;
}

/**
 * A repro run's status, as cmd/handoff-runner reports it.
 *
 * CONFIRMED, NOT-CONFIRMED AND ERROR ARE THREE DIFFERENT THINGS and ReproPanel
 * must never fold one into another. confirmed/not-confirmed are both verdicts
 * about the FINDING - the bug did or did not reproduce. error is a verdict
 * about the SANDBOX - the docker run broke before it could ask the question -
 * and drawing it the same way as not-confirmed is a finding silently declared
 * fixed because its own reproduction environment fell over.
 */
export type ReproStatus =
  | "queued"
  | "building"
  | "running"
  | "confirmed"
  | "not-confirmed"
  | "error";

/**
 * ReproRun is one row of a finding's repro history, as cmd/handoff-runner's Run
 * is written on the wire: GET /runs. The runner keeps every attempt rather than
 * the latest verdict, which is what lets the per-version table in ReproPanel
 * show a version going red after it was once green - see
 * internal/store/findingruns.go's own head comment for why that history is the
 * point.
 *
 * THE FIELDS ARE THE DOOR'S, not a shape this console would have chosen. It
 * carries `finding` because /runs answers with every run the process knows
 * about and the filtering is the caller's (see api.reproRuns), and its three
 * timestamps are unix seconds off that binary's own record rather than an `at`
 * string - a run has three interesting moments and which one matters depends on
 * where the run got to.
 *
 * confirmed is THREE-VALUED and must stay that way: true, false, and absent for
 * a run that has no verdict - queued, running, or ended in error. A reader that
 * flattened absent to false would be reporting a broken sandbox as a finding
 * that did not reproduce.
 */
export interface ReproRun {
  id: string;
  finding: string;
  project?: string;
  version: string;
  sha?: string;
  status: ReproStatus;
  confirmed?: boolean | null;
  note?: string;
  queued_at?: number;
  started_at?: number;
  ended_at?: number;
}

/**
 * ReproRuns is GET /runs' whole answer, and `linked` is the half that matters
 * as much as the list.
 *
 * A runner built without its run queue linked in answers every run route with a
 * refusal that names what is missing, and an empty `runs` from one of those is
 * indistinguishable from a runner nobody has asked to do anything yet. So the
 * door states which it is, and the panel draws a run button only when the
 * answer is yes - see cmd/handoff-runner/queue.go, which is where that word
 * comes from.
 */
export interface ReproRuns {
  runs: ReproRun[];
  linked: boolean;
}

/**
 * ReproQueued is POST /run's answer: what it accepted and what it turned down,
 * because a call naming several findings must not fail all of them over one,
 * nor drop the one silently. One finding queued reads as a one-entry list.
 */
export interface ReproQueued {
  queued: { run: string; finding: string }[];
  refused?: { finding: string; error: string }[];
  version: string;
}

/**
 * ReproVersion is what the runner can say about a version label - "latest", a
 * release tag, a branch, a bare sha - without running anything: GET /version.
 * buildable/source_build say whether asking for a run would need a build
 * first, so the panel can show that rather than let the reader find out from
 * a run that sits in "queued" for minutes.
 *
 * binary_ready is a BOOLEAN and not a path, deliberately, on the door's side:
 * whether a binary for this commit is already built is the caller's business,
 * and where it sits on that host is not. `runnable` is the same fact /runs
 * carries as `linked` - whether this deployment can run what it just resolved,
 * or only describe and package it.
 */
export interface ReproVersion {
  project: string;
  requested: string;
  sha: string;
  image: string;
  binary_ready: boolean;
  buildable: boolean;
  source_build: boolean;
  note: string;
  runnable: boolean;
}

/**
 * Task is one handoff: an artifact, the two people it is between, the thread
 * they talk in and where it got to. artifact_title is joined in by the node
 * through the same permission filter a direct read would use, so it is present
 * exactly when the share is live.
 */
export interface Task {
  id: string;
  artifact: string;
  from_user: string;
  to_user: string;
  project?: string;
  state: TaskState;
  assignee_agent?: string;
  thread: string;
  hlc: number;
  node: string;
  artifact_title?: string;
  artifact_type?: string;
}

export type TaskState = "open" | "delegated" | "done";

/** TaskMove is what /delegate and /state answer with: the row, and the event. */
export interface TaskMove {
  task: Task;
  event: FlowyEvent;
}

/** StatusMove is what a lifecycle transition answers with. */
export interface StatusMove {
  artifact: Artifact;
  event: FlowyEvent;
}

/**
 * History is an artifact's status trail. next is where the workflow allows it
 * to go from here - the console draws the dropdown from it rather than keeping
 * its own copy of the rules, which is how the two stay in agreement.
 */
export interface History {
  artifact: string;
  status: string;
  next: string[];
  events: FlowyEvent[];
}

export interface User {
  id: string;
  handle: string;
  display: string;
  auto_delegate: boolean;
  hlc: number;
  node: string;
}

/** The room assignment threads are opened in, on both sides of the wire. */
export const ASSIGN_ROOM = "handoffs";

/**
 * The page size the cross-project queue asks for, which is also the node's own
 * ceiling (store.maxLimit). Asking for it and knowing the number are the same
 * fact: a page that hit the cap has to say so, and it can only say so by
 * comparing what came back against what it asked for.
 */
export const TODO_PAGE = 1000;

/**
 * The artifact types that have a lifecycle to move through. finding is on
 * this list because lifecycle.go's lifecycleTypes puts it there - "a finding
 * behaves exactly like bug" - and a second, out-of-step copy of that set here
 * is exactly how StatusControl silently stops being offered on a type the
 * node still moves through open -> triaged -> ... -> done.
 */
export const LIFECYCLE_TYPES = ["bug", "feature", "note", "task", "finding"];

export interface NodeCounts {
  ok: boolean;
  node: string;
  version: string;
  db: string;
  hlc: number;
  uptime_ms: number;
  counts?: Record<string, number>;
}

/**
 * Availability is on every metric group: whether it was measured, and when it
 * was not, why.
 *
 * The console renders the reason wherever it would otherwise render a zero.
 * "0 artifacts" and "we could not read the artifacts" are different sentences,
 * and a dashboard that shows the first for the second is a dashboard that says
 * everything is fine when nothing was looked at.
 */
export interface Availability {
  available: boolean;
  reason?: string;
  measured?: string;
}

export interface MetricScope {
  user?: string;
  agent?: string;
  project?: string;
  operator: boolean;
  all: boolean;
  key: string;
}

export interface NodeGroup extends Availability {
  uptime_s?: number;
  started?: string;
  build?: string;
  db?: Availability & { up: boolean; engine: string; latency_ms: number; hlc: number };
  pool?: Availability & {
    in_use: number;
    idle: number;
    open: number;
    max_open: number;
    of: string;
  };
  cpu?: Availability & { core_share: number; of: string; window_s: number; cores: number };
  memory?: Availability & { rss_bytes: number; source: string };
  traces?: Availability & { kept: number; dropped: number; exporter?: string };
}

export interface CorpusGroup extends Availability {
  artifacts: number;
  events: number;
  by_type: Record<string, number>;
  by_scope: Record<string, number>;
  by_project: Record<string, number>;
  by_user: Record<string, number>;
  index: { artifacts: number; text_indexed: number; embedded: number } & Availability;
  storage: { tables_bytes: Record<string, number>; total_bytes: number } & Availability;
  growth: { artifacts_24h: number; artifacts_7d: number; events_24h: number; of: string };
  embedding: Availability & {
    embedded: number;
    bm25_only: number;
    denominator: number;
    of: string;
  };
}

export interface PeerMetrics {
  peer: string;
  pull_cursor: number;
  pushed_cursor: number;
  last_seen?: string;
  last_seen_age_s?: number;
  pending_push: number;
  conflicts: number;
  refused: number;
  applied: number;
}

export interface SyncGroup extends Availability {
  peers: PeerMetrics[];
  local_hwm: number;
  offline_queue: number;
  conflicts_total: number;
  pending_pull: Availability;
}

export interface CollabGroup extends Availability {
  messages_24h: number;
  messages_by_day: { day: string; count: number }[];
  tasks_by_state: Record<string, number>;
  open_todos: number;
  active_rooms_24h: number;
  active_users_24h: number;
  active_agents_24h: number;
  handoffs_in_flight: number;
  window: string;
}

export interface PermGroup extends Availability {
  grants: number;
  artifact_shares: number;
  cross_project_grants: number;
  tombstoned_grants: number;
  denied_24h: number;
  denied_by_status: Record<string, number>;
  window: string;
}

/** Anomaly is one series' verdict, with what the verdict rests on. */
export interface Anomaly {
  series: string;
  verdict: "normal" | "unusual" | "insufficient samples";
  latest: number;
  baseline?: number;
  sigma?: number;
  z?: number;
  samples: number;
  required: number;
  reason?: string;
}

export interface AnomaliesGroup extends Availability {
  min_samples: number;
  series: Anomaly[];
  unusual: number;
  insufficient: number;
  basis: string;
}

export interface Metrics {
  node: string;
  version: string;
  generated: string;
  scope: MetricScope;
  groups: {
    node?: NodeGroup;
    corpus?: CorpusGroup;
    sync?: SyncGroup;
    collaboration?: CollabGroup;
    permissions?: PermGroup;
    anomalies?: AnomaliesGroup;
  };
}

/** Span is one recorded operation, as the waterfall draws it. */
export interface Span {
  span_id: string;
  trace_id: string;
  parent_id?: string;
  name: string;
  kind: string;
  node: string;
  actor?: string;
  user?: string;
  project?: string;
  artifact?: string;
  status?: string;
  started: string;
  ended: string;
  duration_us: number;
  attrs?: Record<string, string>;
}

export interface Trace {
  trace_id: string;
  spans: Span[];
  nodes: string[];
  root?: string;
  started: string;
  ended: string;
  duration_us: number;
  errors: number;
}

export interface TraceSummary {
  trace_id: string;
  root: string;
  spans: number;
  nodes: string[];
  started: string;
  duration_us: number;
  errors: number;
}

/**
 * WorklogMeta is what a worklog entry says, as the node stamped it onto the
 * event. The timeline hands meta back verbatim, so the worklog view reads the
 * entry's own fields from there rather than parsing them back out of the body
 * the way a surface that knows nothing about the kind has to.
 *
 * Every field but what is optional in the strong sense: an entry that arrived
 * from a peer running a build older than the field has none, and a reader
 * drawing a gap for it would be inventing one.
 */
export interface WorklogMeta {
  what?: string;
  next?: string;
  as_of?: string;
  /** The branch or worktree the shift worked in, on the entries that name one. */
  branch?: string;
  refs?: string[];
  /**
   * subject is whose work the entry is about, on the entries written by one seat
   * about another's shift - a harness recording a run it drove, say. The actor is
   * still whoever wrote it. An entry with a subject is VOUCHED rather than
   * authored, and the difference has to reach the reader: an entry written by
   * something else about flowy-claude drawn as flowy-claude's own account is the
   * impersonation shape this fabric refuses everywhere, so the view says which
   * of the two it is. See isVouched.
   */
  subject?: string;
  /** The run the work was done in, and what the gate said about it. */
  run?: string;
  verify?: string;
}

/**
 * isVouched says an entry is one seat's report of another's work rather than
 * that seat's own account.
 *
 * Derived from the two ids rather than read off a flag, for the reason the node
 * derives it the same way: a subject equal to the actor is somebody's own account
 * however it got written, and two fields that can disagree about one fact is one
 * too many.
 */
export function isVouched(item: ActivityItem) {
  const subject = item.meta?.subject?.trim() ?? "";
  return subject !== "" && subject !== item.actor;
}

/**
 * ActivityItem is one line of the timeline: a turn, a log line, a message, a
 * steer, or a worklog entry. The worklog kind is read-only here - entries are
 * written with the worklog_append tool or POST /api/worklog, both of which check
 * the artifact ids they reference, and the post box below deliberately cannot
 * write one: it takes no refs and so could not check them.
 */
export interface ActivityItem {
  id: string;
  kind: "turn" | "log" | "chat" | "steer" | "worklog" | "activity";
  type: string;
  actor: string;
  actor_kind?: string;
  actor_user?: string;
  /** What the speaker was called, on the lines that carry one - see FlowyEvent. */
  actor_name?: string;
  project: string | null;
  room?: string;
  /** Who a message named, and whether the two of them were its only readers. */
  addressee?: string;
  private?: boolean;
  thread?: string;
  artifact?: string;
  parents: string[];
  body: string;
  trace?: string;
  seq_hlc: number;
  node: string;
  created: string;
  /** What the node stamped on the event. A worklog entry keeps its fields here. */
  meta?: WorklogMeta & { actor_kind?: string; actor_user?: string; actor_name?: string };
}

export interface ActivityPage {
  items: ActivityItem[];
  since: number;
  cursor: number;
  query: string;
}

/**
 * Announcement is an artifact of type 'announcement'. The scope, the resource
 * and the mode live in fields, which is a jsonb column the node signs as part
 * of the row - so what the banner reads is what the posting node wrote, even
 * when the announcement arrived through a peer.
 */
export interface AnnouncementFields {
  scope: "node" | "project" | "federation";
  resource?: string;
  mode?: "drain" | "pause" | "ack-required";
  resolved_at?: string;
}

export interface Announcement extends Artifact {
  fields?: AnnouncementFields;
}

/** Quiesce is what an announcement is still waiting for before it may resolve. */
export interface Quiesce {
  announcement: string;
  resource: string;
  mode: string;
  holders: string[];
  acked: string[];
  pending: string[];
  state: "held" | "released";
}

/** ApiError carries the status, because 401 and 404 mean different things to the UI. */
export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

export function getToken(): string {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? "";
  } catch {
    return "";
  }
}

export function setToken(token: string) {
  try {
    if (token) {
      localStorage.setItem(TOKEN_KEY, token);
    } else {
      localStorage.removeItem(TOKEN_KEY);
    }
  } catch {
    // A browser with storage switched off still gets a working console for the
    // length of the page: the token lives in memory either way.
  }
  memoryToken = token;
}

let memoryToken = "";

// Exported for lib/stream, which cannot go through `request` above: an SSE
// connection is a long-lived fetch the browser reconnects by itself, so it
// needs the header rather than the wrapper around it. One place decides what a
// flowy request carries, here, whichever door opens it.
export function authHeader(): HeadersInit {
  const token = getToken() || memoryToken;
  return token ? { Authorization: `Bearer ${token}` } : {};
}

/** statusText is what an error says when the body said nothing usable. */
function statusText(response: Response): string {
  return `${response.status} ${response.statusText}`.trim();
}

/**
 * parseBody reads the body as JSON, and turns a body that is not JSON into an
 * ApiError carrying the status.
 *
 * Not JSON at all is a proxy's HTML error page, or a plain-text 502 from
 * something standing in front of the node. Parsing it throws a SyntaxError that
 * says where the '<' was and nothing about what happened, and that is what the
 * console showed instead of the status. What is kept of the body is the first
 * of it - enough to recognise whatever sent it, short enough for a toast.
 */
function parseBody(text: string, response: Response) {
  if (!text) {
    return {};
  }
  try {
    return JSON.parse(text);
  } catch {
    throw new ApiError(response.status, text.trim().slice(0, 200) || statusText(response));
  }
}

// Exported so a module that owns its own corner of the API (see lib/diagrams)
// can reuse this door rather than build a second one. Everything that makes a
// request safe lives here - the token header, the body parse, the ApiError -
// and a second fetch wrapper is a second place for any of it to be missing.
export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: { ...authHeader(), ...(init.headers ?? {}) },
  });
  const text = await response.text();
  const body = parseBody(text, response);
  if (!response.ok) {
    throw new ApiError(response.status, body?.error ?? statusText(response));
  }
  return body as T;
}

const REPRO_BASE_KEY = "flowy.reproBase";

/**
 * The repro runner is not flowy. cmd/handoff-runner is a separate binary on a
 * separate trusted host with its own Docker access, so its base URL cannot be
 * "" (relative to this origin) the way every other call above is - a relative
 * /run would land on the flowy node, which does not serve it, and a 404 read
 * as "not confirmed" is exactly the confirmed/not-confirmed/error mixup this
 * panel exists to prevent.
 *
 * Kept as a RUNTIME setting in localStorage, the same way the token above is,
 * rather than a Vite build-time env var. web/dist is embedded into the flowy
 * binary with go:embed, so a build-time value would mean one flowy binary per
 * runner host - every deployment that wants repro runs baking its own
 * console. A runtime setting lets one build of the console be pointed at
 * whichever runner a given flowy deployment trusts, changed without a
 * rebuild, and left unset (see ReproPanel) on every deployment that has none.
 */
export function getReproBase(): string {
  try {
    return (localStorage.getItem(REPRO_BASE_KEY) ?? "").trim();
  } catch {
    return "";
  }
}

export function setReproBase(base: string) {
  const trimmed = base.trim().replace(/\/+$/, "");
  try {
    if (trimmed) {
      localStorage.setItem(REPRO_BASE_KEY, trimmed);
    } else {
      localStorage.removeItem(REPRO_BASE_KEY);
    }
  } catch {
    // As with the token: a browser with storage switched off still runs for
    // the length of the tab, just without the setting surviving a reload.
  }
  memoryReproBase = trimmed;
}

let memoryReproBase = "";

/**
 * Thrown by every repro call when no runner base is configured, so ReproPanel
 * can say so plainly rather than let a call fall through to a relative fetch
 * against flowy itself - see getReproBase above for why that would be worse
 * than doing nothing.
 */
export class ReproUnconfigured extends Error {
  constructor() {
    super("no repro runner configured");
    this.name = "ReproUnconfigured";
  }
}

function reproBase(): string {
  const base = getReproBase() || memoryReproBase;
  if (!base) throw new ReproUnconfigured();
  return base;
}

/** reproRequest is `request` for the runner's door: no flowy auth header (the
 * runner is a different service with a different audience for any token),
 * and a base that must be configured or nothing is sent. */
async function reproRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`${reproBase()}${path}`, init);
  const text = await response.text();
  const body = parseBody(text, response);
  if (!response.ok) {
    throw new ApiError(response.status, body?.error ?? statusText(response));
  }
  return body as T;
}

/** reproText is reproRequest for the one endpoint that answers in plain text
 * rather than JSON - the run log. */
async function reproText(path: string): Promise<string> {
  const response = await fetch(`${reproBase()}${path}`);
  const text = await response.text();
  if (!response.ok) {
    throw new ApiError(response.status, text.trim().slice(0, 200) || statusText(response));
  }
  return text;
}

/** A room as the node knows it, which is the only place that knows. */
export type Room = {
  project: string;
  name: string;
  topic?: string;
  created_by?: string;
  created: string;
  members: number;
  /** The caller's role here, empty when they are not a member. */
  role?: string;
  /**
   * False for a room that exists only because somebody spoke in it. It has no
   * owner, so nobody can be invited to it until it is created - the invite door
   * refuses and says so rather than behaving like a real room until somebody
   * tries.
   */
  declared: boolean;
};

export const api = {
  /**
   * The rooms this node has, rather than the three this file used to name.
   *
   * ROOMS in lib/unread.tsx was a literal array, so a room nobody had typed
   * into it could not appear in the sidebar however much traffic it carried,
   * and a room created through the API was invisible until somebody edited the
   * console. A client with a hardcoded list is always eventually wrong; one
   * that asks cannot be.
   */
  rooms: () => request<{ rooms: Room[] }>("/api/rooms"),

  whoami: () => request<Whoami>("/api/whoami"),

  /**
   * What this node is, and which console it serves. `bundle` is the hashed
   * asset its index.html loads, which is how a tab open across a deploy finds
   * out it is running code that has since been replaced.
   */
  node: () => request<NodeInfo>("/api/node"),

  /**
   * projects is the registry, narrowed to what this token may be shown - and
   * `reads` beside it is the narrower thing: the ones whose rows this token can
   * actually reach. See store.ReadableProjects for why they are not the same
   * list, and the todos page for what goes wrong if a scope line uses the wider
   * one.
   */
  projects: () => request<ProjectsPage>("/api/projects"),

  /** room reads a room from a cursor, exclusive. */
  room: (room: string, since = 0) =>
    request<ChatPage>(`/api/chat/${encodeURIComponent(room)}?since=${since}`),

  /**
   * roomWindow is the room read from its NEW end: the newest `limit` messages,
   * or the `limit` before a reading somebody has already got.
   *
   * It is what a room opens on. `room` above pages FORWARDS from a cursor, so
   * opening on it means taking the oldest page and dragging everything ever
   * said in the room in behind the long poll - reported by the operator as "on
   * reload the whole chat history loads". This takes the last screenful
   * instead, and `before` walks back from there as the reader scrolls up.
   *
   * The page comes back in log order, oldest first, like every other read - so
   * a view prepends it and never sorts. `before` in the answer is the reading
   * to ask for next, strictly exclusive and safe to hand back whole: the node
   * completes the reading its window cut at, so paging back neither repeats a
   * message nor skips one. Nothing older is left when a page comes back short
   * of its limit.
   */
  roomWindow: (room: string, limit: number, before = 0) =>
    request<ChatPage>(
      `/api/chat/${encodeURIComponent(room)}?order=recent&limit=${limit}${
        before > 0 ? `&before=${before}` : ""
      }`,
    ),

  /**
   * wait is the watcher: it blocks on the server for up to ~25s and returns
   * whatever landed, or nothing. The signal is what cancels it when the view
   * goes away, so a room the user has left stops holding a request open.
   */
  wait: (room: string, cursor: number, signal?: AbortSignal, thread?: string) =>
    request<ChatPage>(
      `/api/chat/${encodeURIComponent(room)}/wait?cursor=${cursor}${
        thread ? `&thread=${encodeURIComponent(thread)}` : ""
      }`,
      { signal },
    ),

  /**
   * cite is what the message is about: a message id, and the byte span into
   * its body when it quotes one part of it. The node checks both - that the
   * source is readable and that the span is inside it - and stamps the pointer
   * on the row. It never takes the quoted words from here, which is why there
   * is nowhere to put them.
   */
  say: (
    room: string,
    body: string,
    parents: string[] = [],
    thread?: string,
    to?: string,
    cite?: { message: string; start?: number; end?: number },
  ) =>
    request<FlowyEvent>(`/api/chat/${encodeURIComponent(room)}/say`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        body,
        parents,
        ...(thread ? { thread } : {}),
        ...(to ? { to } : {}),
        ...(cite ? { cite } : {}),
      }),
    }),

  /**
   * dms is the private log: every direct message this token is a party to,
   * whoever the other party is. There is no room in the path because a direct
   * message is not in one - that is part of what makes it private.
   */
  dms: (since = 0) => request<ChatPage>(`/api/dm?since=${since}`),

  /** dmWait is wait over the private log, with the same finite window. */
  dmWait: (cursor: number, signal?: AbortSignal) =>
    request<ChatPage>(`/api/dm/wait?cursor=${cursor}`, { signal }),

  /**
   * sendDm sends a message only the sender and `to` can read. The addressee is
   * the path and not an optional field: a private message with nobody to send
   * it to is the one mistake here that would publish something quietly.
   */
  sendDm: (to: string, body: string, parents: string[] = [], thread?: string) =>
    request<FlowyEvent>(`/api/dm/${encodeURIComponent(to)}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ body, parents, ...(thread ? { thread } : {}) }),
    }),

  inbox: (since = 0) => request<ChatPage>(`/api/inbox?since=${since}`),

  /**
   * Where this token's readers have got to. The console holds one per room and
   * reads them back on every refresh rather than remembering them in the tab:
   * the mark is the node's, and another tab - or the same person's other
   * browser - moves it.
   */
  inboxReaders: () => request<{ readers: InboxReader[] }>("/api/inbox/readers"),

  /**
   * How much one reader has not read in one room. THE NODE COUNTS IT, and that
   * is the point of the call: counting here would mean handing the reader's
   * mark back as a cursor, and a mark is a `seq_hlc` - 57 bits, held here as a
   * double, and therefore up to eight readings out. Measured: a console that
   * asked with the mark it had just been handed was answered with five
   * messages it had already read.
   */
  unreadIn: (as: string, room: string) =>
    request<{ reader: string; room: string; unread: number }>(
      `/api/inbox/unread?as=${encodeURIComponent(as)}&room=${encodeURIComponent(room)}`,
    ),

  /**
   * Declare a reader, at the head of what this token can already read. It is
   * idempotent - an existing label comes back where it stands - so the console
   * can call it for a room it has not read before without checking first.
   */
  declareInboxReader: (as: string, kind?: string) =>
    request<InboxReader>("/api/inbox/reader", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ as, kind }),
    }),

  /**
   * Move a reader's mark to the message it has read through. The node only
   * ever moves a mark forward, so two tabs acking different positions cannot
   * fight.
   *
   * THE MESSAGE AND NOT ITS READING, and that is not a preference. `seq_hlc`
   * is a 57-bit number and every number here is a double, so a reading this
   * console was handed comes back off `JSON.parse` up to eight readings away
   * from the one the node holds - measured, as a mark that landed two readings
   * short of the message the reader had just read and left it unread for good.
   * The id is a string and survives the trip; the node resolves it, through the
   * same read filter as any other id that arrives from outside.
   */
  ackInbox: (as: string, event: string) =>
    request<InboxReader>("/api/inbox/ack", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ as, event, delivered: true }),
    }),

  /**
   * Drop this token's own reader row. A console reader is a bookmark a tab
   * keeps, not a place in the log a process holds, and a tab that closed used
   * to leave its rows behind forever - polling never, kind unknown, the ghost
   * half of a roster that only ever grew. keepalive is for the one call that
   * matters on pagehide: the tab is going away as it is made, and the request
   * has to outlive it by a moment.
   */
  deleteInboxReader: (as: string, keepalive = false) =>
    request<{ deleted: string }>(`/api/inbox/reader/${encodeURIComponent(as)}`, {
      method: "DELETE",
      keepalive,
    }),

  reports: () => request<{ artifacts: Artifact[] }>("/api/artifacts?type=report"),
  attachment: (id: string) =>
    request<{ item: Artifact; content: string | null; bytes?: string }>(
      `/api/attachment/${encodeURIComponent(id)}`,
    ),
  /**
   * The node's ranked search, narrowed to reports.
   *
   * It is the node's and not the page's: the match covers the title, the body,
   * the discovery and the tags, and the list only ever renders titles. A filter
   * over what is already on screen would answer a different question and would
   * miss every report whose subject is in its text, which is most of them.
   */
  searchReports: (q: string) =>
    request<{ query: string; artifacts: Artifact[] }>(
      `/api/search?type=report&q=${encodeURIComponent(q)}`,
    ),

  /** findings/searchFindings are reports()/searchReports() over a different
   * type - the same permission-filtered door, so a findings list is
   * permission-filtered by construction rather than by anything this page
   * does. See Findings.tsx for the status/kind/severity/project/tag filters,
   * which narrow the artifacts these two already returned rather than
   * widening what either endpoint is asked for - ArtifactQuery has no
   * severity or tag column to ask it with. */
  findings: () => request<{ artifacts: Artifact[] }>("/api/artifacts?type=finding"),
  searchFindings: (q: string) =>
    request<{ query: string; artifacts: Artifact[] }>(
      `/api/search?type=finding&q=${encodeURIComponent(q)}`,
    ),

  presence: () => request<Presence>("/api/presence"),
  /** Todos are memory artifacts of kind todo - the same store, filtered by
   * kind rather than by a type of their own, because a todo is a memory
   * item with work still in it. */
  /**
   * Every todo this token can read, in every project it can read one in.
   *
   * It is the SAME endpoint the room panel uses with one narrowing dropped -
   * there is no project on the query, so the answer is whatever the permission
   * filter reaches. That filter is the only reason this is safe to widen: a
   * cross-project read through a door of its own would be a second place for it
   * to be missing, which is the shape of the finding this project already has
   * open. Widening what one query returns adds no door.
   *
   * The limit is the node's cap rather than the default 200, because a queue
   * across every project a fleet works in is exactly the list that quietly
   * stopped at 200 and read as "that was all of them". TODO_PAGE is exported
   * because the page has to know the number it asked for in order to say it
   * stopped there, and two copies of it would drift into a list that hits the
   * cap silently - which is the failure the number exists to report.
   */
  todos: () =>
    request<{ artifacts: Artifact[]; withheld?: Withheld; refused?: Refused }>(
      `/api/artifacts?type=memory&kind=todo&limit=${TODO_PAGE}`,
    ),
  /**
   * The merge queue, with a verdict per request.
   *
   * The verdicts come from the node, not from here. Whether a branch may land
   * is one rule - it compares the tip the gate measured against the tip the
   * merge would land on - and a second implementation of it in TypeScript would
   * be a second answer, disagreeing with the first on the day it matters.
   *
   * A browser has no git, so it cannot know where master is. The node answers
   * with `tip_from`: "stated" when a caller passed one, "deployed" when it fell
   * back to the commit the node was built from, "none" when it has neither.
   * `decided` says outright whether there are verdicts at all, so a page can
   * never read a missing verdict as a yes.
   */
  /**
   * The notes: memory items that are not work. The queue tabs show todos and
   * merges; nothing showed the rest, so a note could be written, searched by an
   * agent, and never seen by the person who asked for it - which is how the
   * operator ended up asking the room where a note had been filed.
   */
  notes: () =>
    request<{ artifacts: Artifact[] }>(`/api/artifacts?type=memory&kind=note&limit=${TODO_PAGE}`),
  /**
   * The merge queue of ONE ROOM: the merges that came out of this conversation.
   *
   * Same endpoint, same verdicts, narrowed by the room the request was raised
   * in - a narrowing, not a second kind of visibility, exactly as roomTodos is
   * to todos. What a room's pane shows is the work that belongs to the
   * conversation happening in it.
   */
  roomMergeQueue: (room: string) =>
    request<{
      target: string;
      target_tip: string;
      tip_from: "stated" | "deployed" | "none";
      decided: boolean;
      gating: number;
      items: MergeRequest[];
    }>(`/api/merge-queue?room=${encodeURIComponent(room)}`),
  mergeQueue: () =>
    request<{
      target: string;
      target_tip: string;
      tip_from: "stated" | "deployed" | "none";
      decided: boolean;
      gating: number;
      items: MergeRequest[];
    }>(`/api/merge-queue?limit=${TODO_PAGE}`),
  /**
   * The todos of one room: the same list, narrowed by the room the item was
   * raised in. It is the same endpoint and the same permission filter - room is
   * a narrowing beside type and kind, not a second kind of visibility - so a
   * todo that carries no room is absent here and present in `todos` above,
   * which is what makes this a filter rather than a move.
   */
  roomTodos: (room: string) =>
    request<{ artifacts: Artifact[] }>(
      `/api/artifacts?type=memory&kind=todo&room=${encodeURIComponent(room)}`,
    ),
  /**
   * Raise a todo out of a room. message is the id of the message it came out
   * of, and the node keeps it on the item: the plan says what to do and the
   * message says what was being talked about when somebody decided it had to
   * happen. The node writes the item and one message in the room together.
   */
  /**
   * A room's pinned strip: what is up, and the log it was folded out of.
   *
   * The log comes back with it because "who decided this was the decision" is
   * most of why a room pins anything, and a list of ids cannot answer it.
   */
  pins: (room: string) => request<PinsView>(`/api/chat/${encodeURIComponent(room)}/pins`),

  /**
   * Put a message up in the room it was said in. The room is in the path as
   * well as the id for assignTodo's reason: the node refuses a message that is
   * not in this room, so a stale id cannot put a line in a strip whose readers
   * cannot open it.
   */
  pin: (room: string, message: string) =>
    request<FlowyEvent>(`/api/chat/${encodeURIComponent(room)}/pin`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ message }),
    }),

  /** Take it down. The log still gains an entry - see the DELETE handler. */
  unpin: (room: string, message: string) =>
    request<FlowyEvent>(
      `/api/chat/${encodeURIComponent(room)}/pin/${encodeURIComponent(message)}`,
      { method: "DELETE" },
    ),

  raiseTodo: (room: string, title: string, body = "", message?: string, category = "") =>
    request<{ item: Artifact; event: FlowyEvent }>(`/api/chat/${encodeURIComponent(room)}/todo`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      // category is sent only when chosen: empty means unclassified, which is a
      // legal state and what most of the queue is, so sending "" would be
      // indistinguishable from choosing it and is left out instead.
      body: JSON.stringify({
        title,
        body,
        ...(message ? { message } : {}),
        ...(category ? { category } : {}),
      }),
    }),

  /**
   * Say who is carrying one of the room's todos. An empty name says nobody is.
   *
   * The room is in the path as well as the id because a panel edits its own
   * room's plan: the node refuses a todo that is not in this room, so a stale
   * id cannot write into another room's queue and announce it in this one.
   */
  /**
   * expect is who the caller read as carrying it when they opened the editor -
   * the cell's own text. The write is a claim against that reading, so a row
   * that changed hands underneath the editor is refused naming the winner
   * rather than overwritten, and a held row cannot be moved by a write that
   * never said whose it was.
   */
  assignTodo: (room: string, id: string, assignee: string, expect: string) =>
    request<{ item: Artifact; event: FlowyEvent }>(
      `/api/chat/${encodeURIComponent(room)}/todo/${encodeURIComponent(id)}/assignee`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ assignee, expect }),
      },
    ),

  artifact: (id: string) => request<Artifact>(`/api/artifact/${encodeURIComponent(id)}`),

  /** thread reads one thread of the log, whichever room it was said in. */
  thread: (thread: string) =>
    request<{ events: FlowyEvent[] }>(`/api/events?thread=${encodeURIComponent(thread)}`),

  /** tasks is the work waiting for this principal, newest first. */
  tasks: (state = "") =>
    request<{ tasks: Task[] }>(
      `/api/inbox/tasks${state ? `?state=${encodeURIComponent(state)}` : ""}`,
    ),

  task: (id: string) => request<Task>(`/api/task/${encodeURIComponent(id)}`),

  /** assign shares an artifact with somebody and hands them the work on it. */
  assign: (artifact: string, toUser: string, note?: string) =>
    request<Task>("/api/assign", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ artifact, to_user: toUser, ...(note ? { note } : {}) }),
    }),

  /** delegate hands a task to the assignee's agent. Only the assignee may. */
  delegate: (id: string, agent?: string) =>
    request<TaskMove>(`/api/task/${encodeURIComponent(id)}/delegate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(agent ? { agent } : {}),
    }),

  taskState: (id: string, state: TaskState) =>
    request<TaskMove>(`/api/task/${encodeURIComponent(id)}/state`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ state }),
    }),

  /** autoDelegate is the standing answer to inbound work: give it to my agent. */
  autoDelegate: (on: boolean) =>
    request<User>("/api/me/auto_delegate", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ on }),
    }),

  status: (id: string, status: string) =>
    request<StatusMove>(`/api/artifact/${encodeURIComponent(id)}/status`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ status }),
    }),

  history: (id: string) => request<History>(`/api/artifact/${encodeURIComponent(id)}/history`),

  /**
   * noteTodo attaches what somebody learned to a row: an append, not an edit,
   * so it takes no `saw` and is not refused because the work has started.
   *
   * The answer carries the row with every note on it, the one just written
   * included, which is why there is no read door beside this one. The page got
   * its notes from the single-row read it already does and gets them again from
   * here, out of the same permission-filtered read the node made - a second
   * fetch would be the same rows asked twice, with a window in between where
   * the two answers disagree. See todonote.go's viewNotes.
   */
  noteTodo: (id: string, note: string) =>
    request<{ item: Artifact; notes: NoteEntry[] }>(`/api/todo/${encodeURIComponent(id)}/note`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ note }),
    }),

  /**
   * health needs no token: it is the one thing the console can show logged out.
   * The counts are the exception - they come back only for the operator's
   * token, which request() sends when there is one - so a logged-out console
   * shows the node and its version and no tiles.
   */
  health: () => request<NodeCounts>("/healthz?counts=1"),

  /**
   * metrics is the whole set, scope-filtered by the token. all=true asks for
   * the node's own view and is answered that way only for the operator - for
   * anybody else it comes back as their own numbers, which the response's scope
   * block says plainly.
   */
  metrics: (all = false) => request<Metrics>(`/api/metrics${all ? "?scope=all" : ""}`),

  /** traces lists the recent traces this token may read. */
  traces: (all = false) =>
    request<{ node: string; traces: TraceSummary[] }>(`/api/traces${all ? "?scope=all" : ""}`),

  /** trace reads one trace: this node's spans of it, in start order. */
  trace: (id: string, all = false) =>
    request<{ node: string; trace: Trace }>(
      `/api/trace/${encodeURIComponent(id)}${all ? "?scope=all" : ""}`,
    ),

  /** activity reads the timeline: turns, logs, chat and steers, oldest first. */
  activity: (
    params: { q?: string; kind?: string; room?: string; thread?: string; order?: string } = {},
  ) => {
    const search = new URLSearchParams();
    for (const [key, value] of Object.entries(params)) {
      if (value) search.set(key, value);
    }
    const query = search.toString();
    return request<ActivityPage>(`/api/activity${query ? `?${query}` : ""}`);
  },

  /**
   * The worklog: the chronology, newest first.
   *
   * It is the timeline read narrowed to the one kind, and deliberately not an
   * endpoint of its own - the permission filter that decides which entries a
   * token may see is on /api/activity, and a second door onto the same rows is
   * a second place for that filter to be forgotten. order=recent is what makes
   * it the NEWEST entries rather than the first page of the oldest ones.
   */
  worklog: (q = "") => api.activity({ kind: "worklog", order: "recent", q }),

  /**
   * announcements is what the banner reads: the ones that are still active and
   * that this token may see. The node decides both - which is why the banner
   * has no filter of its own.
   */
  announcements: () => request<{ announcements: Announcement[] }>("/api/announcements"),

  /** quiesce is who an announcement is still waiting on. */
  quiesce: (id: string) => request<Quiesce>(`/api/announcement/${encodeURIComponent(id)}/quiesce`),

  /** ack says this principal has seen the announcement and is out of the way. */
  ack: (id: string) =>
    request<{ quiesce: Quiesce; event: FlowyEvent }>(
      `/api/announcement/${encodeURIComponent(id)}/ack`,
      { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" },
    ),

  /** post says something into the timeline: into a room, or into a run's thread. */
  postActivity: (post: { kind: string; body: string; room?: string; thread?: string }) =>
    request<ActivityItem>("/api/activity", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(post),
    }),

  /**
   * The repro runner's door - cmd/handoff-runner, not this node. Every one of
   * these throws ReproUnconfigured when no base is set; see reproBase above
   * and ReproPanel, which is the only caller and the only place that
   * decides what to show for it.
   */

  /** Enqueue a repro run of `finding` against `version`. The answer names what
   * was queued and what was refused rather than a bare id - see ReproQueued,
   * and ReproPanel, which shows the refusal instead of a run that never
   * started. */
  reproRun: (finding: string, version: string) =>
    reproRequest<ReproQueued>("/run", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ finding, version }),
    }),

  /**
   * Every run the runner knows about, and whether it can run anything at all.
   *
   * THERE IS NO PER-FINDING DOOR: GET /runs takes no filter and answers with
   * every run whose finding the caller may read, so narrowing to one finding is
   * this side's job and ReproPanel does it on `run.finding`. Asking for it in
   * the query string instead - which this call used to do - got every other
   * finding's runs back and drew them under whichever finding was open, because
   * an ignored query parameter looks exactly like an honoured one.
   */
  reproRuns: () => reproRequest<ReproRuns>("/runs"),

  /** One run's log, plain text. Polled, not streamed - see ReproPanel. */
  reproLog: (id: string) => reproText(`/run/${encodeURIComponent(id)}/log`),

  /**
   * A self-contained docker-compose repro package for one finding at one
   * version, as a downloadable blob. Not JSON on success, so this bypasses
   * reproRequest and reads the response itself - the filename comes off
   * Content-Disposition when the runner sends one, and a fallback name
   * otherwise so the download always has one.
   */
  reproPackage: async (finding: string, version: string) => {
    const response = await fetch(
      `${reproBase()}/package?finding=${encodeURIComponent(finding)}&version=${encodeURIComponent(version)}`,
      { cache: "no-store" },
    );
    if (!response.ok) {
      const text = await response.text();
      let message = text.trim().slice(0, 200) || statusText(response);
      try {
        message = JSON.parse(text)?.error ?? message;
      } catch {
        // text wasn't JSON either - the slice above is what there is to say.
      }
      throw new ApiError(response.status, message);
    }
    const blob = await response.blob();
    const disposition = response.headers.get("content-disposition") ?? "";
    const named = /filename="?([^";]+)"?/.exec(disposition)?.[1];
    return { blob, filename: named ?? `repro-${finding}-${version}.tgz` };
  },

  /**
   * What the runner can say about a version label without running anything.
   *
   * The project is named because a runner holds several - one checkout, one
   * base image and one cache each - and it can only guess when it happens to
   * hold exactly one. A finding knows whose code it is about, so the caller
   * passes it and gets "this runner is not configured for project X" instead of
   * "name a project" from a deployment that holds two.
   */
  reproVersion: (project: string | null | undefined, v: string) =>
    reproRequest<ReproVersion>(
      `/version?v=${encodeURIComponent(v)}${
        project ? `&project=${encodeURIComponent(project)}` : ""
      }`,
    ),
};

/** isAgent reads the speaker's kind off the message the node stamped it with. */
export function isAgent(event: FlowyEvent) {
  return event.meta?.actor_kind === "agent";
}
