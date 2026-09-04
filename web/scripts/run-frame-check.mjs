/**
 * A RUN PAINTS ONE FRAME, NOT A STACK OF CARDS.
 *
 *   node scripts/run-frame-check.mjs BASE_URL TOKEN ROOM
 *
 * The operator, on a screenshot of a merged run: "merging messages works, but
 * round borders do not merge". Both halves exact. The HEADER merges - a run says
 * who is speaking once, which message-runs.sh counts - and the FRAME did not, so
 * three lines from one seat read as three cards that had lost their headers.
 *
 * Measured off that screenshot: the row OPENING a run kept all four corners
 * rounded with a square-topped continuation glued under it, and the row CLOSING
 * one stayed square-bottomed while every standalone card around it was rounded.
 *
 * FOUR ROWS, FOUR DIFFERENT ANSWERS, which is what makes this a measurement
 * rather than a look. A run of three plus a lone message gives every case the
 * component has:
 *
 *   opens a run   top rounded, bottom square
 *   middle        both square
 *   closes a run  top square, bottom rounded
 *   alone         both rounded
 *
 * COMPUTED RADIUS, NOT THE CLASS. Reading `rounded-t-lg` in a className passes on
 * a build where the class is present and the rule did not survive - the same trap
 * as reading `font-mono` instead of the face.
 *
 * AND THE LONE ROW IS THE ARM THAT CATCHES OVER-APPLICATION. A fix that squared
 * every corner in the room satisfies "the run merges" perfectly and is a worse
 * console than the one it replaced, so the single message is asserted first and
 * by itself.
 */

import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/run-frame-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}
const die = (why) => {
  console.error(why);
  process.exit(1);
};
// A radius a person would call "rounded". The component uses one token for all
// four corners, so this only has to tell a curve from a corner.
const round = (px) => Number.parseFloat(px) >= 4;

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 30_000 });

  const rows = page.locator("[data-message]");
  await rows
    .first()
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  const total = await rows.count();
  if (total < 4) {
    die(
      `the room drew ${total} messages and this needs at least four - a run of three and a lone one`,
    );
  }

  const seen = await page.evaluate(() => {
    return [...document.querySelectorAll("[data-message]")].map((el) => {
      const s = getComputedStyle(el);
      return {
        id: el.getAttribute("data-message"),
        header: el.querySelector("[data-msg-header]")?.getAttribute("data-msg-header") ?? null,
        topLeft: s.borderTopLeftRadius,
        topRight: s.borderTopRightRadius,
        bottomLeft: s.borderBottomLeftRadius,
        bottomRight: s.borderBottomRightRadius,
      };
    });
  });

  // EVERY ROW SAYS WHICH IT IS, before any claim about shape. A row with no
  // header attribute is a row this check cannot speak about, and a silent gap
  // is how a count starts lying.
  const mute = seen.filter((r) => r.header === null);
  if (mute.length > 0) {
    die(
      `${mute.length} of ${seen.length} row(s) carry no data-msg-header, so which of them open a run is not knowable here`,
    );
  }

  // WHAT EACH ROW SHOULD BE, derived from its neighbours the same way the
  // component derives it - so this asserts the RULE and not a transcription of
  // the current classes.
  const want = seen.map((row, i) => {
    const continues = row.header === "continues";
    const continued = seen[i + 1]?.header === "continues";
    return { ...row, wantTop: !continues, wantBottom: !continued };
  });

  const wrong = want.filter(
    (r) =>
      round(r.topLeft) !== r.wantTop ||
      round(r.topRight) !== r.wantTop ||
      round(r.bottomLeft) !== r.wantBottom ||
      round(r.bottomRight) !== r.wantBottom,
  );
  if (wrong.length > 0) {
    const lines = wrong.map(
      (r) =>
        `  ${r.id} (${r.header}): top ${r.topLeft}/${r.topRight} bottom ${r.bottomLeft}/${r.bottomRight}` +
        ` - want top ${r.wantTop ? "rounded" : "square"}, bottom ${r.wantBottom ? "rounded" : "square"}`,
    );
    die(
      `${wrong.length} of ${seen.length} row(s) paint the wrong corners, so a run reads as stacked cards rather than one block:\n${lines.join("\n")}`,
    );
  }

  // AND THE FOUR CASES WERE ACTUALLY PRESENT. Every assertion above is
  // vacuously true on a room of four lone messages, which is the shape this
  // room must not quietly become.
  const opens = want.filter((r) => r.header === "opens").length;
  const continuing = want.filter((r) => r.header === "continues").length;
  const closing = want.filter((r) => r.header === "continues" && !r.wantBottom).length;
  const lone = want.filter((r) => r.header === "opens" && r.wantBottom).length;
  if (continuing === 0 || closing === 0 || lone === 0) {
    die(
      `this room drew ${opens} opener(s), ${continuing} continuation(s), ${closing} run-closing row(s) and ${lone} lone row(s). Every shape assertion above passes on a room with no runs in it, so a fixture that stopped producing one would read as green.`,
    );
  }
  console.log(
    `${seen.length} rows: ${opens} open a run, ${continuing} continue one, ${closing} close one, ${lone} stand alone - each painting only the corners its position calls for`,
  );
} finally {
  await browser.close();
}
