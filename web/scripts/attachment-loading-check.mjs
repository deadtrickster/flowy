/**
 * A CARD THAT IS STILL FETCHING SAYS SO, AND "NOT ON THIS NODE" MEANS THE NODE
 * SAID SO.
 *
 *   node scripts/attachment-loading-check.mjs BASE_URL TOKEN ROOM
 *
 * The operator, on five renders another seat had just posted: "all attachments
 * \"not on this node\"". Nothing was wrong with them - 1.5MB and 425KB of
 * image/png, on the node, readable with a token. The card said it WHILE
 * FETCHING, because content was null before the answer arrived and null is also
 * what the node sends when it holds no bytes.
 *
 * THE DELAY IS THE WHOLE TEST. A small attachment fills before anybody could
 * read the words, so an assertion made against a normal file passes on the bug
 * and passes on the fix, and would have been written in good faith. So the
 * attachment route is held open deliberately and the card is asked what it says
 * while the answer is in flight - which is the only moment the defect exists.
 *
 * THREE STATES, ASSERTED AS THREE:
 *   in flight        "fetching…"      and NOT "not on this node"
 *   answered, empty  "not on this node"
 *   answered, bytes  an <img> with a data: src
 *
 * The middle one is produced by fulfilling the route with content:null rather
 * than by finding a real attachment with no bytes, because "the node holds the
 * row and not the payload" is a state this suite cannot make on demand and the
 * card's behaviour is a function of the answer, not of how it came about.
 */

import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/attachment-loading-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}
const die = (why) => {
  console.error(why);
  process.exit(1);
};

// ITS OWN FIXTURE, IN ITS OWN ROOM. A check that hunts for an attachment
// somebody else posted inherits every way that message can change, and an empty
// room would satisfy every assertion below about what a card must NOT say.
const bearer = { Authorization: `Bearer ${token}` };
const png = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAEklEQVR4nGP8z4AATAxIHAgHAB1ZAQvOKB0VAAAAAElFTkSuQmCC",
  "base64",
);
const wrote = await fetch(`${base}/api/attachment`, {
  method: "POST",
  headers: { ...bearer, "Content-Type": "application/json" },
  body: JSON.stringify({
    title: "attachment-loading-check: a picture to be slow about",
    content_base64: png.toString("base64"),
    content_type: "image/png",
    filename: "slow.png",
    room,
  }),
});
if (!wrote.ok) die(`could not write the attachment: ${wrote.status} ${await wrote.text()}`);
const attachment = (await wrote.json()).item?.id;
if (!attachment) die("the write answered without an id");

const said = await fetch(`${base}/api/chat/${encodeURIComponent(room)}/say`, {
  method: "POST",
  headers: { ...bearer, "Content-Type": "application/json" },
  body: JSON.stringify({ body: "attachment-loading-check", attachments: [attachment] }),
});
if (!said.ok) die(`could not post the message: ${said.status} ${await said.text()}`);

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  // HELD OPEN, not slowed by luck. The card is asked while this is unresolved.
  let release = () => {};
  const held = new Promise((r) => {
    release = r;
  });
  let mode = "hold";
  await page.route("**/api/attachment/*", async (route) => {
    if (mode === "hold") await held;
    if (mode === "empty") {
      const real = await route.fetch();
      const body = await real.json();
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ ...body, content: null, bytes: "not on this node" }),
      });
      return;
    }
    await route.continue();
  });

  await page.goto(`${base}/chat/${room}`, { timeout: 30_000 });
  const card = page.locator("[data-attachment]").first();
  await card.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await card.count()) === 0) {
    die(`no attachment card ([data-attachment]) in ${room}, so this measured nothing - an empty room would
satisfy every assertion below about what a card must not say`);
  }
  await card.locator("[data-attachment-toggle]").first().click({ timeout: 10_000 });
  await page.waitForTimeout(700);

  // 1. IN FLIGHT.
  const sayingAbsent = await page.locator("[data-attachment-absent]").count();
  const sayingLoading = await page.locator("[data-attachment-loading]").count();
  if (sayingAbsent > 0) {
    die(`while the fetch was still in flight the card said "not on this node".
The bytes are on the node - the answer had simply not arrived. This is the operator's
report: five real uploads, all of them declared missing, because a pending request and
an empty answer are the same null.`);
  }
  if (sayingLoading === 0) {
    die(`while the fetch was in flight the card said nothing at all - no "fetching…".
A card that goes blank is better than one that lies, and still leaves a reader unable to
tell a slow file from a missing one.`);
  }

  release();
  await page.waitForTimeout(1200);

  // 2. ANSWERED, WITH BYTES.
  if ((await page.locator("[data-attachment-loading]").count()) > 0) {
    die(`the answer arrived and the card is still saying "fetching…"`);
  }
  const drew = await page.locator("[data-attachment] img").count();
  if (drew === 0) {
    die(`the answer arrived with bytes and no picture was drawn`);
  }

  // 3. ANSWERED, EMPTY - the real "not on this node".
  mode = "empty";
  await page.reload({ timeout: 30_000 });
  await card.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  await card.locator("[data-attachment-toggle]").first().click({ timeout: 10_000 });
  await page.waitForTimeout(900);
  if ((await page.locator("[data-attachment-absent]").count()) === 0) {
    die(`the node answered with content:null and the card did not say "not on this node".
The message has to survive for the case it was written for, or hiding it while loading
has simply deleted it.`);
  }

  console.log("a card in flight says fetching, with bytes draws, and empty says not on this node");
} finally {
  await browser.close();
}
