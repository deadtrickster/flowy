/**
 * A finding opens BESIDE the list it was chosen from, and closing it leaves the
 * list where it was.
 *
 *   node scripts/finding-pane-check.mjs BASE_URL TOKEN
 *
 * THE OPERATOR REPORTED "old server parity" four or five times. Every reading
 * of it went looking for a missing widget, and the widgets were all there - the
 * per-version table, the package download, the log viewer, a filter set richer
 * than the old console's. What was missing was the SHAPE: the old console put
 * the list and the finding on one screen, and flowy made a reader leave the
 * list to read one and come back to run the next.
 *
 * So this asserts geometry, not markup. "Beside" is a fact about where two
 * boxes are on the screen, and a check that asserts a class name would pass on
 * a pane rendered under the list, which is the thing being fixed.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/finding-pane-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  // A finding of this check's own, so it does not depend on the seed carrying
  // one. A page with no findings satisfies every assertion below by having
  // nothing to open, which is how a check reports green on a broken pane.
  // Navigated FIRST, so the write below is a same-origin fetch from the page.
  // Evaluated on about:blank it fails as "Failed to fetch" and says nothing
  // about the pane - a check that dies before it measures anything.
  await page.goto(`${base}/findings`, { timeout: 30_000 }).catch(() => {});
  const made = await page.evaluate(
    async ([t]) => {
      const res = await fetch("/api/artifacts", {
        method: "POST",
        headers: { Authorization: `Bearer ${t}`, "Content-Type": "application/json" },
        body: JSON.stringify({
          type: "finding",
          title: "finding-pane-check: a finding to open",
          body: "## what happens\n\nreading it must not cost the list.",
          severity: "high",
        }),
      });
      if (!res.ok) return { error: `${res.status} ${await res.text()}` };
      return await res.json();
    },
    [token],
  );
  if (made.error) die(`could not write a finding to open: ${made.error}`);

  // Reloaded so the list includes what was just written.
  await page.goto(`${base}/findings`, { timeout: 30_000 }).catch(() => {});
  const openers = page.locator("[data-finding-open]");
  await openers
    .first()
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if (crashes.length > 0) die(`the findings page threw: ${crashes.join("; ")}`);
  const count = await openers.count();
  if (count === 0) {
    // WHICH ABSENCE IT IS. Run against a console with no pane, the first
    // version of this said "the list did not render" - and the list had
    // rendered perfectly, with rows whose titles were links rather than
    // openers. A failure that names the wrong thing sends the next reader to
    // the wrong file, which is the defect this suite spent today removing.
    const rows = await page.locator("[data-finding]").count();
    die(
      rows === 0
        ? "no findings on the page at all - the list did not render, or this node has none"
        : `${rows} findings on the page and none of them opens: no [data-finding-open] control, so the titles still navigate away`,
    );
  }

  if ((await page.locator("[data-finding-pane]").count()) !== 0) {
    die("a finding pane was open before anything was clicked");
  }

  await openers.first().click();
  const pane = page.locator("[data-finding-pane]");
  await pane.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
  if ((await pane.count()) !== 1) die("clicking a finding opened no pane");

  // BESIDE, measured. Same row and to the right of the list, with the list
  // still on screen - a pane that replaced the list would satisfy "a pane
  // exists" and be the bug.
  const geometry = await page.evaluate(() => {
    const list = document.querySelector('ol[aria-label="findings"]');
    const pane = document.querySelector("[data-finding-pane]");
    if (!list || !pane) return null;
    const l = list.getBoundingClientRect();
    const p = pane.getBoundingClientRect();
    return {
      listWidth: Math.round(l.width),
      paneWidth: Math.round(p.width),
      listRight: Math.round(l.right),
      paneLeft: Math.round(p.left),
      overlapsVertically: p.top < l.bottom && l.top < p.bottom,
    };
  });
  if (!geometry) die("the list went away when the pane opened");
  if (geometry.listWidth === 0) die("the list is on the page with no width - it was pushed out");
  // A few pixels of slack for the gap between them, and none for the pane
  // being under the list: paneLeft below listRight means they are stacked.
  if (geometry.paneLeft < geometry.listRight - 8) {
    die(
      `the pane is not beside the list: list ends at ${geometry.listRight}, pane starts at ${geometry.paneLeft}`,
    );
  }
  if (!geometry.overlapsVertically) {
    die("the pane and the list share no rows - the pane is above or below it, not beside it");
  }

  // And it closes, leaving the list. A pane with no way out is a page.
  await page.locator("[data-finding-pane-close]").first().click();
  await page.waitForTimeout(300);
  if ((await pane.count()) !== 0) die("the close control left the pane open");
  if ((await openers.count()) === 0) die("closing the pane took the list with it");

  console.log(
    `a finding opens beside the list: list ${geometry.listWidth}px, pane ${geometry.paneWidth}px, and closing it leaves ${count} row(s)`,
  );
} finally {
  await browser.close();
}
