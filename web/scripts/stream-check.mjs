/**
 * THE QUEUE FOLLOWS THE NODE WITH NOBODY TOUCHING THE BROWSER.
 *
 *   node scripts/stream-check.mjs BASE_URL TOKEN
 *
 * The defect this is the regression check for, in the operator's words: "why
 * todo panel didnt reload itself on your reassignments - I have to reload the
 * whole page, this sucks". /todos read the queue once per page load and never
 * again, so every claim and reassignment three agents made over an hour was
 * invisible to the person watching the board.
 *
 * WHAT IS ASSERTED IS A DIFFERENCE, never a presence. A check that watched one
 * reading could not tell a live board from a dead one, and a check that only
 * asserted "a request happened" would pass on a page that fetches and draws
 * nothing. So every arm here is two readings of the RENDERED ROWS with exactly
 * one thing changed between them:
 *
 *   nothing changes on the node   -> the rendered list is IDENTICAL. Without
 *                                    this arm a page that repainted on a timer
 *                                    would pass the next one.
 *   a row is claimed on the node  -> the rendered list CHANGES, and changes to
 *                                    what the node now holds.
 *   a poll lands mid-interaction  -> the filter the operator set, the focus
 *                                    they hold and the scroll they left are all
 *                                    still theirs afterwards.
 *
 * The change is made over the API with nothing touching the page: no click, no
 * key, no reload. That is the whole claim.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/stream-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const die = (why) => {
  console.error(why);
  process.exit(1);
};

const api = async (method, path, body) => {
  const res = await fetch(`${base}${path}`, {
    method,
    headers: { Authorization: `Bearer ${token}`, "content-type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  if (!res.ok) die(`${method} ${path} came back ${res.status}: ${text.slice(0, 300)}`);
  return text ? JSON.parse(text) : {};
};

const stamp = Date.now();
const TITLE = `stream check ${stamp}`;
const TAKER = `streamcheck${stamp % 100000}`;

// The row this check drives. Filed as a bug so the kind filter below has
// something true to narrow to - a filter arm that hid its own subject would be
// asserting about an empty list.
const made = await api("POST", "/api/artifacts", {
  type: "memory",
  kind: "todo",
  title: TITLE,
  body: TITLE,
  status: "todo",
});
const ROW = made.id;
if (!ROW) die(`creating the seed row answered no id: ${JSON.stringify(made)}`);
await api("POST", `/api/todo/${ROW}/category`, { category: "bug" });

const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1600, height: 1000 } });
const page = await context.newPage();
const crashes = [];
page.on("pageerror", (err) => crashes.push(String(err)));
await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
await page.goto(`${base}/todos`, { timeout: 20_000 }).catch(() => {});

const row = page.locator(`li[data-todo-row="${ROW}"]`);
try {
  await row.waitFor({ state: "visible", timeout: 20_000 });
} catch {
  die(
    `/todos never drew the seed row ${ROW}${crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : ""}`,
  );
}

/** Every row on the page as the browser sees it: id, status and who has it.
 * Read off attributes rather than text, so an assertion is about the data the
 * row carries and not about a sentence describing it. */
const rendered = () =>
  page
    .locator("li[data-todo-row]")
    .evaluateAll((nodes) =>
      nodes.map(
        (n) =>
          `${n.getAttribute("data-todo-row")}|${
            n.querySelector("[data-todo-status]")?.getAttribute("data-todo-status") ?? ""
          }|${n.querySelector("[data-todo-assignee]")?.getAttribute("data-todo-assignee") ?? ""}`,
      ),
    )
    .then((rows) => rows.join("\n"));

/**
 * The freshness reading the panel is drawing, or null when it is not drawing
 * one.
 *
 * An empty attribute counts as NULL and that is not pedantry: an earlier
 * version of this check read `data-stream-asof=""` off the "connecting" state
 * and called it a present reading, so a page whose stream never connected at
 * all passed the first assertion and failed four seconds later with an
 * incomprehensible message. A mark that is present and empty is not a reading.
 */
const asOf = async () => {
  const mark = page.locator("[data-stream-asof]").first();
  if ((await mark.count()) === 0) return null;
  const value = await mark.getAttribute("data-stream-asof");
  return value ? value : null;
};

/** What the panel says about the connection: connecting, live or stale. */
const streamState = async () => {
  const mark = page.locator("[data-stream-state]").first();
  if ((await mark.count()) === 0) return null;
  return mark.getAttribute("data-stream-state");
};

/**
 * What the stream door itself answers, asked directly.
 *
 * A page that will not connect says "connecting" and nothing else, which names
 * no cause. This asks the door the same question the browser is asking and
 * reports the status and the first bytes, so a refusal reads as a refusal
 * rather than as a mysterious silence in a console.
 */
async function probeTheDoor() {
  try {
    const controller = new AbortController();
    const res = await fetch(`${base}/api/stream?topics=todos,queue`, {
      headers: { Authorization: `Bearer ${token}`, Accept: "text/event-stream" },
      signal: controller.signal,
    });
    if (!res.ok) {
      const body = await res.text();
      return `GET /api/stream answered ${res.status}: ${body.slice(0, 300)}`;
    }
    // Read only the opening frame. The stream never ends by itself, so this
    // takes what is there and hangs up.
    const reader = res.body.getReader();
    const first = await Promise.race([
      reader.read().then(({ value }) => new TextDecoder().decode(value ?? new Uint8Array())),
      new Promise((ok) => setTimeout(() => ok("<nothing within two seconds>"), 2000)),
    ]);
    controller.abort();
    return `GET /api/stream answered ${res.status} ${res.headers.get("content-type")} and opened with: ${JSON.stringify(first)}`;
  } catch (err) {
    return `GET /api/stream could not be read at all: ${err}`;
  }
}

// ---------------------------------------------------------------- the as of
//
// Before anything else, because every other arm below is only meaningful if the
// panel is actually connected. A board with no freshness mark at all is the
// state this row was filed against; a board stuck on "connecting" is a stream
// that never opened, and both have to fail here rather than later and sideways.
await page
  .waitForFunction(
    () => document.querySelector("[data-stream-asof]")?.getAttribute("data-stream-asof"),
    null,
    { timeout: 20_000 },
  )
  .catch(() => {});
const firstAsOf = await asOf();
if (firstAsOf === null) {
  die(`/todos is not connected to the node: the freshness mark reads ${JSON.stringify(
    await streamState(),
  )} and carries no reading.

A board that cannot say when it last heard from the node looks exactly like a
board where nothing has changed, which is the defect this check exists for. If
the state is "connecting" the stream never opened at all - check that
GET /api/stream answers, and that the ResponseController can reach the
connection through the router's middleware.

the door itself says: ${await probeTheDoor()}`);
}

// ------------------------------------------------------------ the control arm
//
// Nothing changes on the node. Two things must be true across the same window,
// and they pull in opposite directions, which is what makes the pair worth
// asserting:
//
//   the rendered rows are IDENTICAL - so the arm below cannot be satisfied by a
//     page that simply repaints on a timer without reading anything
//   the "as of" ADVANCES - because it rides the heartbeat, and a clock that
//     needed a change to move could not tell a quiet node from a dead one
//
// The window is "until the as of moves", not a fixed sleep. The server beats
// every five seconds and a fixed four-second wait is a race that passes on the
// machine it was written on - a check that fails when the box is busy teaches
// people to re-run it, which is how a real failure gets waved through.
const before = await rendered();
try {
  await page.waitForFunction(
    (was) => {
      const now = document.querySelector("[data-stream-asof]")?.getAttribute("data-stream-asof");
      return Boolean(now) && now !== was;
    },
    firstAsOf,
    { timeout: 20_000 },
  );
} catch {
  die(`the "as of" never advanced from ${firstAsOf} while the node was quiet.

It must read the HEARTBEAT and not the last event. Nothing changed on the node
during this window and nothing was going to - a clock that needs a change to
move cannot tell a quiet node from a dead one, which is the same picture and the
reason this mark exists. The door beats every five seconds; if this timed out,
either the beat is not being written or the panel is not listening to it.`);
}
const still = await rendered();
if (still !== before) {
  die(`the rendered queue changed with nothing changed on the node.

That makes the next assertion meaningless: a page that repaints on its own clock
would pass "the list changed after a row changed" without ever reading the node.

before:
${before}
after:
${still}`);
}

// ------------------------------------------- what the operator is in the middle of
//
// Set a filter, put the focus in it, and scroll the list. Everything below has
// to survive the change that lands next: a refresh that discards these turns a
// stale board into a hostile one, which is worse than not refreshing at all.
await page.selectOption("[data-todo-kind-filter]", "bug");
await page.locator("[data-todo-kind-filter]").focus();
const list = page.locator('ol[aria-label="todos across projects"]');
await list.evaluate((el) => {
  el.scrollTop = Math.min(120, Math.max(0, el.scrollHeight - el.clientHeight));
});
const scrolledTo = await list.evaluate((el) => el.scrollTop);
const filteredBefore = await rendered();

// ------------------------------------------------------------- the live arm
//
// One claim, made on the NODE, over the API. Nothing touches the browser.
await api("POST", `/api/todo/${ROW}/assignee`, { assignee: TAKER });

try {
  await page.waitForFunction(
    ([id, who]) =>
      document
        .querySelector(`li[data-todo-row="${id}"] [data-todo-assignee]`)
        ?.getAttribute("data-todo-assignee") === who,
    [ROW, TAKER],
    { timeout: 20_000 },
  );
} catch {
  die(`a row claimed on the node never reached the open page.

The row is ${ROW} and it was claimed by ${TAKER} over the API, with nothing
touching the browser - no click, no keypress, no reload. That is the operator's
report exactly: "why todo panel didnt reload itself on your reassignments".

the page still holds:
${await rendered()}${crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : ""}`);
}

const filteredAfter = await rendered();
if (filteredAfter === filteredBefore) {
  die(
    "the assignee attribute changed but the rendered queue did not - read the check, not the page",
  );
}

// ------------------------------------------- and the interaction is still theirs
const kindNow = await page.locator("[data-todo-kind-filter]").inputValue();
if (kindNow !== "bug") {
  die(`the arriving change reset the kind filter from "bug" to ${JSON.stringify(kindNow)}.

A refresh that discards what somebody set is worse than no refresh: it turns a
stale board into one that fights back.`);
}
const focused = await page.evaluate(() =>
  document.activeElement?.hasAttribute("data-todo-kind-filter") ? "filter" : "elsewhere",
);
if (focused !== "filter") {
  die("the arriving change moved the focus off the control the operator was using");
}
const scrollNow = await list.evaluate((el) => el.scrollTop);
if (scrollNow !== scrolledTo) {
  die(`the arriving change moved the list from scrollTop ${scrolledTo} to ${scrollNow}`);
}
// Every row still on screen is a bug, so the filter is still FILTERING and not
// merely still selected. A control that reads "bug" over an unfiltered list is
// the same lie one step later.
const kinds = await page
  .locator("li[data-todo-row]")
  .evaluateAll((nodes) => [...new Set(nodes.map((n) => n.getAttribute("data-todo-kind")))]);
if (kinds.length !== 1 || kinds[0] !== "bug") {
  die(
    `after the change landed the filtered list holds kinds ${JSON.stringify(kinds)}, want only "bug"`,
  );
}

// ------------------------------------------------------------- a status move
//
// The second fact a queue carries, so this is not a check about one attribute.
await api("POST", `/api/artifact/${ROW}/status`, { status: "active" });
try {
  await page.waitForFunction(
    (id) =>
      document
        .querySelector(`li[data-todo-row="${id}"] [data-todo-status]`)
        ?.getAttribute("data-todo-status") === "active",
    ROW,
    { timeout: 20_000 },
  );
} catch {
  die(`a status moved on the node never reached the open page:\n${await rendered()}`);
}

console.log(
  `the open queue followed the node with no interaction: ${ROW} claimed by ${TAKER} and moved to active, the filter, focus and scroll survived, and the "as of" advanced while the node was quiet`,
);
await context.close();
await browser.close();
