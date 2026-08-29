/**
 * The console shows a token its own readers, and says they are its own.
 *
 *   node scripts/readers-panel-check.mjs BASE_URL TOKEN QUIET_READER LIVE_READER
 *
 * WHY. Measured 2026-08-20: three seats compared /api/inbox/readers in the room
 * and found abandoned readers in all three lists - declared by a console load on
 * the 18th and having acknowledged nothing in two days. Each of us then blamed
 * another seat's rows, twice, because one label is worn by one row per principal
 * and none of us checked whose we were reading.
 *
 * `api.inboxReaders()` had been in the client since it was written and no route
 * drew it. The reason nobody had noticed their own abandoned reader is that
 * there was nowhere to look.
 *
 * THREE THINGS ASSERTED, and the third is the one that matters:
 *
 *   the rows are there at all, by label
 *   a reader that has not moved its mark in hours offers a way to forget it,
 *     and one that just moved does NOT - deleting a live reader's row sends its
 *     waiter back to the head and silently skips everything in between
 *   the panel says whose these are, in words, on the page
 *
 * The last is not decoration. It is the entire fix for a mistake three
 * independent readers of this data made in a row within ten minutes.
 */

import { chromium } from "playwright";

const [base, token, quietReader, liveReader] = process.argv.slice(2);
if (!base || !token || !quietReader || !liveReader) {
  console.error(
    "usage: node scripts/readers-panel-check.mjs BASE_URL TOKEN QUIET_READER LIVE_READER",
  );
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/profile`, { timeout: 20_000 }).catch(() => {});

  /**
   * THE TOKEN IS A PRECONDITION, SO PROVE IT BEFORE ASSERTING ANYTHING ELSE.
   *
   * addInitScript writes localStorage and lib/api getToken() reads it inside a
   * try/catch that returns "" when the read throws. So on a browser where
   * storage is unavailable - a profile it cannot write, storage partitioned or
   * disabled - the page sends NO Authorization header, the node answers as a
   * different principal, and the panel truthfully lists nothing. The check then
   * reports "the panel does not list <reader>, which the node says this token
   * holds", which blames the panel for an empty cupboard.
   *
   * That is why this check has been green on the box and red in a firecode
   * guest across three unrelated branches - a shell relay, an argv change and a
   * go.mod minimum, none of them anywhere near readers. A check that cannot
   * tell "the feature is broken" from "my browser has no storage" reports the
   * second as the first, every time, on whichever machine happens to lack it.
   *
   * Reading it back costs one evaluate and turns that into a named refusal.
   */
  const carried = await page.evaluate(() => {
    try {
      return localStorage.getItem("flowy.token") ?? "";
    } catch (err) {
      return `THREW: ${String(err)}`;
    }
  });
  if (carried !== token) {
    die(
      `the page is not carrying the token this check was given, so nothing below would have been measured against it. localStorage says ${JSON.stringify(carried)}. That is this browser, not the panel: lib/api getToken() returns "" when the read throws, so the page would send no Authorization header and the node would answer as somebody else.`,
    );
  }

  const panel = page.locator("[data-your-readers]");
  await panel.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if (crashes.length > 0) die(`the profile page threw: ${crashes.join("; ")}`);
  if ((await panel.count()) === 0) die("no readers panel on /profile at all");

  /**
   * AND THAT THE NODE AGREES, from inside the page and with the page's own
   * credential. The suite already asserted this over curl with the bearer
   * token; asking again here answers a different question - whether the door
   * says the same thing to THIS browser - and separates "the node did not send
   * it" from "the panel did not draw it", which is the whole ambiguity this
   * check has been failing inside.
   */
  const fromDoor = await page.evaluate(async () => {
    const res = await fetch("/api/inbox/readers", {
      headers: { Authorization: `Bearer ${localStorage.getItem("flowy.token") ?? ""}` },
    });
    if (!res.ok) return `status ${res.status}`;
    return ((await res.json())?.readers ?? []).map((r) => r.reader);
  });
  if (typeof fromDoor === "string") {
    die(`the door refused the page's own credential: ${fromDoor}`);
  }
  for (const want of [quietReader, liveReader]) {
    if (!fromDoor.includes(want)) {
      die(
        `the node did not send ${want} to this page - it sent ${JSON.stringify(fromDoor)}. The panel draws what it is given, so this is the door or the credential, not the panel.`,
      );
    }
  }

  for (const want of [quietReader, liveReader]) {
    if ((await page.locator(`[data-reader="${want}"]`).count()) === 0) {
      die(`the panel does not list ${want}, which the node says this token holds`);
    }
  }

  // The guard, both directions. A "forget" on every row would be the defect
  // this check exists to prevent, and a "forget" on none of them would pass a
  // panel that offers nothing.
  if ((await page.locator(`[data-reader-forget="${quietReader}"]`).count()) === 0) {
    die(`${quietReader} has not moved its mark in days and the panel offers no way to forget it`);
  }
  if ((await page.locator(`[data-reader-forget="${liveReader}"]`).count()) !== 0) {
    die(`${liveReader} moved its mark moments ago and the panel offers to delete it`);
  }

  // And it says whose. Asserted on the words, because the words ARE the fix.
  const text = (await panel.innerText().catch(() => "")) || "";
  if (!text.includes("only ever yours")) {
    die(`the panel does not say whose readers these are:\n${text}`);
  }
  // It must not call a quiet reader stuck: idle and stuck are indistinguishable
  // from these columns, and a confident wrong answer is the defect this fleet
  // found six times in one night.
  //
  // ASKED OF THE PANEL'S OWN WORDS, NOT OF THE NAMES IT IS LISTING. This tested
  // panel.innerText(), and YourReaders.tsx:165 renders {r.reader} - so every
  // reader name this token holds was in the string being matched. A seat called
  // `dead-claude` exists on this fleet, and \bdead\b matches it: the hyphen is a
  // word boundary. The check would then have died saying "the panel calls a
  // reader stuck" about a panel that had done nothing but print a name it was
  // given.
  //
  // That is the exact failure this row is about - a check pointing at the panel
  // for something that is not the panel - reproduced inside the check written to
  // diagnose it. Whether it is the guest red I cannot say, and this does not
  // claim to be that fix; it removes a way for the check to be wrong.
  //
  // So the names are taken out before the words are read. Every name the DOOR
  // said this token holds is removed, which is the same list the panel drew
  // from, so what is left is the panel's own vocabulary.
  let vocabulary = text;
  for (const name of fromDoor) {
    vocabulary = vocabulary.split(name).join(" ");
  }
  if (/\bstuck\b|\bdead\b/i.test(vocabulary)) {
    die(`the panel calls a reader stuck, which this data cannot know. With the reader
names removed, what is left still says it:\n${vocabulary}`);
  }

  if (crashes.length > 0) die(`the panel threw: ${crashes.join("; ")}`);
  console.log(`the panel lists ${quietReader} with a way to forget it, and ${liveReader} without`);
} finally {
  await browser.close();
}
