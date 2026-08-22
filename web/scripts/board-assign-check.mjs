/**
 * The board can say who is carrying a row, including a row in no room.
 *
 *   node scripts/board-assign-check.mjs BASE_URL TOKEN HANDLE
 *
 * 01M0KXZ6VT, the operator: "i cannot reassign / assign todos". They were
 * right twice over, and both halves are asserted here.
 *
 *   THE BOARD HAD NO CONTROL. Every caller of api.assignTodo was inside a
 *   room's todo pane - `git log -S'assignTodo' -- routes/Todos.tsx` is empty,
 *   so this was a gap and not a regression. The page you open to see
 *   everything was the one page you could not act on.
 *
 *   AND THE OLD DOOR NEEDS A ROOM. /api/chat/{room}/todo/{id}/assignee cannot
 *   be built for a row that is in no room, and 3 of 26 open rows on this
 *   fleet's board carried none. So the seeded row here HAS NO ROOM on purpose:
 *   it is the case that had no path at all, and a check that seeded one into a
 *   room would pass while the reported bug survived.
 *
 * ASSERTED AS A WRITE, NOT A RENDER. The control is typed into and the NODE is
 * asked afterwards - a check that only looked at the screen would pass on a
 * console that draws an input and posts nothing, which is the more likely bug
 * of the two.
 */

import { chromium } from "playwright";

const [base, token, handle] = process.argv.slice(2);
if (!base || !token || !handle) {
  console.error("usage: node scripts/board-assign-check.mjs BASE_URL TOKEN HANDLE");
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

let row = null;
const browser = await chromium.launch();
try {
  // NO ROOM IN fields, deliberately - see the head comment.
  const made = await call("/api/artifacts", {
    method: "POST",
    body: JSON.stringify({
      type: "memory",
      kind: "todo",
      title: `board-assign-check ${Date.now().toString(36)}`,
      body: "seeded by board-assign-check",
      visibility: "project",
    }),
  });
  if (!made.ok)
    die(`could not file the seed row: HTTP ${made.status} ${JSON.stringify(made.body)}`);
  row = made.body.id;

  const page = await browser.newPage({ viewport: { width: 1500, height: 950 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/todos`, { timeout: 30_000 }).catch(() => {});

  const control = page.locator(`[data-todo-assign="${row}"]`);
  await control.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await control.count()) === 0) {
    die(`the board draws no way to assign row ${row}.
It shows who is carrying a row and offers nothing to change it, which is the
report: the page you open to see everything is the one you cannot act on.`);
  }

  await control.first().click();
  const input = page.locator(`[data-todo-assign-input="${row}"]`);
  await input.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
  if ((await input.count()) === 0) die("the assign control does not open an input");
  await input.first().fill(handle);
  await page.locator(`[data-todo-assign-save="${row}"]`).first().click();

  // THE NODE IS THE WITNESS. Waiting on the screen would let a console that
  // renders optimistically and posts nothing pass this.
  let owner = "";
  for (let i = 0; i < 20; i++) {
    const back = await call(`/api/todo/${row}/assignee`);
    owner = back.body?.assignment?.assignee ?? back.body?.assignee ?? "";
    if (owner === handle) break;
    await page.waitForTimeout(500);
  }
  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);
  if (owner !== handle) {
    die(`the control was used and the node still says the row is carried by ${JSON.stringify(owner)}.
The screen may have changed; the row did not.`);
  }

  console.log(`the board assigned a roomless row to ${handle}, and the node agrees`);
} finally {
  await browser.close();
  if (row) {
    await call(`/api/artifact/${row}/status`, {
      method: "POST",
      body: JSON.stringify({ status: "abandoned" }),
    });
  }
}
