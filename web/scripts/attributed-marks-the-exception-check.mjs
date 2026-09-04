/**
 * THE ORDINARY MESSAGE CARRIES NO AUTHORSHIP WORD.
 *
 *   node scripts/attributed-marks-the-exception-check.mjs BASE_URL TOKEN ROOM
 *
 * 01M1PA01MXCAXF8Q0N27B68E6F. Every message on this node is "attributed" -
 * principals do not have keys yet - so the room drew that word on every row,
 * beside the operator's own handle and every agent's. They asked what it meant,
 * from a screenshot of their own console. A word that appears on everything
 * carries no information, and one that reads like a caveat and appears on
 * everything teaches a reader to skip the single case where it matters. Their
 * answer: "yes, draw nothing for common case".
 *
 * SO THE ABSENCE IS THE ASSERTION. "signed" stays as a badge and the ordinary
 * case is silent, which means the distinction is now the presence of a badge
 * rather than a choice between two labels - and it begins to MEAN something on
 * the day a principal has a key, instead of being a constant.
 *
 * SCOPED TO THE MESSAGE ROWS, not to the page. "attributed" is a perfectly good
 * word elsewhere - a citation is attributed to whoever was quoted, and a note is
 * attributed to the seat that wrote it - so a check that grepped the document
 * would go red on prose that has nothing to do with authorship, and somebody
 * would then relax it until it caught nothing.
 *
 * THE EXCEPTION IS NOT TESTED HERE AND THAT IS DELIBERATE. On a message somebody
 * has disowned the word is not a constant: "authored and disowned" is a key its
 * owner says was not theirs, "attributed and disowned" is a relay nobody can
 * vouch for - a stolen key against a forgery. disowned-check.mjs already asserts
 * both readings survive on that row, and duplicating its setup here would give
 * two checks that must be changed together and one that would be forgotten.
 */

import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/attributed-marks-the-exception-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}
const die = (why) => {
  console.error(why);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 30_000 });

  const rows = page.locator("[data-message]");
  await rows.first().waitFor({ state: "visible", timeout: 20_000 });
  if (crashes.length > 0) die(`the room threw: ${crashes.join("; ")}`);

  const drawn = await rows.count();
  if (drawn === 0) {
    die(`no messages drawn in ${room}, so this measured nothing - an empty room would pass
an assertion about what messages do not say, which is the one way this check could
report a clean console while saying nothing at all`);
  }

  // WHAT EACH ROW SAYS, so a failure can name the row rather than the page.
  const said = await rows.allInnerTexts();
  const offenders = said
    .map((text, i) => [i, text])
    .filter(([, text]) => /\battributed\b/i.test(String(text)));

  if (offenders.length > 0) {
    die(`${offenders.length} of ${drawn} message row(s) still draw "attributed":
${offenders
  .slice(0, 3)
  .map(([i, text]) => `  row ${i}: ${JSON.stringify(String(text).slice(0, 160))}`)
  .join("\n")}
Every message here is attributed, so the word is a constant - it tells the reader
nothing and trains them past the disowned case where it is the whole point.`);
  }

  console.log(`${drawn} message row(s) in ${room}, none carrying an authorship word`);
} finally {
  await browser.close();
}
