/**
 * The board says which row the rail's dot is counting.
 *
 *   node scripts/waiting-row-check.mjs BASE_URL TOKEN
 *
 * The operator, 2026-08-21: "have one unread todo, went to todo list - no idea
 * which one, fix". The dot beside `todos` draws mine_todo from /api/nag - rows
 * assigned to you and not started - and the list it sends you to marked none of
 * them, so the only way to clear the dot was to open rows until it went away.
 *
 * WHAT THIS ASSERTS, and the second claim is the one that matters:
 *
 *   the row that IS waiting carries data-todo-waiting
 *   a row that is NOT waiting does not
 *
 * A check with only the first passes on a board that marks every row, which is
 * the same defect with the opposite sign and worse - a mark on everything is a
 * mark on nothing, and a reader who trusts it stops looking.
 *
 * The second row is assigned to the SAME principal and left ACTIVE, so the two
 * differ only in the property under test. A control assigned to somebody else
 * would also pass on a board that marked "mine" rather than "mine and not
 * started", which is a different number from the one on the rail.
 *
 * IT CLEANS UP WHAT IT FILED, in finally AND before every exit: process.exit
 * skips finally, and a check that leaves rows behind changes the number the
 * next run of this very check reads.
 */
import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/waiting-row-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const filed = [];
const api = async (path, init) => {
  const res = await fetch(`${base}${path}`, {
    ...init,
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
  });
  if (!res.ok) throw new Error(`${path}: ${res.status} ${await res.text()}`);
  return res.json();
};

const close = async () => {
  for (const id of filed.splice(0)) {
    await api(`/api/artifact/${id}/status`, {
      method: "POST",
      body: JSON.stringify({ status: "done", note: "seeded by waiting-row-check" }),
    }).catch(() => {});
  }
};

// WHO THIS TOKEN IS, inside the try below rather than here: anything that
// throws before the try escapes as a stack trace and skips the cleanup, and a
// check that leaves its own rows behind changes the number its next run reads.
let handle = "";

const stamp = Date.now().toString(36);
const raise = async (title) => {
  const row = await api("/api/artifacts", {
    method: "POST",
    body: JSON.stringify({
      type: "memory",
      kind: "todo",
      title,
      body: "seeded by waiting-row-check",
    }),
  });
  filed.push(row.id);
  await api(`/api/todo/${row.id}/assignee`, {
    method: "POST",
    body: JSON.stringify({ assignee: handle }),
  });
  return row.id;
};

let failure = "";
const browser = await chromium.launch();
try {
  const me = await api("/api/me");
  handle = me.user?.handle ?? "";
  if (!handle) throw new Error("this token's user has no handle, so nothing can be assigned to it");

  const waiting = await raise(`waiting-row-check ${stamp} - not started`);
  const started = await raise(`waiting-row-check ${stamp} - already active`);
  await api(`/api/artifact/${started}/status`, {
    method: "POST",
    body: JSON.stringify({ status: "active", note: "seeded by waiting-row-check" }),
  });

  // THE NODE'S OWN ANSWER, read before the browser opens, so a screen that
  // disagrees fails as a disagreement rather than as an absence.
  const nag = await api("/api/nag");
  const ids = nag.mine_todo_ids;
  if (!Array.isArray(ids)) {
    failure = `/api/nag sent no mine_todo_ids, so the board cannot say which row: ${JSON.stringify(nag)}`;
  } else if (!ids.includes(waiting)) {
    failure = `the node does not count ${waiting} as waiting: ${JSON.stringify(ids)}`;
  } else if (ids.includes(started)) {
    failure = `the node counts the ACTIVE row ${started} as waiting, so mine_todo has changed meaning`;
  }

  if (!failure) {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
    await page.goto(`${base}/todos`, { timeout: 20_000 });
    const marked = page.locator(`[data-todo-row="${waiting}"]`);
    await marked.waitFor({ state: "visible", timeout: 20_000 });
    if ((await marked.getAttribute("data-todo-waiting")) === null) {
      failure = `the row the node calls waiting (${waiting}) is drawn with no mark`;
    }
    const plain = page.locator(`[data-todo-row="${started}"]`);
    await plain.waitFor({ state: "visible", timeout: 20_000 });
    if (!failure && (await plain.getAttribute("data-todo-waiting")) !== null) {
      failure = `an active row (${started}) is marked as waiting, so the mark means "mine" and not "waiting"`;
    }
  }
} catch (err) {
  failure = `waiting-row-check: ${err instanceof Error ? err.message : String(err)}`;
} finally {
  await browser.close();
  await close();
}

if (failure) {
  console.error(failure);
  process.exit(1);
}
console.log("the board marks the row the rail is counting, and only that row");
