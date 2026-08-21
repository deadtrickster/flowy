/**
 * "reply in thread" opens the thread and puts the caret in the pane's composer.
 *
 *   node scripts/thread-reply-check.mjs BASE_URL TOKEN ROOM
 *
 * THE OPERATOR, 01M0HCXXJB: "like we have now 'reply' this is cited reply in the
 * room. and then 'reply in thread' is well in thread - when I click on it -
 * message appears in thread pane and stays here."
 *
 * Until this change `reply`, `thread <id>` and `N replies` all called onSelect,
 * so three controls did one thing and none of them said where the words would
 * go. A control that does not say where it puts your words is one you have to
 * try in order to understand.
 *
 * THE ASSERTION IS THE CARET, and it is here because the first cut got it
 * wrong. requestAnimationFrame after setPane focused nothing: the pane is a
 * ROUTE, so the composer does not exist for however many frames the navigation
 * takes, and one frame is a guess about a render the code cannot see. Measured
 * in a browser - focus still on the button - and replaced with an effect. A
 * check that only asserted "the pane opened" would have passed on that.
 */

import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/thread-reply-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}

const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 950 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 }).catch(() => {});
  const rit = page.locator("[data-thread-reply]").first();
  await rit.waitFor({ state: "attached", timeout: 20_000 }).catch(() => {});
  if ((await rit.count()) === 0) {
    die(`no "reply in thread" control on any message in #${room}. The room draws
[data-reply] for the room-citing one; this asserts the other half of the pair.`);
  }
  if (crashes.length > 0) die(`the room threw: ${crashes.join("; ")}`);

  // BOTH CONTROLS ON EVERY MESSAGE. One without the other is the state the
  // operator was complaining about - a single gesture that could mean either.
  const replies = await page.locator("[data-reply]").count();
  const inThread = await page.locator("[data-thread-reply]").count();
  if (replies !== inThread) {
    die(`${replies} messages offer "reply" and ${inThread} offer "reply in thread".
They are a pair - a message that offers one and not the other cannot say where
the words are going.`);
  }

  await rit.click();
  await page.waitForTimeout(1500);

  // THE PANE OPENED, ON THIS MESSAGE'S THREAD.
  const paneId = await page
    .locator("[data-thread-pane-id]")
    .first()
    .getAttribute("data-thread-pane-id")
    .catch(() => null);
  if (!paneId) die(`"reply in thread" did not open a thread pane`);

  // AND THE CARET IS IN THE PANE'S COMPOSER, not the room's and not on the
  // button. This is the arm that caught the frame-timing bug.
  const focused = await page.evaluate(() => {
    const el = document.activeElement;
    return {
      tag: el?.tagName ?? "",
      label: el?.getAttribute("aria-label") ?? "",
      inPane: !!el?.closest("[data-thread-compose]"),
    };
  });
  if (!focused.inPane || focused.tag !== "TEXTAREA") {
    die(`the caret is not in the thread composer after "reply in thread": it is on
${focused.tag} ${JSON.stringify(focused.label)} (inPane=${focused.inPane}).

A reader who presses this and starts typing would be writing into the ROOM, which
is the other half of the pair and the opposite of what they asked for.`);
  }

  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);
  console.log(`reply in thread opened ${paneId} and put the caret in its composer`);
} finally {
  await browser.close();
}
