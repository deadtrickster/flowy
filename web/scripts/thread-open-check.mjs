/**
 * Opening a thread is not quoting a message.
 *
 *   node scripts/thread-open-check.mjs BASE_URL TOKEN OTHER_TOKEN ROOM
 *
 * THE OPERATOR, 01M0HGRFN5: clicking `thread ...` at the bottom of a message
 * opens the thread pane "|ANd cites the message shouldnt cite".
 *
 * The two thread controls called onSelect, which is what arms a citation - it
 * is how reply quotes the message it is answering - so "show me this
 * conversation" and "quote this message" were one gesture. The operator had
 * complained about that same collapse for message clicks two days earlier; it
 * came back in a new control because the control was wired to the handler that
 * was already there.
 *
 * BOTH HALVES, because either one alone is satisfiable by a broken build: the
 * pane must show the thread that was opened, AND no citation may be armed. A
 * control that armed nothing and opened nothing would pass the second on its
 * own.
 *
 * The positive control is `cite` on the same message: it must still arm one.
 * Without it this check passes against a console where citing is broken
 * everywhere, which is a worse bug than the one it is about.
 */

import { chromium } from "playwright";

const [base, token, other, room] = process.argv.slice(2);
if (!base || !token || !other || !room) {
  console.error("usage: node scripts/thread-open-check.mjs BASE_URL TOKEN OTHER_TOKEN ROOM");
  process.exit(2);
}

const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

const say = async (body, as) => {
  const r = await fetch(`${base}/api/chat/${encodeURIComponent(room)}/say`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${as}` },
    body: JSON.stringify(body),
  });
  if (!r.ok) die(`saying in #${room} answered ${r.status} ${await r.text()}`);
  return r.json();
};

// A thread with a shape, so the pane has something to be right about.
const root = await say({ body: "thread-open-check: the root of a thread" }, other);
await say({ body: "thread-open-check: a reply in it", thread: root.thread }, other);

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 950 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 }).catch(() => {});

  const open = page.locator(`[data-thread-open="${root.id}"]`);
  await open.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await open.count()) === 0) die(`no thread control on message ${root.id}`);
  if (crashes.length > 0) die(`the room threw: ${crashes.join("; ")}`);

  if ((await page.locator("[data-citation]").count()) > 0) {
    die("a citation was armed before anything was clicked");
  }

  await open.click();

  // IT OPENED, and on the thread that was asked for.
  const pane = page.locator('[data-room-pane-body="thread"]');
  await pane.waitFor({ state: "visible", timeout: 15_000 }).catch(() => {});
  if ((await pane.count()) === 0) die("clicking the thread control opened no thread pane");
  const showing = await page
    .locator("[data-thread-pane-id]")
    .first()
    .getAttribute("data-thread-pane-id")
    .catch(() => null);
  if (showing !== root.thread) {
    die(
      `the pane is showing ${showing ?? "nothing"}, not the thread ${root.thread} that was opened`,
    );
  }

  // AND IT QUOTED NOTHING.
  const armed = await page.locator("[data-citation]").count();
  if (armed > 0) {
    const what = await page.locator("[data-citation]").first().getAttribute("data-citation");
    die(`opening the thread armed a citation of ${what}.
Wanting to read a conversation is not wanting to quote the message it starts
from, and the composer now sends a reply nobody asked to attach.`);
  }

  // THE POSITIVE CONTROL: citing still cites. Back on the room's own transcript.
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 20_000 }).catch(() => {});
  const cite = page.locator(`[data-cite="${root.id}"]`);
  await cite.waitFor({ state: "visible", timeout: 15_000 }).catch(() => {});
  if ((await cite.count()) === 0) die(`no cite control on message ${root.id}`);
  await cite.click();
  const preview = page.locator(`[data-citation="${root.id}"]`);
  await preview.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
  if ((await preview.count()) === 0) {
    die(`cite armed nothing on ${root.id}, so the assertion above says nothing:
a console that cites nowhere would pass it.`);
  }

  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);
  console.log(`opening thread ${root.thread} quoted nothing, and cite still quotes ${root.id}`);
} finally {
  await browser.close();
}
