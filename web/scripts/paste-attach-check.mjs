/**
 * A screenshot pasted into the message box reaches the room, and comes back out
 * of the node byte for byte.
 *
 *   node scripts/paste-attach-check.mjs BASE_URL TOKEN [ROOM]
 *
 * THE OPERATOR SAID "no way to post a screenshot". A screenshot is in the
 * CLIPBOARD, not on disk - the whole point of the print-screen key is that you
 * never named a file - so the gesture this has to answer is paste, and this
 * check dispatches a real paste of a real File at the real textarea rather than
 * calling the upload function directly. A control that works when called and
 * not when used is the failure being checked for.
 *
 * The last arm is the one that matters most and is the cheapest to skip: the
 * bytes that come back are compared to the bytes that went in. base64 in the
 * console, base64 out of the node, a sniffed content type in between - every
 * one of those is a place a byte can change, and an image that arrives subtly
 * corrupted looks exactly like an image that arrived.
 */

import { chromium } from "playwright";

const [base, token, room = "general"] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/paste-attach-check.mjs BASE_URL TOKEN [ROOM]");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

// A one-pixel PNG, written out byte by byte so the expected value is in this
// file rather than read from somewhere that could change under it.
const PNG = [
  0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
  0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
  0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
  0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
  0x42, 0x60, 0x82,
];

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  const refused = [];
  page.on("response", (r) => {
    if (r.url().includes("/api/") && r.status() >= 400) {
      refused.push(`${r.request().method()} ${r.status()} ${r.url()}`);
    }
  });
  const why = () => (refused.length > 0 ? `\n  the node refused: ${refused.join("; ")}` : "");

  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 }).catch(() => {});
  const message = page.locator('textarea[aria-label="message"]');
  await message.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if (crashes.length > 0) die(`the room threw: ${crashes.join("; ")}`);
  if ((await message.count()) === 0) die(`no message box in #${room}${why()}`);

  // THE PASTE, as the browser delivers one: a File on a DataTransfer, dispatched
  // as a real ClipboardEvent at the element the person is typing in.
  await page.evaluate((bytes) => {
    const box = document.querySelector('textarea[aria-label="message"]');
    const file = new File([new Uint8Array(bytes)], "screenshot.png", { type: "image/png" });
    const data = new DataTransfer();
    data.items.add(file);
    box.dispatchEvent(new ClipboardEvent("paste", { clipboardData: data, bubbles: true }));
  }, PNG);

  const chip = page.locator("[data-carrying] [data-carried]");
  await chip
    .first()
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if ((await chip.count()) !== 1) {
    die(`pasting an image put ${await chip.count()} attachments on the message${why()}`);
  }
  const id = await chip.first().getAttribute("data-carried");

  // AND IT ONLY COUNTS IF IT REACHES THE ROOM. The box holding it is a variable
  // in the page until the message is sent.
  const words = `pasted by paste-attach-check ${id}`;
  await message.fill(words);
  // Scoped to the form the textarea is in: the console has three submit buttons
  // on a room - "use", the token form, and this - and an unscoped selector
  // clicked whichever came first. Measured on a scratch node, where it resolved
  // to "use" and never sent the message.
  await page.locator('form:has(textarea[aria-label="message"]) button[type="submit"]').click();
  const posted = page.locator(`text=${words}`);
  await posted
    .first()
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if ((await posted.count()) === 0) die(`the message never arrived in #${room}${why()}`);

  // Asked of the NODE rather than of the page, because the page is showing what
  // it just sent and would agree with itself either way.
  const said = await page.evaluate(
    async ([r, t, w]) => {
      const res = await fetch(`/api/chat/${r}?order=recent&limit=20`, {
        headers: { Authorization: `Bearer ${t}` },
      });
      const page_ = await res.json();
      const mine = (page_.events || []).find((e) => (e.body || "").includes(w));
      return mine ? { id: mine.id, attachments: mine.meta?.attachments ?? null } : null;
    },
    [room, token, words],
  );
  if (!said) die(`the node has no message saying ${JSON.stringify(words)} in #${room}`);
  if (!said.attachments || !said.attachments.includes(id)) {
    die(`the message reached the room carrying ${JSON.stringify(said.attachments)}, not ${id}`);
  }

  // BYTE FOR BYTE, which is the arm the rest of this exists to reach.
  const back = await page.evaluate(
    async ([i, t]) => {
      const res = await fetch(`/api/attachment/${i}`, {
        headers: { Authorization: `Bearer ${t}` },
      });
      if (!res.ok) return { error: `${res.status} ${await res.text()}` };
      const answer = await res.json();
      // MEASURED against the running node rather than assumed: the read door
      // answers {content, item}, and the SNIFFED type lives at
      // item.fields.content_type. A top-level content_type reads as undefined,
      // which fails the last arm about a type nobody claimed.
      return { content: answer.content ?? null, type: answer.item?.fields?.content_type ?? null };
    },
    [id, token],
  );
  if (back.error) die(`the attachment would not come back: ${back.error}`);
  if (!back.content) die("the attachment came back with no bytes in it");
  const got = Buffer.from(back.content, "base64");
  const want = Buffer.from(PNG);
  if (!got.equals(want)) {
    die(
      `the bytes changed in flight: sent ${want.length}, got ${got.length} (${got.subarray(0, 8).toString("hex")} against ${want.subarray(0, 8).toString("hex")})`,
    );
  }
  // Sniffed from the content and not from what the console claimed, which is
  // the property the naming in mcp_attachments.go exists to keep.
  // startsWith, because the sniffer appends a charset to text types and could
  // reasonably say more about an image than the bare name.
  if (!(back.type ?? "").startsWith("image/png")) {
    die(`the node calls those bytes ${JSON.stringify(back.type)}, not an image/png`);
  }

  // AND THE CEILING IS THE CONSOLE'S OWN, spent before the bytes are. The node
  // refuses this too and would say so - the property here is that it never has
  // to, because a person who attached a phone photo should not wait for eight
  // megabytes to travel before being told no.
  const attempts = [];
  page.on("request", (r) => {
    if (r.url().endsWith("/api/attachment") && r.method() === "POST") attempts.push(r.url());
  });
  await page.evaluate(
    (over) => {
      const box = document.querySelector('textarea[aria-label="message"]');
      const file = new File([new Uint8Array(over)], "huge.png", { type: "image/png" });
      const data = new DataTransfer();
      data.items.add(file);
      box.dispatchEvent(new ClipboardEvent("paste", { clipboardData: data, bubbles: true }));
    },
    (4 << 20) + 1,
  );
  const refusal = page.locator("text=/is \\d+ bytes and the ceiling is/");
  await refusal
    .first()
    .waitFor({ state: "visible", timeout: 10_000 })
    .catch(() => {});
  if ((await refusal.count()) === 0) {
    die("a file over the ceiling was attached with no refusal shown");
  }
  if (attempts.length > 0) {
    die(`the over-ceiling file was sent to the node anyway: ${attempts.length} POSTs`);
  }
  if ((await chip.count()) !== 0) {
    die(`the refused file was still carried: ${await chip.count()} attachments on the message`);
  }

  console.log(
    `pasted ${want.length} bytes into #${room}, carried on ${said.id}, back identical as ${back.type}; over-ceiling refused without a round trip`,
  );
} finally {
  await browser.close();
}
