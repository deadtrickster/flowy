/**
 * Clicking a message does not make it the thing you are answering. A button on
 * it does.
 *
 *   node scripts/reply-check.mjs BASE_URL TOKEN ROOM LAST_BODY
 *
 * The room had a bug with no visible symptom: every row was a div wearing
 * role="button" and a click on it selected the message, which is what the next
 * thing you say attaches to and quotes. So reading the room with a mouse -
 * clicking a line, clicking to dismiss something - silently armed a reply at
 * whatever you touched. Raised by the operator: "dont cite automatically when
 * message clicked. add reply to button, as other messages have".
 *
 * Every assertion here is on an ELEMENT, for the reason browser-check.mjs is:
 * the quoted words are also in the message being quoted, a few rows up the same
 * transcript, so searching the page for them passes with the feature entirely
 * absent or entirely broken. What a reply is armed at is read off the
 * composer's quoted block - `form [data-citation]`, the CitedMessage the
 * message box draws above itself - because that block IS the console saying
 * "this is what you are about to answer", and it is the only [data-citation]
 * inside a form.
 *
 * Four claims, and the fourth is the regression this change is most likely to
 * cause:
 *
 *   - a click on the body arms nothing;
 *   - the reply control arms it, by pointer and from the keyboard, and the
 *     keyboard half is reached by TAB rather than by focus() alone, because a
 *     control that scripts can focus and a person cannot tab to is not
 *     keyboard reachable;
 *   - the row is not announced as a control it no longer is;
 *   - and a span selected in a body, then CITED with the control beside it,
 *     quotes exactly that span - which is a
 *     deliberate feature and was never what the complaint was about.
 */

import { chromium } from "playwright";

const [base, token, room, lastBody] = process.argv.slice(2);
if (!base || !token || !room || !lastBody) {
  console.error("usage: node scripts/reply-check.mjs BASE_URL TOKEN ROOM LAST_BODY");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

/** What the composer says the next message will answer, or null for nothing. */
const armed = (page) =>
  page.evaluate(() => {
    const block = document.querySelector("form [data-citation]");
    if (!block) return null;
    const quote = block.querySelector("[data-cite-text]");
    return {
      message: block.getAttribute("data-citation"),
      whole: block.getAttribute("data-cite-whole") === "true",
      text: (quote?.textContent || "").trim(),
    };
  });

/** Put the composer back to answering nothing, so the next claim starts clean. */
const disarm = async (page) => {
  const stop = page.locator('form [aria-label="stop replying"]');
  if ((await stop.count()) > 0) await stop.first().click();
  await page.waitForTimeout(200);
  if (await armed(page)) die("the composer still holds a reply target after it was cleared");
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 }).catch(() => {});

  // The witness that this run measured anything. A room that never painted
  // would answer "nothing is armed" to every question below, which is exactly
  // what a working implementation answers to the first one.
  await page.waitForSelector("main [data-body]", { timeout: 15_000 }).catch(() => {});
  const bodies = page.locator("main [data-body]");
  const count = await bodies.count();
  if (count < 2) {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(`#${room} painted ${count} message bodies and this check needs two, so nothing was
tested. The fixture is missing, not the feature.${errors}`);
  }

  const last = bodies.nth(count - 1);
  const prev = bodies.nth(count - 2);
  const lastId = await last.getAttribute("data-body");
  const prevId = await prev.getAttribute("data-body");
  await last.scrollIntoViewIfNeeded();
  await page.waitForTimeout(300);
  const shown = (await last.innerText()).trim();
  if (shown !== lastBody) {
    die(`the last message in #${room} reads ${JSON.stringify(shown)} and this check was told to
expect ${JSON.stringify(lastBody)} - it is reading a different room than it was pointed at`);
  }

  // 1. THE ROW IS NOT A CONTROL. Asked of the body's own ancestors rather than
  // of the page, so a role="button" somewhere else on the screen - a real one,
  // on a real control - cannot fail this and a role on the row cannot escape
  // it.
  const dressed = await bodies.evaluateAll((nodes) =>
    nodes.map((n) => ({
      role: n.closest('[role="button"]') !== null,
      tab: n.closest("[tabindex]") !== null,
    })),
  );
  const asButton = dressed.filter((d) => d.role).length;
  const asTabStop = dressed.filter((d) => d.tab).length;
  if (asButton > 0 || asTabStop > 0) {
    die(`${asButton} of ${count} message rows are still announced as role="button" and
${asTabStop} are still tab stops. A row is not a control: clicking one used to arm a reply at
it, which is the whole of what was reported, and a screen reader is still being told the row
does something it does not.`);
  }

  // 2. A CLICK ON THE BODY ARMS NOTHING. The assertion is on the composer and
  // not on a class: a row that merely stops LOOKING selected while the next
  // message still attaches to it is the same bug wearing less.
  await last.click({ position: { x: 8, y: 8 } });
  await page.waitForTimeout(300);
  const afterClick = await armed(page);
  if (afterClick) {
    die(`clicking a message body armed a reply at ${afterClick.message}: the composer is quoting
${JSON.stringify(afterClick.text.slice(0, 60))}. Reading the room must not decide what you are
answering.`);
  }

  // 3a. THE REPLY CONTROL DOES ARM IT, and it is per message rather than one
  // control somewhere on the page.
  const replies = page.locator("main [data-reply]");
  if ((await replies.count()) === 0) {
    die(`no message in #${room} carries a reply control ([data-reply]), so the only way to answer
one is still a click on the row - which is the thing being replaced.`);
  }
  const lastReply = page.locator(`main [data-reply="${lastId}"]`);
  const prevReply = page.locator(`main [data-reply="${prevId}"]`);
  for (const [id, control] of [
    [lastId, lastReply],
    [prevId, prevReply],
  ]) {
    if ((await control.count()) !== 1) {
      die(`message ${id} has ${await control.count()} reply controls; each message needs its own`);
    }
    const tag = await control.evaluate((n) => n.tagName);
    if (tag !== "BUTTON") {
      die(`the reply control on ${id} is a <${tag.toLowerCase()}> and not a <button>, so enter,
space and the tab order are whatever this file remembered to implement`);
    }
  }

  // The name it answers to. A screen reader moving down the room hears this
  // against every line, so "reply" alone names none of them.
  const names = await replies.evaluateAll((nodes) =>
    nodes.map((n) => (n.getAttribute("aria-label") || n.textContent || "").trim()),
  );
  if (names.some((n) => n.length === 0)) die("a reply control has no accessible name at all");
  if (new Set(names).size !== names.length) {
    die(`the reply controls all answer to the same name (${JSON.stringify(names[0])}), so nothing
says which message any of them replies to`);
  }

  await lastReply.click();
  await page.waitForTimeout(300);
  const byPointer = await armed(page);
  if (!byPointer || byPointer.message !== lastId) {
    die(`clicking the reply control on ${lastId} armed ${JSON.stringify(byPointer)} - the only
way to answer a message does not answer it`);
  }
  if (!byPointer.whole) {
    die("the reply control armed a span citation; pressing reply means the whole message");
  }
  await disarm(page);

  // 3b. AND FROM THE KEYBOARD, reached by tab. The previous message's control
  // is focused and then TAB has to land on this one: that asks the browser
  // whether these are in the tab order and in document order, which .focus()
  // does not - it succeeds on anything with a tabindex and tells you nothing
  // about whether a person could ever get there.
  await prevReply.focus();
  const focusedPrev = await page.evaluate(() => document.activeElement?.getAttribute("data-reply"));
  if (focusedPrev !== prevId) {
    die(`focusing the reply control on ${prevId} left the focus on
${JSON.stringify(focusedPrev)} - it is not focusable`);
  }
  // TAB UNTIL IT ARRIVES, bounded, rather than exactly once.
  //
  // It was one press, on the assumption that a message carries exactly one
  // control. That stopped being true when reactions landed - a row now draws
  // `react` and a `row` chip beside `reply` - and the check failed for a
  // feature being added rather than for the property being broken, which is a
  // check measuring the count of controls when it means to measure the ORDER
  // of them.
  //
  // Bounded and not a loop-until-found: an unbounded walk would eventually
  // reach the next message's reply through the browser chrome and the page
  // furniture and call that a pass. Six is every control a row can draw plus
  // room, and each stop is asserted to be INSIDE the transcript, so a stray tab
  // stop between the two messages still fails.
  const stops = [];
  let focusedLast = null;
  for (let press = 0; press < 6 && focusedLast !== lastId; press += 1) {
    await page.keyboard.press("Tab");
    const at = await page.evaluate(() => {
      const el = document.activeElement;
      return {
        reply: el?.getAttribute("data-reply") ?? null,
        message: el?.closest("[data-message]")?.getAttribute("data-message") ?? null,
      };
    });
    stops.push(at);
    if (at.message === null) {
      die(`tabbing forward from the reply control on ${prevId} left the transcript after
${press + 1} press(es) without reaching ${lastId}: a control between two messages is not on
either of them, so a keyboard reader walking the room falls out of it`);
    }
    focusedLast = at.reply;
  }
  if (focusedLast !== lastId) {
    die(`tabbing forward from the reply control on ${prevId} reached
${JSON.stringify(stops)} rather than ${lastId}: the controls are not in the tab order, so
a keyboard reader cannot arrive at them however they are drawn`);
  }
  await page.keyboard.press("Enter");
  await page.waitForTimeout(300);
  const byKeyboard = await armed(page);
  if (!byKeyboard || byKeyboard.message !== lastId) {
    die(`pressing enter on the focused reply control armed ${JSON.stringify(byKeyboard)}: the
control is reachable and does nothing when it is used`);
  }
  await disarm(page);

  // 4. DRAGGING STILL CITES THE SPAN. This is the feature the complaint was
  // NOT about - a reply quotes exactly the bytes you dragged over - and it
  // shares an element and a gesture with everything above, so it is the thing
  // this change is most likely to break.
  //
  // A real pointer drag, the way copy-check.mjs does it, near the TOP of the
  // box: a tall message is mostly empty space and a drag through its vertical
  // middle selects nothing, which fails exactly like a broken feature.
  const box = await last.boundingBox();
  if (!box) die("the last message has no box on screen, so there was nothing to drag across");
  const y = box.y + 8;
  await page.mouse.move(box.x + 8, y);
  await page.mouse.down();
  for (let i = 1; i <= 20; i++) await page.mouse.move(box.x + 8 + i * 8, y);
  await page.mouse.up();
  await page.waitForTimeout(300);

  const dragged = await page.evaluate(() => window.getSelection()?.toString().trim() ?? "");
  if (dragged.length === 0) {
    die("dragging across the last message selected no text, so nothing about citing was tested");
  }

  // SELECTING IS COPYING; CITING IS A CONTROL. This check used to assert that
  // the drag itself armed the citation - the operator, 2026-08-20: "why
  // whenever i select message text here it automatically becomes a citation? I
  // just wanted to copy it." The span citation below is the feature and it
  // survives; what changed is the gesture that starts it.
  if (await armed(page)) {
    die(`dragging across ${JSON.stringify(dragged)} armed a citation on its own. Selecting is how a
reader copies - citing is the control beside it.`);
  }
  await page.locator(`[data-cite="${lastId}"]`).first().click();
  await page.waitForTimeout(200);
  const bySpan = await armed(page);
  if (!bySpan || bySpan.message !== lastId) {
    die(`with ${JSON.stringify(dragged)} selected, pressing cite armed ${JSON.stringify(bySpan)}:
the control no longer cites the message the selection is in`);
  }
  if (bySpan.whole || bySpan.text === lastBody) {
    die(`dragging over ${JSON.stringify(dragged)} cited the WHOLE of the message. A span citation
that quietly quotes everything is the failure that looks like a working feature until somebody
quotes one clause of a long message.`);
  }
  if (!lastBody.includes(bySpan.text) || !dragged.includes(bySpan.text.slice(0, 8))) {
    die(`the span armed by dragging quotes ${JSON.stringify(bySpan.text)}, which is not the part
of the message that was dragged over (${JSON.stringify(dragged)})`);
  }

  if (crashes.length > 0) die(`the page threw while this ran:\n  ${crashes.join("\n  ")}`);

  console.log(
    `ok  a click on a message arms nothing; its reply control arms it by pointer and by tab+enter; ${count} rows announce no button role; dragging still cites ${JSON.stringify(bySpan.text)}`,
  );
} finally {
  await browser.close();
}
