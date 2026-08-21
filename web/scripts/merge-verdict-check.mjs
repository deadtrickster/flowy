/**
 * The merges pane tells three states apart: waiting, red, and landable.
 *
 *   node scripts/merge-verdict-check.mjs BASE_URL TOKEN
 *
 * THE OPERATOR read "0 may land, 4 refused" off a healthy queue and could not
 * tell it from four rejected branches. Of those four, one had a red and three
 * had simply never been gated against the current master - the node answers
 * admissible:false for all of them, and the pane drew one word.
 *
 * IT WENT WRONG TWICE, IN OPPOSITE DIRECTIONS, which is why this asserts three
 * states and not two:
 *
 *   before   every unmeasured row was drawn "refused"
 *   the fix  the refusal code turned ungated and stale_gate into "waiting"
 *   and      a RED row refuses with merge.ungated too - applyRed never writes
 *            gated_tip, deliberately, because a written tip is what
 *            MergeAdmissible reads as evidence FOR landing - so the first cut
 *            of the fix drew "we looked and it failed" as "waiting for the
 *            gate". mergered_test.go:50-59 asserts that code on master.
 *
 * So the check seeds all three, and requires the three BADGES TO DIFFER. Any
 * version that collapses a pair - in either direction - fails, and a pane that
 * happened to draw one word for everything cannot pass by accident.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/merge-verdict-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

/**
 * Stop, BEFORE anything is seeded. Exiting is right here: nothing is held.
 */
const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

/**
 * Stop, AFTER something is seeded - which is a different verb on purpose.
 * `process.exit()` does not run a `finally`, measured rather than assumed, so a
 * die() from inside the try skipped the teardown entirely and left three rows
 * and the landing lock behind. On exactly the failing run somebody repeats.
 * This throws so the finally runs, and the exit code is set there.
 */
class Died extends Error {}
const fail = (message, shown = "") => {
  throw new Died(shown ? `${message}\n${shown}` : message);
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

// The tip every row is judged against. Read from the node rather than invented:
// admissibility compares the row's gated base against THIS, so a made-up tip
// would make every row stale and the three states would be one again.
const queue = await call("/api/merge-queue");
if (!queue.ok) die(`/api/merge-queue answered ${queue.status}`);
const targetTip = queue.body.target_tip;
if (!targetTip) {
  die(`the queue states no target tip, so nothing here can be judged against one.
That is a fixture problem, not a console one.`);
}

/** file a merge row, the way `flowy merge open` does - /api/artifacts. */
const fileRow = async (name) => {
  const made = await call("/api/artifacts", {
    method: "POST",
    body: JSON.stringify({
      type: "memory",
      kind: "merge",
      title: `merge-verdict-check ${name}`,
      body: "seeded by merge-verdict-check",
      visibility: "project",
      fields: { branch: `merge-verdict-check/${name}`, target: "master" },
    }),
  });
  if (!made.ok)
    fail(`could not file the ${name} row: HTTP ${made.status} ${JSON.stringify(made.body)}`);
  return made.body.id;
};

/** declare a run against a row, which is also what takes the landing lock. */
const declare = async (id, run) => {
  const said = await call(`/api/merge/${id}/gate`, {
    method: "POST",
    body: JSON.stringify({ run }),
  });
  if (!said.ok) {
    fail(`could not declare a run on ${id}: HTTP ${said.status} ${JSON.stringify(said.body)}.
The lock is held by whoever is gating for real; this check cannot run beside a
live drainer on the same target.`);
  }
  holding.add(id);
};

/**
 * Rows this caller still holds the target for. A declaration takes the landing
 * lock; a RED gives it back inside the verdict (internal/store/mergegate.go,
 * "a red gives the target back"), a PASS deliberately does not, because in the
 * real drainer the land follows immediately and needs it. Nothing lands here,
 * so the green row's lock is this check's to return.
 */
const holding = new Set();

/** record a verdict - pass or red - for a run this caller declared. */
const verdict = async (id, run, result, note) => {
  const said = await call(`/api/merge/${id}/gate`, {
    method: "POST",
    body: JSON.stringify({ run, gated_tip: targetTip, result, note }),
  });
  if (!said.ok)
    fail(`could not record a ${result} on ${id}: HTTP ${said.status} ${JSON.stringify(said.body)}`);
  if (result === "red") holding.delete(id);
};

const seeded = [];
/** The check's own verdict, carried past the teardown the throw now runs. */
let failed = false;
/** Set when teardown could not give the target back, which is its own defect. */
let left = false;
try {
  // ONE ROW PER STATE, and each one is made the way the drainer makes it.
  const waiting = await fileRow("waiting");
  seeded.push(waiting);

  const red = await fileRow("red");
  seeded.push(red);
  await declare(red, "merge-verdict-check-red");
  await verdict(red, "merge-verdict-check-red", "red", "seeded: 1/1 the check that says no");

  const landable = await fileRow("landable");
  seeded.push(landable);
  await declare(landable, "merge-verdict-check-green");
  await verdict(landable, "merge-verdict-check-green", "pass", "seeded: all green");

  // WHAT THE NODE SAYS ABOUT EACH, before looking at any pixels. If the node
  // does not put them in three different states there is nothing for the pane
  // to tell apart, and this says so rather than blaming the console.
  const after = await call("/api/merge-queue");
  if (!after.ok) fail(`/api/merge-queue answered ${after.status}`);
  const rows = Object.fromEntries((after.body.items ?? []).map((i) => [i.id, i]));
  if (!rows[waiting] || !rows[red] || !rows[landable]) {
    fail("a seeded row is missing from the queue, so the three states were never set up");
  }
  if (rows[landable].admissible !== true) {
    fail(`the green row is not admissible: ${rows[landable].reason ?? "(no reason)"}.
The fixture is wrong, not the pane.`);
  }
  if (!rows[red].red?.tip) {
    fail("the red row carries no red - the verdict did not record, so nothing below is measured");
  }

  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({ viewport: { width: 1500, height: 950 } });
    const crashes = [];
    page.on("pageerror", (err) => crashes.push(String(err)));
    await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
    // /todos/merge, not /todos. The page is two views of one queue and the
    // merge rows live on the second tab - the first version of this went to
    // /todos and reported "no verdict badge for the waiting row" about a pane
    // that was not on screen at all. Todos.tsx:133 keys the tab off the path.
    await page.goto(`${base}/todos/merge`, { timeout: 30_000 }).catch(() => {});

    const verdictOf = async (id) => {
      const badge = page.locator(`[data-merge-row="${id}"] [data-merge-verdict]`);
      await badge
        .first()
        .waitFor({ state: "visible", timeout: 20_000 })
        .catch(() => {});
      if ((await badge.count()) === 0) return null;
      return {
        state: await badge.first().getAttribute("data-merge-verdict"),
        words: (await badge.first().textContent())?.trim(),
      };
    };

    const drawn = {
      waiting: await verdictOf(waiting),
      red: await verdictOf(red),
      landable: await verdictOf(landable),
    };
    for (const [name, got] of Object.entries(drawn)) {
      if (!got) fail(`the pane drew no verdict badge for the ${name} row`);
    }
    if (crashes.length > 0) fail(`the page threw: ${crashes.join("; ")}`);

    // THE ASSERTION IS THE DIFFERENCE. Not the words - those are the console's
    // to choose and have changed twice - but that a reader can tell the three
    // apart at all.
    const states = new Set(Object.values(drawn).map((d) => d.state));
    if (states.size !== 3) {
      fail(`the pane draws these three rows as ${states.size} state(s):
  waiting  ${drawn.waiting.state} / ${JSON.stringify(drawn.waiting.words)}
  red      ${drawn.red.state} / ${JSON.stringify(drawn.red.words)}
  landable ${drawn.landable.state} / ${JSON.stringify(drawn.landable.words)}
"nobody has looked", "we looked and it failed" and "it may land" are three
different things to do next.`);
    }
    if (drawn.red.state === drawn.waiting.state) {
      fail("a row whose gate FAILED is drawn like one nobody has measured");
    }

    console.log(
      `the pane tells three apart: waiting=${drawn.waiting.state}, red=${drawn.red.state}, landable=${drawn.landable.state}`,
    );
  } finally {
    await browser.close();
  }
} catch (err) {
  if (!(err instanceof Died)) throw err;
  console.error(err.message);
  failed = true;
} finally {
  // THE TARGET GOES BACK FIRST, before the rows do. Abandoning the artifact
  // ends the row; it does not end the LOCK, and this check declared runs that
  // took one. A green declaration with no land behind it held master for the
  // full MergeLockBelievedFor - fifteen minutes, longer than the rest of the
  // suite - so every later check that wanted the target was denied it by a
  // check that had finished. That is what made one gate-state run red on a
  // normal suite, and the hour went into the pane rather than into here.
  //
  // Order matters the other way too: /api/merge/{id}/abandon wants a merge
  // request, so it goes before the status write that stops the row being one.
  for (const id of holding) {
    const gave = await call(`/api/merge/${id}/abandon`, {
      method: "POST",
      body: JSON.stringify({ reason: "merge-verdict-check is done with the target" }),
    });
    if (!gave.ok) {
      console.error(
        `could not give master back after seeding ${id}: HTTP ${gave.status} ${JSON.stringify(gave.body)}`,
      );
      left = true;
    }
  }

  // THE ROWS GO, in a finally, because a failing run is the one somebody
  // repeats and three stuck rows in the queue would be this check leaving
  // exactly the mess it exists to describe.
  for (const id of seeded) {
    await call(`/api/artifact/${id}/status`, {
      method: "POST",
      body: JSON.stringify({ status: "abandoned" }),
    });
  }

  // AND THE RELEASE IS READ BACK, not assumed. A teardown that calls a door
  // and never asks whether it worked is how this leaked in the first place -
  // the status write above returns 200 and says nothing at all about the lock.
  // Asserted against OUR row id rather than held:false, so a real drainer
  // taking the target the moment we let go is not read as our failure.
  const free = await call("/api/merge-queue");
  if (free.ok && free.body.lock?.held && seeded.includes(free.body.lock.item)) {
    console.error(
      `the target is still held for ${free.body.lock.item} after teardown, until ${free.body.lock.until}.`,
    );
    left = true;
  }
  if (left) {
    console.error("Every later check that wants the landing lock will be refused it by this one.");
  }
  if (failed || left) process.exit(1);
}
