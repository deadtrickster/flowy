/**
 * The merges pane says whether a gate is running.
 *
 *   node scripts/gate-state-check.mjs BASE_URL TOKEN
 *
 * THE OPERATOR, twice in one morning: "i have no idea what is going on, and i
 * have no idea about gate and merger status", then "I want to see gate status
 * and mergr status too here". The pane counted rows - "0 may land, 1 refused,
 * 4 queued" - which answers how many and never what is happening.
 *
 * WHY COUNTING ROWS CANNOT ANSWER IT. One gate runs at a time and a pass takes
 * about twelve minutes, so the queue spends nearly all its life with nothing
 * landable, CORRECTLY. A working drainer and a dead one draw the identical
 * pane, and every "is it stuck?" this evening was somebody looking at that with
 * no way to tell. The facts were already on the wire the whole time -
 * /api/merge-queue carries lock{held,holder_name,item,until,taken_at} - and
 * web/src/lib/api.ts simply did not declare the field, so it could not reach
 * the component.
 *
 * THE ASSERTION IS A DIFFERENCE, not a phrase. The same page is read twice,
 * once with the target free and once with a run declared against it, and the
 * two must not say the same thing. A pane that drew one line for both states -
 * which is what master does, by drawing none - cannot pass this by accident.
 * The words themselves are the console's to choose.
 *
 * AND THE ZERO TIME, which is the trap under this feature. `until` and
 * `taken_at` are on the wire even when nothing is held, as
 * `0001-01-01T00:00:00Z`: their Go tags say omitempty, and omitempty does not
 * omit a time.Time because a struct is never empty to encoding/json. That
 * string PARSES, so the obvious render shows a reader a deadline from the first
 * century. This check proves the trap is live on the wire and then requires the
 * pane not to fall into it.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/gate-state-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

const call = async (path, init = {}) => {
  const r = await fetch(`${base}${path}`, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
      ...init.headers,
    },
  });
  const text = await r.text();
  let body = text;
  try {
    body = JSON.parse(text);
  } catch {
    /* a refusal is a sentence */
  }
  return { ok: r.ok, status: r.status, body };
};

// NOTHING MAY BE HELD WHEN THIS STARTS. The check works by taking the landing
// lock, so a lock already held is both a fixture it cannot create and evidence
// that a real drainer is mid-pass on this node. Saying so beats timing out
// against somebody's live gate and reporting it as a console defect.
const before = await call("/api/merge-queue");
if (!before.ok) die(`/api/merge-queue answered ${before.status}`);
if (before.body.lock === undefined) {
  die(`this node's /api/merge-queue sends no lock at all, so the pane has nothing to draw.
That is the node, not the console - api_mergequeue.go sets response.Lock
unconditionally, as held:false when the target is free.`);
}
if (before.body.lock?.held) {
  die(`the landing lock is already held by ${before.body.lock.holder_name || before.body.lock.holder}.
This check takes the lock itself, so it cannot run beside a live drainer on the
same target. Nothing is wrong with the pane; try again when the gate is done.`);
}

// THE TRAP, MEASURED RATHER THAN ASSUMED. If a later node starts omitting these
// properly, the pane's guard stops mattering and this stops asserting it -
// rather than failing and sending somebody to look at a console that is fine.
const zeroTimeIsOnTheWire = String(before.body.lock?.until ?? "").startsWith("0001-");

const openPane = async (browser) => {
  const page = await browser.newPage({ viewport: { width: 1500, height: 950 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  // /todos/merge, not /todos: Todos.tsx keys the tab off the path, and a check
  // written against /todos reports "nothing drawn" about a pane that is not on
  // screen at all. That mistake cost a full pass earlier tonight on a different
  // row, and it fails identically with and without the fix - which is the
  // signature of a check asserting about the wrong screen.
  await page.goto(`${base}/todos/merge`, { timeout: 30_000 }).catch(() => {});
  const line = page.locator("[data-gate-state]");
  await line
    .first()
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if ((await line.count()) === 0) {
    await page.close();
    return { state: null, words: "", crashes };
  }
  const got = {
    state: await line.first().getAttribute("data-gate-state"),
    words: ((await line.first().textContent()) ?? "").replace(/\s+/g, " ").trim(),
    crashes,
  };
  await page.close();
  return got;
};

let row = null;
let declared = false;
const run = `gate-state-check-${Date.now()}`;
const browser = await chromium.launch();
try {
  // ARM ONE: nothing held.
  const free = await openPane(browser);
  if (free.crashes.length > 0)
    die(`the page threw with the target free: ${free.crashes.join("; ")}`);
  if (!free.state) {
    die(`the pane says nothing about the gate when the target is free.
"no gate is running" is a measurement a reader needs - it is the difference
between a queue that is idle and one that is blocked.`);
  }
  if (zeroTimeIsOnTheWire && /\b0001\b|\b1\/1\/1\b/.test(free.words)) {
    die(`the pane printed the zero time as a real one: ${JSON.stringify(free.words)}
lock.until arrives as 0001-01-01T00:00:00Z whenever nothing is held, because
omitempty does not omit a time.Time. Key off lock.held, never off whether the
stamp is present.`);
  }

  // ARM TWO: a run declared against a real row, which is what takes the lock.
  const made = await call("/api/artifacts", {
    method: "POST",
    body: JSON.stringify({
      type: "memory",
      kind: "merge",
      title: "gate-state-check seeded row",
      body: "seeded by gate-state-check",
      visibility: "project",
      fields: { branch: "gate-state-check/branch", target: "master" },
    }),
  });
  if (!made.ok)
    die(`could not file the seed row: HTTP ${made.status} ${JSON.stringify(made.body)}`);
  row = made.body.id;

  const said = await call(`/api/merge/${row}/gate`, {
    method: "POST",
    body: JSON.stringify({ run }),
  });
  if (!said.ok) {
    die(`could not declare a run on ${row}: HTTP ${said.status} ${JSON.stringify(said.body)}`);
  }
  declared = true;

  // WHAT THE NODE SAYS, before looking at pixels. If the declaration did not
  // take the lock there is nothing for the pane to draw, and this blames the
  // fixture rather than the console.
  const during = await call("/api/merge-queue");
  if (!during.ok) die(`/api/merge-queue answered ${during.status}`);
  if (!during.body.lock?.held) {
    die(`declaring a run did not take the landing lock, so the fixture never reached
the state this check is about: ${JSON.stringify(during.body.lock)}`);
  }

  const held = await openPane(browser);
  if (held.crashes.length > 0)
    die(`the page threw with a gate running: ${held.crashes.join("; ")}`);
  if (!held.state) die("the pane says nothing about the gate while a run holds the target");

  // THE DIFFERENCE. Two readings of one page, one fact changed between them.
  if (held.state === free.state) {
    die(`the pane draws a running gate and a free target the same way (${held.state}):
  free    ${JSON.stringify(free.words)}
  running ${JSON.stringify(held.words)}
This is the whole point of the line - a reader cannot tell a working drainer
from a stopped one.`);
  }
  if (held.words === free.words) {
    die(`the gate line reads identically in both states: ${JSON.stringify(held.words)}`);
  }

  // WHO AND WHAT FOR. "something is happening" is not actionable; "X is
  // measuring Y until T" is. The branch is what the reader can act on, so it is
  // required; the holder's name is required only when the node resolved one,
  // because a principal that maps to no user has none to print.
  if (!held.words.includes("gate-state-check/branch")) {
    die(
      `the pane says a gate is running but not what it is measuring: ${JSON.stringify(held.words)}`,
    );
  }
  const who = during.body.lock.holder_name;
  if (who && !held.words.includes(who)) {
    die(`the pane does not say who holds the target (${who}): ${JSON.stringify(held.words)}
"who do I talk to" is half of why the lock records a holder.`);
  }

  console.log(`the pane tells them apart: free=${free.state}, running=${held.state}`);
  console.log(`  running line: ${held.words}`);
} finally {
  await browser.close();
  // GIVE THE LOCK BACK FIRST. A declaration holds the target for fifteen
  // minutes, and this check runs inside a suite that gates other things: a
  // failing run that left the lock behind would fail everything after it for a
  // quarter of an hour, with a symptom pointing at innocent code. Abandon is
  // the door for exactly this - "give master back without landing".
  if (row && declared) {
    await call(`/api/merge/${row}/abandon`, {
      method: "POST",
      body: JSON.stringify({ reason: "gate-state-check is done with the target" }),
    });
  }
  if (row) {
    await call(`/api/artifact/${row}/status`, {
      method: "POST",
      body: JSON.stringify({ status: "abandoned" }),
    });
  }
}
