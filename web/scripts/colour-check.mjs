/**
 * Speakers are drawn in their own colour, and the colour is really applied.
 *
 * A stylesheet that defines a palette and a component that never uses it look
 * identical from every angle except a rendered page - which is the whole reason
 * this check runs in a browser rather than reading the source. What it asserts
 * is what a person would look at: the name of whoever spoke has a colour of its
 * own, and two different speakers do not share one.
 *
 *   node scripts/colour-check.mjs BASE_URL TOKEN
 *
 * The second half is the discriminating one. A version of this feature that
 * compiled, ran, and gave every speaker the SAME colour would pass any check
 * that only asked "is it coloured" - and would be useless, because the point is
 * telling people apart.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/colour-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/general`, { timeout: 20_000 }).catch(() => {});

  // The speaker names in the transcript, with the colour actually computed for
  // them - getComputedStyle, so an inline style that is overridden or a class
  // that never landed both read as what the eye would see.
  await page.waitForSelector("main span[title]", { timeout: 15_000 }).catch(() => {});
  const speakers = await page.$$eval("main span[title]", (nodes) =>
    nodes
      .map((n) => ({ name: (n.textContent || "").trim(), colour: getComputedStyle(n).color }))
      .filter((s) => s.name),
  );

  if (speakers.length === 0) {
    console.error("no speaker names were rendered, so nothing about colour was tested");
    process.exit(1);
  }

  // The default text colour, taken from a paragraph of message body rather than
  // assumed: themes move, and a hard-coded "rgb(0, 0, 0)" would make this pass
  // on a dark theme whatever the names looked like.
  const plain = await page
    .$eval("main span[title] ~ span", (n) => getComputedStyle(n).color)
    .catch(() => "");

  const coloured = speakers.filter((s) => s.colour && s.colour !== plain);
  if (coloured.length === 0) {
    // A few examples and a count, not all of them. This runs over a whole
    // transcript, and a failure that prints two hundred identical lines buries
    // its own first line - which is the one that says what went wrong.
    const sample = [...new Set(speakers.map((s) => `  ${s.name}: ${s.colour}`))].slice(0, 6);
    console.error(
      `every speaker is drawn in the ordinary text colour (${plain}), so nobody is tagged.
${speakers.length} names, ${sample.length} distinct shown:
${sample.join("\n")}`,
    );
    process.exit(1);
  }

  const names = [...new Set(speakers.map((s) => s.name))];
  if (names.length > 1) {
    const byName = new Map(speakers.map((s) => [s.name, s.colour]));
    if (new Set(byName.values()).size < 2) {
      const shown = [...byName].map(([n, c]) => `  ${n}: ${c}`).join("\n");
      console.error(`${names.length} speakers all share one colour, which tags nobody:
${shown}`);
      process.exit(1);
    }
    console.log(`${names.length} speakers, ${new Set(byName.values()).size} colours, in a browser`);
  } else {
    // Said out loud rather than passed quietly: one speaker cannot demonstrate
    // that two are told apart, and a check that hid that would be claiming more
    // than it tested.
    console.log(
      `one speaker (${names[0]}) drawn in ${byNameColour(speakers)}, distinctness untested`,
    );
  }
} finally {
  await browser.close();
}

function byNameColour(speakers) {
  return speakers[0]?.colour ?? "no colour";
}
