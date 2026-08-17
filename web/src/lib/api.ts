/**
 * The node's HTTP API, as the console sees it.
 *
 * Every call carries the bearer token the node resolves to a principal, so the
 * console never decides what anybody may read: it asks, and the permission
 * filter on the other end answers. A room that comes back empty is a room this
 * token cannot see, and that is the correct thing to render.
 */

const TOKEN_KEY = "flowy.token";

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
   * meta is where the node stamps who is speaking. actor_name is what they
   * were called when they said it, and it is optional in the strong sense:
   * every message said before the node recorded a name has none, so a reader
   * falls back to the actor id rather than drawing a gap.
   */
  meta?: { actor_kind?: "user" | "agent"; actor_user?: string; actor_name?: string };
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
 * Presence is the two rosters a room view wants. Members is who has spoken;
 * listeners is who holds a reader place. The node sees polling, not processes,
 * so a listener line says when a poll last started and whether one is in
 * flight - "polled 4s ago" is checkable, "online" would be a claim.
 */
export interface Presence {
  members: { actor: string; name: string; kind: string }[];
  listeners: {
    principal: string;
    project: string;
    reader: string;
    user_name: string;
    attached: boolean;
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
}

/**
 * ActivityItem is one line of the timeline: a turn, a log line, a message, a
 * steer, or a worklog entry. The worklog kind is read-only here - entries are
 * written with the worklog_append tool, which checks the artifact ids they
 * reference, and the post box below deliberately cannot write one.
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

  /** projects is the registry, narrowed to what this token may be shown. */
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

  say: (room: string, body: string, parents: string[] = [], thread?: string, to?: string) =>
    request<FlowyEvent>(`/api/chat/${encodeURIComponent(room)}/say`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ body, parents, ...(thread ? { thread } : {}), ...(to ? { to } : {}) }),
    }),

  inbox: (since = 0) => request<ChatPage>(`/api/inbox?since=${since}`),
  reports: () => request<{ artifacts: Artifact[] }>("/api/artifacts?type=report"),
  presence: () => request<Presence>("/api/presence"),
  /** Todos are memory artifacts of kind todo - the same store, filtered by
   * kind rather than by a type of their own, because a todo is a memory
   * item with work still in it. */
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
