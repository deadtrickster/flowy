/**
 * A todo raised from a room carries the file it is about.
 *
 *   node scripts/todo-attach-check.mjs BASE_URL TOKEN ROOM
 *
 * THE OPERATOR, 01M0GGQ8D4: "no way to attach a file to todo from the chat
 * todo". The message box beside the panel has taken files since attachments
 * landed - paste a screenshot, it goes up and the room draws a card - and the
 * panel three centimetres to its right could take a sentence and nothing else.
 * So work raised out of a screenshot pointed at a title, and whoever picked the
 * row up a day later had to go back and read the room to find the evidence,
 * which is the errand the panel exists to end.
 *
 * WHAT THIS ASSERTS IS THE ROW, NOT THE PANEL. The file is chosen in the
 * browser because the upload path is browser code - the ceiling check, the
 * chunked base64, the picker - but the claim is settled by ASKING THE NODE for
 * the artifact the raise minted and reading its fields. A card drawn in a panel
 * that never reached the row would pass a DOM assertion and lose the file.
 *
 * Two readers, because they answer different questions and both were the point:
 * the ROW carries it, so the work is about the file wherever it is picked up;
 * and the ANNOUNCING MESSAGE carries it, so the room shows the file at the
 * moment it is raised.
 */

import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/todo-attach-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}

const bearer = { Authorization: `Bearer ${token}` };
const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

// A one pixel PNG, written to a temp file for the picker. Small on purpose: the
// ceiling and the chunking have their own tests, and this one is about whether
// the id arrives on the row.
const { writeFileSync, mkdtempSync } = await import("node:fs");
const { tmpdir } = await import("node:os");
const { join } = await import("node:path");
const pixel = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
  "base64",
);
const shot = join(mkdtempSync(join(tmpdir(), "flowy-todo-attach-")), "evidence.png");
writeFileSync(shot, pixel);

const title = `carries a file ${Date.now()}`;

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 }).catch(() => {});

  const tab = page.locator('[data-room-pane="todos"]');
  await tab.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await tab.count()) === 0) die("no todos tab on the room page");
  await tab.click();
  const panel = page.locator('[data-room-pane-body="todos"]');
  await panel.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
  if ((await panel.count()) === 0) die("the todos tab drew no todos pane");

  // THE CONTROL ITSELF. Named, because "there is no way to attach a file" is
  // the operator's own sentence and a missing button is the whole finding.
  const clip = page.locator("[data-todo-attach]");
  if ((await clip.count()) === 0) {
    die("the todo panel offers no way to attach a file - no [data-todo-attach] control");
  }

  await page.locator('input[aria-label="attach a file to this todo"]').setInputFiles(shot);
  const chip = page.locator("[data-todo-carried]");
  await chip
    .first()
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if ((await chip.count()) === 0) {
    die(`the file was picked and the panel is carrying nothing. The bytes go up when the
file is chosen, so either the write was refused or the chip is not drawn.`);
  }
  const carried = await chip.first().getAttribute("data-todo-carried");
  if (!carried) die("the panel is carrying a file with no attachment id");

  await page.locator('input[name="todo-title"]').fill(title);
  await page.getByRole("button", { name: "raise", exact: true }).click();

  // WAIT FOR THE ROW ON THE NODE, not for the panel to redraw. The panel is
  // refilled from a poll, so a check that watched the DOM here would be racing
  // the poll and would pass on a stale list.
  let row = null;
  for (let i = 0; i < 40 && !row; i++) {
    // The same door the panel is filled from - type, kind and room as a
    // narrowing on the artifact list, not a todo endpoint. Asking a door the
    // console does not use would be measuring a different reader.
    const r = await fetch(
      `${base}/api/artifacts?type=memory&kind=todo&room=${encodeURIComponent(room)}`,
      { headers: bearer },
    );
    if (r.ok) {
      const list = await r.json();
      row = (list.artifacts ?? []).find((item) => item.title === title) ?? null;
    }
    if (!row) await new Promise((done) => setTimeout(done, 500));
  }
  if (!row) die(`no todo titled ${JSON.stringify(title)} reached the node after 20s`);

  const named = String(row.fields?.attachments ?? "")
    .split(" ")
    .filter(Boolean);
  if (!named.includes(carried)) {
    die(`the row was raised and did not carry the file. fields.attachments is
${JSON.stringify(row.fields?.attachments ?? null)}, wanted it to name ${carried}.
The panel had the id in front of it, so the loss is between the panel and the door.`);
  }

  // AND THE ROOM SHOWS IT WHERE IT WAS RAISED. Same key, same encoding, which
  // is why MessageList needed nothing added for this.
  const messages = await fetch(
    `${base}/api/chat/${encodeURIComponent(room)}?order=recent&limit=20`,
    { headers: bearer },
  ).then((r) => (r.ok ? r.json() : die(`reading #${room} answered ${r.status}`)));
  const raise = (messages.events ?? []).find((e) => (e.body ?? "").includes(row.id));
  if (!raise) die(`nothing in #${room} announced ${row.id}`);
  const shown = String(raise.meta?.attachments ?? "")
    .split(" ")
    .filter(Boolean);
  if (!shown.includes(carried)) {
    die(`the raise was announced without the file: meta.attachments is
${JSON.stringify(raise.meta?.attachments ?? null)}. The row has it, so the room draws a
sentence about work whose evidence is one click away instead of in front of the reader.`);
  }

  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);
  console.log(`a todo raised from #${room} carries ${carried}, on the row and in the room`);
} finally {
  await browser.close();
}
