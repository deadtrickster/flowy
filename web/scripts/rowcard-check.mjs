/**
 * Tapping the row a message raised shows the row, without leaving the room.
 *
 *   node scripts/rowcard-check.mjs BASE_URL TOKEN ROOM ROW_ID ROW_TITLE
 *
 * Raised by the operator: "when i tap / click on the todo item here in the chat
 * i should see a popup with the todo item card with a link to the full todo
 * page". Measured before it was built: `event.artifact` has carried the row id
 * on every raise since raises existed and MessageList never read it, so the tap
 * landed on prose and did nothing. Nothing was broken - the transcript had no
 * idea a message was about a row.
 *
 * WHY EVERY ASSERTION IS ON AN ELEMENT AND NOT ON PAGE TEXT. The row's title is
 * also in the raise message itself, a few pixels above, so `page has the title`
 * passes with the card absent, present-but-empty, or never opened. Each claim
 * below reads a specific attribute inside `[data-row-card]`.
 *
 * The flows, in the order the row states them:
 *
 *   1. a raise is visibly about a row and an ordinary message is not;
 *   2. the chip opens a card carrying that row's title and holder;
 *   3. the card links to the full row, and following it lands on a page
 *      showing the same title - the operator's ask, and the assertion that the
 *      two surfaces agree rather than merely both existing;
 *   4. Escape dismisses, leaving the URL and the transcript where they were;
 *   5. a row that will not load says WHICH failure it was, because an empty
 *      card and a dead button are indistinguishable to the person tapping.
 *
 * Claim 5 is driven by intercepting the row's fetch and answering 410. That is
 * a statement about the card's handling and not about the node, which is the
 * honest scope: what the node does with a withdrawn row is asserted elsewhere,
 * and what this file is for is that the reader is told.
 */

import { chromium } from "playwright";

const [base, token, room, rowId, rowTitle] = process.argv.slice(2);
if (!base || !token || !room || !rowId || !rowTitle) {
  console.error("usage: node scripts/rowcard-check.mjs BASE_URL TOKEN ROOM ROW_ID ROW_TITLE");
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
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 }).catch(() => {});

  // The witness that this run measured anything: a room that never painted
  // would answer "no card" to every question below, which is also what a
  // completely broken implementation answers.
  const chip = page.locator(`[data-row-chip="${rowId}"]`);
  await chip
    .first()
    .waitFor({ timeout: 20_000 })
    .catch(() => {});
  if ((await chip.count()) === 0) {
    die(
      `no message in ${room} offers the row ${rowId} - either the room did not paint or the raise carries no artifact`,
    );
  }

  // 1. AND ONLY A RAISE. Every chip on screen must name a row; a plain message
  // must not have grown one, or the mark says nothing by being on everything.
  const chips = await page.locator("[data-row-chip]").count();
  const messages = await page.locator("[data-reply]").count();
  if (chips >= messages) {
    die(`${chips} chips across ${messages} messages - the mark is on messages that raised nothing`);
  }

  // 2. THE CHIP OPENS THE CARD.
  await chip.first().click();
  const card = page.locator(`[data-row-card="${rowId}"]`);
  await card.waitFor({ timeout: 10_000 }).catch(() => {});
  if ((await card.count()) === 0) die("the chip was clicked and no card opened");
  const title = (await card.locator("[data-row-card-title]").first().textContent()) || "";
  if (title.trim() !== rowTitle) {
    die(`the card shows ${JSON.stringify(title.trim())}, the row is ${JSON.stringify(rowTitle)}`);
  }
  if ((await card.locator("[data-row-card-assignee]").count()) === 0) {
    die("the card does not say who holds the row");
  }

  // 3. AND LINKS TO THE ROW ITSELF.
  const href = await card.locator("[data-row-card-open]").first().getAttribute("href");
  if (!href || !href.endsWith(`/${rowId}`)) {
    die(`the card's link is ${JSON.stringify(href)}, which does not end at the row`);
  }
  const before = page.url();
  await card.locator("[data-row-card-open]").first().click();
  await page.waitForURL((url) => url.pathname === href, { timeout: 10_000 }).catch(() => {});
  if (new URL(page.url()).pathname !== href) {
    die(`following the card's link stayed on ${page.url()}`);
  }
  // WAITED FOR, NOT SAMPLED: the row page fetches its own artifact, so asking
  // the instant the route changes measures the render before the answer lands.
  //
  // And read off the whole page rather than off one element, which is the
  // opposite of what this file does everywhere else - deliberately. The reason
  // for element-level assertions is the transcript, where the row's title sits
  // a few pixels from the card. This page has no transcript, so "the page the
  // link goes to shows this row" is exactly the claim, and pinning it to a
  // particular tag would fail the day somebody moves the title.
  const pageText = () => page.locator("body").innerText();
  const shows = async () => (await pageText()).includes(rowTitle);
  for (let waited = 0; waited < 30_000 && !(await shows()); waited += 250) {
    await page.waitForTimeout(250);
  }
  if (!(await shows())) {
    // WHICH failure, because the two are different bugs: a page that never
    // painted is a routing or fetch problem, and a page that painted without
    // the title is the card and the row disagreeing - which is what this
    // assertion is actually for.
    // The URL and not the page text decides which failure this is: the room
    // itself contains the row id - in the chip and in the raise message - so
    // "the id is on the page" is true of the room we may not have left.
    die(
      `on ${page.url()}: the page the card links to does not show ${JSON.stringify(rowTitle)}`,
    );
  }

  // 4. DISMISS LEAVES NOTHING BEHIND. Back to the room, open it again, escape.
  await page.goto(before, { timeout: 20_000 }).catch(() => {});
  await chip
    .first()
    .waitFor({ timeout: 20_000 })
    .catch(() => {});
  const roomUrl = page.url();
  await chip.first().click();
  await card.waitFor({ timeout: 10_000 }).catch(() => {});
  await page.keyboard.press("Escape");
  await page.waitForTimeout(300);
  if ((await card.count()) !== 0) die("Escape did not dismiss the card");
  if (page.url() !== roomUrl) die(`dismissing the card navigated to ${page.url()}`);
  if ((await chip.count()) === 0) die("the transcript lost the message the card was opened from");

  // 5. A ROW THAT WILL NOT LOAD SAYS SO, rather than opening an empty card or
  // doing nothing at all - the two shapes that read as a broken button.
  await page.route(`**/api/artifact/${rowId}`, (route) =>
    route.fulfill({ status: 410, contentType: "application/json", body: '{"error":"gone"}' }),
  );
  await chip.first().click();
  const problem = card.locator("[data-row-card-problem]");
  await problem.waitFor({ timeout: 10_000 }).catch(() => {});
  if ((await problem.count()) === 0) {
    die("a row the node answered 410 for produced no card and no explanation");
  }
  const said = ((await problem.first().textContent()) || "").toLowerCase();
  if (!said.includes("withdrawn")) {
    die(
      `the card says ${JSON.stringify(said)} for a withdrawn row, which does not name what happened`,
    );
  }

  if (crashes.length) die(`the console threw while the card was used: ${crashes.join("; ")}`);
  console.log("the row card opens, links to the row, dismisses, and names a failure");
} finally {
  await browser.close();
}
