/**
 * A person sees the message they just posted.
 *
 *   node scripts/see-your-own-check.mjs BASE_URL TOKEN ROOM
 *
 * The operator, in #general: "I also dont the the message I just posted". The
 * messages were on the node - the write landed and the console did not draw
 * them back - so this is about the SCREEN, and that is the gap.
 *
 * person-can-post-check.mjs asserts the message reaches the NODE: it posts
 * through the composer and then reads /api/chat looking for the text. It never
 * looks at the message list. So a console that accepted a message, cleared the
 * box, and drew nothing would pass it - which is exactly what was reported.
 *
 * THE ID IS THE WITNESS, not the text. The write's own id is looked for as
 * data-message on screen, so a room that drew a DIFFERENT message with similar
 * words fails, and a check cannot be satisfied by its own canary appearing
 * somewhere else on the page.
 *
 * TWO MESSAGES, ONE IN A THREAD AND ONE NOT, because that is the difference the
 * report turns on: the operator's first message was a REPLY into somebody
 * else's thread, and the room folds replies. A person must be able to see their
 * own words either way - inside an open fold is fine, behind a closed one is
 * the defect.
 */

import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/see-your-own-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const bearer = { Authorization: `Bearer ${token}` };

/** What the room holds now, so a post can be turned into an id. */
const idOf = async (mark) => {
  for (let i = 0; i < 60; i++) {
    const res = await fetch(`${base}/api/chat/${encodeURIComponent(room)}?limit=50`, {
      headers: bearer,
    });
    if (res.ok) {
      const page = await res.json();
      const found = (page.events ?? []).find((e) => (e.body ?? "").includes(mark));
      if (found) return found;
    }
    await new Promise((done) => setTimeout(done, 250));
  }
  return null;
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 });

  const box = page.locator('[aria-label="message"]').first();
  await box.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await box.count()) === 0) die("the room draws no composer at all");

  // 1 - A MESSAGE INTO THE ROOM.
  const plain = `see-your-own plain ${Date.now().toString(36)}`;
  await box.fill(plain);
  await box.press("Enter");

  const posted = await idOf(plain);
  if (!posted) {
    die(`the composer took ${JSON.stringify(plain)} and the node never got it - this check is
about the SCREEN and cannot say anything until the write lands.`);
  }

  // ON SCREEN, WITHOUT A RELOAD. The room's poll window is up to ~25s, so the
  // wait is longer than that: a message that only appears after F5 is the
  // defect, and one that appears after a poll is not.
  const drawn = page.locator(`[data-message="${posted.id}"]`);
  try {
    await drawn.waitFor({ state: "visible", timeout: 40_000 });
  } catch {
    const onScreen = await page.locator("[data-message]").count();
    die(`the node holds ${posted.id} and the room never drew it - ${onScreen} message(s) are on
screen and this is not one of them. The person who typed it is looking at a room
that does not contain what they just said.`);
  }

  // 2 - AND A REPLY INTO A THREAD, which is what the report was actually about.
  // Posted through the door rather than the composer: opening a thread in the
  // UI is a different gesture with its own controls, and this arm is about
  // whether the ROOM shows an author their own reply, not about how it was
  // sent.
  const mark = `see-your-own reply ${Date.now().toString(36)}`;
  const res = await fetch(`${base}/api/chat/${encodeURIComponent(room)}/say`, {
    method: "POST",
    headers: { ...bearer, "Content-Type": "application/json" },
    body: JSON.stringify({ body: mark, thread: posted.thread }),
  });
  if (!res.ok)
    die(`could not post a reply into ${posted.thread}: ${res.status} ${await res.text()}`);
  const reply = await idOf(mark);
  if (!reply) die("the reply was accepted and never came back from the room");
  if (reply.thread !== posted.thread) {
    die(`the reply was asked for in thread ${posted.thread} and the node filed it under
${reply.thread} - this arm is not testing what it says it is`);
  }

  // FINDABLE, WHICH IS NOT THE SAME AS UNFOLDED. If the room folds it, the fold
  // has to be openable and the words has to be there afterwards. What must not
  // happen is that the author's own reply is reachable from nowhere.
  const replyDrawn = page.locator(`[data-message="${reply.id}"]`);
  let seen = false;
  for (let i = 0; i < 80; i++) {
    if ((await replyDrawn.count()) > 0) {
      seen = true;
      break;
    }
    const fold = page.locator("[data-fold]").first();
    if ((await fold.count()) > 0) {
      await fold.click().catch(() => {});
    }
    await page.waitForTimeout(500);
  }
  if (!seen) {
    const folds = await page.locator("[data-fold]").count();
    die(`the node holds the author's own reply ${reply.id} in thread ${reply.thread} and the
room never drew it, with ${folds} fold control(s) on screen. A person cannot see
what they just wrote.`);
  }

  console.log(
    `a message posted from the composer appeared as ${posted.id.slice(0, 10)}, and the author's own reply ${reply.id.slice(0, 10)} in thread ${reply.thread.slice(0, 10)} was findable too`,
  );
} finally {
  await browser.close();
}
