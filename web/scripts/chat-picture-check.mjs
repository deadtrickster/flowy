/**
 * A picture referenced in a chat message body draws inline, as it does in
 * artifacts and notes - and one the reader may not see says so.
 *
 *   node scripts/chat-picture-check.mjs BASE_URL TOKEN_A TOKEN_A_PC TOKEN_OP ROOM
 *
 * lib/markdown documents `![what it shows](01M0...)` as the way a body refers
 * to a file it carries, and ArtifactView and RowNotes draw it. The room did not:
 * MessageBody rendered chat without the resolver, so the same body drew a bare
 * <img src="01M0..."> - a broken glyph. The operator, 2026-09-23, on a snake
 * sent with exactly that reference: "ha you replying with something that
 * renders a broken image tag".
 *
 * THE ASSERTION IS THE BYTES, not the tag: an <img data-attachment> whose src
 * is a data URI with the picture in it. A renderer that emitted the marked-up
 * tag and never fetched would pass a check that only looked for the attribute.
 *
 * AND THE THIRD STATE: a body may name a file its readers cannot reach - the
 * body is prose and nothing validates a reference in it. A writes a picture
 * into project pc, where nobody in the room has a grant, and names it from
 * the room in pa; the operator, reading in pa, sees the sentence that says it
 * cannot be shown - not a broken image, not silence. Absent is not empty, in
 * the room as much as in a document. The operator is the second reader because
 * B lives in project pb and never sees this room at all.
 */

import zlib from "node:zlib";
import { chromium } from "playwright";

const [base, tokenA, tokenAPC, tokenOP, room] = process.argv.slice(2);
if (!base || !tokenA || !tokenAPC || !tokenOP || !room) {
  console.error(
    "usage: node scripts/chat-picture-check.mjs BASE_URL TOKEN_A TOKEN_A_PC TOKEN_OP ROOM",
  );
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

// A small real PNG, 3x2, built by hand: a picture the node sniffs as image/png
// and a browser can decode.
const png = (() => {
  const width = 3;
  const height = 2;
  const raw = Buffer.alloc((width * 3 + 1) * height);
  for (let y = 0; y < height; y++) {
    const row = y * (width * 3 + 1);
    raw[row] = 0;
    for (let x = 0; x < width; x++) {
      raw[row + 1 + x * 3] = 30;
      raw[row + 2 + x * 3] = 140;
      raw[row + 3 + x * 3] = 60;
    }
  }
  const table = [...Array(256)].map((_, n) => {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    return c >>> 0;
  });
  const crc = (buf) => {
    let c = 0xffffffff;
    for (const b of buf) c = table[(c ^ b) & 0xff] ^ (c >>> 8);
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

async function post(token, path, body) {
  const res = await fetch(`${base}${path}`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) die(`POST ${path}: ${res.status} ${await res.text()}`);
  return res.json();
}

// A's picture for the room, and a picture A wrote into pc, out of the room's reach.
const shared = (
  await post(tokenA, "/api/attachment", {
    title: "chat-picture-check: a picture in the room",
    content_base64: png.toString("base64"),
    content_type: "image/png",
    filename: "shot.png",
    room,
  })
).item?.id;
const personal = (
  await post(tokenAPC, "/api/attachment", {
    title: "chat-picture-check: a picture in pc, which the room cannot reach",
    content_base64: png.toString("base64"),
    content_type: "image/png",
    filename: "elsewhere.png",
  })
).item?.id;
if (!shared || !personal) die("an attachment write answered without an id");

const shown = (
  await post(tokenA, `/api/chat/${encodeURIComponent(room)}/say`, {
    body: `chat-picture-check, drawn:\n\n![a shot](${shared})`,
    attachments: [shared],
  })
).id;
// Named in the body only: the attachments field is validated against what the
// speaker can read, and the body is not - which is exactly how a reader comes
// to be shown a reference they cannot follow.
const withheld = (
  await post(tokenA, `/api/chat/${encodeURIComponent(room)}/say`, {
    body: `chat-picture-check, out of reach:\n\n![elsewhere](${personal})`,
  })
).id;
if (!shown || !withheld) die("a say answered without an id");

const browser = await chromium.launch();
try {
  const open = async (token) => {
    const page = await browser.newPage({ viewport: { width: 1400, height: 950 } });
    await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
    await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 });
    return page;
  };

  // A SEES THE PICTURE, AS BYTES.
  const asA = await open(tokenA);
  const message = asA.locator(`[data-message="${shown}"]`);
  await message.waitFor({ state: "visible", timeout: 30_000 }).catch(() => {});
  if ((await message.count()) === 0) die(`the message ${shown} was not drawn`);
  const drawn = message.locator(`[data-body] img[data-attachment="${shared}"]`);
  await drawn.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await drawn.count()) === 0) {
    const bare = await message.locator(`[data-body] img[src="${shared}"]`).count();
    die(
      bare > 0
        ? `the body drew <img src="${shared}"> - the id as a URL, a broken glyph - instead of the picture`
        : `the body drew no picture for ![a shot](${shared}); see MessageBody and lib/markdown`,
    );
  }
  const src = (await drawn.getAttribute("src")) ?? "";
  if (!src.startsWith("data:image/png;base64,") || src.length < 100) {
    die(`the picture's src is not its bytes: ${src.slice(0, 40)}…`);
  }
  const box = await drawn.boundingBox();
  if (!box || box.height === 0) die("the picture has no height, so nothing was drawn");

  // THE OPERATOR IS TOLD, IN WORDS, THAT THE PC PICTURE CANNOT BE SHOWN.
  const asB = await open(tokenOP);
  const held = asB.locator(`[data-message="${withheld}"]`);
  await held.waitFor({ state: "visible", timeout: 30_000 }).catch(() => {});
  if ((await held.count()) === 0)
    die(`the operator cannot see the message ${withheld} at all, so the third state is untested`);
  const missing = held.locator(`[data-body] [data-attachment-missing="${personal}"]`);
  await missing.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await missing.count()) === 0) {
    const leaked = await held.locator(`[data-body] img[data-attachment="${personal}"]`).count();
    die(
      leaked > 0
        ? `the operator was shown the pc picture ${personal} from a room in pa`
        : `the operator sees neither the picture nor the sentence for ${personal} - absent drawn as empty`,
    );
  }
  const words = (await missing.textContent()) ?? "";
  if (!words.includes("cannot be shown"))
    die(`the withheld picture's sentence does not say so: ${words}`);
  // And A's shared picture still draws for the operator, so the sentence is about THIS file.
  const alsoB = asB.locator(
    `[data-message="${shown}"] [data-body] img[data-attachment="${shared}"]`,
  );
  await alsoB.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await alsoB.count()) === 0)
    die("B does not see the shared picture, so the two states did not differ");

  console.log(
    `a picture in a message body draws inline from its bytes (${Math.round(box.width)}x${Math.round(box.height)}px), and one the reader cannot reach says "cannot be shown"`,
  );
} finally {
  await browser.close();
}
