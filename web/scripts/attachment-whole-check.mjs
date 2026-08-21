/**
 * An image in the room can be seen whole.
 *
 *   node scripts/attachment-whole-check.mjs BASE_URL TOKEN ROOM
 *
 * The operator, one minute after we agreed that screenshots belong in the room
 * rather than in a terminal only one seat reads: "hmm when i tap on the image it
 * stays small preview". It was capped at 256 pixels, the image was not
 * clickable, and GET /api/attachment/{id} answers JSON rather than bytes - so
 * there was no link to open either. A console screenshot at 256px does not
 * contain the thing it was taken to show.
 *
 * THE ASSERTION IS THE SIZE, not the click. A lightbox that opened and drew the
 * same 256-pixel image would pass a check that only looked for an overlay, so
 * this measures the rendered height and requires it to be substantially bigger
 * than the preview it came from.
 */

import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/attachment-whole-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}

const bearer = { Authorization: `Bearer ${token}` };
const die = (message) => {
  console.error(message);
  process.exit(1);
};

// A PNG of known size, made here rather than fetched: 900x600 is larger than
// the 256-pixel cap in both directions, so "it grew" is unambiguous.
const png = await (async () => {
  const { createCanvas } = await import("node:module").then(() => ({ createCanvas: null }));
  void createCanvas;
  // No canvas in this runtime, so the bytes are a fixed 900x600 PNG built by
  // hand: a single-colour image compresses to a few hundred bytes and the
  // dimensions live in the IHDR, which is all this check needs.
  const zlib = await import("node:zlib");
  const width = 900;
  const height = 600;
  const raw = Buffer.alloc((width * 3 + 1) * height);
  for (let y = 0; y < height; y++) {
    const row = y * (width * 3 + 1);
    raw[row] = 0;
    for (let x = 0; x < width; x++) {
      raw[row + 1 + x * 3] = 40;
      raw[row + 2 + x * 3] = 90 + ((x + y) % 120);
      raw[row + 3 + x * 3] = 160;
    }
  }
  const crcTable = [...Array(256)].map((_, n) => {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    return c >>> 0;
  });
  const crc = (buf) => {
    let c = 0xffffffff;
    for (const b of buf) c = crcTable[(c ^ b) & 0xff] ^ (c >>> 8);
    return (c ^ 0xffffffff) >>> 0;
  };
  const chunk = (type, data) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
    const sum = Buffer.alloc(4);
    sum.writeUInt32BE(crc(body));
    return Buffer.concat([len, body, sum]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8;
  ihdr[9] = 2;
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", zlib.deflateSync(raw)),
    chunk("IEND", Buffer.alloc(0)),
  ]);
})();

const wrote = await fetch(`${base}/api/attachment`, {
  method: "POST",
  headers: { ...bearer, "Content-Type": "application/json" },
  body: JSON.stringify({
    title: "attachment-whole-check: a picture worth opening",
    content_base64: png.toString("base64"),
    content_type: "image/png",
    filename: "whole.png",
    room,
  }),
});
if (!wrote.ok) die(`could not write the attachment: ${wrote.status} ${await wrote.text()}`);
const attachment = (await wrote.json()).item?.id;
if (!attachment) die("the write answered without an id");

const said = await fetch(`${base}/api/chat/${encodeURIComponent(room)}/say`, {
  method: "POST",
  headers: { ...bearer, "Content-Type": "application/json" },
  body: JSON.stringify({ body: "attachment-whole-check", attachments: [attachment] }),
});
if (!said.ok) die(`could not post the message: ${said.status} ${await said.text()}`);
const message = (await said.json()).id;

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 950 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 });

  const card = page.locator(`[data-message="${message}"]`);
  await card.waitFor({ state: "visible", timeout: 30_000 }).catch(() => {});
  if ((await card.count()) === 0) die(`the message carrying ${attachment} was not drawn`);

  // The bytes are fetched on demand, so the preview is behind the card's own
  // open control - that is deliberate and not what this check is about.
  const opener = card.getByRole("button", { name: "open", exact: true });
  await opener
    .first()
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if ((await opener.count()) === 0) die("the attachment card has no open control");
  await opener.first().click();

  const preview = page.locator(`[data-attachment-open="${attachment}"] img`);
  await preview.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await preview.count()) === 0) {
    die("the image has no control to open it full size - see AttachmentCards");
  }
  const small = (await preview.boundingBox())?.height ?? 0;
  if (small === 0) die("the preview has no height, so nothing was measured");

  await page.locator(`[data-attachment-open="${attachment}"]`).click();
  const whole = page.locator(`[data-attachment-whole="${attachment}"] img`);
  await whole.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
  if ((await whole.count()) === 0) die("clicking the image opened nothing");

  const big = (await whole.boundingBox())?.height ?? 0;
  // TWICE THE PREVIEW, not merely "an overlay exists": a lightbox drawing the
  // same 256 pixels would pass any check that only looked for the element.
  if (big < small * 2) {
    die(`the full-size view is ${Math.round(big)}px against a ${Math.round(small)}px preview -
that is not seeing it whole.`);
  }

  // AND ESCAPE CLOSES IT, which is the first thing a person tries.
  await page.keyboard.press("Escape");
  await whole.waitFor({ state: "detached", timeout: 5_000 }).catch(() => {});
  if ((await whole.count()) !== 0) die("Escape did not close the full-size view");

  console.log(
    `an image opens from ${Math.round(small)}px to ${Math.round(big)}px and Escape closes it`,
  );
} finally {
  await browser.close();
}
