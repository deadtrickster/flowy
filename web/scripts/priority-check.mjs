/**
 * A row can be told to happen first, and the queue believes it.
 *
 *   node scripts/priority-check.mjs BASE_URL TOKEN ROOM
 *
 * The operator, on the board: "add priorities to todos, and merges" - filed
 * with sixteen unowned rows on it and nothing saying which of them they wanted.
 *
 * TWO ASSERTIONS, and the second is the one worth the browser. Setting a
 * priority from the panel has to reach the NODE, or it is a chip that survives
 * until the next poll. And the ORDER has to change, because a ranking that
 * nothing sorts by is a label.
 *
 * It raises its own rows and closes them again: a check that leaves rows on a
 * live board is the defect 01M0HADJ2R was about, and this one would leave three.
 */

import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/priority-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}

const bearer = { Authorization: `Bearer ${token}` };
const raised = [];

const clearRaised = async () => {
  for (const id of raised) {
    const res = await fetch(`${base}/api/artifact/${encodeURIComponent(id)}/status`, {
      method: "POST",
      headers: { ...bearer, "Content-Type": "application/json" },
      body: JSON.stringify({
        status: "done",
        note: "closed by priority-check: a fixture this check raised",
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

const file = async (title) => {
  const res = await fetch(`${base}/api/chat/${encodeURIComponent(room)}/todo`, {
    method: "POST",
    headers: { ...bearer, "Content-Type": "application/json" },
    body: JSON.stringify({ title }),
  });
  if (!res.ok) await die(`could not file ${title}: ${res.status} ${await res.text()}`);
  const id = (await res.json()).item?.id;
  if (!id) await die(`filing ${title} answered without item.id`);
  raised.push(id);
  return id;
};

// THE OLDEST IS FILED FIRST, so the queue's own order puts it on top and any
// reordering this check sees is the priority rather than the clock.
const stamp = Date.now().toString(36);
const oldest = await file(`priority-check ${stamp}: filed first, ranked later`);
const middle = await file(`priority-check ${stamp}: filed second, left unjudged`);
const newest = await file(`priority-check ${stamp}: filed last, ranked now`);

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 });

  const rank = async (id, priority) => {
    const opener = page.locator(`[data-todo-open="${id}"]`);
    await opener.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
    if ((await opener.count()) === 0) await die(`${id} is not in the room's todo panel`);
    await opener.click();
    const select = page.locator(`[data-todo-priority-set="${id}"]`);
    await select.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
    if ((await select.count()) === 0) {
      await die(`the summary for ${id} has no control to say what to do first`);
    }
    await select.selectOption(priority);
    // ASKED OF THE NODE, not of the chip: the panel draws what it was handed,
    // so a value that never left the browser looks identical here.
    for (let i = 0; i < 40; i++) {
      const res = await fetch(`${base}/api/artifact/${encodeURIComponent(id)}`, {
        headers: bearer,
      });
      if (res.ok) {
        const art = await res.json();
        if ((art.fields?.priority ?? "") === priority) return;
      }
      await page.waitForTimeout(250);
    }
    await die(`${id} was set to ${priority} in the panel and the node never took it`);
  };

  await rank(newest, "now");
  await rank(oldest, "later");

  // AND THE QUEUE BELIEVES IT. Asked of the door the board reads, in queued
  // order, which is where the sort lives.
  const listed = await fetch(
    `${base}/api/artifacts?kind=todo&status=todo&room=${encodeURIComponent(room)}&limit=200`,
    { headers: bearer },
  );
  if (!listed.ok) await die(`could not read the queue back: ${listed.status}`);
  const rows = (await listed.json()).artifacts ?? [];
  const seen = rows.map((r) => r.id).filter((id) => raised.includes(id));
  const want = [newest, middle, oldest];
  if (seen.length !== want.length) {
    await die(`the queue came back with ${seen.length} of the three fixtures`);
  }
  for (let i = 0; i < want.length; i++) {
    if (seen[i] !== want[i]) {
      await die(`the queue is ordered ${seen.join(", ")}
want ${want.join(", ")} - now, then the unjudged one, then the one set to later.
The oldest row is ranked later and must sort BELOW a row nobody has judged.`);
    }
  }

  console.log(
    "a row can be ranked from the panel, the node keeps it, and the queue orders now before unjudged before later",
  );
} finally {
  await clearRaised();
  await browser.close();
}
