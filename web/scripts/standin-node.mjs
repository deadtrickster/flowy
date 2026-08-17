/**
 * A stand-in flowy node that answers the room poll INSTANTLY with a cursor that
 * never moves, and counts how often it is asked.
 *
 * It exists because the real node hides the bug it is here to catch. The watch
 * loop in ChatRoom.tsx flooded a node at 567 requests a second for as long as
 * anybody had the console open, and every check the gate had still passed: the
 * page rendered, the messages were right, the API was right. The symptom was a
 * REQUEST RATE, and nothing on a screen shows a request rate. A correctly
 * long-polling node makes the loop look perfect whether or not it is, because
 * the server's own 25s window paces a client that has no pacing of its own.
 *
 * So the node is the fixture. Answer every wait immediately, hand back the same
 * cursor forever, and a client that only advances its cursor when events arrive
 * spins as fast as the socket allows. One that enforces "a wait either blocked
 * or advanced" pauses instead. The two are 500x apart and the difference is a
 * number, which is the only form this failure has.
 *
 *   node scripts/standin-node.mjs DIST_DIR [PORT]
 *
 * GET /__waits answers {waits} so a caller can measure a window rather than a
 * total, and the page is served from DIST_DIR so what is under test is the
 * shipped bundle rather than a second build made for the occasion.
 */

import { readFile } from "node:fs/promises";
import { createServer } from "node:http";
import { extname, join, normalize } from "node:path";

const DIST = process.argv[2];
const PORT = Number(process.argv[3] ?? 8899);
// The console this stand-in claims to serve. Empty means "whatever the page is
// running", which the freshness check reads as nothing to do.
const BUNDLE = process.argv[4] ?? "";

if (!DIST) {
  console.error("usage: node scripts/standin-node.mjs DIST_DIR [PORT]");
  process.exit(2);
}

let waits = 0;
let loads = 0;

const EVENT = {
  id: "01M00000000000000000000001",
  type: "chat",
  project: "flowy",
  room: "general",
  thread: "01M00000000000000000000001",
  parents: [],
  actor: "01M0000000000000000000000A",
  actor_name: "standin",
  artifact: "",
  seq_hlc: 100,
  node: "standin",
  body: "the one message this node will ever have",
  created: "2026-01-01T00:00:00Z",
};

// The pathological answer, and the whole point: events present, cursor
// unchanged. A client that advances its cursor only when events arrive sets it
// to where it already was, hands back the same number, and is answered
// instantly again - forever.
const PAGE = { room: "general", events: [EVENT], since: 0, cursor: 100 };

const MIME = {
  ".html": "text/html",
  ".js": "text/javascript",
  ".css": "text/css",
  ".svg": "image/svg+xml",
  ".json": "application/json",
  ".woff2": "font/woff2",
};

const json = (res, body) => {
  res.writeHead(200, { "content-type": "application/json" });
  res.end(JSON.stringify(body));
};

createServer(async (req, res) => {
  const path = new URL(req.url, "http://x").pathname;

  if (path === "/__waits") return json(res, { waits });
  // How many times the app has been loaded from scratch, which is how a caller
  // counts reloads without asking the page to report on itself - a page that
  // reloads in a loop cannot be trusted to keep count of its own loops.
  if (path === "/__loads") return json(res, { loads });
  // What this stand-in claims to be serving. Told from the command line so one
  // run can answer "the same console you are running" and another can answer
  // "a different one", which is the only difference the freshness check reads.
  if (path === "/api/node") {
    return json(res, { node: "standin", version: "standin", console: true, bundle: BUNDLE });
  }
  if (path.startsWith("/api/chat/") && path.endsWith("/wait")) {
    waits++;
    return json(res, PAGE);
  }
  if (path.startsWith("/api/chat/")) return json(res, PAGE);
  if (path === "/api/whoami") {
    return json(res, {
      user: "01M0000000000000000000000A",
      user_name: "standin-user",
      agent: "",
      agent_name: "",
      project: "flowy",
      node: "standin",
    });
  }
  if (path === "/api/presence") return json(res, { members: [], listeners: [] });
  if (path === "/api/announcements") return json(res, { announcements: [] });
  // Everything else the room asks for on mount. The shapes matter: a page that
  // throws on a missing field unmounts, and an unmounted page makes no requests
  // at all - which reads as a bounded loop and is the reason the check below
  // demands a witness that the room actually rendered.
  //
  // `readers` and `unread` are the unread badges' two: /api/inbox/readers is a
  // list to iterate and /api/inbox/unread is a number to draw.
  if (path.startsWith("/api/")) {
    return json(res, { artifacts: [], events: [], tasks: [], readers: [], cursor: 0, unread: 0 });
  }

  const rel = normalize(path).replace(/^(\.\.[/\\])+/, "");
  let file = join(DIST, rel);
  let body = await readFile(file).catch(() => null);
  if (body === null) {
    // The SPA fallback: /chat/general is a route, not a file.
    file = join(DIST, "index.html");
    body = await readFile(file).catch(() => null);
    if (body !== null) loads++;
  }
  if (body === null) {
    res.writeHead(404);
    return res.end("no dist");
  }
  res.writeHead(200, { "content-type": MIME[extname(file)] ?? "application/octet-stream" });
  res.end(body);
}).listen(PORT, "127.0.0.1", () => process.stdout.write(`standin on ${PORT}\n`));
