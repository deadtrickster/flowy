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
  meta?: { actor_kind?: "user" | "agent"; actor_user?: string };
  created: string;
}

/** ChatPage is what a room read or a long poll answers with. */
export interface ChatPage {
  room?: string;
  events: FlowyEvent[];
  since: number;
  cursor: number;
}

export interface Whoami {
  user: string;
  agent?: string;
  project?: string;
  operator?: boolean;
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

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: { ...authHeader(), ...(init.headers ?? {}) },
  });
  const text = await response.text();
  const body = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new ApiError(response.status, body?.error ?? `${response.status} ${response.statusText}`);
  }
  return body as T;
}

export const api = {
  whoami: () => request<Whoami>("/api/whoami"),

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

  say: (room: string, body: string, parents: string[] = [], thread?: string) =>
    request<FlowyEvent>(`/api/chat/${encodeURIComponent(room)}/say`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ body, parents, ...(thread ? { thread } : {}) }),
    }),

  inbox: (since = 0) => request<ChatPage>(`/api/inbox?since=${since}`),

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
};

/** isAgent reads the speaker's kind off the message the node stamped it with. */
export function isAgent(event: FlowyEvent) {
  return event.meta?.actor_kind === "agent";
}
