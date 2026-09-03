/**
 * ONE FOOTER, ONE SHAPE.
 *
 *   node scripts/one-footer-one-shape-check.mjs BASE_URL TOKEN ROOM
 *
 * UI row 01M173AT9V2PYK7XHG4GZDD1MJ item 1: "every message carries eight
 * controls, all the same weight". The half about the MESSAGE was fixed - the
 * machinery is 11px muted against 14px body - and the half about each OTHER was
 * not. Measured on the deployed console, the eight were three shapes:
 *
 *   cite / todo / keep / thread <id>   borderless prose, underline on hover
 *   row <id> / N replies               bordered chip, resting opacity 1
 *   reply / reply in thread            bordered chip, resting opacity 0.6
 *
 * So `reply` whispered and `cite` did not, two centimetres apart, and which one
 * a reader gets is not predicted by anything the control does.
 *
 * COMPUTED STYLE, NOT THE CLASS. Reading `border` in a className passes on a
 * build where the class is present and the rule is not - the same trap as
 * reading `font-mono` instead of the face.
 *
 * A SET OF TRIPLES, AND ITS SIZE IS THE ASSERTION. Each control contributes
 * (border-style, font-size, resting opacity) and the set must have exactly one
 * member. A count rather than an eye: "looks consistent" is not a measurement,
 * and the failure prints every triple with the controls holding it, so an
 * unchanged measurement after a change you believe you made is visible.
 *
 * AND THE COUNT IS NOT ZERO FIRST. A footer that drew no controls satisfies
 * "all the controls agree" perfectly, which is the same wrong answer shaped
 * like a right one as a badge that appears on every row. Four is the floor: a
 * message in a room draws at least cite, todo, keep and reply.
 *
 * COLOUR IS NOT IN THE TRIPLE, deliberately. Two controls carry text-primary
 * for reasons that are not emphasis - `N replies` is a link to another surface,
 * and `keep` is primary while it is ON, which is state. Folding colour in would
 * make this check refuse a distinction the design means.
 */

import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/one-footer-one-shape-check.mjs BASE_URL TOKEN ROOM");
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
  await page.goto(`${base}/chat/${room}`, { timeout: 30_000 });
  await page.locator("[data-message]").first().waitFor({ state: "visible", timeout: 20_000 });
  await page.waitForTimeout(1500);
  if (crashes.length > 0) die(`the console threw: ${crashes.join("; ")}`);

  const found = await page.evaluate(() => {
    // The FIRST message that draws controls at all. Reading every row would
    // average over rows that legitimately differ - a head row carrying replies
    // has controls a plain line does not - and the claim is about one footer.
    for (const row of document.querySelectorAll("[data-message]")) {
      const buttons = [...row.querySelectorAll("button")].filter(
        (b) => b.getBoundingClientRect().width > 0,
      );
      if (buttons.length === 0) continue;
      const controls = buttons.map((b) => {
        const s = getComputedStyle(b);
        const named = [...b.attributes].map((a) => a.name).find((a) => a.startsWith("data-"));
        return {
          what: (b.textContent ?? "").trim().slice(0, 24) || named || b.tagName,
          border: `${s.borderTopStyle}/${s.borderTopWidth}`,
          size: s.fontSize,
          opacity: s.opacity,
        };
      });
      return { controls };
    }
    return { controls: [] };
  });

  const { controls } = found;
  // ABSENT IS NOT AGREEING. This runs before the set assertion on purpose.
  if (controls.length < 4) {
    die(`only ${controls.length} control(s) are drawn in a message footer in #${room}, and this
check needs at least four to be measuring agreement rather than emptiness. A footer that
draws nothing agrees with itself perfectly, which is the wrong answer wearing the right
one's shape.`);
  }

  const triple = (c) => `${c.border} ${c.size} opacity:${c.opacity}`;
  const shapes = new Map();
  for (const c of controls) {
    const key = triple(c);
    if (!shapes.has(key)) shapes.set(key, []);
    shapes.get(key).push(c.what);
  }

  if (shapes.size !== 1) {
    const lines = [...shapes.entries()].map(([k, who]) => `  ${k}  <- ${who.join(", ")}`);
    die(`a message footer draws its ${controls.length} controls in ${shapes.size} shapes, want 1.
A reader cannot tell which of these is an act on the message and which is a link, because the
difference between them does not track anything they do:
${lines.join("\n")}`);
  }

  const [[shape, who]] = [...shapes.entries()];
  console.log(`${controls.length} controls in one message footer, one shape: ${shape}`);
  console.log(`  ${who.join(", ")}`);
} finally {
  await browser.close();
}
