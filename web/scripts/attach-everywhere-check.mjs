/**
 * A file rides any memory type, is drawn where the prose refers to it, and is
 * still listed at the foot.
 *
 *   node scripts/attach-everywhere-check.mjs BASE_URL TOKEN
 *
 * Attachments existed before this and the surfaces did not honour them: a note
 * or a report could carry a file that nothing drew, and a body could not refer
 * to one at all. So every arm here is a DIFFERENCE - a panel that drew a card
 * on every row unconditionally would satisfy a check that only counted one.
 *
 *   1  a note WITH a file draws it; a note WITHOUT draws no card and no empty
 *      heading. Fails on master, where a note draws neither.
 *   2  a body referring to a file renders a picture - naturalWidth non-zero,
 *      not merely an <img> tag, because a broken reference is also an <img>.
 *   3  and the same file is listed at the foot as well. JIRA style is both.
 *   4  a reference the reader cannot follow is NAMED, not a broken glyph:
 *      "you may not read this" and "there is nothing here" are different
 *      answers and store.ErrNoBytes exists to keep them apart.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/attach-everywhere-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const bearer = { Authorization: `Bearer ${token}` };
const raised = [];

const clearRaised = async () => {
  for (const id of raised) {
    await fetch(`${base}/api/artifact/${encodeURIComponent(id)}/status`, {
      method: "POST",
      headers: { ...bearer, "Content-Type": "application/json" },
      body: JSON.stringify({ status: "done", note: "closed by attach-everywhere-check" }),
    }).catch(() => {});
  }
};

const die = async (message) => {
  console.error(message);
  await clearRaised();
  process.exit(1);
};

/**
 * A one-pixel PNG, as bytes rather than as a string of hex somebody has to
 * trust. It is a real image: the point of arm 2 is that the browser DECODED
 * something, and a payload the decoder rejects would fail the arm for the wrong
 * reason.
 */
const ONE_PIXEL_PNG =
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==";

const writeAttachment = async (title) => {
  const res = await fetch(`${base}/api/attachment`, {
    method: "POST",
    headers: { ...bearer, "Content-Type": "application/json" },
    body: JSON.stringify({
      content_base64: ONE_PIXEL_PNG,
      title,
      filename: "pixel.png",
      content_type: "image/png",
    }),
  });
  if (!res.ok) await die(`could not write the attachment: ${res.status} ${await res.text()}`);
  const id = (await res.json()).item?.id;
  if (!id) await die("the attachment write answered without item.id");
  return id;
};

/** A memory of some type, with whatever fields the arm needs on it. */
const writeArtifact = async (type, title, body, fields = {}) => {
  const res = await fetch(`${base}/api/artifacts`, {
    method: "POST",
    headers: { ...bearer, "Content-Type": "application/json" },
    body: JSON.stringify({ type, title, body, visibility: "project-only", fields }),
  });
  if (!res.ok) await die(`could not write the ${type}: ${res.status} ${await res.text()}`);
  const id = (await res.json()).id;
  if (!id) await die(`writing the ${type} answered without an id`);
  raised.push(id);
  return id;
};

const pixel = await writeAttachment("a pixel, for the attachment check");

// THE PROJECT AND TYPE IN THE PATH come from the artifact the node stored, not
// from what was posted: /p/:project/:type/:id is the route, and guessing either
// segment would be a check that asserts its own inputs.
const pathOf = async (id) => {
  const res = await fetch(`${base}/api/artifact/${encodeURIComponent(id)}`, { headers: bearer });
  if (!res.ok) await die(`could not read ${id} back: ${res.status}`);
  const row = await res.json();
  return `/p/${encodeURIComponent(row.project)}/${encodeURIComponent(row.type)}/${encodeURIComponent(id)}`;
};

const withFile = await writeArtifact("note", "a note that carries a file", "nothing inline here.", {
  attachments: pixel,
});
const without = await writeArtifact("note", "a note that carries nothing", "nothing inline here.");
const inline = await writeArtifact(
  "report",
  "a report that shows its evidence",
  `The measurement is below.\n\n![the pixel](${pixel})\n\nAnd that is the whole of it.`,
);
// AN ID THAT IS WELL FORMED AND NAMES NOTHING. A ULID this node has never seen
// reads exactly like one the caller may not see, which is the point: the reader
// is told the reference cannot be followed and not which of the two it was.
const missingId = "01JZZZZZZZZZZZZZZZZZZZZZZZ";
const dangling = await writeArtifact(
  "report",
  "a report referring to something it cannot show",
  `Here is the evidence.\n\n![the missing one](${missingId})\n`,
);

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  // 1 - A NOTE CARRIES A FILE, AND ITS NEIGHBOUR DOES NOT.
  await page.goto(`${base}${await pathOf(withFile)}`, { timeout: 30_000 });
  const listedOn = page.locator("[data-artifact-attachments]");
  try {
    await listedOn.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    await die(`the note ${withFile} carries ${pixel} in fields.attachments and the page draws no
list of files at all, so a note can hold evidence that is reachable from nowhere.`);
  }
  const said = await listedOn.getAttribute("data-artifact-attachments");
  if (said !== "1") {
    await die(`the note carries one file and the page says it carries ${said}`);
  }

  await page.goto(`${base}${await pathOf(without)}`, { timeout: 30_000 });
  await page.waitForLoadState("networkidle");
  if ((await page.locator("[data-artifact-attachments]").count()) !== 0) {
    await die(`a note carrying no files still drew a list of them, so the list says nothing
about whether there is anything to see.`);
  }

  // 2 and 3 - DRAWN WHERE THE PROSE REFERS TO IT, AND STILL LISTED.
  await page.goto(`${base}${await pathOf(inline)}`, { timeout: 30_000 });
  const drawn = page.locator(`img[data-attachment="${pixel}"]`);
  try {
    await drawn.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    await die(`the body of ${inline} refers to ${pixel} and no picture was drawn for it.`);
  }
  // THE BROWSER DECODED IT. An <img> with a dead src is still an <img>, and
  // naturalWidth is the only thing here that can tell them apart.
  const width = await drawn.evaluate((el) => el.naturalWidth);
  if (!width) {
    await die(`the picture for ${pixel} is in the document and the browser decoded nothing:
naturalWidth is ${width}. A reference that renders a broken image is the state
this was built to end.`);
  }
  const alsoListed = await page.locator("[data-artifact-attachments]").count();
  if (alsoListed !== 1) {
    await die(`${pixel} is drawn inside the body of ${inline} and is not listed at the foot.
Inline and listed answer different questions - what this sentence is about, and
what does this row carry - and JIRA style is both.`);
  }

  // 5 - A COMMENT DRAWS ITS FILE, AND THE COMMENT BESIDE IT DOES NOT. The
  // operator called this surface "notes/comments", and it is the one where two
  // bodies sit next to each other - so it is also the one where a picture drawn
  // under the wrong one would be invisible as a defect.
  const commented = await writeArtifact("memory", "a row two people commented on", "the row.", {
    kind: "todo",
  });
  for (const text of [`with the evidence: ![the pixel](${pixel})`, "without any evidence"]) {
    const res = await fetch(`${base}/api/todo/${encodeURIComponent(commented)}/note`, {
      method: "POST",
      headers: { ...bearer, "Content-Type": "application/json" },
      body: JSON.stringify({ note: text }),
    });
    if (!res.ok) await die(`could not comment on ${commented}: ${res.status} ${await res.text()}`);
  }
  await page.goto(`${base}${await pathOf(commented)}`, { timeout: 30_000 });
  const inComments = page.locator(`[data-note-body] img[data-attachment="${pixel}"]`);
  try {
    await inComments.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    await die(`a comment on ${commented} refers to ${pixel} and no picture was drawn in it, so a
screenshot posted in a comment is reachable only by copying an id out of the text.`);
  }
  const drawnInComments = await inComments.count();
  if (drawnInComments !== 1) {
    await die(`${drawnInComments} comments drew the picture and only one refers to it - a file
drawn under every comment says nothing about which comment it belongs to.`);
  }

  // 4 - AND A REFERENCE THAT CANNOT BE FOLLOWED SAYS SO, BY NAME.
  await page.goto(`${base}${await pathOf(dangling)}`, { timeout: 30_000 });
  const named = page.locator(`[data-attachment-missing="${missingId}"]`);
  try {
    await named.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const broken = await page.locator("img").count();
    await die(`${dangling} refers to ${missingId}, which this node cannot hand over, and the
document says nothing about it - ${broken} img element(s) on the page. A dead
<img> draws "you may not read this" and "there is nothing here" identically,
which is the distinction store.ErrNoBytes exists to keep.`);
  }
  const explains = (await named.innerText()) || "";
  if (!explains.includes(missingId) && !explains.toLowerCase().includes("cannot be shown")) {
    await die(`the placeholder for ${missingId} says ${JSON.stringify(explains)}, which does not
tell a reader which file is missing or why.`);
  }

  // 6 - AND A WORD IN ANGLE BRACKETS IS STILL A WORD. Reported as "some
  // markdown rendering is broken on some times" and measured: marked hands
  // <id> through as raw HTML, the sanitizer drops the unknown element, and an
  // unknown EMPTY element has no children to keep - so the word is gone and the
  // sentence still reads as a sentence. This house writes <file>, <name>, <id>
  // and <you> unbackticked all through its own prose.
  //
  // Four things in one body, because the fix must not be a wider HTML
  // allowance: the bracketed word comes back, a real <em> is still emphasis, a
  // bare less-than is untouched, and <script> is NOT resurrected as text.
  const angled = await writeArtifact(
    "report",
    "a report that says how to run it",
    "run flowy inbox --as <you>, where a < b, with real <em>emphasis</em>.\n\n<script>alert(1)</script>\n",
  );
  await page.goto(`${base}${await pathOf(angled)}`, { timeout: 30_000 });
  const bodyText = await page.locator("[data-artifact-body]").innerText();
  if (!bodyText.includes("--as <you>")) {
    await die(`the body says "run flowy inbox --as <you>" and the page reads
${JSON.stringify(bodyText)}. The word in angle brackets was deleted, which is
worse than a visible glitch: the sentence still reads as a sentence and the only
part that said what to type is gone.`);
  }
  if (!bodyText.includes("a < b")) {
    await die(`a bare less-than was disturbed: the page reads ${JSON.stringify(bodyText)}`);
  }
  if ((await page.locator("[data-artifact-body] em").count()) !== 1) {
    await die(`real HTML in a body stopped working - the fix widened into an escape of
everything. A deliberate <em> must still be emphasis.`);
  }
  if (bodyText.includes("<script>") || bodyText.includes("alert(1)")) {
    await die(`<script> came back as visible text: ${JSON.stringify(bodyText)}. Putting an
eaten word back must not put a forbidden tag back with it.`);
  }

  // 7 - ATTACHING FROM THE CONSOLE REACHES THE NODE, AND MOVES NOTHING ELSE.
  //
  // The arms above all seeded through the door and asserted the drawing. This
  // one goes the other way: a file is chosen in the comment box, the comment is
  // added, and the NODE is asked what it now holds - because a composer that
  // rendered its own optimism and posted nothing would satisfy every arm so far.
  //
  // AND THE ARM THAT MATTERS. Attaching must not change the row's assignee, its
  // status or its body. A control that quietly rewrote the body to carry its own
  // reference would look exactly like success, and would be the console editing
  // prose somebody else wrote.
  const before = await (
    await fetch(`${base}/api/artifact/${encodeURIComponent(commented)}`, {
      headers: bearer,
    })
  ).json();
  await page.goto(`${base}${await pathOf(commented)}`, { timeout: 30_000 });
  const picker = page.locator("[data-note-attach]");
  await picker.waitFor({ state: "attached", timeout: 20_000 }).catch(() => {});
  if ((await picker.count()) === 0) {
    await die(`the comment box on ${commented} offers no way to attach a file, so a screenshot
can only be put on a comment by writing an id into the text by hand.`);
  }
  await picker.setInputFiles({
    name: "evidence.png",
    mimeType: "image/png",
    buffer: Buffer.from(ONE_PIXEL_PNG, "base64"),
  });
  const draft = page.locator("[data-note-draft]");
  let referenced = "";
  for (let i = 0; i < 40; i++) {
    const text = (await draft.inputValue()) || "";
    const found = /!\[[^\]]*\]\((01[0-9A-HJKMNP-TV-Z]{24})\)/.exec(text);
    if (found) {
      referenced = found[1];
      break;
    }
    await page.waitForTimeout(250);
  }
  if (!referenced) {
    await die(`a file was chosen in the comment box on ${commented} and the draft never came to
refer to it: ${JSON.stringify(await draft.inputValue())}`);
  }
  await draft.fill(`${await draft.inputValue()}\n\nwith the evidence above.`);
  await page.locator("[data-note-add]").click();

  let carriedNow = null;
  for (let i = 0; i < 40; i++) {
    const notes = await (
      await fetch(`${base}/api/todo/${encodeURIComponent(commented)}/notes`, { headers: bearer })
    ).json();
    // {item, notes}, and `notes` is never null - viewNotes substitutes an empty
    // slice precisely so a client cannot read "no notes" off the same value as
    // "this door does not carry notes". So an absent key here is a broken door,
    // not an empty row, and it is worth saying which.
    if (!Array.isArray(notes.notes)) {
      await die(
        `GET /api/todo/{id}/notes answered without a notes array: ${JSON.stringify(notes).slice(0, 200)}`,
      );
    }
    const entries = notes.notes;
    if (entries.some((entry) => String(entry.note ?? "").includes(referenced))) {
      carriedNow = entries;
      break;
    }
    await page.waitForTimeout(250);
  }
  if (!carriedNow) {
    await die(`the comment box said it had attached ${referenced} and the node holds no note
that refers to it. The console drew its own optimism.`);
  }

  const after = await (
    await fetch(`${base}/api/artifact/${encodeURIComponent(commented)}`, {
      headers: bearer,
    })
  ).json();
  for (const [what, was, is] of [
    ["assignee", before.fields?.assignee ?? "", after.fields?.assignee ?? ""],
    ["status", before.status ?? "", after.status ?? ""],
    ["body", before.body ?? "", after.body ?? ""],
  ]) {
    if (was !== is) {
      await die(`attaching a file to a comment changed the row's ${what} from
${JSON.stringify(was)} to ${JSON.stringify(is)}. Attaching is not editing.`);
    }
  }

  console.log(
    `a note drew its file and its neighbour drew none; a body drew ${pixel.slice(0, 10)} at ${width}px and listed it too; one comment of two drew it; an unfollowable reference was named; <you> survived the sanitizer and <script> did not; and a file chosen in the comment box reached the node without moving the row's assignee, status or body`,
  );
} finally {
  await clearRaised();
  await browser.close();
}
