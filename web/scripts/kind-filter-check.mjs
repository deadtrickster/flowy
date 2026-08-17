/**
 * The two labels a todo carries, in a real browser: the KIND out of a closed
 * set, and the free TAGS beside it - both drawn, and both able to narrow the
 * list.
 *
 *   node scripts/kind-filter-check.mjs BASE_URL TOKEN BUG_TITLE PLAIN_TITLE \
 *        TAGGED_TITLE TAG
 *
 * BUG_TITLE is filed as a bug, PLAIN_TITLE is classified as nothing at all, and
 * TAGGED_TITLE carries TAG and no kind. Those three rows are what make the two
 * controls discriminating: a filter that returns everything and a filter that
 * returns nothing both look plausible on a page with one row on it.
 *
 * THE THIRD ROW IS THE POINT. An unclassified todo has to be readable, listed,
 * and absent from the list of bugs - all three - because "absent is a value" is
 * the compatibility claim the whole field rests on, and a console that quietly
 * dropped the rows with no kind would break the queue for everything raised
 * before today while every check about bugs still passed.
 *
 * Elements and attributes, never page text. The kind is read off the ROW's own
 * data-todo-kind, so "the list is filtered" is asserted about the rows rather
 * than about a sentence describing them.
 *
 * The node is asked first: an absence assertion against a store that never had
 * the row passes against a page that renders nothing at all.
 */

import { chromium } from "playwright";

const [base, token, bugTitle, plainTitle, taggedTitle, tag] = process.argv.slice(2);

if (!base || !token || !bugTitle || !plainTitle || !taggedTitle || !tag) {
  console.error(
    "usage: node scripts/kind-filter-check.mjs BASE_URL TOKEN BUG_TITLE PLAIN_TITLE " +
      "TAGGED_TITLE TAG",
  );
  process.exit(2);
}

const die = (why) => {
  console.error(why);
  process.exit(1);
};

/** What the node holds, so a page assertion is about the page. */
const answer = await fetch(`${base}/api/artifacts?type=memory&kind=todo&limit=1000`, {
  headers: { Authorization: `Bearer ${token}` },
});
if (!answer.ok) die(`the node refused the queue read: ${answer.status}`);
const held = (await answer.json()).artifacts;
const rowFor = (title) => held.find((a) => a.title === title);
for (const [title, want] of [
  [bugTitle, "bug"],
  [plainTitle, ""],
  [taggedTitle, ""],
]) {
  const row = rowFor(title);
  if (!row) die(`the node does not hold ${JSON.stringify(title)} - the seed is wrong`);
  if ((row.category ?? "") !== want) {
    die(
      `the node says ${JSON.stringify(title)} is filed as ${JSON.stringify(row.category ?? "")}, want ${JSON.stringify(want)} - the seed is wrong`,
    );
  }
}
if (!(rowFor(taggedTitle).tags ?? []).includes(tag)) {
  die(`the node does not hold ${JSON.stringify(tag)} on ${JSON.stringify(taggedTitle)}`);
}

const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1600, height: 1000 } });
const page = await context.newPage();
const crashes = [];
page.on("pageerror", (err) => crashes.push(String(err)));
await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
await page.goto(`${base}/todos`, { timeout: 20_000 }).catch(() => {});

const list = page.locator('ol[aria-label="todos across projects"]');
const rows = list.locator("li[data-todo-row]");
try {
  await list.waitFor({ state: "visible", timeout: 15_000 });
  await rows.first().waitFor({ state: "visible", timeout: 15_000 });
} catch {
  die(`/todos drew no rows${crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : ""}`);
}

/** shown is every row on the page now, with the kind off its own attribute. */
const shown = () =>
  rows.evaluateAll((nodes) =>
    nodes.map((n) => ({
      text: n.innerText,
      kind: n.getAttribute("data-todo-kind") ?? "",
      badge: n.querySelector("[data-todo-kind-badge]")?.getAttribute("data-todo-kind-badge") ?? "",
      tags: [...n.querySelectorAll("[data-todo-tag]")].map((t) => t.getAttribute("data-todo-tag")),
    })),
  );

const has = (view, title) => view.some((r) => r.text.includes(title));

/**
 * settled waits for the list to actually be what the filter asked for before
 * anything is asserted about it.
 *
 * Picking an option is one event and the repaint is another, so reading the rows
 * the instant selectOption resolves is a race - and a race in a check is worse
 * than no check, because it fails on a busy machine and passes on the one it was
 * written on. The predicate here IS the assertion; the read below it is what
 * turns a timeout into a sentence naming the rows that were there.
 */
async function settled(predicate, why) {
  try {
    await page.waitForFunction(predicate, null, { timeout: 5_000 });
  } catch {
    const rows = await shown();
    die(`${why}\nthe list holds: ${JSON.stringify(rows.map((r) => `${r.kind}|${r.text}`))}`);
  }
}

/** The rows on the page now, as the browser sees them. */
const eachRow = "[...document.querySelectorAll('li[data-todo-row]')]";

// Unfiltered: all three rows, the bug wearing its badge and the other two
// wearing none. A kind drawn on a row nobody classified would be this console
// inventing a value the node refused to invent.
let view = await shown();
for (const title of [bugTitle, plainTitle, taggedTitle]) {
  if (!has(view, title)) die(`/todos does not draw ${JSON.stringify(title)} unfiltered`);
}
const bugRow = view.find((r) => r.text.includes(bugTitle));
if (bugRow.badge !== "bug") die(`the bug's row wears the badge ${JSON.stringify(bugRow.badge)}`);
const plainRow = view.find((r) => r.text.includes(plainTitle));
if (plainRow.badge !== "") {
  die(`a todo nobody classified wears the badge ${JSON.stringify(plainRow.badge)}`);
}
const taggedRow = view.find((r) => r.text.includes(taggedTitle));
if (!taggedRow.tags.includes(tag)) {
  die(`the tagged row draws ${JSON.stringify(taggedRow.tags)} and not ${JSON.stringify(tag)}`);
}

// Narrowed to one kind. Every row that is left is a bug, which is the assertion
// that fails if the control is drawn and wired to nothing.
await page.selectOption("[data-todo-kind-filter]", "bug");
await settled(
  `() => ${eachRow}.length > 0 && ${eachRow}.every((n) => n.getAttribute("data-todo-kind") === "bug")`,
  "filtering to bugs left rows that are not bugs",
);
view = await shown();
if (!has(view, bugTitle)) die("filtering to bugs dropped the bug");
if (has(view, plainTitle)) die("filtering to bugs kept a todo nobody classified");
for (const row of view) {
  if (row.kind !== "bug") {
    die(`filtering to bugs left a row filed as ${JSON.stringify(row.kind)}`);
  }
}
if (!(await page.locator("[data-todo-filtered]").count())) {
  die("the page narrowed the list and does not say it is showing fewer rows");
}

// And to the ones nobody has classified, which is the state most of a real
// queue is in and therefore the one that has to be askable for.
await page.selectOption("[data-todo-kind-filter]", "-none-");
await settled(
  `() => ${eachRow}.length > 0 && ${eachRow}.every((n) => n.getAttribute("data-todo-kind") === "")`,
  "filtering to unclassified left rows that carry a kind",
);
view = await shown();
if (!has(view, plainTitle)) die("filtering to unclassified dropped an unclassified todo");
if (has(view, bugTitle)) die("filtering to unclassified kept the bug");

// The free label, from the other control. Tags are nobody's schema, so the list
// it offers is built from the rows on the page rather than from a fixed set.
await page.selectOption("[data-todo-kind-filter]", "");
await page.selectOption("[data-todo-tag-filter]", tag);
await settled(
  `() => ${eachRow}.length > 0 && ${eachRow}.every((n) => n.querySelector('[data-todo-tag="${tag}"]'))`,
  `filtering to the tag ${tag} left rows that do not carry it`,
);
view = await shown();
if (!has(view, taggedTitle)) die(`filtering to the tag ${JSON.stringify(tag)} dropped its own row`);
if (has(view, bugTitle)) die("filtering to a tag kept a row that does not carry it");

// Clicking a tag on a row is the same filter, which is what makes a drawn tag
// worth drawing: the answer to "what else is tagged like this" is one click.
await page.locator("[data-todo-filter-clear]").click();
await page.locator(`[data-todo-tag="${tag}"]`).first().click();
await settled(
  `() => ${eachRow}.length > 0 && ${eachRow}.every((n) => n.querySelector('[data-todo-tag="${tag}"]'))`,
  "clicking a tag on a row did not narrow the list to that tag",
);
view = await shown();
if (!has(view, taggedTitle) || has(view, bugTitle)) {
  die("clicking a tag on a row did not narrow the list to that tag");
}

// And clearing puts the queue back, whole. A filter nobody can get out of is a
// list that lies from the second click onwards.
await page.locator("[data-todo-filter-clear]").click();
await settled(
  "() => !document.querySelector('[data-todo-filtered]')",
  "the page still says it is filtered after the filter was cleared",
);
view = await shown();
for (const title of [bugTitle, plainTitle, taggedTitle]) {
  if (!has(view, title)) die(`clearing the filter did not bring ${JSON.stringify(title)} back`);
}
if (await page.locator("[data-todo-filtered]").count()) {
  die("the page says it is still filtered after the filter was cleared");
}

console.log(
  `kind and tag both narrow the queue: ${view.length} rows unfiltered, the bug badged, the unclassified one listed and out of the bugs`,
);
await context.close();
await browser.close();
