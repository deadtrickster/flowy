/**
 * What the console makes of the REPRO RUNNER'S OWN ANSWERS.
 *
 *   node scripts/repro-contract-check.mjs
 *
 * cmd/handoff-runner is a second binary on a second host, so nothing in this
 * repository's build makes the console and that door agree - a mismatch here is
 * a page that breaks in front of an operator, on a deployment neither `go test`
 * nor `vite build` can see. The two halves were written at the same time against
 * a seam agreed in the room, and they disagreed on all four routes:
 *
 *   GET  /runs     answers {"runs": [...], "linked": true|false}, and the
 *                  console read the body AS an array - so runs.filter threw and
 *                  the panel died on mount rather than showing a run history.
 *   GET  /runs     takes NO finding filter, and the console asked for one - an
 *                  ignored query parameter looks exactly like an honoured one,
 *                  so every other finding's runs were drawn under whichever
 *                  finding was open.
 *   POST /run      answers {"queued": [{run, finding}], "refused": [...]}, and
 *                  the console read `.id` off it - undefined, so it opened a log
 *                  viewer for a run that does not exist and showed nothing when
 *                  the runner had in fact refused and said why.
 *   GET  /version  wants a project when the runner holds more than one, and
 *                  reports `binary_ready` and `runnable`; the console sent no
 *                  project and read a `binary` path that is deliberately never
 *                  on the wire.
 *
 * So this drives the real client in web/src/lib/api.ts with fetch stood in for,
 * answering exactly what cmd/handoff-runner/http.go writes, and asserts both
 * what the client PARSES and what it ASKS FOR. The module is TypeScript, so it
 * goes through vite's own esbuild first - the same transform the bundle is built
 * with - and is imported as a data url. api-error-check.mjs is the same shape,
 * for the same reason.
 *
 * `linked`/`runnable` are checked hardest, because that pair is what decides
 * whether the panel offers a run button at all: a runner whose queue is not
 * wired in refuses /run by name, and a button that can only produce that
 * refusal teaches the reader nothing about their finding.
 */

import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { transformWithEsbuild } from "vite";

const here = dirname(fileURLToPath(import.meta.url));
const source = resolve(here, "..", "src", "lib", "api.ts");

const { code } = await transformWithEsbuild(await readFile(source, "utf8"), source, {
  loader: "ts",
  format: "esm",
  target: "node20",
});
const { api, setReproBase, setToken } = await import(
  `data:text/javascript;base64,${Buffer.from(code).toString("base64")}`
);

const failures = [];
const check = (name, ok, detail) => {
  if (ok) return;
  failures.push(detail ? `${name}: ${detail}` : name);
};

/** asked records every request the client makes, so the check can assert the
 * URL as well as the parse - half of these bugs were in what was sent. */
const asked = [];

/** answers stands in for fetch with one JSON body, the way the runner sends it. */
function answers(body, status = 200) {
  globalThis.fetch = async (url, init) => {
    asked.push({
      url: String(url),
      method: init?.method ?? "GET",
      headers: new Headers(init?.headers ?? {}),
    });
    const text = JSON.stringify(body);
    return {
      ok: status >= 200 && status < 300,
      status,
      statusText: "",
      headers: { get: () => null },
      async text() {
        return text;
      },
    };
  };
}

// localStorage is a browser's; the client falls back to a value held in the
// module when there is none, which is what setReproBase leaves behind.
setReproBase("http://runner.invalid:8099");

// GET /runs - the envelope, not an array, and no finding in the query.
answers({
  runs: [
    {
      id: "7",
      finding: "F1",
      project: "serenedb",
      version: "latest",
      sha: "abc123def456",
      status: "not-confirmed",
      confirmed: false,
      queued_at: 1_760_000_000,
      started_at: 1_760_000_010,
      ended_at: 1_760_000_200,
    },
    { id: "8", finding: "F2", version: "v0.9", status: "error", confirmed: null },
  ],
  linked: true,
});
const runs = await api.reproRuns();
check("GET /runs is read as an envelope", Array.isArray(runs?.runs), `got ${typeof runs?.runs}`);
check("with the linked flag beside the list", runs?.linked === true, JSON.stringify(runs?.linked));
check("and every run the door sent", runs?.runs?.length === 2, `got ${runs?.runs?.length}`);
check(
  "a run keeps the finding it is about",
  runs?.runs?.[0]?.finding === "F1",
  "without it the panel cannot narrow a list the door does not narrow",
);
check(
  "and its three timestamps",
  runs?.runs?.[0]?.ended_at === 1_760_000_200,
  JSON.stringify(runs?.runs?.[0]),
);
check(
  "an errored run's verdict stays absent rather than false",
  runs?.runs?.[1]?.confirmed !== false,
  "a broken sandbox reported as not-confirmed is a finding silently declared fixed",
);
check(
  "the runs door is asked for no finding it cannot filter by",
  !asked.at(-1)?.url.includes("finding="),
  asked.at(-1)?.url,
);

// A runner with its queue not linked in says so, and that is not an empty list.
answers({ runs: [], linked: false });
const unlinked = await api.reproRuns();
check(
  "an unlinked runner is distinguishable from an idle one",
  unlinked?.linked === false && Array.isArray(unlinked?.runs),
  JSON.stringify(unlinked),
);

// POST /run - what was queued and what was refused, per finding.
answers({ queued: [{ run: "11", finding: "F1" }], version: "latest" }, 202);
const started = await api.reproRun("F1", "latest");
check(
  "POST /run names the run it queued",
  started?.queued?.[0]?.run === "11",
  JSON.stringify(started),
);
check("and the version it queued it at", started?.version === "latest", JSON.stringify(started));
check("and it is a POST", asked.at(-1)?.method === "POST", asked.at(-1)?.method);

answers(
  {
    queued: [],
    refused: [{ finding: "F1", error: "finding F1 has no repro tree" }],
    version: "latest",
  },
  400,
);
// THE REASON HAS TO SURVIVE, not merely the failure. This used to assert only
// that something was thrown, which passed while the panel showed
// "400 Bad Request" over the runner's actual sentence - the door writes its
// reasons per finding in `refused` and puts no top-level `error` on that body,
// so a client that treats the status as the whole answer discards everything
// worth reading. See api.reproRun.
let refused = null;
let threw = null;
try {
  refused = await api.reproRun("F1", "latest");
} catch (err) {
  threw = err;
}
check("a refusal is not thrown away as a status line", threw === null, threw ? String(threw) : "");
check("a refused run queued nothing", refused?.queued?.length === 0, JSON.stringify(refused));
check(
  "and the runner's own reason is what the caller gets",
  refused?.refused?.[0]?.error === "finding F1 has no repro tree",
  JSON.stringify(refused),
);

// THE OTHER ARM: a 400 that is NOT a per-finding refusal still throws, or
// "carries reasons" would be indistinguishable from "never raises".
answers({ error: "bad request body: unexpected EOF" }, 400);
let bad = null;
try {
  await api.reproRun("F1", "latest");
} catch (err) {
  bad = err;
}
check(
  "a 400 with no refusals on it is still an error, carrying what it said",
  bad !== null && String(bad?.message ?? "").includes("unexpected EOF"),
  String(bad?.message ?? bad),
);

// GET /version - the project goes with the question, and the answer's two
// booleans are the ones the panel draws.
answers({
  project: "serenedb",
  requested: "latest",
  sha: "0123456789ab",
  image: "serenedb:latest",
  binary_ready: true,
  buildable: true,
  source_build: true,
  note: "resolved from origin/main",
  runnable: false,
});
const version = await api.reproVersion("serenedb", "latest");
check(
  "GET /version reports whether a binary is ready",
  version?.binary_ready === true,
  JSON.stringify(version),
);
check(
  "and whether this deployment can run at all",
  version?.runnable === false,
  "a runner that only packages must be able to say so",
);
check(
  "the project is in the query",
  asked.at(-1)?.url.includes("project=serenedb"),
  asked.at(-1)?.url,
);
check("and so is the version", asked.at(-1)?.url.includes("v=latest"), asked.at(-1)?.url);

// THE READER'S OWN TOKEN GOES TO THE RUNNER, on every route.
//
// It used to go to none of them, on the reasoning that the runner is a
// different service and so a different audience. cmd/handoff-runner/http.go's
// authed() says the opposite - "THE TOKEN IS THE CALLER'S OWN" - and resolves
// the bearer against the SAME Postgres this console's node writes to, so that
// a run is recorded against whoever asked for it. Every route there but
// /healthz is behind it, and the whole panel was therefore a 401.
//
// Two arms: with a token set the header is there and carries it; with the
// token cleared there is no header at all. One arm could not tell "sends the
// caller's token" from "sends a constant".
const authOf = () => asked.at(-1)?.headers?.get("authorization") ?? "";
setToken("tok-the-reader");
answers({ runs: [], linked: true });
await api.reproRuns();
check("GET /runs carries the reader's own token", authOf() === "Bearer tok-the-reader", authOf());
answers({ queued: [{ run: "12", finding: "F1" }], version: "latest" }, 202);
await api.reproRun("F1", "latest");
check("POST /run carries it too", authOf() === "Bearer tok-the-reader", authOf());
check(
  "and still declares its body as JSON",
  (asked.at(-1)?.headers?.get("content-type") ?? "").includes("application/json"),
  asked.at(-1)?.headers?.get("content-type") ?? "",
);
answers({}, 200);
await api.reproLog("12").catch(() => {});
check("and so does the log", authOf() === "Bearer tok-the-reader", authOf());

setToken("");
answers({ runs: [], linked: true });
await api.reproRuns();
check(
  "a reader with no token sends no bearer, rather than a constant one",
  authOf() === "",
  authOf(),
);

if (failures.length) {
  console.error(`the console does not speak the runner's answers:\n  ${failures.join("\n  ")}`);
  process.exit(1);
}
console.log("repro contract: /runs, /run and /version are read as cmd/handoff-runner writes them");
