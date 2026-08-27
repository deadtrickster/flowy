/**
 * A merge row can be ranked from the merge pane, the node keeps it, and the
 * queue carries the word - WITHOUT reordering.
 *
 *   node scripts/merge-priority-check.mjs BASE_URL TOKEN
 *
 * The operator asked for priorities on todos AND merges. The todos half landed
 * with priority.sh; the merge half was the gap this check closes: the door
 * stores the word on a merge row, but the queue projection dropped it, so the
 * pane had nothing to draw and nothing to set.
 *
 * THREE ASSERTIONS, and the third is the policy. Setting a priority from the
 * pane has to reach the NODE, or it is a chip that survives until the next
 * poll. The /api/merge-queue answer has to CARRY the word, or no surface can
 * show it. And the queue's FIFO order must hold: the operator settled "FIFO
 * for the time being", so the ranking must not move a row out of its place -
 * the newest row stays last even ranked now.
 *
 * NO GATE RUN: ranking is orthogonal to landing evidence, and a check that
 * declared a run would take the landing lock a real drainer needs. It files
 * one row and closes it again, per 01M0HADJ2R.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/merge-priority-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const bearer = { Authorization: `Bearer ${token}` };
let seeded = null;

/** Close the fixture and take its ranking back - the check's own teardown. */
const clearSeeded = async () => {
  if (!seeded) return;
  await fetch(`${base}/api/todo/${encodeURIComponent(seeded)}/priority`, {
    method: "POST",
    headers: { ...bearer, "Content-Type": "application/json" },
    body: JSON.stringify({ priority: "" }),
  }).catch(() => {});
  const res = await fetch(`${base}/api/artifact/${encodeURIComponent(seeded)}/status`, {
    method: "POST",
    headers: { ...bearer, "Content-Type": "application/json" },
    body: JSON.stringify({
      status: "done",
      note: "closed by merge-priority-check: a fixture this check raised",
    }),
  }).catch((err) => ({ ok: false, status: String(err) }));
  if (!res.ok) console.error(`could not clear the fixture ${seeded}: ${res.status}`);
};

const die = async (message) => {
  console.error(message);
  await clearSeeded();
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

// FILE THE ROW THE WAY `flowy merge open` DOES - /api/artifacts, same body
// merge-verdict-check uses. No gate run: this row is judged by no tip, which
// is fine, because the ranking is what is under test.
const stamp = Date.now().toString(36);
const made = await call("/api/artifacts", {
  method: "POST",
  body: JSON.stringify({
    type: "memory",
    kind: "merge",
    title: `merge-priority-check ${stamp}`,
    body: "seeded by merge-priority-check",
    visibility: "project",
    fields: { branch: `merge-priority-check/${stamp}`, target: "master" },
  }),
});
if (!made.ok)
  await die(`could not file the fixture row: HTTP ${made.status} ${JSON.stringify(made.body)}`);
seeded = made.body.id;

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  // /todos/merge, not /todos - the page is two views of one queue and the
  // merge rows live on the second tab, the same route merge-verdict-check
  // learned the hard way.
  await page.goto(`${base}/todos/merge`, { timeout: 30_000 });

  // The row itself first, so a missing control and a missing row die in
  // different words - one is this feature absent, the other is the fixture
  // broken.
  const row = page.locator(`[data-merge-row="${seeded}"]`);
  await row.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await row.count()) === 0) {
    await die(`${seeded} is not on the merge pane - the fixture row never drew`);
  }
  const select = page.locator(`[data-merge-priority-set="${seeded}"]`);
  await select.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
  if ((await select.count()) === 0) {
    await die(`the merge pane has no control to say what to do first about ${seeded}`);
  }
  await select.selectOption("now");

  // THE PANE REPAINTED FROM THE NODE'S ANSWER, not the click: the badge only
  // exists once the setPriority answer lands and the row is patched.
  const badge = page.locator(`[data-merge-priority="${seeded}"]`);
  await badge.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
  if ((await badge.count()) === 0) {
    await die(
      `${seeded} was set to now in the pane and no badge drew - the pane never took the answer`,
    );
  }
  if ((await badge.textContent())?.trim() !== "now") {
    await die(`the badge for ${seeded} reads "${await badge.textContent()}", wanted now`);
  }

  // ASKED OF THE NODE, not of the chip: the pane draws what it was handed, so
  // a value that never left the browser looks identical here.
  for (let i = 0; i < 40; i++) {
    const res = await fetch(`${base}/api/artifact/${encodeURIComponent(seeded)}`, {
      headers: bearer,
    });
    if (res.ok) {
      const art = await res.json();
      if ((art.fields?.priority ?? "") === "now") break;
    }
    if (i === 39) {
      await die(`${seeded} was set to now in the pane and the node never took it`);
    }
    await page.waitForTimeout(250);
  }

  // AND THE QUEUE CARRIES THE WORD. Asked of the door every surface reads,
  // because a projection that drops it is a pane with nothing to draw.
  const queue = await call("/api/merge-queue");
  if (!queue.ok) await die(`/api/merge-queue answered ${queue.status}`);
  const items = queue.body.items ?? [];
  const mine = items.find((i) => i.id === seeded);
  if (!mine) await die(`${seeded} is not in the merge queue answer`);
  if (mine.priority !== "now") {
    await die(
      `the queue carries priority ${JSON.stringify(mine.priority)} for ${seeded}, wanted now`,
    );
  }

  // AND FIFO HOLDS. The fixture is the newest row filed, so in queued order it
  // sits last - a ranking that moved it would be the reorder the operator
  // explicitly settled against for now.
  const last = items[items.length - 1];
  if (!last || last.id !== seeded) {
    await die(
      `the newest row was ranked now and the queue moved it: it sits at ${items.findIndex((i) => i.id === seeded) + 1} of ${items.length}, want last`,
    );
  }

  console.log(
    "a merge row is ranked from the pane, the node keeps it, the queue carries the word, and FIFO holds",
  );
} finally {
  await clearSeeded();
  await browser.close();
}
