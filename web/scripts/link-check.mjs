/**
 * A URL somebody typed is a link, and the mention beside it survives.
 *
 * Bodies used to render down two paths - markdown for the ones a heuristic read
 * as structured, and a plain path that carried the mention chips and the span
 * citations - and this check was written when linkifying happened on the plain
 * one. There is one path now, and the claim is unchanged and worth more: the
 * renderer that turns the URL into an anchor is the same renderer that has to
 * draw the chip beside it, so a body carrying both is exactly where the two
 * would come apart.
 *
 *   node scripts/link-check.mjs BASE_URL TOKEN ROOM HANDLE
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, token, room = "links", handle] = process.argv.slice(2);
if (!base || !token || !handle) {
  console.error("usage: node scripts/link-check.mjs BASE_URL TOKEN ROOM HANDLE");
  process.exit(2);
}
refuseRemote(base, "link-check");

const URL_IN_BODY = "https://example.invalid/spec#section";

const browser = await chromium.launch();
try {
  const said = await fetch(`${base}/api/chat/${room}/say`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
    body: JSON.stringify({ body: `@${handle} see ${URL_IN_BODY} before merging.` }),
  }).catch((err) => ({ ok: false, status: 0, text: async () => String(err) }));
  if (!said.ok) {
    console.error(`the seed was refused: HTTP ${said.status} ${await said.text()}
  Nothing about links was tested.`);
    process.exit(1);
  }

  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 }).catch(() => {});
  await page.waitForSelector("main [data-body]", { timeout: 20_000 }).catch(() => {});

  const anchor = page.locator(`main a[href="${URL_IN_BODY}"]`).first();
  try {
    await anchor.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const text = await page
      .locator("main")
      .innerText()
      .catch(() => "");
    console.error(
      `the URL is not a link: no <a href="${URL_IN_BODY}"> in the transcript.
  ${text.includes(URL_IN_BODY) ? "The text IS on screen, so it rendered as plain text." : "The message did not arrive at all - this is the seed or the poll, not the render."}`,
    );
    process.exit(1);
  }

  // A link out of a message body must not hand the target a window handle it
  // can navigate back, and the referrer is nobody else's business.
  const rel = (await anchor.getAttribute("rel")) || "";
  if (!rel.includes("noopener") || !rel.includes("noreferrer")) {
    console.error(`the link is clickable but rel is ${JSON.stringify(rel)} - a body is text a peer
  wrote, so an outbound link needs noopener and noreferrer.`);
    process.exit(1);
  }

  // THE REGRESSION THIS CHANGE WOULD CAUSE: linkifying by rendering the whole
  // body as markdown eats the mention chips, because that path does not have
  // them. So the chip beside the link is what proves the plain path survived.
  const chip = page.locator("main [data-mention]").first();
  if ((await chip.count()) === 0) {
    console.error(`the link rendered and the @mention beside it did not - the body went down the
  markdown path, which has no mention chips and no span citations.`);
    process.exit(1);
  }

  console.log(
    `ok  the URL is an <a> with rel="${rel}", and the @${handle} chip survived beside it`,
  );
} finally {
  await browser.close();
}
