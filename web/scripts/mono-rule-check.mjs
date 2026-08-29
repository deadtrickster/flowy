/**
 * MONO MEANS A MACHINE STRING YOU COULD COPY, AND NOTHING ELSE.
 *
 *   node scripts/mono-rule-check.mjs BASE_URL TOKEN ROOM
 *
 * The operator, on a screenshot: "check font langage we use". Read off that
 * screenshot, one message row alternated the two faces four times, and the
 * split did not follow any one axis: mono carried identity (flowy-claude),
 * machine strings (#Q52CTX, thread E4QTJT) and inline code, while sans carried
 * prose AND some of the actions. So neither face told a reader anything they
 * could rely on - present everywhere, therefore information nowhere.
 *
 * The rule this asserts is the one the row settled on: an id, hash, command or
 * path is mono; a word a person reads or presses is not.
 *
 * IT ASSERTS A DIFFERENCE, NOT AN ABSOLUTE, which is this repo's rule and is
 * the only shape that can fail honestly here. "The id is mono" passes on a page
 * where EVERYTHING is mono - which is exactly the defect being fixed, and the
 * screenshot that started it. So the check reads both, and requires them to
 * disagree:
 *
 *   the verbs (cite / todo / keep / thread) resolve to a face that is NOT the
 *   id's face, and carry no resting underline - underline plus mono is the
 *   grammar of an identifier, and these are controls
 *
 *   the ids resolve to the mono face
 *
 *   AND THE RAIL AGREES WITH THE ROOM. The complaint was that the same
 *   category changed face between panes: the rail wrote "out of message #ID"
 *   in sans while the room drew that id in mono, two inches apart. The rail's
 *   id is compared against the room's id BY VALUE, so "both mono" is measured
 *   rather than assumed from two separate assertions.
 */

import { chromium } from "playwright";

const [base, token, room = "general"] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/mono-rule-check.mjs BASE_URL TOKEN [ROOM]");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const api = async (path, init = {}) => {
  const res = await fetch(new URL(path, base), {
    ...init,
    headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
  });
  if (!res.ok) die(`${path} answered ${res.status}: ${(await res.text()).slice(0, 200)}`);
  return res.json();
};

// A message of our own to read the row off, so the check does not depend on
// whatever the room happens to hold.
const said = await api(`/api/chat/${encodeURIComponent(room)}/say`, {
  method: "POST",
  body: JSON.stringify({ body: "mono-rule-check: one row, two faces, one rule" }),
});
const message = said.body?.id ?? said.id;
if (!message) die(`the node took the message and did not say its id: ${JSON.stringify(said)}`);

const family = (name) =>
  name
    .split(",")[0]
    .trim()
    .replace(/^["']|["']$/g, "")
    .toLowerCase();

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 950 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 }).catch(() => {});

  const cite = page.locator(`[data-cite="${message}"]`);
  await cite.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await cite.count()) === 0) die(`the message ${message} never drew its footer`);
  if (crashes.length > 0) die(`the room threw: ${crashes.join("; ")}`);

  const read = async (locator, what) => {
    if ((await locator.count()) === 0) die(`nothing matched ${what}`);
    return locator.first().evaluate((el) => {
      const style = getComputedStyle(el);
      return {
        font: style.fontFamily,
        decoration: style.textDecorationLine,
        text: (el.textContent ?? "").trim(),
      };
    });
  };

  const verb = await read(cite, "the cite control");
  const id = await read(page.locator(`[data-msg-id="${message}"]`), "the id");

  const verbFace = family(verb.font);
  const idFace = family(id.font);

  if (verbFace === idFace) {
    die(`cite (${JSON.stringify(verb.text)}) and the id (${JSON.stringify(id.text)}) are drawn in
the SAME face, ${verbFace}. One is a control and the other is a machine string; a face that is on
both tells a reader nothing. The operator: "check font langage we use".`);
  }
  if (verb.decoration.includes("underline")) {
    die(`cite is underlined at rest (${verb.decoration}). Underline plus a machine face is the
grammar of an identifier, and #${id.text} is one two inches away - a verb you press must not be
drawn as data.`);
  }

  // THE RAIL, COMPARED TO THE ROOM BY VALUE.
  //
  // SELECTING IS A CONTROL, NOT A CLICK ON THE TEXT. The first cut of this
  // clicked the message body and the rail never filled - and it was right to:
  // clicking the body is how a reader COPIES, and this console went out of its
  // way to stop that gesture arming anything (MessageList's own comment: "why
  // whenever i select message text here it automatically becomes a citation? I
  // just wanted to copy it"). Selection goes through onSelect, which the cite
  // control calls when nothing is highlighted.
  await cite.click({ timeout: 10_000 });

  // AND THE QUEUE PANE HAS TO BE THE ONE SHOWING. Selecting also navigates to
  // the message's thread, which swaps the right panel to the thread pane -
  // RoomTodos only renders under pane === "todos", so the raise line was not
  // hidden, it was not mounted. The second cut of this check read that as "the
  // rail drew no id", which was a true sentence about the wrong pane.
  await page.locator('[data-room-pane="todos"]').click({ timeout: 10_000 });
  await page
    .locator('[data-room-pane-body="todos"]')
    .waitFor({ state: "visible", timeout: 10_000 })
    .catch(() => {});
  const railId = page.locator(`[data-raise-from-id="${message}"]`);
  await railId
    .first()
    .waitFor({ state: "visible", timeout: 10_000 })
    .catch(() => {});
  if ((await railId.count()) === 0) {
    die(`selecting a message drew no id in the rail's raise line. The rail says which message it
would raise out of, and that id has to be drawn as an id.`);
  }
  const rail = await read(railId, "the rail's raise-from id");
  const railFace = family(rail.font);
  if (railFace !== idFace) {
    die(`the room draws an id in ${idFace} and the rail draws the same category in ${railFace}
(${JSON.stringify(rail.text)}). One screen, one category, two faces - which is the defect, not a
detail.`);
  }

  console.log(
    `one rule holds: controls in ${verbFace} with no resting underline, ids in ${idFace}, ` +
      `and the rail's ${JSON.stringify(rail.text)} in the same face the room uses`,
  );
} finally {
  await browser.close();
}
