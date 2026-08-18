/**
 * A person fixes a typo in something they wrote.
 *
 *   node scripts/editwords-check.mjs BASE_URL TOKEN
 *
 * /new landed this afternoon and a row could be written and never corrected,
 * which is half of what the operator asked for - "user creatable/editable".
 *
 * TWO RULES AND THE STORE DECIDES BOTH. An item's title and body are its
 * AUTHOR'S: a stranger rewriting them is refused in one sentence. Its queue
 * metadata - status, assignee, category - moves for anybody who can read it.
 * So the page offers the editor on the words and only to the owner, and this
 * drives that: the control is there for the author, the save lands, and the
 * page shows the new words rather than the old ones.
 *
 * The refusal for a stranger is asserted at the store, where it lives. What a
 * browser can add is that the CONTROL is not offered to somebody the node
 * would refuse - a page that showed it to everybody would be handing people a
 * refusal instead of a control.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/editwords-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const die = (why) => {
  console.error(why);
  process.exit(1);
};

const stamp = Date.now();
const made = await fetch(`${base}/api/artifacts`, {
  method: "POST",
  headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
  body: JSON.stringify({
    type: "memory",
    kind: "note",
    title: `a note with a typo in it ${stamp}`,
    body: "the frist line is wrong",
    visibility: "project",
  }),
}).then((r) => r.json());

if (!made.id) die(`could not write the note to edit: ${JSON.stringify(made)}`);

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1200, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page
    .goto(`${base}/p/${made.project}/memory/${made.id}`, { timeout: 30_000 })
    .catch(() => {});

  const open = page.locator("[data-edit-open]");
  try {
    await open.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(`the author of a row is offered no way to fix its words.${errors}`);
  }
  await open.click();

  const fixed = `a note with the typo fixed ${stamp}`;
  await page.locator("[data-edit-title]").fill(fixed);
  await page.locator("[data-edit-body]").fill("the first line is right now");
  await page.locator("[data-edit-save]").click();

  // THE PAGE SHOWS WHAT THE NODE HAS, not what was typed. Waited for the
  // editor to close and the title element to carry the new words: a save that
  // updated the form and not the row would look identical while it was open.
  const title = page.locator(`[data-artifact-title="${made.id}"]`);
  try {
    await title.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const refused = await page
      .locator("[data-edit-refused]")
      .innerText()
      .catch(() => "");
    die(`saving left the editor open${refused ? `, refused: ${refused}` : " and said nothing"}`);
  }
  const says = ((await title.innerText()) || "").trim();
  if (says !== fixed) {
    die(`after saving, the page still says ${JSON.stringify(says)}`);
  }

  // AND THE NODE AGREES. A console that repainted its own draft would pass
  // everything above.
  const back = await fetch(`${base}/api/artifact/${made.id}`, {
    headers: { Authorization: `Bearer ${token}` },
  }).then((r) => r.json());
  if (back.title !== fixed) {
    die(`the node still holds ${JSON.stringify(back.title)} - the page repainted its own draft`);
  }
  if (back.body !== "the first line is right now") {
    die(`the body did not land: ${JSON.stringify(back.body)}`);
  }

  if (crashes.length) die(`the console threw while editing: ${crashes.join("; ")}`);
  console.log(`the author fixed the words of ${made.id} and the node holds the new ones`);
} finally {
  await browser.close();
}
