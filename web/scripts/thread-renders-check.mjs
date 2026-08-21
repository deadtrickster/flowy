/**
 * A thread message shows what a room message shows.
 *
 *   node scripts/thread-renders-check.mjs BASE_URL TOKEN SPEAKER_TOKEN ROOM
 *
 * THE OPERATOR, 01M0HP4N06: "messages in threads dont show attachements". The
 * cause was bigger than attachments. The thread pane is a SECOND renderer of the
 * same events, and its entire message was one span of event.body - so it had no
 * markdown, no mentions, no citation and no cards, and every feature the room
 * grew since the pane was written had to be added to the pane by hand. None had
 * been.
 *
 * SO IT ASSERTS THE PAIR, not the pane alone: one message, drawn in the room and
 * drawn in the thread, and what the room shows the thread must show. A check
 * that only looked at the thread would pass on a console where the room had
 * quietly lost the feature too, and would have to be rewritten every time a
 * fifth thing is added to a message.
 */

import { chromium } from "playwright";

const [base, token, speaker, room] = process.argv.slice(2);
if (!base || !token || !speaker || !room) {
  console.error("usage: node scripts/thread-renders-check.mjs BASE_URL TOKEN SPEAKER_TOKEN ROOM");
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
  try {
    return { ok: r.ok, status: r.status, body: JSON.parse(text) };
  } catch {
    return { ok: r.ok, status: r.status, body: text };
  }
};

// A one pixel PNG, so the message carries something real rather than an id that
// resolves to nothing.
const png = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
  "base64",
);
const written = await call(
  "/api/attachment",
  {
    method: "POST",
    body: JSON.stringify({
      content_base64: png.toString("base64"),
      title: "thread-renders-check evidence",
      filename: "evidence.png",
      content_type: "image/png",
      room,
    }),
  },
  speaker,
);
if (!written.ok) die(`could not write the attachment: HTTP ${written.status}`);
const attachment = written.body.item.id;

// The root, and a reply in its thread carrying the attachment and some markdown.
const root = await call(
  `/api/chat/${encodeURIComponent(room)}/say`,
  { method: "POST", body: JSON.stringify({ body: "thread-renders-check: the root" }) },
  speaker,
);
if (!root.ok) die(`the root was refused: HTTP ${root.status}`);
const reply = await call(
  `/api/chat/${encodeURIComponent(room)}/say`,
  {
    method: "POST",
    body: JSON.stringify({
      body: "thread-renders-check: **bold** in a reply",
      thread: root.body.thread,
      parents: [root.body.id],
      attachments: [attachment],
    }),
  },
  speaker,
);
if (!reply.ok) die(`the reply was refused: HTTP ${reply.status} ${JSON.stringify(reply.body)}`);
const message = reply.body.id;

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 950 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  // ---- what the ROOM shows, which is the standard the pane is held to ----
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 }).catch(() => {});
  const inRoom = page.locator(`[data-body="${message}"]`);
  await inRoom.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await inRoom.count()) === 0) die(`the room did not draw ${message}`);
  const roomBold = await inRoom.locator("strong").count();
  const roomCards = await page.locator(`[data-attachment="${attachment}"]`).count();
  if (roomBold === 0 || roomCards === 0) {
    die(`the ROOM is not drawing this message properly - bold=${roomBold} cards=${roomCards}.
The pane is measured against the room, so there is nothing to measure against.`);
  }

  // ---- and the thread pane, opened the way a reader opens it ----
  const open = page.locator(`[data-thread-open="${message}"]`);
  await open.waitFor({ state: "visible", timeout: 15_000 }).catch(() => {});
  if ((await open.count()) === 0) die(`no thread control on ${message}`);
  await open.click();
  const pane = page.locator('[data-room-pane-body="thread"]');
  await pane.waitFor({ state: "visible", timeout: 15_000 }).catch(() => {});
  if ((await pane.count()) === 0) die("clicking the thread control opened no pane");

  const inThread = pane.locator(`[data-body="${message}"]`);
  await inThread.waitFor({ state: "visible", timeout: 15_000 }).catch(() => {});
  if ((await inThread.count()) === 0) {
    die(`the thread pane drew no rendered body for ${message}. It used to draw the
raw text in a span, which is the whole of 01M0HP4N06: one renderer had every
feature and the other had none.`);
  }
  if ((await inThread.locator("strong").count()) === 0) {
    die("the thread pane is not rendering markdown - the room shows bold and the pane does not");
  }
  if ((await pane.locator(`[data-attachment="${attachment}"]`).count()) === 0) {
    die(`the thread pane shows no card for ${attachment}, which is the sentence the
operator wrote: "messages in threads dont show attachements".`);
  }

  // ---- and clicking the words still selects the message ----
  //
  // The body moved OUT of the row's button so links and cards could work, and
  // that took the click with it - reported in review rather than caught here,
  // which is why the assertion exists now. The row keeps the click, guarded on
  // what was clicked, and this is the half a reader notices.
  await inThread.click();
  const armed = page.locator(`[data-citation="${message}"]`);
  await armed.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
  if ((await armed.count()) === 0) {
    die(`clicking the words of a thread message selected nothing. The body sits
outside the row's button now so that its links work; the row has to keep the
click or a reader who clicks a message to answer it gets silence.`);
  }

  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);
  console.log(
    `the thread pane draws ${message} with its markdown and its attachment, as the room does, and clicking its words selects it`,
  );
} finally {
  await browser.close();
}
