/**
 * THE RAIL SAYS THE PROJECT ONCE, AND ITS LABELS SHARE A LEFT EDGE.
 *
 *   node scripts/rail-says-it-once-check.mjs BASE_URL TOKEN
 *
 * The operator screenshotted this corner twice, six hours apart, the second
 * time with "still see this":
 *
 *   flowy          the product's name
 *   flowy          the project's name
 *   flowy - here   the picker, whose current option is the same word again
 *
 * claude-host traced it: eac0319 added the project line AND the picker under an
 * app name that was already there, so it is redundancy the sidebar work created.
 *
 * TWO PROPERTIES, both measured rather than counted off the markup.
 *
 * 1. WITHIN THE PROJECT CONTROL, the project is named ONCE. Scoped to
 *    [data-rail-project] deliberately: the app title says "flowy" because that
 *    is the product, and on this node the product and the project share a
 *    string. Asserting "flowy appears once in the rail" would fail on a correct
 *    rail for a project called anything else, and pass on a broken one called
 *    something unique. The claim is about the CONTROL, not the word.
 *
 * 2. EVERY ROW LABEL STARTS AT THE SAME X. The group headers used a 12px
 *    chevron where the links use 16px icons, so with the same gap their labels
 *    began four pixels to the left. That is what made "library" and "the log"
 *    read as rows that failed to render: the eye takes a ragged left edge for
 *    breakage before it takes it for hierarchy. Measured off the rendered text
 *    with a Range, not off the class names - a check reading `h-4 w-4` would
 *    pass on a rail whose icons were displaced by something else entirely.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/rail-says-it-once-check.mjs BASE_URL TOKEN");
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
  await page.goto(`${base}/`, { timeout: 30_000 });

  const control = page.locator("[data-rail-project]");
  await control.waitFor({ state: "visible", timeout: 20_000 });
  if (crashes.length > 0) die(`the console threw: ${crashes.join("; ")}`);

  const project = (await control.getAttribute("data-rail-project")) ?? "";
  if (!project || project === "none") {
    die(`the rail names no project (data-rail-project=${JSON.stringify(project)}), so this run
cannot judge whether it says it once - that is a token with no project, not a passing rail`);
  }

  // 1. HOW MANY TIMES THE CONTROL SAYS IT. Visible text only, and the select's
  // options count once for the one that is selected - an unopened <select>
  // shows exactly its current option.
  const said = await control.evaluate((root, name) => {
    const seen = [];
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      const text = (n.textContent ?? "").trim();
      if (!text.includes(name)) continue;
      // An <option> that is not the selected one is not on screen.
      const option = n.parentElement?.closest("option");
      if (option && !option.selected) continue;
      seen.push(text);
    }
    return seen;
  }, project);

  if (said.length > 1) {
    die(`the rail's project control names ${JSON.stringify(project)} ${said.length} times: ${JSON.stringify(said)}
The picker already says which project is current, in the place you would go to change it.
The operator sent this screenshot twice.`);
  }
  if (said.length === 0) {
    die(`the rail's project control does not name ${JSON.stringify(project)} at all - going from
three copies to none is the other way to get this wrong, and an agent credential draws no
picker, so it is the plain name or nothing.`);
  }

  // 2. THE LEFT EDGE OF EVERY ROW LABEL.
  const edges = await page.evaluate(() => {
    const nav = document.querySelector("[data-nav]");
    if (!nav) return null;
    const rows = [...nav.querySelectorAll("a[href]"), ...nav.querySelectorAll("details > summary")];
    const out = [];
    for (const row of rows) {
      if (!(row instanceof HTMLElement) || row.offsetParent === null) continue;
      const walker = document.createTreeWalker(row, NodeFilter.SHOW_TEXT);
      for (let n = walker.nextNode(); n; n = walker.nextNode()) {
        const text = (n.textContent ?? "").trim();
        if (!text) continue;
        const range = document.createRange();
        range.selectNodeContents(n);
        const box = range.getBoundingClientRect();
        if (box.width > 0) out.push({ text, left: Math.round(box.left) });
        break;
      }
    }
    return out;
  });

  if (!edges || edges.length < 4) {
    die(
      `found ${edges ? edges.length : 0} rail rows with labels, which is too few to judge an edge`,
    );
  }
  const lefts = [...new Set(edges.map((e) => e.left))].sort((a, b) => a - b);
  const spread = lefts[lefts.length - 1] - lefts[0];
  if (spread > 2) {
    const by = {};
    for (const e of edges) {
      if (!by[e.left]) by[e.left] = [];
      by[e.left].push(e.text);
    }
    die(`the rail's row labels start at ${lefts.length} different x positions, spread ${spread}px: ${JSON.stringify(by)}
A ragged left edge reads as rows that failed to render, which is what the operator saw.`);
  }

  console.log(
    `the rail names ${JSON.stringify(project)} once (${JSON.stringify(said[0])}) and all ` +
      `${edges.length} row labels start at x=${lefts[0]}${spread ? ` (spread ${spread}px)` : ""}`,
  );
} finally {
  await browser.close();
}
