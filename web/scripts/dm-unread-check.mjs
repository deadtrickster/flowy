/**
 * A direct message says it is waiting, and stops saying it once it is read.
 *
 *   node scripts/dm-unread-check.mjs BASE_URL READER_TOKEN SENDER_TOKEN
 *
 * 01M0GP1S0K: the private log had no read mark anywhere. api.dms and api.dmWait
 * both take a raw cursor, and the only thing holding it was the open tab, so
 * nothing on the node knew which private messages a person had read. The rail's
 * direct row was shipped SILENT on purpose - the only number available was "how
 * many DMs exist", and a badge that never clears standing next to two that do
 * costs the reader all three.
 *
 * So the assertion is not "a badge appears". It is that reading the message
 * moves a mark ON THE NODE - asked of the node, not of the badge, because a
 * console that clears its own number and tells nobody looks identical from the
 * outside until the tab closes.
 *
 * WHY THERE IS NO RELOAD ASSERTION HERE, though the row's whole subject is a
 * mark that outlives the tab: this console DELETES its reader rows on pagehide,
 * deliberately - see lib/unread, "a closed tab takes its bookmarks with it" -
 * so after a reload every mark is re-declared at the head and everything older
 * reads as already seen. A reload therefore shows an empty badge whatever the
 * node holds, which is a check that cannot fail. It passed against a build with
 * the ack deleted, which is how this was found.
 *
 * It navigates by CLICKING rather than by goto for the same reason. A full page
 * load fires pagehide, so the second goto in a check deletes the very row the
 * first one declared, and the count that comes back is about a mark half a
 * second old.
 */

import { chromium } from "playwright";

const [base, reader, sender] = process.argv.slice(2);
if (!base || !reader || !sender) {
  console.error("usage: node scripts/dm-unread-check.mjs BASE_URL READER_TOKEN SENDER_TOKEN");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const whoami = async (token) => {
  const res = await fetch(`${base}/api/whoami`, { headers: { Authorization: `Bearer ${token}` } });
  if (!res.ok) die(`whoami: ${res.status} ${await res.text()}`);
  return res.json();
};

const me = await whoami(reader);
if (!me.user) die("the reader token resolves to nobody, so nothing can be addressed to it");

const stamp = Date.now().toString(36);
const send = async (body) => {
  const res = await fetch(`${base}/api/dm/${encodeURIComponent(me.user)}`, {
    method: "POST",
    headers: { Authorization: `Bearer ${sender}`, "Content-Type": "application/json" },
    body: JSON.stringify({ body }),
  });
  if (!res.ok) die(`could not send a dm: ${res.status} ${await res.text()}`);
  return res.json();
};

// THE READER HAS TO EXIST BEFORE THE MESSAGE DOES, and this is a fact about
// the feature rather than a trick to make the check pass. A console reader is
// declared AT THE HEAD on its first refresh, because everything said before
// somebody first opened the console is history rather than unread - so a
// message sent in the window between the page loading and that first refresh
// is, correctly, already read. Measured while writing this: the DM's seq_hlc
// and the fresh reader's cursor were the same number, and the badge was
// correctly 0 about a message that had just arrived.
const readerExists = async (page) => {
  for (let i = 0; i < 60; i++) {
    const res = await fetch(`${base}/api/inbox/readers`, {
      headers: { Authorization: `Bearer ${reader}` },
    });
    if (res.ok) {
      const held = (await res.json()).readers ?? [];
      if (held.some((row) => row.reader === "console-dm")) return;
    }
    await page.waitForTimeout(500);
  }
  die(`the console never declared its console-dm reader, so there is no mark to move. See
lib/unread, which declares it beside the one per room on the first refresh.`);
};

// WHAT THE NODE THINKS IS UNREAD, which is where the mark actually lives.
//
// The badge cannot answer this. An absent badge means "no number is showing",
// and that is true both when the count is zero AND when the refresh has not
// come back yet - so an assertion that the badge reads 0 after a reload passes
// on a page that has not counted anything. Measured: with the ack deleted and
// the badge cleared locally, this check passed. The node is asked directly now,
// and the badge is asked about separately, because they are two claims.
const nodeUnread = async () => {
  const res = await fetch(`${base}/api/inbox/unread?as=console-dm&direct=1`, {
    headers: { Authorization: `Bearer ${reader}` },
  });
  if (!res.ok) die(`the node would not count the direct messages: ${res.status}`);
  return (await res.json()).unread;
};

const nodeSettles = async (page, want, what) => {
  for (let i = 0; i < 60; i++) {
    if (want(await nodeUnread())) return;
    await page.waitForTimeout(500);
  }
  die(`${what} - the node says ${await nodeUnread()} unread under console-dm`);
};

const badgeOf = async (page) => {
  const badge = page.locator('nav a[href="/direct"] [data-waiting="direct"]');
  if ((await badge.count()) === 0) return 0;
  return Number(await badge.first().getAttribute("data-waiting-count"));
};

// WAITED FOR, not slept on: the rail refills on its own clock, so the number
// arrives some time after the message does. want() is what the reader is
// waiting to see, and the failure says what it saw instead.
const settles = async (page, want, what) => {
  for (let i = 0; i < 60; i++) {
    if (want(await badgeOf(page))) return;
    await page.waitForTimeout(500);
  }
  die(`${what} - the direct row shows ${await badgeOf(page)}`);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), reader);

  // Open somewhere that is NOT the direct page, or the badge would clear as
  // fast as it appeared and this would measure nothing.
  await page.goto(`${base}/inbox`, { timeout: 20_000 });
  await page.locator('nav a[href="/direct"]').waitFor({ state: "visible", timeout: 20_000 });

  await readerExists(page);
  const before = await badgeOf(page);
  await send(`a private message nobody has read yet ${stamp}`);
  await settles(page, (n) => n > before, "a direct message arrived and the rail never said so");

  // READING IT CLEARS IT, and reading means reaching the message - the page
  // reports the newest one on screen only while the transcript is at the
  // bottom, which is the same rule the rooms use.
  //
  // CLICKED, not navigated to: a goto is a full load, a full load fires
  // pagehide, and pagehide deletes this console's reader rows.
  await page.locator('nav a[href="/direct"]').click();
  await page
    .getByText(`a private message nobody has read yet ${stamp}`, { exact: false })
    .first()
    .waitFor({ state: "visible", timeout: 20_000 });
  await settles(page, (n) => n === 0, "the message was read and the badge did not clear");
  // AND THE MARK MOVED ON THE NODE, which is the claim the badge cannot make:
  // a console that cleared its own number and told nobody looks identical from
  // here until the tab is closed.
  await nodeSettles(
    page,
    (n) => n === 0,
    "the badge cleared and the node was never told - the mark is in the tab, not on the node",
  );

  // A SECOND MESSAGE STILL COUNTS. A mark left at the END OF THE LOG rather
  // than at the message would swallow everything after it and the row would be
  // permanently silent - which reads exactly like this check passing.
  //
  // AWAY FROM THE PAGE FIRST, and this is not a detail: on /direct with the
  // transcript at the bottom, an arriving message is REPORTED AS READ as it
  // lands, which is what onSeen means and is correct - somebody looking at the
  // conversation has read it. The badge never rises, and the first draft of
  // this check failed here against a working build for exactly that reason.
  await page.locator('nav a[href="/inbox"]').click();
  await page.locator('nav a[href="/direct"]').waitFor({ state: "visible", timeout: 20_000 });
  await send(`a second private message, after the mark moved ${stamp}`);
  await settles(page, (n) => n === 1, "a message sent after the mark moved did not raise the row");

  console.log(
    "the direct row said 1, reading it moved the mark on the node, and the next message counted again",
  );
} finally {
  await browser.close();
}
