/**
 * Keeping a message puts it on a page only you can see, and dropping it takes
 * it off.
 *
 *   node scripts/bookmark-check.mjs BASE_URL TOKEN OTHER_TOKEN ROOM
 *
 * 01M0HGTV9B: "I should be able to bookmark messages". The room already had
 * pins and they answer a different question - a pin is the room saying "this is
 * what we decided", it changes everybody's strip, and putting one up is a claim
 * about the conversation. Somebody who wants to find their own way back to a
 * message tomorrow had nothing.
 *
 * THE ROUND TRIP IS THE CLAIM, so it goes through the browser twice: keep it in
 * a room, then find it on /bookmarks, then drop it there and see the room's own
 * control agree. A list that filled and never emptied would pass anything
 * shorter.
 *
 * THE PRIVACY IS ASSERTED OVER THE WIRE as the other token, because that is the
 * half that cannot be seen from the page that owns the list. It is the same
 * question the store test asks in-process, and a rule that is only true inside
 * the process is a rule that has not been tested.
 */

import { chromium } from "playwright";

// THREE PRINCIPALS, and the third is not decoration.
//
// SPEAKER has to be in the READER's project or the message lands in another
// project's #general and the reader never sees it - the same trap unread-check
// records, and the one this file fell into on its first run: "no way to keep a
// message" about a message that was never on the page.
//
// STRANGER is somebody else entirely, and is asked whether the bookmark reached
// them. Deliberately a different project as well as a different person: the
// same-project non-party is the sharper question and the store test asks it
// in-process, with a reader who wrote the message and can read it. Between them
// the two cover it.
const [base, token, speaker, stranger, room] = process.argv.slice(2);
if (!base || !token || !speaker || !stranger || !room) {
  console.error(
    "usage: node scripts/bookmark-check.mjs BASE_URL TOKEN SPEAKER_TOKEN STRANGER_TOKEN ROOM",
  );
  process.exit(2);
}

const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

const call = async (path, init = {}, as = token) => {
  const r = await fetch(`${base}${path}`, {
    ...init,
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${as}`, ...init.headers },
  });
  const text = await r.text();
  let body = text;
  try {
    body = JSON.parse(text);
  } catch {
    /* left as text: a refusal is a sentence */
  }
  return { ok: r.ok, status: r.status, body };
};

const said = await call(
  `/api/chat/${encodeURIComponent(room)}/say`,
  { method: "POST", body: JSON.stringify({ body: "bookmark-check: the one worth keeping" }) },
  speaker,
);
if (!said.ok) die(`the probe was not accepted: HTTP ${said.status} ${JSON.stringify(said.body)}`);
const message = said.body.id;

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 950 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 }).catch(() => {});

  const control = page.locator(`[data-bookmark="${message}"]`);
  await control.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await control.count()) === 0) {
    die(`no way to keep a message - no [data-bookmark] beside ${message}`);
  }
  if (crashes.length > 0) die(`the room threw: ${crashes.join("; ")}`);
  if ((await control.getAttribute("data-kept")) !== "false") {
    die("the message is drawn as kept before anybody kept it");
  }

  await control.click();
  await page
    .locator(`[data-bookmark="${message}"][data-kept="true"]`)
    .waitFor({ state: "visible", timeout: 15_000 })
    .catch(() => {});
  if ((await control.getAttribute("data-kept")) !== "true") {
    die(`the control still says "keep" after being pressed. The node's answer is what
lands in state here, so this is either a refused write or a list that was not re-read.`);
  }

  // ON THE PAGE, which is the point of keeping it.
  await page.goto(`${base}/bookmarks`, { timeout: 20_000 }).catch(() => {});
  const kept = page.locator(`[data-kept-message="${message}"]`);
  await kept.waitFor({ state: "visible", timeout: 15_000 }).catch(() => {});
  if ((await kept.count()) === 0) {
    die("the message was kept and /bookmarks does not show it.");
  }
  const shown = await kept.textContent();
  if (!shown?.includes("the one worth keeping")) {
    die(`the page lists the message and not its words: ${JSON.stringify(shown?.slice(0, 120))}.
A page of ULIDs is a page nobody can read, which is why this list carries the
messages and not only their ids.`);
  }
  const where = await kept.locator("[data-kept-in]").getAttribute("data-kept-in");
  if (where !== room) {
    die(`the kept message says it was said in ${where}, not #${room} - "find my way back"
is the whole reason somebody kept it.`);
  }

  // NOBODY ELSE'S. Asked over the wire as the other token, which is the half
  // the page cannot answer about itself.
  const theirs = await call("/api/bookmarks", {}, stranger);
  if (!theirs.ok) die(`/api/bookmarks answered ${theirs.status} for the stranger's token`);
  if ((theirs.body.kept ?? []).includes(message)) {
    die(`the stranger's own list holds ${message}. A bookmark is private because it
carries no project and no room; this one reached another reader.`);
  }

  // AND IT COMES OFF.
  await kept.locator(`[data-drop="${message}"]`).click();
  await kept.waitFor({ state: "detached", timeout: 15_000 }).catch(() => {});
  if ((await page.locator(`[data-kept-message="${message}"]`).count()) !== 0) {
    die("dropping it left it on the page");
  }
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 20_000 }).catch(() => {});
  const again = page.locator(`[data-bookmark="${message}"]`);
  await again.waitFor({ state: "visible", timeout: 15_000 }).catch(() => {});
  if ((await again.getAttribute("data-kept")) !== "false") {
    die(`the room still draws ${message} as kept after it was dropped on the other page.
Two surfaces disagreeing about one list is the state this is built to avoid.`);
  }

  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);
  console.log(
    `kept ${message}, found it on /bookmarks with its room, dropped it, and both surfaces agree`,
  );
} finally {
  await browser.close();
}
