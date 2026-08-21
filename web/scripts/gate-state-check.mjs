/**
 * The merges pane says what the landing lock says.
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
 * THE ASSERTION IS AGREEMENT BETWEEN TWO INDEPENDENT READINGS: what the node
 * says about the lock, and what the pane draws. Neither is a constant this file
 * chose, so a pane that draws nothing - which is what master does - fails, and
 * a pane that draws the wrong one of three states fails differently.
 *
 * WHY IT NO LONGER DEMANDS A FREE LOCK, which is the bug that made its first
 * full-suite run red at 759/1 while ONLY= passed. Recording a verdict does NOT
 * release the landing lock - api_mergegate.go has no ReleaseMergeLock in it,
 * and only land and abandon give a target back - so the checks that declare
 * runs earlier in a suite hold master for the full fifteen minutes of
 * MergeLockBelievedFor. A check that treated that as a fixture error was
 * refusing a NORMAL state of the suite it runs in, and reporting somebody
 * else's held lock as a defect in this pane. Waiting it out is not an option
 * either: nothing releases it.
 *
 * So the lock's state is an INPUT here rather than a precondition. The
 * transition arm - take it, watch the pane change - runs only when the target
 * happens to be free, and says so out loud when it does not, because a check
 * that quietly measures less than it did yesterday is how coverage evaporates.
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

/**
 * The three states, derived from the node's own answer.
 *
 * A lock row that is present and not live means its holder never gave the
 * target back - releasing DELETES the row - and it blocks nothing, which is a
 * different sentence from both of the others.
 */
const expectedFrom = (lock) => {
  if (!lock) return null;
  if (lock.held) return "running";
  return lock.holder ? "stale" : "free";
};

const readPane = async (browser) => {
  const page = await browser.newPage({ viewport: { width: 1500, height: 950 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  // /todos/merge, not /todos: Todos.tsx keys the tab off the path, and a check
  // written against /todos reports "nothing drawn" about a pane that is not on
  // screen at all - a failure identical with and without the fix, which is the
  // signature of asserting about the wrong screen.
  await page.goto(`${base}/todos/merge`, { timeout: 30_000 }).catch(() => {});
  const line = page.locator("[data-gate-state]");
  await line
    .first()
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  const got =
    (await line.count()) === 0
      ? { state: null, words: "" }
      : {
          state: await line.first().getAttribute("data-gate-state"),
          words: ((await line.first().textContent()) ?? "").replace(/\s+/g, " ").trim(),
        };
  await page.close();
  return { ...got, crashes };
};

const first = await call("/api/merge-queue");
if (!first.ok) die(`/api/merge-queue answered ${first.status}`);
if (first.body.lock === undefined) {
  die(`this node's /api/merge-queue sends no lock at all, so the pane has nothing to draw.
That is the node, not the console - api_mergequeue.go sets response.Lock
unconditionally, as held:false when the target is free.`);
}

// THE TRAP, MEASURED RATHER THAN ASSUMED. If a later node starts omitting these
// properly, the pane's guard stops mattering and this stops asserting it -
// rather than failing and sending somebody to look at a console that is fine.
const zeroTimeIsOnTheWire = String(first.body.lock?.until ?? "").startsWith("0001-");

let row = null;
let declared = false;
const run = `gate-state-check-${Date.now()}`;
const browser = await chromium.launch();
try {
  const want = expectedFrom(first.body.lock);
  const pane = await readPane(browser);
  if (pane.crashes.length > 0) die(`the page threw: ${pane.crashes.join("; ")}`);
  if (!pane.state) {
    die(`the pane says nothing about the gate, and the node says it is ${want}.
Whether a run holds the target is the one fact that separates a queue that is
working from one that is stopped, and the page a person opens to ask does not
carry it.`);
  }
  if (pane.state !== want) {
    die(`the node says the lock is ${want} and the pane drew ${pane.state}: ${JSON.stringify(pane.words)}
lock: ${JSON.stringify(first.body.lock)}`);
  }
  if (zeroTimeIsOnTheWire && /\b0001\b|\b1\/1\/1\b/.test(pane.words)) {
    die(`the pane printed the zero time as a real one: ${JSON.stringify(pane.words)}
lock.until arrives as 0001-01-01T00:00:00Z whenever nothing is held, because
omitempty does not omit a time.Time. Key off lock.held, never off whether the
stamp is present.`);
  }

  // WHO, AND WHAT FOR, on the states that have a holder. "something is
  // happening" is not actionable; "X is measuring Y" is. The name is asserted
  // only when the node resolved one - a principal that maps to no user has
  // none to print, and demanding it would fail on a node that is behaving.
  if (want !== "free") {
    const who = first.body.lock.holder_name;
    if (who && !pane.words.includes(who)) {
      die(`the pane does not say who holds the target (${who}): ${JSON.stringify(pane.words)}
"who do I talk to" is half of why the lock records a holder.`);
    }
  }
  console.log(`the pane agrees with the node: ${pane.state} - ${pane.words}`);

  // THE TRANSITION ARM, when and only when the target is free. Everything above
  // is one reading compared against another source; this is the pane following
  // a change, which is the part that cannot pass by drawing a constant.
  if (want !== "free") {
    console.log(
      `transition arm NOT run: the target is ${want}, held by ${first.body.lock.holder_name || first.body.lock.holder}.`,
    );
    console.log(
      "  Nothing in a suite releases a declared lock - a verdict does not, only land and abandon do - so this is normal here and not worth failing over.",
    );
  } else {
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
    if (!made.ok) {
      die(`could not file the seed row: HTTP ${made.status} ${JSON.stringify(made.body)}`);
    }
    row = made.body.id;

    const said = await call(`/api/merge/${row}/gate`, {
      method: "POST",
      body: JSON.stringify({ run }),
    });
    if (!said.ok) {
      die(`could not declare a run on ${row}: HTTP ${said.status} ${JSON.stringify(said.body)}`);
    }
    declared = true;

    const during = await call("/api/merge-queue");
    if (!during.ok) die(`/api/merge-queue answered ${during.status}`);
    if (!during.body.lock?.held) {
      die(`declaring a run did not take the landing lock, so the fixture never reached
the state this arm is about: ${JSON.stringify(during.body.lock)}`);
    }

    const held = await readPane(browser);
    if (held.crashes.length > 0)
      die(`the page threw with a gate running: ${held.crashes.join("; ")}`);
    if (held.state !== "running") {
      die(`a run holds the target and the pane drew ${held.state}: ${JSON.stringify(held.words)}`);
    }
    if (held.state === pane.state || held.words === pane.words) {
      die(`the pane reads the same free and running: ${JSON.stringify(held.words)}
This is the whole point of the line - a reader cannot tell a working drainer
from a stopped one.`);
    }
    if (!held.words.includes("gate-state-check/branch")) {
      die(
        `the pane says a gate is running but not what it is measuring: ${JSON.stringify(held.words)}`,
      );
    }
    console.log(`and it follows the lock: free -> ${held.state} - ${held.words}`);
  }
} finally {
  await browser.close();
  // GIVE THE LOCK BACK FIRST, and only the one this check took. A declaration
  // holds the target for fifteen minutes and nothing else in a suite releases
  // one, so leaking it here would deny the target to every later check that
  // wants it - which is exactly the state this check had to be taught to
  // tolerate in the first place.
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
