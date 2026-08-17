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

/** ChatPage is what a room read or a long poll answers with. */
export interface ChatPage {
  room?: string;
  events: FlowyEvent[];
  since: number;
  cursor: number;
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
   * authorship is what this node can say about the owner's claim to the words:
   * "authored" when a signature made with the owner's own key verified here,
   * "attributed" when it did not and the row rests on the word of whichever
   * node relayed it. See FlowyEvent.authorship - it is the same finding about
   * the other kind of row, and what an owner signs is what only an owner writes
   * (title, body, project, tags), so a party's status move does not disturb it.
   */
  authorship?: "authored" | "attributed";
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

/** The artifact types that have a lifecycle to move through. */
export const LIFECYCLE_TYPES = ["bug", "feature", "note", "task"];

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

function authHeader(): HeadersInit {
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

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
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

export const api = {
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
  declareInboxReader: (as: string) =>
    request<InboxReader>("/api/inbox/reader", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ as }),
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
  raiseTodo: (room: string, title: string, body = "", message?: string) =>
    request<{ item: Artifact; event: FlowyEvent }>(`/api/chat/${encodeURIComponent(room)}/todo`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title, body, ...(message ? { message } : {}) }),
    }),

  /**
   * Say who is carrying one of the room's todos. An empty name says nobody is.
   *
   * The room is in the path as well as the id because a panel edits its own
   * room's plan: the node refuses a todo that is not in this room, so a stale
   * id cannot write into another room's queue and announce it in this one.
   */
  assignTodo: (room: string, id: string, assignee: string) =>
    request<{ item: Artifact; event: FlowyEvent }>(
      `/api/chat/${encodeURIComponent(room)}/todo/${encodeURIComponent(id)}/assignee`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ assignee }),
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
};

/** isAgent reads the speaker's kind off the message the node stamped it with. */
export function isAgent(event: FlowyEvent) {
  return event.meta?.actor_kind === "agent";
}
