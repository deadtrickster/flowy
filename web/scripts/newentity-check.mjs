/**
 * A person makes a row, in a browser, and it lands where its list reads it.
 *
 *   node scripts/newentity-check.mjs BASE_URL TOKEN
 *
 * The operator's ask was "make all entity types user creatable" and the console
 * could make exactly two of nine things this store holds - a diagram, and a
 * todo from inside a room. This is the door, and the check is the journey
 * rather than the handler: type a title, pick what it is, press the button, and
 * end up on the row.
 *
 * THE ASSERTION THAT MATTERS IS THE SECOND ONE. Identity is written two ways
 * here - `kind` under type=memory for 344 rows, `type` itself for a handful -
 * so a create door that picked the wrong level would write rows that every
 * existing list quietly fails to show. The row this makes has to appear in the
 * list of its own type, read through the node's own query, which is the only
 * way to tell "it wrote something" from "it wrote something anybody will see".
 *
 * And the list of what can be made is CLOSED on purpose: offering both
 * spellings would make the ambiguity permanent. So the check also asserts that
 * the picker offers no option this door cannot fill in honestly.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/newentity-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const die = (why) => {
  console.error(why);
  process.exit(1);
};

const ask = async (path) => {
  const res = await fetch(`${base}${path}`, { headers: { Authorization: `Bearer ${token}` } });
  if (!res.ok) throw new Error(`GET ${path} -> ${res.status}`);
  return res.json();
};

const stamp = Date.now();
const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/new`, { timeout: 30_000 }).catch(() => {});

  const form = page.locator("[data-new-entity]");
  try {
    await form.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(`/new draws no form at all.${errors}`);
  }

  // THE CLOSED LIST. Every option has to be something this door can write; an
  // option that wrote the other spelling of identity would make the ambiguity
  // permanent, which is the whole reason the list is short.
  const offered = await page
    .locator("[data-new-entity-type] option")
    .evaluateAll((nodes) => nodes.map((n) => n.getAttribute("value")));
  const want = ["todo", "note", "report"];
  if (offered.join(",") !== want.join(",")) {
    die(`the picker offers [${offered.join(", ")}] and this door writes [${want.join(", ")}]`);
  }

  const title = `a note written from the console ${stamp}`;
  await page.locator("[data-new-entity-type]").selectOption("note");
  await page.locator("[data-new-entity-title]").fill(title);
  await page.locator("[data-new-entity-body]").fill("the reasoning goes here");
  await page.locator("[data-new-entity-write]").click();

  // ON THE ROW, not back on the form. A create that leaves somebody where they
  // started has written something they now have to go and find.
  const shown = page.locator("[data-artifact-title]");
  try {
    await shown.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    die(`writing a note left the browser at ${page.url()} without opening the row it made`);
  }
  const says = (await shown.first().textContent())?.trim();
  if (says !== title) {
    die(`the page it opened is titled ${JSON.stringify(says)}, want ${JSON.stringify(title)}`);
  }

  // AND IN THE LIST OF ITS OWN TYPE, through the node's own query. This is the
  // assertion that catches a door writing the level nothing reads.
  const notes = await ask("/api/artifacts?type=memory&kind=note&limit=200");
  const found = (notes.artifacts ?? []).find((a) => a.title === title);
  if (!found) {
    die(
      "the note is not in the list notes are read from - a row nothing shows is a row nobody has",
    );
  }
  if (found.visibility === "personal") {
    die("the note was written personal, so nobody else in the project can read it");
  }

  if (crashes.length) die(`the console threw while writing a row: ${crashes.join("; ")}`);
  console.log(
    `a person can write a row from /new: picked note, typed a title, landed on ${found.id}, and it is in the notes list at visibility ${found.visibility}`,
  );
} finally {
  await browser.close();
}
