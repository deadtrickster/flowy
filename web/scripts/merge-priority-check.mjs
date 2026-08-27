/**
 * A merge row can be ranked from the merge pane, the node keeps it, the queue
 * carries the word - and the queue REORDERS by it.
 *
 *   node scripts/merge-priority-check.mjs BASE_URL TOKEN
 *
 * The operator asked for priorities on todos AND merges. The todos half landed
 * with priority.sh; the merge half was the gap this check closes: the door
 * stores the word on a merge row, but the queue projection dropped it, so the
 * pane had nothing to draw and nothing to set.
 *
 * THREE ASSERTIONS. Setting a priority from the pane has to reach the NODE, or
 * it is a chip that survives until the next poll. The /api/merge-queue answer
 * has to CARRY the word - and carry "" for an unjudged row rather than
 * dropping the key, or an unjudged row reads like an older node that does not
 * rank at all. And the ORDER has to follow the word: the queue's own sort is
 * now, next, UNJUDGED, later - age breaking ties within a rank - so the
 * newest row filed, ranked now, sorts above an older one nobody judged, and
 * one somebody shelved sorts below both. That sort is the landed discipline
 * the drainer already consumes (store.ArtifactQuery.QueuedOrder); the
 * operator's "FIFO for the time being" survives as the age tie-break.
 *
 * It files three rows and closes them again, per 01M0HADJ2R. No gate run:
 * ranking is orthogonal to landing evidence, and declaring one would take the
 * landing lock a real drainer needs.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/merge-priority-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const bearer = { Authorization: `Bearer ${token}` };
const raised = [];

/** Close a fixture and take its ranking back - the check's own teardown. */
const clearRaised = async () => {
  for (const id of raised) {
    await fetch(`${base}/api/todo/${encodeURIComponent(id)}/priority`, {
      method: "POST",
      headers: { ...bearer, "Content-Type": "application/json" },
      body: JSON.stringify({ priority: "" }),
    }).catch(() => {});
    const res = await fetch(`${base}/api/artifact/${encodeURIComponent(id)}/status`, {
      method: "POST",
      headers: { ...bearer, "Content-Type": "application/json" },
      body: JSON.stringify({
        status: "done",
        note: "closed by merge-priority-check: a fixture this check raised",
      }),
    }).catch((err) => ({ ok: false, status: String(err) }));
    if (!res.ok) console.error(`could not clear the fixture ${id}: ${res.status}`);
  }
};

const die = async (message) => {
  console.error(message);
  await clearRaised();
  process.exit(1);
};

const call = async (path, init = {}) => {
  const res = await fetch(`${base}${path}`, {
    ...init,
    headers: { ...bearer, ...init.headers },
  });
  const text = await res.text();
  let body = text;
  try {
    body = JSON.parse(text);
  } catch {
    /* a refusal is a sentence */
  }
  return { ok: res.ok, status: res.status, body };
};

// FILE THREE ROWS THE WAY `flowy merge open` DOES - /api/artifacts, same body
// merge-verdict-check uses, OLDEST FIRST so the queue's age order is fixed
// before any ranking. No gate run: these rows are judged by no tip, which is
// fine, because the ranking is what is under test.
const stamp = Date.now().toString(36);
const fileRow = async (name) => {
  const made = await call("/api/artifacts", {
    method: "POST",
    body: JSON.stringify({
      type: "memory",
      kind: "merge",
      title: `merge-priority-check ${stamp} ${name}`,
      body: "seeded by merge-priority-check",
      visibility: "project",
      fields: { branch: `merge-priority-check/${stamp}/${name}`, target: "master" },
    }),
  });
  if (!made.ok)
    await die(`could not file the ${name} row: HTTP ${made.status} ${JSON.stringify(made.body)}`);
  raised.push(made.body.id);
  return made.body.id;
};

const oldest = await fileRow("oldest");
const middle = await fileRow("middle");
const newest = await fileRow("newest");

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  // /todos/merge, not /todos - the page is two views of one queue and the
  // merge rows live on the second tab, the same route merge-verdict-check
  // learned the hard way.
  await page.goto(`${base}/todos/merge`, { timeout: 30_000 });

  const rank = async (id, priority) => {
    // The row itself first, so a missing control and a missing row die in
    // different words - one is this feature absent, the other is the fixture
    // broken.
    const row = page.locator(`[data-merge-row="${id}"]`);
    await row.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
    if ((await row.count()) === 0) {
      await die(`${id} is not on the merge pane - the fixture row never drew`);
    }
    const select = page.locator(`[data-merge-priority-set="${id}"]`);
    await select.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
    if ((await select.count()) === 0) {
      await die(`the merge pane has no control to say what to do first about ${id}`);
    }
    await select.selectOption(priority);

    // THE PANE REPAINTED FROM THE NODE'S ANSWER, not the click: the badge only
    // exists once the setPriority answer lands and the row is patched.
    const badge = page.locator(`[data-merge-priority="${id}"]`);
    await badge.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
    if ((await badge.count()) === 0) {
      await die(
        `${id} was set to ${priority} in the pane and no badge drew - the pane never took the answer`,
      );
    }
    if ((await badge.textContent())?.trim() !== priority) {
      await die(`the badge for ${id} reads "${await badge.textContent()}", wanted ${priority}`);
    }

    // ASKED OF THE NODE, not of the chip: the pane draws what it was handed,
    // so a value that never left the browser looks identical here.
    for (let i = 0; i < 40; i++) {
      const res = await fetch(`${base}/api/artifact/${encodeURIComponent(id)}`, {
        headers: bearer,
      });
      if (res.ok) {
        const art = await res.json();
        if ((art.fields?.priority ?? "") === priority) break;
      }
      if (i === 39) {
        await die(`${id} was set to ${priority} in the pane and the node never took it`);
      }
      await page.waitForTimeout(250);
    }
  };

  // The newest is ranked now, the oldest shelved, and the middle one is left
  // exactly as filed - nobody has judged it.
  await rank(newest, "now");
  await rank(oldest, "later");

  // AND THE QUEUE CARRIES THE WORD. Asked of the door every surface reads,
  // because a projection that drops it is a pane with nothing to draw - and
  // an unjudged row must answer "" rather than drop the key, or it reads as
  // an older node that does not rank at all.
  const queue = await call("/api/merge-queue");
  if (!queue.ok) await die(`/api/merge-queue answered ${queue.status}`);
  const items = queue.body.items ?? [];
  const byId = Object.fromEntries(items.map((i) => [i.id, i]));
  for (const id of raised) {
    if (!byId[id]) await die(`${id} is not in the merge queue answer`);
  }
  if (byId[newest].priority !== "now") {
    await die(
      `the queue carries priority ${JSON.stringify(byId[newest].priority)} for the newest row, wanted now`,
    );
  }
  if (byId[oldest].priority !== "later") {
    await die(
      `the queue carries priority ${JSON.stringify(byId[oldest].priority)} for the oldest row, wanted later`,
    );
  }
  if (byId[middle].priority !== "") {
    await die(
      `the queue carries priority ${JSON.stringify(byId[middle].priority)} for the unjudged row, wanted "" - absent and empty must not read alike`,
    );
  }

  // AND THE QUEUE REORDERS BY IT. The queue's own sort is now, next,
  // UNJUDGED, later, age breaking ties within a rank - so among these three,
  // the newest row ranked now comes FIRST, then the unjudged middle one, then
  // the oldest one somebody shelved. Other rows on the queue are ignored: the
  // assertion is the relative order of this check's own fixtures.
  const seen = items.map((i) => i.id).filter((id) => raised.includes(id));
  const want = [newest, middle, oldest];
  if (seen.length !== want.length || seen.some((id, at) => id !== want[at])) {
    await die(
      `the queue ordered these ${seen.join(", ")}
want ${want.join(", ")} - now, then the unjudged one, then the one set to later.
The oldest row is ranked later and must sort BELOW a row nobody has judged.`,
    );
  }

  console.log(
    "a merge row is ranked from the pane, the node keeps it, the queue carries the word, and the queue reorders",
  );
} finally {
  await clearRaised();
  await browser.close();
}
