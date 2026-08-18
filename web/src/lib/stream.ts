/**
 * ONE EVENT STREAM PER TAB, instead of one poll per panel.
 *
 * Measured before it was built: an idle tab on /chat/general made about thirty
 * requests a minute across six independent clocks, and the queue page made
 * none - so every claim and reassignment an agent made was invisible to a
 * person until they pressed F5. That is what this replaces.
 *
 * WHAT ARRIVES IS AN ENVELOPE, NEVER A ROW:
 *
 *     {topic: "todos", hlc: 117..., artifact: "01M...", type: "todo.assign"}
 *
 * A subscriber is told THAT its topic moved and re-reads the list it already
 * knows how to read. Everything hard about a streaming client falls out of
 * that: a duplicate is a wasted read, an out-of-order delivery is invisible,
 * and NOTHING PARTIAL IS EVER APPLIED over a row somebody is editing. The
 * alternative - applying deltas - would have to reconcile a half-row against a
 * cell with a caret in it, which is the case the operator hit and called
 * hostile.
 *
 * THE CONNECTION IS SHARED. One per tab is the whole point, so subscribers
 * register here and the socket is opened by the first and closed by the last.
 * Two panels asking for `todos` is one subscription and one connection.
 *
 * THE CLIENT IS A LIBRARY, not a hand-rolled reader. Native EventSource cannot
 * send an Authorization header and every door on this node takes a bearer
 * token; a token in the query string lands in access logs and in this node's
 * own parameter-guard refusal text. `eventsource` takes a `fetch` override,
 * which is the sanctioned way to attach the header, and brings the parts
 * everybody gets wrong by hand: reconnect with backoff, and Last-Event-ID
 * resume so the gap after a disconnect is closed by the server rather than
 * guessed at here.
 */

import { EventSource } from "eventsource";

import { authHeader } from "@/lib/api";

/** The topics the node serves. It refuses anything else by name - see
 * streamTopics in stream.go - so this list is the client half of one fact. */
export type Topic = "todos" | "queue";

/**
 * How long without a word from the node before the connection is called stale.
 *
 * It is a multiple of the server's heartbeat (5s), not a guess. Two beats plus
 * change: one missed beat is a slow network, three is a connection that is not
 * coming back. Too tight and a busy laptop shows a warning that is not true;
 * too loose and the board claims to be current for a minute after it stopped.
 */
export const STALE_AFTER_MS = 15_000;

/**
 * A message from the node. `hello` and `heartbeat` carry no change and are the
 * whole reason the panel can tell quiet from dead; `change` is an envelope.
 */
type Envelope = { topic: Topic; hlc: number; artifact?: string; type: string; project?: string };

/** What a subscriber is handed when its topic moves. */
export type OnChange = (envelope: Envelope) => void;

/**
 * The health of the one connection, which is what the panel's "as of" reads.
 *
 * `lastHeard` IS THE HEARTBEAT'S CLOCK AND NOT THE LAST EVENT'S. This is the
 * single most important line in the file. A silent connection and a dead one
 * are byte-identical to a client until a write fails, so a panel whose clock
 * reads the last CHANGE freezes the moment the queue goes quiet - which is
 * indistinguishable from the node having gone away, and is precisely the defect
 * this whole piece of work was filed about. Every message from the node moves
 * this, including the ones that say nothing happened.
 */
export interface Health {
  /** Epoch ms of the last message of any kind, or null before the first. */
  lastHeard: number | null;
  /** Why the connection is unhappy, in words, or null when it is fine. */
  error: string | null;
  /** Whether a connection is currently open. */
  open: boolean;
}

type Watcher = { topics: Topic[]; onChange: OnChange };

const watchers = new Set<Watcher>();
const listeners = new Set<(health: Health) => void>();

let source: EventSource | null = null;
let health: Health = { lastHeard: null, error: null, open: false };

/** The topics anybody is currently watching, deduplicated and ordered so the
 * URL is stable - an unstable URL would reconnect for no reason. */
function wanted(): Topic[] {
  const all = new Set<Topic>();
  for (const w of watchers) for (const t of w.topics) all.add(t);
  return [...all].sort();
}

function publish(next: Partial<Health>) {
  health = { ...health, ...next };
  for (const listener of listeners) listener(health);
}

/** heard records that the node spoke, whatever it said. */
function heard() {
  publish({ lastHeard: Date.now(), error: null, open: true });
}

/**
 * open connects, or reconnects onto a different topic set.
 *
 * The library owns retry and resume: it reconnects with backoff by itself and
 * replays Last-Event-ID, so the server closes the gap from its own log rather
 * than this file trying to remember what it missed.
 */
function open() {
  const topics = wanted();
  if (topics.length === 0) {
    close();
    return;
  }
  const url = `/api/stream?topics=${encodeURIComponent(topics.join(","))}`;
  if (source?.url.endsWith(url)) return;
  close();

  const es = new EventSource(url, {
    // The auth header, which is the whole reason this is not native
    // EventSource. Read per request rather than captured, so a token pasted
    // after the page loaded is used by the next reconnect.
    fetch: (input, init) =>
      fetch(input, { ...init, headers: { ...init?.headers, ...authHeader() } }),
  });
  source = es;

  es.addEventListener("hello", () => heard());
  es.addEventListener("heartbeat", () => heard());
  es.addEventListener("change", (event) => {
    heard();
    let envelope: Envelope;
    try {
      envelope = JSON.parse(event.data);
    } catch {
      // A message this client cannot parse is a node ahead of this bundle. It
      // is not a reason to tear the connection down, and it is not a reason to
      // claim the board is current either - so it is simply not delivered.
      return;
    }
    for (const w of watchers) {
      if (w.topics.includes(envelope.topic)) w.onChange(envelope);
    }
  });
  es.addEventListener("error", (event: { code?: number; message?: string }) => {
    // The library retries by itself, so this reports rather than reconnects.
    // The code is what makes the report worth reading: 401 is a token the
    // operator has to fix and a dropped socket is one that fixes itself, and a
    // panel that said "not answering" for both would send somebody looking in
    // the wrong place.
    const code = event.code;
    publish({
      open: false,
      error:
        code === 401 || code === 403
          ? `the node refused the stream (${code}) - the token may have expired`
          : (event.message ?? "the stream is not connected"),
    });
  });
}

function close() {
  if (!source) return;
  source.close();
  source = null;
  publish({ open: false });
}

/**
 * watch subscribes to topics and returns its own unsubscribe.
 *
 * The connection is opened on the first watcher and closed after the last one
 * leaves, so a tab that navigates away from every live panel holds no socket.
 */
export function watch(topics: Topic[], onChange: OnChange): () => void {
  const w: Watcher = { topics, onChange };
  watchers.add(w);
  open();
  return () => {
    watchers.delete(w);
    if (watchers.size === 0) close();
    else open();
  };
}

/** onHealth reports the connection's state, starting with what it is now. */
export function onHealth(listener: (health: Health) => void): () => void {
  listeners.add(listener);
  listener(health);
  return () => {
    listeners.delete(listener);
  };
}

/** isStale says a reading is old enough that the board must stop claiming to be
 * current. Exported as a function of a reading rather than read off a clock in
 * here, so the component that draws the age and the rule that colours it can
 * never disagree. */
export function isStale(lastHeard: number | null, now: number = Date.now()): boolean {
  if (lastHeard === null) return false;
  return now - lastHeard > STALE_AFTER_MS;
}
