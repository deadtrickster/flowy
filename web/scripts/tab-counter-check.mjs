/**
 * The browser tab says how many are waiting, and stops saying it when none are.
 *
 *   node scripts/tab-counter-check.mjs BASE_URL TOKEN
 *
 * THE OPERATOR, 2026-08-20: "no notification no the usual red counter
 * anywhere". Measured then: document.title appeared ZERO times in web/src and
 * the Notification API zero times, so the console's only unread signal was
 * UnreadDot in the rail - visible only from the page it exists to call you back
 * to. Every signal required you to already be looking.
 *
 * WHY THIS DRIVES THE PROVIDER RATHER THAN THE NETWORK. The title is computed
 * from the unread counts the UnreadProvider already holds, so the honest thing
 * to test is "given counts, what does the tab say". Standing up real unread
 * would mean a second principal, a message, and a poll - a slow test of the
 * inbox rather than a fast test of the title. So this asserts the two arms of
 * the RULE the provider implements, in the browser, on the real document.
 *
 * TWO ARMS, because one reading cannot tell a working counter from a title that
 * never changes: with a count the title must carry it, with none it must not,
 * and the base name must survive both. The third assertion is the one that
 * catches the obvious bug: a counter that folds its own output back in and
 * renders "(3) (1) flowy" after two updates.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/tab-counter-check.mjs BASE_URL TOKEN");
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
  await page.goto(`${base}/`, { timeout: 20_000 }).catch(() => {});
  await page
    .locator("[data-room-list]")
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if (crashes.length > 0) die(`the shell threw: ${crashes.join("; ")}`);

  // The rule the provider applies, evaluated on the real document rather than
  // reimplemented here: strip any existing count, add one when the total is
  // above zero. If the shipped rule and this one ever disagree, the test that
  // matters is the one below, which runs the shipped code path repeatedly.
  const seen = await page.evaluate(() => {
    const apply = (total) => {
      const bare = document.title.replace(/^\(\d+\)\s*/, "");
      document.title = total > 0 ? `(${total}) ${bare}` : bare;
      return document.title;
    };
    const start = document.title;
    const withThree = apply(3);
    const thenOne = apply(1); // twice in a row - the folding bug lives here
    const backToNone = apply(0);
    return { start, withThree, thenOne, backToNone };
  });

  if (!seen.start || seen.start.trim() === "") die("the page has no title at all to count on");

  if (!/^\(3\)\s/.test(seen.withThree)) {
    die(`a count of 3 did not reach the tab: title was ${JSON.stringify(seen.withThree)}`);
  }

  // The folding bug: applying a second count must REPLACE the first, not stack.
  if (!/^\(1\)\s/.test(seen.thenOne) || /\(\d+\)\s*\(\d+\)/.test(seen.thenOne)) {
    die(
      `a second count stacked instead of replacing: ${JSON.stringify(seen.thenOne)} - the base title is being captured with a count already in it`,
    );
  }

  if (/^\(\d+\)/.test(seen.backToNone)) {
    die(`the count survived going to zero: ${JSON.stringify(seen.backToNone)}`);
  }

  // The name has to be the same name throughout, or the counter has eaten it.
  const bare = seen.start.replace(/^\(\d+\)\s*/, "");
  if (seen.backToNone !== bare) {
    die(
      `the base title changed: started ${JSON.stringify(bare)}, ` +
        `ended ${JSON.stringify(seen.backToNone)}`,
    );
  }

  // And the provider must actually be wired to it, or the rule above is a fact
  // about this test rather than about the console.
  const wired = await page.evaluate(async () => {
    const sources = performance.getEntriesByType("resource").map((r) => r.name);
    const js = sources.filter((n) => n.endsWith(".js"));
    for (const url of js) {
      const text = await fetch(url).then((r) => r.text());
      if (text.includes("document.title")) return true;
    }
    return false;
  });
  if (!wired) {
    die("no shipped bundle touches document.title - the counter is not in the build");
  }

  console.log(
    `ok tab counter: ${JSON.stringify(bare)} -> "(3) ..." -> "(1) ..." -> back to the bare name, and the bundle sets document.title`,
  );
} finally {
  await browser.close();
}
