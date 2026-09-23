/**
 * AN ATTACHMENT ROW'S OWN PAGE SHOWS THE FILE, AND THE FILE CAN BE TAKEN AWAY.
 *
 *   node scripts/attachment-download-check.mjs BASE_URL TOKEN ROOM
 *
 * The operator, on five attachments posted to a room within one hour: "the
 * attachment rows they did do not render images nor do they have a download
 * button". Both halves were true and they had different causes.
 *
 * THE ROW WAS BLANK OF ITSELF. ArtifactView built its files list out of what a
 * row CARRIES - the attachments field, plus the ids its body names - and an
 * attachment row carries nothing, it IS the file. So the list was empty, the
 * section never rendered, and the card component that would have drawn the
 * picture was never reached. The page drew a title, some badges and the body
 * prose about an image, and not the image.
 *
 * THERE WAS NO DOWNLOAD AT ALL, anywhere in the console, for any attachment.
 * GET /api/attachment/{id} answers JSON with base64, so there was no URL to
 * link to either: an image could be seen at full size in the overlay and
 * anything that was not an image - a pdf, a log, a tarball - was reachable only
 * by a seat with a token and a shell.
 *
 * NO MESSAGE IS THE FIXTURE, deliberately. `flowy attach` without --message
 * makes exactly this row and nothing else, and --message is the flag three
 * seats could not use (01M37KAH9K82492PP2ZMDCSJYM), so the bare row is the case
 * every seat actually hits. A check that posted a message first would exercise
 * the transcript card, which already worked, and would have gone green on the
 * bug.
 *
 * THE ASSERTIONS ARE THE BYTES AND THE NAME, not the presence of a button. A
 * control that opens a blank tab, or saves a file called by its ULID with no
 * extension, would pass anything that only looked for the element - and the
 * ULID name is a real way to fail this, since the title is a sentence ("snake
 * for Nikita") and the filename is a file ("snake.jpg").
 *
 * The empty case is fulfilled rather than found: "the node holds the row and
 * not the payload" is a state this suite cannot make on demand, the card's
 * behaviour is a function of the answer, and attachment-loading-check settled
 * that argument first.
 */

import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/attachment-download-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}
const die = (why) => {
  console.error(why);
  process.exit(1);
};

const bearer = { Authorization: `Bearer ${token}` };

// A 2x2 PNG, small on purpose: this check is about what is drawn and what is
// saved, and attachment-whole-check already owns the question of how big.
const png = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAEklEQVR4nGP8z4AATAxIHAgHAB1ZAQvOKB0VAAAAAElFTkSuQmCC",
  "base64",
);
const digest = createHash("sha256").update(png).digest("hex");
// A NAME THAT IS NOT THE TITLE, which is the point of one of the assertions.
const filename = "the-file-itself.png";

const wrote = await fetch(`${base}/api/attachment`, {
  method: "POST",
  headers: { ...bearer, "Content-Type": "application/json" },
  body: JSON.stringify({
    title: "attachment-download-check: a sentence about a picture",
    content_base64: png.toString("base64"),
    content_type: "image/png",
    filename,
    room,
  }),
});
if (!wrote.ok) die(`could not write the attachment: ${wrote.status} ${await wrote.text()}`);
const item = (await wrote.json()).item;
if (!item?.id) die("the write answered without an id");
// The page path the console builds for a row - lib/api.ts artifactPath. "_"
// stands in for a project the row does not have, and the route accepts it.
const where = `/p/${encodeURIComponent(item.project || "_")}/${encodeURIComponent(
  item.type || "_",
)}/${encodeURIComponent(item.id)}`;

// NO MESSAGE IS POSTED. See the header.

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}${where}`, { timeout: 30_000 });

  const card = page.locator(`[data-attachment="${item.id}"]`);
  await card.waitFor({ state: "visible", timeout: 30_000 }).catch(() => {});
  if ((await card.count()) === 0) {
    die(`${where} drew no card for the file the row IS.
This page builds its file list from what a row CARRIES, and an attachment row carries
nothing - see ArtifactView, where the row's own id has to be in that list. Without it the
page shows a title, badges and prose about a picture, and no picture.`);
  }

  // 1. DRAWN WITHOUT A CLICK. A page about one file that hides it behind a
  //    control is the same blank page with one more step in front of it, so
  //    this deliberately does not touch the open toggle.
  const shown = page.locator(`[data-attachment="${item.id}"] img`);
  await shown.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await shown.count()) === 0) {
    die(`the card is on the page and no picture is drawn until something is clicked.
On the file's own page the bytes are the answer to the question that was asked - the lazy
fetch belongs in a transcript, where a room of cards would be a room of megabytes.`);
  }
  const box = await shown.first().boundingBox();
  if (!box || box.height === 0) die("the picture was drawn with no height, so nothing is visible");

  // 2. THE BYTES COME AWAY, UNDER THE FILE'S OWN NAME.
  const save = page.locator(`[data-attachment-save="${item.id}"]`);
  if ((await save.count()) === 0) {
    die(`there is no download control on the card.
The attachment door answers JSON with base64, so a reader without a shell has no way at
all to reach a file that is not an image, and no way to keep one that is.`);
  }
  const [got] = await Promise.all([
    page.waitForEvent("download", { timeout: 20_000 }),
    save.click(),
  ]);
  const named = got.suggestedFilename();
  if (named !== filename) {
    die(`the download is called "${named}" and the file is called "${filename}".
A ULID with no extension, or the row's title - which is a sentence somebody wrote about
the file - is not a name any viewer will open.`);
  }
  const saved = await got.path();
  if (!saved) die("the download produced no file on disk");
  const back = createHash("sha256")
    .update(await readFile(saved))
    .digest("hex");
  if (back !== digest) {
    die(`the saved bytes are not the bytes that were written.
  wrote ${digest}
  saved ${back}
A download that hands over a re-encoded or truncated copy is worse than none: it looks
like the file.`);
  }

  // 3. AND WHEN THERE ARE NO BYTES IT SAYS SO. Absent is not empty: a control
  //    that quietly does nothing is indistinguishable from one that is broken,
  //    which is how this file's sibling defect went unnoticed for a month.
  await page.route("**/api/attachment/*", async (route) => {
    const real = await route.fetch();
    const body = await real.json();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ ...body, content: null, bytes: "not on this node" }),
    });
  });
  await page.reload({ timeout: 30_000 });
  await card.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  let fired = false;
  page.on("download", () => {
    fired = true;
  });
  await page.locator(`[data-attachment-save="${item.id}"]`).click({ timeout: 10_000 });
  const why = page.locator(`[data-attachment-save-why="${item.id}"]`);
  await why.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
  if ((await why.count()) === 0) {
    die(`the node answered with content:null and the download control said nothing.
It has to name the case - the row is readable and its payload is not here - or a reader
is left watching a button that appears to do nothing at all.`);
  }
  if (fired) die("the node answered with no bytes and a file was saved anyway");

  console.log(
    `an attachment row draws its own ${Math.round(box.width)}x${Math.round(box.height)} picture unclicked, ` +
      `saves it as ${named} with matching sha256, and says why when there are no bytes`,
  );
} finally {
  await browser.close();
}
