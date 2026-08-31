/**
 * A ROW ID LOOKS THE SAME WHEREVER IT IS DRAWN.
 *
 *   node scripts/one-face-for-an-id-check.mjs BASE_URL TOKEN ROOM ROW_ID
 *
 * The operator, on a screenshot of the room: "check font langage we use". One
 * category was being drawn two ways depending on which pane you were looking
 * at - the board draws a row id in `font-mono`, and the room drew the same id
 * in the body face, both as the chip beside a message and as a link inside the
 * prose. Same id, same meaning, two faces.
 *
 * MEASURED AS COMPUTED FONT-FAMILY, not as a class name. A check reading
 * `font-mono` passes on a build where the class exists and the face does not -
 * the rule was never about the class, it is about whether a reader can tell an
 * identifier from a sentence.
 *
 * ASSERTED AS AGREEMENT ACROSS SURFACES rather than as "the room is mono".
 * "This element is monospace" is true of a console that draws EVERYTHING in
 * monospace, which is a different complaint and one this fleet has had. What
 * this says is narrower and is the actual defect: every place a row id appears,
 * it appears in ONE face - and that the face is not the one the prose beside it
 * is written in, or the distinction does no work.
 */

import { chromium } from "playwright";

const [base, token, room, rowId] = process.argv.slice(2);
if (!base || !token || !room || !rowId) {
  console.error("usage: node scripts/one-face-for-an-id-check.mjs BASE_URL TOKEN ROOM ROW_ID");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

/** The faces an id is drawn in on one page, and the face of the prose beside it. */
const facesOn = async (page, id) => {
  return page.evaluate((wanted) => {
    const short = wanted.slice(-6);
    const ids = [];
    let prose = "";
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      const text = (n.textContent ?? "").trim();
      const el = n.parentElement;
      if (!el || el.getBoundingClientRect().width === 0) continue;
      const face = getComputedStyle(el).fontFamily;
      if (text === wanted || text === short) {
        ids.push({
          text,
          face,
          where: `${el.tagName}.${String(el.className || "").split(" ")[0]}`,
        });
        continue;
      }
      // A sentence somewhere on the page, for the contrast below: the id's face
      // has to differ from the prose's or it is not distinguishing anything.
      if (!prose && text.length > 25 && / /.test(text)) prose = face;
    }
    return { ids, prose };
  }, id);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  await page.goto(`${base}/chat/${room}`, { timeout: 30_000 });
  await page.waitForTimeout(2500);
  const room_ = await facesOn(page, rowId);

  await page.goto(`${base}/todos`, { timeout: 30_000 });
  await page.waitForTimeout(2500);
  const board = await facesOn(page, rowId);

  const all = [...room_.ids, ...board.ids];
  if (room_.ids.length === 0) {
    die(`the row id ${rowId} is drawn nowhere in #${room}, so there is nothing here to compare -
that is a fixture that did not arrive, not a console with one face`);
  }
  if (board.ids.length === 0) {
    die(`the row id ${rowId} is drawn nowhere on /todos, so this run compared one surface against
itself - the whole assertion is that two surfaces agree`);
  }

  const faces = [...new Set(all.map((e) => e.face))];
  if (faces.length > 1) {
    const by = {};
    for (const e of all) {
      if (!by[e.face]) by[e.face] = [];
      by[e.face].push(`${e.text} in ${e.where}`);
    }
    die(`one row id, ${faces.length} faces: ${JSON.stringify(by, null, 1)}
A reader cannot learn what a face means when the same kind of thing wears two.`);
  }

  // AND IT IS NOT THE FACE OF THE SENTENCE NEXT TO IT.
  const prose = room_.prose || board.prose;
  if (prose && prose === faces[0]) {
    die(`row ids are drawn in ${JSON.stringify(faces[0])}, which is also the face of the prose on
the page - consistent, and distinguishing nothing. An id that reads as a sentence is the
complaint this check exists under, one pane further along.`);
  }

  console.log(
    `the row id is drawn ${all.length} time(s) across the room and the board, all in ` +
      `${JSON.stringify(faces[0].slice(0, 30))}, and the prose beside it is ` +
      `${JSON.stringify((prose || "(none found)").slice(0, 30))}`,
  );
} finally {
  await browser.close();
}
