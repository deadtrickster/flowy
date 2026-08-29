/**
 * A REFUSED /vms IS A PAGE, NOT A HEADLINE ON A BLACK FIELD.
 *
 *   node scripts/vms-refusal-check.mjs BASE_URL OPERATOR_TOKEN OTHER_TOKEN
 *
 * The operator, reading a screenshot of the live console: "/vms for a
 * non-operator is a headline and one sentence on a full black page. The refusal
 * is right and the emptiness is not - 95% of the viewport says nothing."
 *
 * Both halves are asserted, because fixing the second by deleting the first
 * would be worse than the defect: a page that stops saying WHICH refusal it is
 * has thrown away the only actionable sentence on it.
 *
 *   1. the refusal survives - [data-vm-refusal] is still drawn, and the panel
 *      still carries the state that separates 403 from 503
 *   2. the page is not mostly nothing - MEASURED, as the share of the viewport
 *      the drawn text actually covers
 *
 * (2) IS GEOMETRY ON PURPOSE. The obvious check - "the explainer element is
 * present" - counts the thing that produces the property instead of measuring
 * the property, and that exact substitution shipped a broken message list here
 * earlier today with a green check on it: it counted rendered headers and read
 * a badge out of one, both true of a page nobody could read. So this walks the
 * visible text nodes, sums the height they occupy, and compares it against the
 * viewport. A page that renders the explainer inside a collapsed or clipped box
 * fails, and it should - the reader's complaint was about area, not markup.
 */

import { chromium } from "playwright";

const [base, operatorToken, otherToken] = process.argv.slice(2);
if (!base || !operatorToken || !otherToken) {
  console.error("usage: node scripts/vms-refusal-check.mjs BASE_URL OPERATOR_TOKEN OTHER_TOKEN");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

// The share of the viewport below which the page reads as empty. The defect as
// shipped drew an h1 and one line - about 60px of a 1000px viewport, 6%. The
// bar is set well under what the fix draws and well over what it replaced, so
// it fails the old page and is not a pixel-exact transcription of the new one.
const FLOOR = 0.15;

const VIEWPORT = { width: 1400, height: 1000 };
const browser = await chromium.launch();

try {
  const page = await browser.newPage({ viewport: VIEWPORT });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), otherToken);
  await page.goto(`${base}/vms`, { timeout: 30_000 });

  const panel = page.locator("[data-vm-panel]");
  await panel.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await panel.count()) === 0) {
    die("/vms drew no panel at all for a non-operator, so there is nothing to judge");
  }

  const state = await panel.getAttribute("data-vm-state");
  if (state === "ok") {
    die(`this token opened /vms (state ${JSON.stringify(state)}), so it is not the non-operator
this check needs - it would pass without ever seeing a refusal`);
  }

  // 1. THE REFUSAL IS STILL THERE.
  const refusal = page.locator("[data-vm-refusal]");
  if ((await refusal.count()) === 0) {
    die(`/vms in state ${JSON.stringify(state)} drew no refusal sentence. Filling the page must
not cost the reader the one line that says why they cannot see it.`);
  }
  const said = ((await refusal.first().textContent()) ?? "").trim();
  if (said.length < 20) {
    die(`the refusal is ${said.length} characters (${JSON.stringify(said)}) - too short to name
which refusal it is`);
  }

  // 2. THE PAGE IS NOT MOSTLY NOTHING - measured as covered area, not markup.
  const covered = await page.evaluate(() => {
    const root = document.querySelector("[data-vm-panel]");
    if (!root) return 0;
    const seen = [];
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      if (!n.textContent || !n.textContent.trim()) continue;
      const range = document.createRange();
      range.selectNodeContents(n);
      const box = range.getBoundingClientRect();
      if (box.width > 0 && box.height > 0) seen.push(box);
    }
    if (seen.length === 0) return 0;
    // Union by rows rather than summing boxes, so text side by side is not
    // counted twice and a tall column is not counted as more than it covers.
    const top = Math.min(...seen.map((b) => b.top));
    const bottom = Math.max(...seen.map((b) => b.bottom));
    return Math.max(0, bottom - top);
  });

  const share = covered / VIEWPORT.height;
  if (share < FLOOR) {
    die(`a refused /vms covers ${Math.round(share * 100)}% of the viewport with text (${Math.round(
      covered,
    )}px of ${VIEWPORT.height}px). The operator's words were "95% of the viewport says nothing".
The refusal is right; the emptiness is the defect.`);
  }

  console.log(
    `a refused /vms (state ${state}) still says why, and covers ${Math.round(
      share * 100,
    )}% of the viewport rather than a headline on a black field`,
  );
} finally {
  await browser.close();
}
