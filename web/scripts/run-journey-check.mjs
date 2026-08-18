/**
 * THE RUN JOURNEY, in a real browser, against a runner on a SECOND ORIGIN.
 *
 *   node scripts/run-journey-check.mjs BASE_URL TOKEN PROJECT WITH_TREE WITHOUT_TREE
 *
 * The repro panel had every part of this already - a version box, a run
 * button, a per-version verdict table, a pausable log - and none of it could
 * work in a browser, because two things are only true across an origin
 * boundary and nothing in this repository had one:
 *
 *   THE TOKEN. web/src/lib/api.ts sent no Authorization to the runner, on the
 *   reasoning that a separate service is a separate audience.
 *   cmd/handoff-runner/http.go's authed() says the opposite in its own head
 *   comment - "THE TOKEN IS THE CALLER'S OWN" - and resolves the bearer
 *   against the same Postgres this node writes to, so that a run is recorded
 *   against whoever asked for it. Every route there but /healthz is behind it,
 *   so every call the panel made was a 401.
 *
 *   THE PREFLIGHT. POST /run carries a JSON content type and that token, so a
 *   browser sends OPTIONS first and will not send the real request unless it
 *   is answered. The runner had no OPTIONS route and set no CORS headers at
 *   all - see cmd/handoff-runner/cors.go and cors_test.go, which is that half.
 *
 * Neither is visible to `go test` (one process), to `vite build` (the types
 * agreed all along), or to repro-contract-check.mjs (fetch is stood in for, so
 * there is no origin and no browser enforcing anything). It took this.
 *
 * THE STAND-IN RUNNER IS A NODE SERVER IN THIS PROCESS, on a port of its own -
 * a different port is a different origin, which is the whole point. It speaks
 * cmd/handoff-runner/http.go's answers, demands the same bearer that binary
 * demands, and sets the CORS headers cors.go sets, so what is under test here
 * is the CONSOLE's half of the journey. The runner's own half is asserted in
 * Go. NOTHING IS EXECUTED AND NO CONTAINER IS STARTED by any of this: a real
 * repro run needs Docker, this check needs none, and it therefore runs
 * anywhere the rest of the gate does.
 *
 * THE FLOWS, as named in row 01M09S8NKM9MX3EMV869KA9RBB before any of this was
 * written, each with two arms - one reading cannot tell a rule being applied
 * from a rule that does not exist:
 *
 *   F1  A finding whose repro tree is attached offers a run control; one with
 *       no tree says there is nothing to run and offers none.
 *   F2  With no runner configured the panel asks for a base and offers no run;
 *       pasting one makes the version box and the run button appear.
 *   F3  Clicking run reaches the runner CARRYING THE READER'S TOKEN and a row
 *       appears for that version. The other arm is the same click against a
 *       runner that does not accept the token: the refusal is on screen and no
 *       row appears.
 *   F4  A verdict arrives without a reload, and only for the run it is about -
 *       a second run left queued still reads queued in the same glance.
 *   F5  The log follows a live run; paused, an appended line does NOT reach
 *       the pane; resumed, it catches up.
 *   F6  A refused run says WHY, in the runner's own words rather than a status
 *       line, and leaves no row and no log viewer behind.
 */

import { createServer } from "node:http";

import { chromium } from "playwright";

const [base, token, project, withTree, withoutTree] = process.argv.slice(2);
if (!base || !token || !project || !withTree || !withoutTree) {
  console.error(
    "usage: node scripts/run-journey-check.mjs BASE_URL TOKEN PROJECT WITH_TREE WITHOUT_TREE",
  );
  process.exit(2);
}

const failures = [];
const claim = (name, ok, detail) => {
  if (ok) return;
  failures.push(detail ? `${name}\n      ${detail}` : name);
};

// ---------------------------------------------------------------- stand-in

/**
 * What the stand-in runner will say next. Every flow below moves one field of
 * this and then asserts on the page, which is the only way to drive a panel
 * whose whole job is to report somebody else's state.
 */
const runner = {
  /** The bearer it accepts. Set to something else to drive F3's second arm. */
  token,
  linked: true,
  runs: [],
  logs: new Map(),
  /** When set, POST /run refuses with this sentence instead of queueing. */
  refuse: null,
  /** Every request it received, so the check can assert what was SENT. */
  calls: [],
  next: 1,
};

/** cors is what cmd/handoff-runner/cors.go sets, and the browser will discard
 * every answer without it - so the stand-in has to be a correct runner or
 * this check would only ever measure CORS. */
function cors(res, origin) {
  if (!origin) return;
  res.setHeader("Vary", "Origin");
  res.setHeader("Access-Control-Allow-Origin", origin);
  res.setHeader("Access-Control-Expose-Headers", "Content-Disposition");
}

const server = createServer((req, res) => {
  const origin = req.headers.origin ?? "";
  const url = new URL(req.url, "http://runner.invalid");
  const auth = req.headers.authorization ?? "";
  cors(res, origin);

  if (req.method === "OPTIONS") {
    res.setHeader("Access-Control-Allow-Methods", "GET, POST, OPTIONS");
    res.setHeader("Access-Control-Allow-Headers", "Authorization, Content-Type");
    res.writeHead(204).end();
    return;
  }

  runner.calls.push({ method: req.method, path: url.pathname, auth, origin });

  const json = (code, body) => {
    res.writeHead(code, { "Content-Type": "application/json" });
    res.end(JSON.stringify(body));
  };

  // The same door the real one has, in the same place: before anything is
  // read or queued. A console that sends no token gets this and nothing else.
  if (auth !== `Bearer ${runner.token}`) {
    json(401, { error: "unknown token" });
    return;
  }

  if (req.method === "GET" && url.pathname === "/runs") {
    json(200, { runs: runner.runs, linked: runner.linked });
    return;
  }
  if (req.method === "GET" && url.pathname === "/version") {
    json(200, {
      project: url.searchParams.get("project") ?? "",
      requested: url.searchParams.get("v") ?? "",
      sha: "0123456789abcdef",
      image: "stand-in:latest",
      binary_ready: true,
      buildable: true,
      source_build: false,
      note: "resolved by the stand-in runner",
      runnable: runner.linked,
    });
    return;
  }
  if (req.method === "GET" && url.pathname.startsWith("/run/")) {
    res.writeHead(200, { "Content-Type": "text/plain; charset=utf-8" });
    res.end(runner.logs.get(url.pathname.split("/")[2]) ?? "");
    return;
  }
  if (req.method === "POST" && url.pathname === "/run") {
    let body = "";
    req.on("data", (chunk) => {
      body += chunk;
    });
    req.on("end", () => {
      const { finding, version } = JSON.parse(body || "{}");
      if (runner.refuse) {
        // Exactly cmd/handoff-runner/handleRun's shape when it queued
        // nothing: 400, the reasons per finding, and NO top-level error key.
        json(400, { queued: [], refused: [{ finding, error: runner.refuse }], version });
        return;
      }
      const id = `stand-in-run-${runner.next++}`;
      runner.runs.push({
        id,
        finding,
        project,
        version,
        sha: "0123456789abcdef",
        status: "running",
        queued_at: Math.floor(Date.now() / 1000),
        started_at: Math.floor(Date.now() / 1000),
      });
      runner.logs.set(id, "docker build -f Dockerfile.repro .\nstep 1/4\n");
      json(202, { queued: [{ run: id, finding }], version });
    });
    return;
  }
  json(404, { error: `no such route: ${req.method} ${url.pathname}` });
});

await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
// Loopback by address, not by name: "localhost" and "127.0.0.1" are different
// origins to a browser, and the console is reached at the second.
const runnerBase = `http://127.0.0.1:${server.address().port}`;

// ------------------------------------------------------------------ driving

/** until polls for a condition, because everything here arrives on the
 * panel's own 2.5s and 1.2s beats rather than on a click. */
async function until(what, ms = 15_000) {
  const deadline = Date.now() + ms;
  for (;;) {
    if (await what()) return true;
    if (Date.now() > deadline) return false;
    await new Promise((r) => setTimeout(r, 200));
  }
}

const findingPage = (id) => `${base}/p/${project}/finding/${id}`;
/** What the open log pane holds, and "" when there is none - a missing pane
 * is one of the ways every F5 claim can fail, and throwing here would report
 * it as a broken check rather than as the finding it is. */
const logText = (where) =>
  where
    .locator("[data-repro-log-pane]")
    .innerText()
    .catch(() => "");

const browser = await chromium.launch();
try {
  const context = await browser.newContext({ viewport: { width: 1500, height: 1000 } });
  // The token only. NOT the runner base: F2's first arm is what an operator
  // who has never configured one sees, and seeding it would skip that.
  await context.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  const page = await context.newPage();
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  // Everything is looked up by a data attribute this panel alone carries, and
  // the two labelled boxes are scoped to the document rather than the whole
  // page. The console's sidebar has its own one-word buttons and its own
  // inputs, and a locator that matched both would fail in strict mode with a
  // message about the check rather than about the panel.
  const main = page.locator("main");
  const runButton = main.locator("[data-repro-run]");
  const versionBox = main.locator('input[aria-label="version"]');
  const baseBox = main.locator('input[aria-label="repro runner base URL"]');
  const saveBase = main.locator("[data-repro-base-save]");
  const runError = main.locator("[data-repro-error]");

  // ------------------------------------------------------------------- F2
  await page.goto(findingPage(withTree), { timeout: 20_000 }).catch(() => {});
  if (!(await until(async () => (await baseBox.count()) > 0))) {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    console.error(
      `the finding page never offered a runner base box.
Either the page did not paint, or ${withTree} carries no repro tree - in which
case the panel drew "nothing here to run" and this check is measuring the wrong
finding.${errors}`,
    );
    process.exit(1);
  }
  claim(
    "F2 with no runner configured, no run button is offered",
    (await runButton.count()) === 0,
    "a button whose every click can only fail is the defect this file exists for",
  );

  await baseBox.fill(runnerBase);
  await saveBase.click();
  claim(
    "F2 pasting a runner base brings up the run control",
    await until(async () => (await runButton.count()) > 0 && (await versionBox.count()) > 0),
    "the setup form was submitted and the panel body never appeared",
  );

  // ------------------------------------------------------------------- F1
  // The other arm, and the reason it is a second page rather than a second
  // assertion here: the base is now configured, so a finding with no run
  // control is one whose TREE is missing, which is the fact under test.
  await page.goto(findingPage(withoutTree), { timeout: 20_000 }).catch(() => {});
  claim(
    "F1 a finding with no repro tree says so",
    await until(async () => (await main.locator('[data-repro-tree="none"]').count()) > 0),
    "the panel drew something else for a finding there is nothing to run",
  );
  claim(
    "F1 and offers no run button, with a runner configured",
    (await runButton.count()) === 0,
    "the control is gated on the deployment and not on the finding",
  );

  await page.goto(findingPage(withTree), { timeout: 20_000 }).catch(() => {});
  await until(async () => (await runButton.count()) > 0);
  claim(
    "F1 a finding whose repro tree is attached offers one",
    (await runButton.count()) === 1,
    "so the absence above is about the tree",
  );

  // ---------------------------------------------------------------- F3 (b)
  // THE REFUSING ARM FIRST, while no run exists, so "no row appeared" is a
  // clean reading. Only the credential's validity changes: same page, same
  // click, same runner.
  runner.token = "a-token-this-runner-does-not-know";
  await versionBox.fill("8.8.8");
  await runButton.click();
  claim(
    "F3 a run the runner will not authenticate says so",
    await until(async () => (await runError.count()) > 0),
    "the click produced neither a run nor a word about why",
  );
  claim(
    "F3 and leaves no row behind",
    (await main.locator('tr[data-repro-version="8.8.8"]').count()) === 0,
    "a version row for a run that was never accepted",
  );

  // ---------------------------------------------------------------- F3 (a)
  runner.token = token;
  await versionBox.fill("latest");
  const before = runner.calls.length;
  await runButton.click();
  claim(
    "F3 clicking run reaches the runner",
    await until(async () => runner.calls.slice(before).some((c) => c.path === "/run")),
    `the runner saw ${runner.calls.length - before} calls after the click and none was POST /run`,
  );
  const posted = runner.calls.filter((c) => c.path === "/run").at(-1);
  claim(
    "F3 carrying the reader's own token",
    posted?.auth === `Bearer ${token}`,
    `the runner was sent Authorization: ${posted?.auth || "(nothing)"} - every route but /healthz is behind that header, so without it the whole panel is a 401`,
  );
  claim(
    "F3 from the console's origin, cross-origin as deployed",
    Boolean(posted?.origin) && posted.origin !== runnerBase,
    `origin was ${posted?.origin || "(none)"} against a runner at ${runnerBase}`,
  );
  claim(
    "F3 and the run it started appears",
    await until(async () => (await main.locator('tr[data-repro-version="latest"]').count()) > 0),
    "the runner accepted a run the panel never drew",
  );

  // --------------------------------------------------------------- F5, F4
  //
  // Both need a run that was actually accepted. When there is none there is no
  // log to follow, no verdict to land and nothing to pause, so this says so in
  // one line rather than raising off an undefined - a crash here would be
  // reported as a broken check, and it is not: it is the journey failing at
  // its first step, which the F3 claims above have already named.
  const started = runner.runs.at(-1) ?? null;
  const statusOf = (version) =>
    main
      .locator(`tr[data-repro-version="${version}"]`)
      .getAttribute("data-repro-status")
      .catch(() => null);

  if (!started) {
    failures.push(
      "F4 and F5 could not be measured at all: the runner accepted no run, so there is " +
        "no log to follow, no verdict to land and nothing to pause. See the F3 claims above " +
        "for why the run was never accepted.",
    );
  } else {
    // ----------------------------------------------------------------- F5
    claim(
      "F5 the log of the run just started is on screen",
      await until(async () => (await logText(main)).includes("docker build")),
      "clicking run opens the new run's log; it showed none",
    );
    runner.logs.set(started.id, `${runner.logs.get(started.id)}LINE-WHILE-FOLLOWING\n`);
    claim(
      "F5 a live log follows what the runner appends",
      await until(async () => (await logText(main)).includes("LINE-WHILE-FOLLOWING")),
      "the pane never picked up a line written after it opened",
    );

    const pause = main.locator("[data-repro-log-pause]");
    await pause.click();
    await until(async () => (await pause.getAttribute("data-paused").catch(() => null)) === "yes");
    runner.logs.set(started.id, `${runner.logs.get(started.id)}LINE-WHILE-PAUSED\n`);
    // Long enough for three of the 1.2s ticks a following pane would have taken.
    await new Promise((r) => setTimeout(r, 4000));
    claim(
      "F5 paused, an appended line does not reach the pane",
      !(await logText(main)).includes("LINE-WHILE-PAUSED"),
      "pause is drawn and does nothing, which is the difference this pair measures",
    );

    await pause.click();
    claim(
      "F5 and resuming catches up",
      await until(async () => (await logText(main)).includes("LINE-WHILE-PAUSED")),
      "resumed and never fetched again, so the pause was one-way",
    );

    // ----------------------------------------------------------------- F4
    // A second run for the same finding, at another version, left queued. It
    // is the arm that tells "the verdict arrived" from "every row repainted".
    runner.runs.push({
      id: "stand-in-waiting",
      finding: withTree,
      project,
      version: "9.9.9",
      status: "queued",
      queued_at: Math.floor(Date.now() / 1000),
    });
    started.status = "confirmed";
    started.confirmed = true;
    started.ended_at = Math.floor(Date.now() / 1000);
    claim(
      "F4 a verdict lands without the page being reloaded",
      await until(async () => (await statusOf("latest")) === "confirmed"),
      "the panel polls /runs every 2.5s and never redrew the run it started",
    );
    claim(
      "F4 and only for the run it is about",
      (await statusOf("9.9.9")) === "queued",
      `the run nobody has finished reads ${await statusOf("9.9.9")}, so "a verdict arrived" cannot be told from every row being repainted`,
    );
  }

  // ------------------------------------------------------------------- F6
  /** Which run the open log viewer is showing, or null when none is open.
   * Read rather than remembered, so "the refusal opened a viewer onto a run
   * that does not exist" is a comparison of two readings. */
  const openLogId = () =>
    main
      .locator("[data-repro-log-pane]")
      .getAttribute("data-repro-log-pane")
      .catch(() => null);
  const errorText = () => runError.innerText().catch(() => "");

  const openBefore = await openLogId();
  runner.refuse = "this runner is not configured for project serenedb";
  await versionBox.fill("7.7.7");
  await runButton.click();
  claim(
    "F6 a refused run says why in the runner's own words",
    await until(async () => (await errorText()).includes("not configured for project")),
    `the panel showed "${await errorText()}" - a 400 from this door carries its reasons per finding and no top-level error, so a client that reads the status shows the reader nothing they can act on`,
  );
  claim(
    "F6 and adds no row for a run that never started",
    (await main.locator('tr[data-repro-version="7.7.7"]').count()) === 0,
    "a phantom version row",
  );
  claim(
    "F6 and opens no log viewer for a run that does not exist",
    (await openLogId()) === openBefore,
    "the refusal moved the log viewer onto an id the runner never minted",
  );

  if (crashes.length) {
    failures.push(`the page threw:\n      ${crashes.join("\n      ")}`);
  }
} finally {
  await browser.close();
  server.close();
}

if (failures.length) {
  console.error(`the run journey does not work in a browser:\n  - ${failures.join("\n  - ")}`);
  process.exit(1);
}
console.log(
  `the run journey works across an origin: configured, started, followed, paused, verdicted and refused - ${runner.calls.length} calls to the stand-in runner`,
);
