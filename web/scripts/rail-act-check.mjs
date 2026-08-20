/**
 * A number in the rail means WORK IS WAITING FOR YOU, and the rows where it
 * does not mean that carry none.
 *
 *   node scripts/rail-act-check.mjs BASE_URL TOKEN ROOM
 *
 * The row (01M0GGEW74) read "the rail carries one number and thirteen rows
 * carry none". The fix is not thirteen numbers. Two of those rows - inbox and
 * todos - hold work handed to this principal, in a state that ends; a badge
 * there goes down when the work is done and is worth a glance. The rest would
 * count HOW MANY EXIST, which on a working node is never zero and never falls,
 * and a badge that never clears is what teaches a reader to stop looking at the
 * two that do.
 *
 * SO THIS CHECK HAS TWO HALVES AND THE SECOND IS THE DURABLE ONE. The first
 * asserts the todos badge carries the node's own mine_todo. The second asserts
 * that eleven named rows carry no badge at all - which is the decision above
 * written down somewhere a future change has to argue with. The regression this
 * is really here for is somebody adding "47" beside memory because the number
 * was easy to fetch.
 *
 * WHY IT FILES A ROW FIRST. A fresh node has nothing assigned to anybody, so
 * mine_todo is 0, the badge is correctly absent, and a check that just looked
 * would pass on a console that never draws it. The row is filed and CLAIMED -
 * an unowned row is not mine_todo - and the node is asked what it now thinks
 * before the browser is opened at all. If the node does not agree the count
 * moved, this fails there and says so, rather than blaming the sidebar for a
 * number that was never going to arrive.
 */

import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/rail-act-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}

const bearer = { Authorization: `Bearer ${token}` };
// Takes a second argument on purpose: the sentence, and then what was on the
// screen when it was false. A failure that only says "no badge" makes the next
// person open a browser to find out what there WAS instead.
const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

const json = async (path, init = {}) => {
  const answer = await fetch(`${base}${path}`, {
    ...init,
    headers: {
      ...bearer,
      ...(init.body ? { "Content-Type": "application/json" } : {}),
      ...init.headers,
    },
  });
  if (!answer.ok) die(`${init.method ?? "GET"} ${path} answered ${answer.status}`);
  return answer.json();
};

const before = (await json("/api/nag")).mine_todo;

const filed = await json(`/api/chat/${encodeURIComponent(room)}/todo`, {
  method: "POST",
  body: JSON.stringify({ title: "the rail-act check needs one row waiting" }),
});
// item.id, and only that. The node answers {item:{id,project}} - see todo.go's
// own reader of this door - and a chain of `|| filed.id || filed.artifact.id`
// fallbacks is how a check keeps passing after the shape it depends on moved,
// having quietly picked up something else.
const id = filed.item?.id;
if (!id) die(`filing a todo answered without item.id: ${JSON.stringify(filed)}`);

// "me", NOT A HANDLE THIS FILE WORKED OUT. mine_todo compares the assignee
// against store.seatHandle - the caller's users.handle - and a check that
// guessed at it from /api/whoami would write the AGENT name, which matches
// nothing, leaving the row unowned and this failing for a reason that has
// nothing to do with the sidebar. The node resolves "me" against the same
// query the count uses, so there is one answer rather than two.
await json(`/api/todo/${encodeURIComponent(id)}/assignee`, {
  method: "POST",
  body: JSON.stringify({ assignee: "me" }),
});

const expected = (await json("/api/nag")).mine_todo;
if (expected !== before + 1) {
  die(`the node did not count the claimed row: mine_todo was ${before}, is ${expected}, and the
check has nothing to look for in the sidebar. The row is ${id}, claimed as "me" -
a seat with no users.handle cannot be resolved and the claim stays the literal
word, which is the one way this fails without the console being involved.`);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({
    viewport: { width: 1400, height: 900 },
  });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/`, { timeout: 30_000 }).catch(() => {});
  await page
    .locator("[data-room-list]")
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if (crashes.length > 0) die(`the shell threw: ${crashes.join("; ")}`);

  // WAITED FOR, NOT READ AT ONCE. The counts arrive from two fetches after the
  // first paint, so reading the DOM the moment the room list appears is reading
  // it before the answer got back - a check that failed for the console being
  // fast rather than for being wrong.
  const todos = page.locator('a[href="/todos"] [data-waiting="todos"]');
  await todos.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await todos.count()) === 0) {
    const rail = await page
      .locator("nav")
      .first()
      .innerText()
      .catch(() => "");
    die(
      `the node says ${expected} row(s) are waiting for this seat and the todos row carries no
badge. The rail reads:`,
      rail,
    );
  }
  const shown = Number(await todos.first().getAttribute("data-waiting-count"));
  if (shown !== expected) {
    die(`the todos badge says ${shown} and the node says ${expected} - the rail is counting
something other than /api/nag's mine_todo, which is the number the board nag
reports and the one the operator sees in two places.`);
  }

  // AND THE INBOX ROW, which had NO assertion at all in the first cut of this
  // file - half the feature shipped with zero coverage while the check read as
  // if it covered the rail. Found by re-reading it before it had ever run.
  //
  // BY AGREEMENT RATHER THAN BY A FIXTURE. Making a task wait for this seat
  // needs an artifact and a second principal to hand it over, which is a lot of
  // machinery for one number; asking the node what it thinks and requiring the
  // rail to match is the same shape the todos half already uses, and it fails on
  // the defect that matters either way:
  //
  //   node says N > 0, rail has no badge   the fetch is broken, or the badge is
  //   node says 0, rail has a badge        it is counting delegated or done work
  //   both agree                           the wire is intact
  //
  // The state filter is the point of the call. A delegated task is somebody
  // else's turn and a done one is nobody's, so `?state=open` is what /inbox
  // lists and what the rail must count - and if paramguard ever stops accepting
  // that parameter, this is what says so rather than the badge quietly going
  // null and drawing nothing.
  const openTasks = ((await json("/api/inbox/tasks?state=open")).tasks ?? []).length;
  const inboxBadge = page.locator('nav a[href="/inbox"] [data-waiting="inbox"]');
  if (openTasks > 0) {
    await inboxBadge.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
    if ((await inboxBadge.count()) === 0) {
      die(`the node says ${openTasks} open task(s) are waiting for this seat and the inbox row
carries no badge - either /api/inbox/tasks?state=open is not being read, or its
failure is being drawn as "nothing waiting" rather than as nothing at all.`);
    }
    const inboxShown = Number(await inboxBadge.first().getAttribute("data-waiting-count"));
    if (inboxShown !== openTasks) {
      die(`the inbox badge says ${inboxShown} and the node says ${openTasks} open - the rail is
counting tasks in some other state, and delegated work is somebody else's turn.`);
    }
  } else if ((await inboxBadge.count()) > 0) {
    const said = await inboxBadge.first().getAttribute("data-waiting-count");
    die(`no task is open for this seat and the inbox row carries a badge saying ${said} - a rail
badge that does not clear is the one thing this whole design is against.`);
  }

  // THE HALF THAT OUTLIVES THE OTHER. Each of these is a row that could carry a
  // count of what exists, and none of them may. Listed by href rather than by
  // position so reordering the rail does not silently drop one from the check.
  const bare = [
    "/direct",
    "/new",
    "/projects",
    "/memory",
    "/reports",
    "/findings",
    "/diagrams",
    "/worklog",
    "/activity",
    "/metrics",
    "/traces",
    "/profile",
  ];
  const wrong = [];
  for (const href of bare) {
    const link = page.locator(`nav a[href="${href}"]`);
    if ((await link.count()) === 0) {
      die(`the rail has no ${href} row, so this check is asserting about a sidebar that changed
under it - fix the list in this file rather than deleting the assertion.`);
    }
    if ((await link.locator("[data-waiting]").count()) > 0) {
      wrong.push(href);
    }
  }
  if (wrong.length > 0) {
    die(`${wrong.join(", ")} carry a badge. A rail badge is a claim that work is waiting for
this person and clears when they do it; a count of how many rows exist never
clears, and standing next to one that does it costs the reader both. If one of
these genuinely became waiting-for-you - a read mark per list would do it - then
say so here and move it out of the list.`);
  }
} finally {
  await browser.close();
}
console.log("the rail counts what is waiting and nothing else");
