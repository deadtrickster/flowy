/**
 * SELECTING TEXT IS NOT CITING IT.
 *
 *   node scripts/cite-gesture-check.mjs BASE_URL TOKEN ROOM
 *
 * The operator: "why whenever i select message text here it automatically
 * becomes a citation? I just wanted to copy it. the citation usually done via a
 * small button or when reply clicked".
 *
 * MessageList armed the citation from onMouseUp on the message body, so copying
 * and citing were the same gesture and the common one got the rare one's
 * behaviour.
 *
 * THE TWO HALVES, and a half-built version passes exactly one of them:
 *
 *   1. drag across a message and NOTHING is cited - no citation block in the
 *      composer;
 *   2. AND THE SELECTION SURVIVES. This is the half that is easy to lose while
 *      fixing the first: storing the selection in React state re-renders the
 *      message, which rewrites its innerHTML and destroys the highlight the
 *      reader is holding - see the comment on MessageBody. Fixing citing by
 *      breaking copying would be no fix at all, and the operator's actual
 *      complaint was that they could not copy.
 *   3. then press `cite` and the citation IS armed, on that message.
 */

import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/cite-gesture-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}

// ITS OWN MESSAGE, rather than whatever another check happened to leave in the
// room. The first run of this check found an empty room and said so - "the room
// drew no message bodies" - which is the right failure for a missing fixture
// and the wrong way to depend on one: a check that reads another check's
// leftovers measures the order the suite ran in.
const said = await fetch(`${base}/api/chat/${encodeURIComponent(room)}/say`, {
  method: "POST",
  headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
  body: JSON.stringify({
    body: "a message to select in, long enough that dragging across it selects real words",
  }),
});
if (!said.ok) {
  console.error(`could not put a message in #${room} to select: ${said.status}`);
  process.exit(1);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 }).catch(() => {});

  const bodies = page.locator("main [data-body]");
  try {
    await bodies.first().waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    console.error(`the room drew no message bodies: nothing matches main [data-body].${errors}`);
    process.exit(1);
  }

  const id = await bodies.first().getAttribute("data-body");
  const body = bodies.first();

  // Drag across the message the way a reader copying it would.
  const box = await body.boundingBox();
  if (!box) {
    console.error("the first message body has no box to drag across");
    process.exit(1);
  }
  await page.mouse.move(box.x + 4, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.move(box.x + Math.min(box.width - 4, 220), box.y + box.height / 2, {
    steps: 12,
  });
  await page.mouse.up();

  // 1. nothing armed.
  if ((await page.locator("[data-citation]").count()) > 0) {
    console.error("selecting text armed a citation: copying and citing are still the same gesture");
    process.exit(1);
  }

  // 2. the selection is still there - which is what the reader came for.
  const selected = (await page.evaluate(() => String(window.getSelection() ?? ""))).trim();
  if (selected === "") {
    console.error(
      "the selection did not survive the drag: whatever re-rendered the message destroyed the highlight, so the reader cannot copy",
    );
    process.exit(1);
  }

  // 3. and pressing cite arms it, on that message.
  await page
    .locator(`[data-cite="${id ?? ""}"]`)
    .first()
    .click();
  const armed = page.locator("[data-citation]");
  try {
    await armed.first().waitFor({ state: "visible", timeout: 5_000 });
  } catch {
    console.error("pressing cite armed nothing");
    process.exit(1);
  }
  const on = await armed.first().getAttribute("data-citation");
  if (on !== id) {
    console.error(`cite on ${id} armed a citation of ${on}`);
    process.exit(1);
  }

  console.log(
    `selecting ${JSON.stringify(selected.slice(0, 30))} cited nothing and kept the highlight; pressing cite armed a citation of ${id}`,
  );
} finally {
  await browser.close();
}
