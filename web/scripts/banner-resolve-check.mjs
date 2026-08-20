/**
 * The banner offers a way to take itself down, to whoever may take it down.
 *
 *   node scripts/banner-resolve-check.mjs BASE_URL TOKEN ANNOUNCEMENT_ID
 *
 * It offered none. AnnouncementBanner drew one button, `ack`, and only for an
 * announcement that NAMES A RESOURCE - so a plain warning rendered with no
 * affordance of any kind. Measured 2026-08-20: a land-guard bypass recorded at
 * 01:19 sat as an active severity=warning at the top of every page for four
 * hours after the condition it warned about was over, and the operator asked
 * how to hide it. The answer was a curl, and only the author's token would do
 * it.
 *
 * A banner nobody can clear is a banner people learn to look past, which spends
 * the surface the next real warning needs. That is the cost, and it is why this
 * is a check rather than a nicety.
 *
 * DRIVEN AS THE OPERATOR AND NOT AS THE OWNER, because the owner path is the
 * one that already worked. The token this runs under is not the announcement's
 * author, so the button appearing at all is the new rule (mayResolve's second
 * limb) reaching the browser, and pressing it is that rule holding at the door.
 *
 * THE BANNER CLEARING IS THE ASSERTION, not the button reporting success. The
 * component re-reads after the POST and draws whatever is active; a refused
 * resolve leaves the announcement on screen, which is exactly the failure this
 * must catch and the reason a click's own return value is not evidence.
 */

import { chromium } from "playwright";

const [base, token, id] = process.argv.slice(2);
if (!base || !token || !id) {
  console.error("usage: node scripts/banner-resolve-check.mjs BASE_URL TOKEN ANNOUNCEMENT_ID");
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
  await page.goto(`${base}/chat/general`, { timeout: 20_000 }).catch(() => {});

  const button = page.locator(`[data-announcement-resolve="${id}"]`);
  await button.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if (crashes.length > 0) {
    die(`the banner threw: ${crashes.join("; ")}`);
  }
  if ((await button.count()) === 0) {
    // TOLD APART FROM A PAGE THAT NEVER DREW A BANNER, on the container's own
    // testid rather than on any words: the first cut of this looked for text
    // from the announcement it was written against, and against a fixture with
    // a different title it reported "proved nothing" for a genuinely missing
    // button. A discriminator that has to be kept in step with a fixture's
    // wording is one that goes wrong in the direction of saying nothing.
    const banner = page.locator('[data-testid="announcements"]');
    if ((await banner.count()) === 0) {
      die(`no banner rendered at all for ${id} - this check proved nothing about the control`);
    }
    die(`the banner draws no resolve control for ${id}, so this reader cannot take it down`);
  }

  await button.click();
  // The banner empties because the announcement resolved, and the component
  // re-read. Waiting on the control's disappearance rather than on a timeout:
  // a fixed sleep passes for a page that is merely slow and fails for one that
  // is merely slower.
  await button
    .waitFor({ state: "detached", timeout: 20_000 })
    .catch(() => die(`${id} is still on the banner after its resolve was pressed`));

  if (crashes.length > 0) {
    die(`the banner threw while resolving: ${crashes.join("; ")}`);
  }
  console.log(`the banner offered resolve for ${id} and cleared when it was pressed`);
} finally {
  await browser.close();
}
