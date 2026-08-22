/**
 * The openspec console, in a real browser, asserted on ELEMENTS.
 *
 *   node scripts/openspec-check.mjs BASE_URL TOKEN
 *
 * The board and the row are the two pages p5 added, and neither claim about
 * them can be made from the page's text alone - the words "openspec" and the
 * change's title also appear in the nav and in the breadcrumb, so this looks
 * for the elements that say WHICH row and WHICH kind.
 *
 * The row view is the slice's surface: a change is a directory of markdown
 * files in fields.openspec.files, and the page draws them per file rather than
 * as one body. Asserted per section:
 *
 *   - the files, by data-openspec-file, with a word that exists in no other
 *     file - a page that rendered the body field instead would show the words
 *     and none of the sections.
 *   - the discuss button, CLICKED: the whole value of the affordance is that
 *     it reaches the draft. The quote is the file's path, so the box must
 *     hold `> proposal.md` afterwards.
 *   - the derived todos, by data-openspec-todo, counted: the tasks.md lines
 *     became rows on the write (p2), and the door is the only read that names
 *     them.
 *   - the conflict edge, naming the OTHER change's id, off the door the pair
 *     was seeded to produce - two changes carrying the same specs/foo/spec.md.
 *   - the state and the verdict: proposed is written at create, and no
 *     validate door has run, so the page must say NOT VALIDATED rather than
 *     drawing nothing - a silent absence reads as a green one.
 *
 * And the pane beside the row is where the operator's p5 answer lands for
 * every document: threads in the discussion column. Seeded through the room's
 * say door - a root and a reply in its thread - then the page's directory
 * must list exactly one thread of two messages, each row a link into the
 * room's thread view.
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, token] = process.argv.slice(2);

if (!base || !token) {
  console.error("usage: node scripts/openspec-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

// The rows this writes cannot be deleted. Pointed at the dogfood node it would
// fill a board nobody asked for.
refuseRemote(base, "openspec-check");

const die = (why) => {
  console.error(why);
  process.exit(1);
};

/** ask talks to the node directly, for the seeding. */
const ask = async (path, init = {}) => {
  const res = await fetch(`${base}${path}`, {
    ...init,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${init.method ?? "GET"} ${path} -> ${res.status} ${text}`);
  return text ? JSON.parse(text) : null;
};

const stamp = Date.now();

// A spec, so the board holds both kinds.
const spec = await ask("/api/openspec", {
  method: "POST",
  body: JSON.stringify({
    kind: "spec",
    title: `the check capability ${stamp}`,
    body: `# the check capability ${stamp}\n\nSPECBODYWORD${stamp}\n`,
  }),
});

// Two changes carrying the same spec delta - specs/foo/spec.md is the clash -
// and one of them with a tasks.md whose two lines derive the todos.
const changeFiles = (word) => ({
  "proposal.md": `# change ${word}\n\nPROPOSALWORD${word}\n`,
  "tasks.md": `## Work\n\n- [ ] first task ${word}\n- [ ] second task ${word}\n`,
  "specs/foo/spec.md": `a delta on foo ${word}\n`,
});
const changeA = await ask("/api/openspec", {
  method: "POST",
  body: JSON.stringify({
    kind: "change",
    title: `the check change A ${stamp}`,
    fields: { openspec: { files: changeFiles(`A${stamp}`) } },
  }),
});
const changeB = await ask("/api/openspec", {
  method: "POST",
  body: JSON.stringify({
    kind: "change",
    title: `the check change B ${stamp}`,
    fields: { openspec: { files: changeFiles(`B${stamp}`) } },
  }),
});

// A thread in the change's own room: a root, then a reply in its thread. The
// pane's directory has to list one thread of two messages. The reply names no
// thread on purpose: the say door hands a parentless message a fresh thread id
// and a reply that names none the thread of what it answers - seeding the
// door's own shape, not a guess at it.
const room = `doc-${changeA.id}`;
const root = await ask(`/api/chat/${encodeURIComponent(room)}/say`, {
  method: "POST",
  body: JSON.stringify({ body: `the root of the discussion ${stamp}`, parents: [] }),
});
await ask(`/api/chat/${encodeURIComponent(room)}/say`, {
  method: "POST",
  body: JSON.stringify({
    body: `an answer in its thread ${stamp}`,
    parents: [root.id],
  }),
});

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  // THE BOARD: both kinds, each row answering which it is.
  await page.goto(`${base}/openspec`, { timeout: 20_000 }).catch(() => {});
  const list = page.locator('ul[aria-label="openspec"]');
  try {
    await list.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(
      `/openspec has no board list: no ul[aria-label="openspec"].\n"openspec" is in the nav too, so this looks for the ELEMENT.${errors}`,
    );
  }
  for (const [row, kind] of [
    [changeA.id, "change"],
    [spec.id, "spec"],
  ]) {
    const li = list.locator(`li[data-openspec="${row}"]`);
    try {
      await li.waitFor({ state: "visible", timeout: 15_000 });
    } catch {
      die(`the board never showed ${kind} ${row}`);
    }
    const said = await li.getAttribute("data-openspec-kind");
    if (said !== kind) {
      die(
        `${row} says it is ${JSON.stringify(said)}, want ${kind} - the board must not guess a kind from a title`,
      );
    }
  }

  // THE ROW: the change drawn as the directory of files it is.
  await page.goto(`${base}/openspec/${changeA.id}`, { timeout: 20_000 }).catch(() => {});
  const title = page.locator("[data-openspec-title]");
  try {
    await title.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    die("the row view has no title element: no [data-openspec-title]");
  }
  const titleKind = await title.getAttribute("data-openspec-kind");
  if (titleKind !== "change") {
    die(`the row view says kind ${JSON.stringify(titleKind)}, want change`);
  }

  // The lifecycle and the verdict: proposed written at create, and NOT
  // VALIDATED said out loud rather than drawn as a silent nothing.
  for (const sel of ['[data-openspec-state="proposed"]', '[data-openspec-verdict="none"]']) {
    try {
      await page.locator(sel).waitFor({ state: "visible", timeout: 10_000 });
    } catch {
      die(
        `the row view is missing ${sel} - a change created at the door is proposed, and an unvalidated row must say so`,
      );
    }
  }

  // The files, each its own section, with a word that lives in only one of
  // them. The body field of a change row is not the change, so this is the
  // difference between the page and a wrong page that would show the words.
  const proposal = page.locator('section[data-openspec-file="proposal.md"]');
  try {
    await proposal.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    die(
      `the row view never drew section[data-openspec-file="proposal.md"] - a change is a directory of files, and this page renders each as a document`,
    );
  }
  const proposalText = await proposal.innerText();
  if (!proposalText.includes(`PROPOSALWORDA${stamp}`)) {
    die(`the proposal section does not hold its file's word:\n${proposalText.slice(0, 300)}`);
  }
  try {
    await page
      .locator('section[data-openspec-file="tasks.md"]')
      .waitFor({ state: "visible", timeout: 10_000 });
  } catch {
    die("the row view never drew tasks.md - every file of the directory is a section");
  }

  // THE DISCUSS BUTTON, CLICKED. Its whole value is that it reaches the
  // draft; a button that only looks right passes nothing.
  try {
    await page.locator('button[data-file-discuss="proposal.md"]').click({ timeout: 8_000 });
  } catch (err) {
    die(`the proposal's discuss button cannot be clicked: ${err}`);
  }
  // The draft is the button's whole value, and it lands in the box one render
  // after the click - read it as a state, not a sample. The first gate run
  // read it immediately and got "", which is what the box holds for the one
  // frame between the click and the effect.
  const quoted = await page
    .waitForFunction(
      () =>
        document.querySelector('textarea[aria-label="message"]')?.value.includes("> proposal.md") ??
        false,
      { timeout: 5_000 },
    )
    .catch(() => false);
  if (!quoted) {
    const draft = await page.locator('textarea[aria-label="message"]').inputValue();
    die(
      `after discuss, the draft holds ${JSON.stringify(draft)}, want it to quote the file's path - the discussion has to name what it is about`,
    );
  }

  // THE DERIVED TODOS, counted: two tasks.md lines became two rows, and the
  // door is the read that names them.
  const todoRows = page.locator("li[data-openspec-todo]");
  try {
    await todoRows.first().waitFor({ state: "visible", timeout: 10_000 });
  } catch {
    die("the row view lists no derived todos - tasks.md derived two on the write");
  }
  if ((await todoRows.count()) !== 2) {
    die(`the row view lists ${await todoRows.count()} derived todos, want the two tasks.md lines`);
  }

  // THE CONFLICT EDGE, naming the other change. Both changes carry
  // specs/foo/spec.md, so the door must list an edge naming B. The wait is
  // for B's id rather than any edge at all: a reused database accumulates
  // pairs from earlier runs, and only an id minted this run can prove THIS
  // pair - an any-edge wait would pass on somebody else's clash.
  try {
    await page
      .locator(`li[data-openspec-conflict="${changeB.id}"]`)
      .waitFor({ state: "visible", timeout: 10_000 });
  } catch {
    die(
      `the row view lists no edge naming ${changeB.id} - the pair was seeded over specs/foo/spec.md`,
    );
  }

  // THE PANE'S THREAD DIRECTORY: one root, two messages, a link into the
  // room's thread view. This is what every document page gets, not just the
  // openspec one - this row is where it is driven because the page is new.
  const threadLinks = page.locator("aside a[data-thread-link]");
  try {
    await threadLinks.first().waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    die(`the discussion pane lists no threads - a root and a reply were seeded in ${room}`);
  }
  if ((await threadLinks.count()) !== 1) {
    die(
      `the discussion pane lists ${await threadLinks.count()} threads, want exactly the one seeded`,
    );
  }
  const threadText = await threadLinks.first().innerText();
  if (!threadText.includes("2 messages")) {
    die(
      `the thread directory says ${JSON.stringify(threadText)}, want a count of 2 - the node counted the root and its reply`,
    );
  }
  const href = await threadLinks.first().getAttribute("href");
  if (href !== `/chat/${room}/thread/${root.id}`) {
    die(`the thread links to ${JSON.stringify(href)}, want the room's thread view for ${root.id}`);
  }

  if (crashes.length > 0) die(`the page threw: ${crashes.join(" | ")}`);
  console.log(
    `/openspec: the board holds both kinds, the row draws the files with ${await todoRows.count()} derived todos and the edge to ${changeB.id}, discuss reaches the draft, and the pane lists the seeded thread of 2`,
  );
} finally {
  await browser.close();
}
