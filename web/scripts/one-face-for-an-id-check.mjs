/**
 * ONE MESSAGE ROW, ONE FACE FOR AN ID.
 *
 *   node scripts/one-face-for-an-id-check.mjs BASE_URL TOKEN ROOM ROW_ID
 *
 * The operator, on a screenshot of the room: "check font langage we use".
 * Measured as computed font-family inside a single [data-message]:
 *
 *   SPAN[data-msg-id]                the message's own id      ui-monospace
 *   SPAN < BUTTON[data-thread-open]  its thread id             ui-monospace
 *   A < P < DIV[data-body]           a row id in the prose     ui-sans-serif
 *
 * Three identifiers on one row, drawn two ways. A reader cannot learn what a
 * face means when the same kind of thing wears two of them, and the one that
 * broke the pattern is the one a person pastes by hand.
 *
 * MEASURED AS THE FACE, NOT THE CLASS. Reading `font-mono` passes on a build
 * where the class is present and the face is not; the rule is about whether a
 * reader can tell an identifier from a sentence.
 *
 * AND ASSERTED AGAINST THE PROSE BESIDE IT, which is what stops this passing on
 * a console that draws EVERYTHING in monospace - a different complaint, and one
 * this fleet has had. Agreement alone is satisfied by a page with one face; the
 * pair of assertions together says ids agree AND are distinguishable.
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

  const found = await page.evaluate((wanted) => {
    // An id as a reader meets it in a room: the full ULID somebody pasted, or
    // the short form the console prints beside a message.
    const SHORT = /^[0-9A-HJKMNP-TV-Z]{6}$/;
    const ids = [];
    let prose = "";
    for (const row of document.querySelectorAll("[data-message]")) {
      const walker = document.createTreeWalker(row, NodeFilter.SHOW_TEXT);
      for (let n = walker.nextNode(); n; n = walker.nextNode()) {
        const text = (n.textContent ?? "").trim();
        const el = n.parentElement;
        if (!el || el.getBoundingClientRect().width === 0) continue;
        const face = getComputedStyle(el).fontFamily;
        if (text === wanted || SHORT.test(text)) {
          const attrs = [...el.attributes, ...(el.parentElement?.attributes ?? [])]
            .map((a) => a.name)
            .filter((a) => a.startsWith("data-"));
          ids.push({ text, face, where: `${el.tagName}${attrs.length ? `[${attrs[0]}]` : ""}` });
        } else if (!prose && text.length > 30 && / /.test(text)) {
          prose = face;
        }
      }
    }
    return { ids, prose };
  }, rowId);

  const pasted = found.ids.filter((e) => e.text === rowId);
  if (pasted.length === 0) {
    die(`the row id ${rowId} is drawn in no message in #${room}, so the case this check is about -
an id a person pasted into a sentence - is not on the page. That is a fixture that did not
arrive, not a console with one face.`);
  }
  // The console's OWN ids have to be present too, or "they all agree" is a
  // statement about one element.
  const drawn = found.ids.filter((e) => e.text !== rowId);
  if (drawn.length === 0) {
    die(`only the pasted id is drawn in #${room} - no message id, no thread id - so there is
nothing for it to agree WITH, and agreement is the whole assertion`);
  }

  const faces = [...new Set(found.ids.map((e) => e.face))];
  if (faces.length > 1) {
    const by = {};
    for (const e of found.ids) {
      if (!by[e.face]) by[e.face] = [];
      if (by[e.face].length < 4) by[e.face].push(`${e.text} in ${e.where}`);
    }
    die(`one kind of thing, ${faces.length} faces in the same message list:
${JSON.stringify(by, null, 1)}
A reader cannot learn what a face means when identifiers wear two of them.`);
  }

  if (found.prose && found.prose === faces[0]) {
    die(`ids are drawn in ${JSON.stringify(faces[0].slice(0, 40))}, which is also the face of the
prose beside them - consistent, and distinguishing nothing. An id that reads as a sentence is
the complaint this exists under, one step further along.`);
  }

  console.log(
    `${found.ids.length} identifier(s) in the message list - the pasted row id, message ids, ` +
      `thread ids - all in ${JSON.stringify(faces[0].slice(0, 30))}, against prose in ` +
      `${JSON.stringify((found.prose || "(none found)").slice(0, 30))}`,
  );
} finally {
  await browser.close();
}
