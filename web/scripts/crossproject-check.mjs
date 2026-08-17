/**
 * The cross-project todo list, in a real browser, for TWO PRINCIPALS WITH
 * DIFFERENT REACH - and the scope line each of them is shown.
 *
 *   node scripts/crossproject-check.mjs BASE_URL WIDE_TOKEN NARROW_TOKEN \
 *        WIDE_PROJECT SHARED_PROJECT WIDE_ONLY_TITLE SHARED_TITLE
 *
 * SHARED_PROJECT holds SHARED_TITLE and both principals reach it. WIDE_PROJECT
 * holds WIDE_ONLY_TITLE and only the wide one reaches that. So the same page,
 * under one name, is two different lists - which is the fact this whole view
 * exists to make visible rather than the one it exists to hide.
 *
 * The discriminating claim is the last one, and it is not "the filter works".
 * A list that filters perfectly and then says "everything, across every
 * project" is the bug: two people compare notes about "the list", disagree
 * about whether a todo exists, and one of them is certain. So the narrow
 * principal's scope line has to say the SMALLER NUMBER, and it has to not name
 * the project it cannot read.
 *
 * Elements, never page text. "todos" is in the global navigation, and a
 * page-text check for it passes with the list entirely absent - this reads the
 * LIST, its ROWS, and the project off each row's own attribute.
 *
 * The node is asked first. An absence assertion against a store that never had
 * the row passes against a page that renders nothing at all.
 */

import { chromium } from "playwright";

const [base, wideToken, narrowToken, wideProject, sharedProject, wideOnly, shared] =
  process.argv.slice(2);

if (!base || !wideToken || !narrowToken || !wideProject || !sharedProject || !wideOnly || !shared) {
  console.error(
    "usage: node scripts/crossproject-check.mjs BASE_URL WIDE_TOKEN NARROW_TOKEN " +
      "WIDE_PROJECT SHARED_PROJECT WIDE_ONLY_TITLE SHARED_TITLE",
  );
  process.exit(2);
}

/** titles is what the node hands one token, straight off the API. */
async function titles(token) {
  const answer = await fetch(`${base}/api/artifacts?type=memory&kind=todo&limit=1000`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!answer.ok) {
    console.error(`the node refused the queue read: ${answer.status}`);
    process.exit(1);
  }
  const page = await answer.json();
  return page.artifacts.map((a) => a.title);
}

const wideHas = await titles(wideToken);
const narrowHas = await titles(narrowToken);
for (const [who, held, want, unwanted] of [
  ["the wide token", wideHas, [shared, wideOnly], []],
  ["the narrow token", narrowHas, [shared], [wideOnly]],
]) {
  for (const title of want) {
    if (!held.includes(title)) {
      console.error(`the node does not hand ${who} ${JSON.stringify(title)} - the seed is wrong`);
      process.exit(1);
    }
  }
  for (const title of unwanted) {
    if (held.includes(title)) {
      console.error(`the node hands ${who} ${JSON.stringify(title)} - the seed is wrong`);
      process.exit(1);
    }
  }
}

const browser = await chromium.launch();

/** read opens /todos as one principal and reports what the page says. */
async function read(token) {
  const context = await browser.newContext({ viewport: { width: 1600, height: 1000 } });
  const page = await context.newPage();
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/todos`, { timeout: 20_000 }).catch(() => {});

  const list = page.locator('ol[aria-label="todos across projects"]');
  const rows = list.locator("li[data-todo-row]");
  const scope = page.locator("[data-todo-scope]");

  try {
    await list.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    console.error(
      `/todos has no list: no ol[aria-label="todos across projects"].
"todos" is in the global nav too, so this looks for the ELEMENT.${errors}`,
    );
    process.exit(1);
  }
  // Wait for a ROW. The list paints from mount with its empty state in it and
  // the rows arrive one fetch later, so reading it the moment it appears
  // asserts on the empty state and fails a page that works.
  try {
    await rows.first().waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    console.error(`/todos rendered the list and no rows at all:\n${await list.innerText()}`);
    process.exit(1);
  }
  if (!(await scope.count())) {
    console.error(
      `/todos draws rows and no scope line: nothing carries data-todo-scope.
A list across projects that does not say whose union it is, is the failure this page is for.`,
    );
    process.exit(1);
  }

  const named = await page
    .locator("[data-scope-project]")
    .evaluateAll((nodes) => nodes.map((n) => n.getAttribute("data-scope-project")));
  const said = Number(await scope.getAttribute("data-project-count"));
  const line = (await scope.innerText()).trim();
  const shownRows = await rows.evaluateAll((nodes) =>
    nodes.map((n) => ({
      text: n.innerText,
      project: n.querySelector("[data-todo-project]")?.getAttribute("data-todo-project") ?? null,
    })),
  );
  await context.close();
  return { named, said, line, rows: shownRows };
}

/** projectOf finds the row whose text holds a title, and the project it names. */
function projectOf(view, title) {
  const row = view.rows.find((r) => r.text.includes(title));
  return row ? row.project : undefined;
}

const die = (why, view) => {
  console.error(`${why}\nthe scope line reads: ${JSON.stringify(view.line)}`);
  process.exit(1);
};

try {
  const wide = await read(wideToken);
  const narrow = await read(narrowToken);

  // The wide reader: both todos, each labelled with the project it is in. A
  // cross-project list where two identically titled rows cannot be told apart
  // is worse than no list, so the project is asserted PER ROW.
  if (projectOf(wide, shared) !== sharedProject) {
    die(
      `the wide reader's row for ${JSON.stringify(shared)} says project ${JSON.stringify(projectOf(wide, shared))}, want ${sharedProject}`,
      wide,
    );
  }
  if (projectOf(wide, wideOnly) !== wideProject) {
    die(
      `the wide reader's row for ${JSON.stringify(wideOnly)} says project ${JSON.stringify(projectOf(wide, wideOnly))}, want ${wideProject}`,
      wide,
    );
  }
  for (const name of [sharedProject, wideProject]) {
    if (!wide.named.includes(name)) {
      die(
        `the wide reader's scope line does not name ${name}: ${JSON.stringify(wide.named)}`,
        wide,
      );
    }
  }

  // The narrow reader: the shared todo, and NOT the other one. The presence
  // assertion comes first, because "this title is absent" is trivially true of
  // a page that never rendered.
  if (projectOf(narrow, shared) !== sharedProject) {
    die(
      `the narrow reader's row for ${JSON.stringify(shared)} says project ${JSON.stringify(projectOf(narrow, shared))}, want ${sharedProject}`,
      narrow,
    );
  }
  if (projectOf(narrow, wideOnly) !== undefined) {
    die(
      `the narrow reader's list holds ${JSON.stringify(wideOnly)}, out of a project it cannot read`,
      narrow,
    );
  }

  // And the half that matters. The narrow reader's list is smaller, and the
  // page has to SAY it is smaller: a correct filter under a line claiming the
  // whole fleet is exactly the disagreement this is meant to prevent.
  if (narrow.named.includes(wideProject)) {
    die(
      `the narrow reader's scope line names ${wideProject}, which it can read nothing in: ${JSON.stringify(narrow.named)}`,
      narrow,
    );
  }
  if (!narrow.named.includes(sharedProject)) {
    die(
      `the narrow reader's scope line does not name ${sharedProject}: ${JSON.stringify(narrow.named)}`,
      narrow,
    );
  }
  if (!(narrow.said < wide.said)) {
    die(
      `the narrow reader is told it reads ${narrow.said} project(s) and the wide one ${wide.said} - the smaller list claims the same reach`,
      narrow,
    );
  }
  // The number in the words, not only in the attribute. What a person reads is
  // the sentence.
  for (const [who, view] of [
    ["wide", wide],
    ["narrow", narrow],
  ]) {
    if (view.said !== view.named.length) {
      die(`the ${who} reader is told ${view.said} projects and shown ${view.named.length}`, view);
    }
    if (!new RegExp(`\\b${view.said} projects? you can read\\b`).test(view.line)) {
      die(`the ${who} reader's scope line does not say "${view.said} projects you can read"`, view);
    }
  }

  console.log(
    `/todos: wide reads ${wide.said} projects [${wide.named}] and ${wide.rows.length} todos, ` +
      `narrow reads ${narrow.said} [${narrow.named}] and ${narrow.rows.length}, each row labelled`,
  );
} finally {
  await browser.close();
}
