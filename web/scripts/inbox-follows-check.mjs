/**
 * The overview's inbox follows the log, and does not spin while it is quiet.
 *
 *   node scripts/inbox-follows-check.mjs BASE_URL TOKEN SPEAKER_TOKEN ROOM
 *
 * MEASURED 2026-08-20 (01M0EE7B3J): Home.tsx read /api/inbox inside a single
 * useEffect keyed on [token], with no timer anywhere in the file. The card was
 * fetched once per sign-in and never again, so an overview left open showed the
 * inbox as it stood at sign-in for as long as the tab lived. Not an expensive
 * poll - a silent staleness, on the surface an agent sits in front of.
 *
 * TWO ARMS, and the second is the one the row actually asks for:
 *
 *   IT FOLLOWS. Something is said while the page is open, and it appears with
 *   no reload and no interaction.
 *
 *   IT DOES NOT SPIN. Requests are counted over a quiet stretch. A waiter that
 *   never acks returns its page instantly every time, which is a flood wearing
 *   the shape of a long poll - so this fails on a rate rather than on a count,
 *   because one request per window is the mechanism working and ten per second
 *   is the same mechanism inverted.
 */

import { chromium } from "playwright";

const [base, token, speaker, room = "general"] = process.argv.slice(2);
if (!base || !token || !speaker) {
  console.error("usage: node scripts/inbox-follows-check.mjs BASE_URL TOKEN SPEAKER_TOKEN [ROOM]");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const say = async (body) => {
  const res = await fetch(`${base}/api/chat/${encodeURIComponent(room)}/say`, {
    method: "POST",
    headers: { Authorization: `Bearer ${speaker}`, "Content-Type": "application/json" },
    body: JSON.stringify({ body }),
  });
  if (!res.ok) die(`could not say into ${room}: ${res.status} ${await res.text()}`);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  // Every request the page makes to the node, so the second arm counts rather
  // than assumes. Counted by URL so a flood is attributable to a door.
  const hits = [];
  page.on("request", (r) => {
    if (r.url().includes("/api/")) hits.push(r.url());
  });

  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/`, { timeout: 20_000 }).catch(() => {});

  // WAIT FOR THE READER TO EXIST BEFORE SAYING ANYTHING. A reader is declared
  // at the head of what the token can read, so a message said before the
  // declaration lands sits above the snapshot the page already took and below
  // the mark the reader starts at - seen by neither. A fixed sleep here passed
  // on my machine and went red in the gate, which is a race in the INSTRUMENT
  // reported as a defect in the code.
  const readerReady = async () => {
    const res = await fetch(`${base}/api/inbox/readers`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (!res.ok) return false;
    const body = await res.json();
    return (body.readers ?? []).some((r) => r.reader === "overview:inbox");
  };
  const until = Date.now() + 30_000;
  while (!(await readerReady())) {
    if (Date.now() > until) die("the overview never declared its inbox reader");
    await new Promise((r) => setTimeout(r, 500));
  }
  if (crashes.length > 0) die(`the overview threw: ${crashes.join("; ")}`);

  const marker = `inbox-follows-${Date.now()}`;
  await say(marker);

  // It arrives without a reload. Generous, because the waiter has to return and
  // the snapshot has to be re-read after it.
  await page
    .waitForFunction((want) => document.body.innerText.includes(want), marker, { timeout: 40_000 })
    .catch(() => die(`the overview never showed a message said while it was open (${marker})`));

  // NOW COUNT, over a stretch with nothing happening. The window is 25s, so a
  // healthy loop makes a handful of requests here and a spinning one makes
  // hundreds.
  // LONGER THAN ONE WINDOW, deliberately. A 15-second count against a
  // 25-second window can read zero for a loop that is working and zero for a
  // loop that has died, and those are the two things this arm exists to tell
  // apart - so it counts across a window boundary and requires at least one.
  const before = hits.length;
  await page.waitForTimeout(40_000);
  const during = hits.length - before;
  if (during === 0) {
    die(
      "the overview made NO requests in 40 quiet seconds - the waiter is not re-arming, which looks identical to it working",
    );
  }
  if (during > 40) {
    const doors = {};
    for (const u of hits.slice(before)) {
      const key = new URL(u).pathname;
      doors[key] = (doors[key] ?? 0) + 1;
    }
    die(
      `the overview made ${during} requests in 40 quiet seconds - that is a spin, not a wait: ${JSON.stringify(doors)}`,
    );
  }

  if (crashes.length > 0) die(`the overview threw while following: ${crashes.join("; ")}`);
  console.log(`the inbox followed the log, and made ${during} requests in 40 quiet seconds`);
} finally {
  await browser.close();
}
