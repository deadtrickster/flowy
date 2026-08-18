/**
 * A stand-in flowy node whose stream can be told to GO SILENT WITHOUT CLOSING.
 *
 *   node scripts/stream-standin.mjs DIST_DIR PORT beating|silent
 *
 * It exists because the real node cannot produce the failure being checked. A
 * correct node either heartbeats or hangs up, and both are easy to notice. The
 * case that matters is the third one: A CONNECTION THAT IS OPEN AND NOT
 * SPEAKING. To a client those bytes are identical to a quiet node - there is
 * nothing to read in either case, and a write only fails when somebody tries -
 * so a console that reads its clock off the last EVENT calls a dead stream
 * "quiet" indefinitely and shows a frozen list as though it were current.
 *
 * That is the same defect the todo panel was filed for, one layer down, which
 * is why the fixture is the node.
 *
 *   beating   hello, then a heartbeat twice a second, forever
 *   silent    hello, then nothing at all, and the socket stays open
 */

import { readFile } from "node:fs/promises";
import { createServer } from "node:http";
import { extname, join, normalize } from "node:path";

const DIST = process.argv[2];
const PORT = Number(process.argv[3] ?? 8903);
const MODE = process.argv[4] ?? "beating";

if (!DIST) {
  console.error("usage: node scripts/stream-standin.mjs DIST_DIR PORT beating|silent");
  process.exit(2);
}

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

// One todo, so the page has a queue to draw and the check is about the
// freshness mark rather than about an empty state.
const TODO = {
  id: "01M0STANDIN0000000000000001",
  type: "memory",
  kind: "todo",
  project: "flowy",
  title: "the one row this stand-in has",
  body: "OWNER: ?",
  status: "todo",
  fields: {},
  tags: [],
  created: "2026-01-01T00:00:00Z",
  updated: "2026-01-01T00:00:00Z",
};

createServer(async (req, res) => {
  const path = new URL(req.url, "http://x").pathname;

  if (path === "/api/stream") {
    res.writeHead(200, {
      "content-type": "text/event-stream",
      "cache-control": "no-cache",
      connection: "keep-alive",
    });
    // The opening message is written in BOTH modes. A stream that never said
    // anything at all would be caught by the page's connecting state, and the
    // case under test is a connection that greeted and then stopped - which is
    // the one that reads as healthy.
    res.write(`event: hello\ndata: {"at":"${new Date().toISOString()}","hlc":0}\n\n`);
    if (MODE === "beating") {
      const beat = setInterval(() => {
        res.write(`event: heartbeat\ndata: {"at":"${new Date().toISOString()}","hlc":0}\n\n`);
      }, 500);
      res.on("close", () => clearInterval(beat));
    }
    // silent: no timer, no close. The socket stays open and says nothing.
    return;
  }

  if (path === "/api/artifacts") return json(res, { artifacts: [TODO] });
  if (path === "/api/projects") return json(res, { projects: ["flowy"], reads: ["flowy"] });
  if (path === "/api/merge-queue") return json(res, { items: [], target_tip: "", tip_from: "none" });
  if (path === "/api/node") return json(res, { node: "standin", version: "standin", console: true });
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
  if (path.startsWith("/api/")) {
    return json(res, { artifacts: [], events: [], tasks: [], readers: [], cursor: 0, unread: 0 });
  }

  const rel = normalize(path).replace(/^(\.\.[/\\])+/, "");
  let file = join(DIST, rel);
  let body = await readFile(file).catch(() => null);
  if (body === null) {
    file = join(DIST, "index.html");
    body = await readFile(file).catch(() => null);
  }
  if (body === null) {
    res.writeHead(404);
    return res.end("no dist");
  }
  res.writeHead(200, { "content-type": MIME[extname(file)] ?? "application/octet-stream" });
  res.end(body);
}).listen(PORT, "127.0.0.1", () => process.stdout.write(`standin on ${PORT} (${MODE})\n`));
