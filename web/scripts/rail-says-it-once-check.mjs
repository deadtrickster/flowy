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
 * 2. A GROUP HEADER STARTS WHERE THE LINKS START. The headers used a 12px
 *    chevron where the links use 16px icons, so with the same gap their labels
 *    began four pixels to the left. That is what made "library" and "the log"
 *    read as rows that failed to render: the eye takes a ragged left edge for
 *    breakage before it takes it for hierarchy. Measured off the rendered text
 *    with a Range, not off the class names - a check reading `h-4 w-4` would
 *    pass on a rail whose icons were displaced by something else entirely.
 *
 *    THE ROOM LIST IS DELIBERATELY OUT OF SCOPE, and this is the second thing
 *    this check got wrong rather than a convenience. A room row carries a name
 *    of any length beside an unread badge and a close control, all of them
 *    flexing, so its label lands where the name lets it: measured on the
 *    dogfood rail, `doc-01M07SCJ5XDXKCSY4SJ1NR87PM` sits at 42 where every
 *    static link sits at 44, and in the gate's 80-room fixture one row lands at
 *    33. Asserting one edge across both families red-flagged a rail nobody has
 *    complained about and buried the four pixels somebody did. The claim is
 *    about the fixed nav - the links and the headers above them - which is the
 *    thing that was actually wrong.
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

// TWO ARMS, AND THE NARROW ONE IS WHY THIS FILE CHANGED. 01M1PBFA0P479XMRGRT5VZJZQ5:
// 127 of 140 browser checks only ever open a viewport of 1200px or wider, and
// the operator - who reported the defect this check was written for - reads the
// console on a Fold 8. A rail measured only at 1500px is measured nowhere near
// where it was complained about.
//
// THE DRAWER IS THE REASON A NARROW ARM IS NOT A ONE-LINE CHANGE. Below md the
// nav is not a column, it is a drawer, and its rows have no offsetParent while
// it is shut - so the edge measurement below finds nothing and the check dies
// saying "too few rows to judge an edge". That is the check failing to set up
// its own world, reported as a defect in the rail. The narrow arm opens the
// drawer first and then asks the same question of the same rows.
const ARMS = [
  ["a desk", 1500, 1000, false],
  ["a folded phone", 360, 780, true],
];

const browser = await chromium.launch();
try {
  for (const [arm, armWidth, armHeight, viaDrawer] of ARMS) {
    const page = await browser.newPage({
      viewport: { width: armWidth, height: armHeight },
      isMobile: viaDrawer,
      hasTouch: viaDrawer,
    });
    const crashes = [];
    page.on("pageerror", (err) => crashes.push(String(err)));
    await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
    await page.goto(`${base}/`, { timeout: 30_000 });

    // OPEN IT BEFORE MEASURING IT. A shut drawer is not a rail with no labels.
    if (viaDrawer) {
      const opener = page.locator("[data-nav-open]").first();
      await opener.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
      if ((await opener.count()) === 0) {
        die(`at ${armWidth}px (${arm}) there is no control to open the navigation drawer, so the
rail cannot be reached at all - which is a worse defect than a ragged edge and is
what this arm found instead of what it went looking for`);
      }
      await opener.click({ timeout: 10_000 });
      await page.waitForTimeout(400);
    }

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
      // WHICH BRANCH DREW, not just that nothing did. Three of them render no
      // select - an agent credential, a person who belongs nowhere, and a whoami
      // still in flight - and they are the ones that have to say the name
      // plainly, so the rendered text is the thing that identifies the fault.
      const drew = (await control.innerText()).trim();
      die(`the rail's project control does not name ${JSON.stringify(project)} at all - going from
three copies to none is the other way to get this wrong, and a credential with no picker
(an agent, or a person who belongs to no project) has the plain name or nothing.
The control rendered: ${JSON.stringify(drew)}`);
    }

    // 2. THE LEFT EDGE OF EVERY ROW LABEL.
    const edges = await page.evaluate(() => {
      const nav = document.querySelector("[data-nav]");
      if (!nav) return null;
      // THE FIXED NAV ONLY. [data-room-list] holds rows whose width is the
      // room's name, so their labels do not and should not share one edge - see
      // the head of this file.
      const roomList = nav.querySelector("[data-room-list]");
      const inRooms = (el) => roomList?.contains(el) === true;
      const rows = [
        ...nav.querySelectorAll("a[href]"),
        ...nav.querySelectorAll("details > summary"),
      ].filter((el) => !inRooms(el));
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
          if (box.width > 0) {
            out.push({ text, left: Math.round(box.left), summary: row.tagName === "SUMMARY" });
          }
          break;
        }
      }
      return out;
    });

    if (!edges || edges.length < 4) {
      die(
        `found ${edges ? edges.length : 0} fixed-nav rows with labels, which is too few to judge an edge`,
      );
    }
    // AND BOTH FAMILIES HAVE TO BE PRESENT, or this passes by measuring one
    // thing: the defect was a header sitting left of the links, and a run that
    // saw no header at all would report a perfect edge.
    const headers = edges.filter((e) => e.summary).length;
    if (headers === 0 || headers === edges.length) {
      die(`the fixed nav drew ${headers} group header(s) out of ${edges.length} rows, so this run
cannot compare a header's left edge against a link's - that comparison IS the check`);
    }
    const lefts = [...new Set(edges.map((e) => e.left))].sort((a, b) => a - b);
    const spread = lefts[lefts.length - 1] - lefts[0];
    if (spread > 2) {
      const by = {};
      for (const e of edges) {
        if (!by[e.left]) by[e.left] = [];
        by[e.left].push(e.text);
      }
      die(`the fixed nav's labels start at ${lefts.length} different x positions, spread ${spread}px: ${JSON.stringify(by)}
A ragged left edge reads as rows that failed to render, which is what the operator saw.`);
    }

    console.log(
      `${arm} (${armWidth}px): the rail names ${JSON.stringify(project)} once ` +
        `(${JSON.stringify(said[0])}) and all ${edges.length} fixed-nav labels - ` +
        `${headers} of them group headers - start at x=${lefts[0]}` +
        `${spread ? ` (spread ${spread}px)` : ""}`,
    );
    await page.close();
  }
} finally {
  await browser.close();
}
