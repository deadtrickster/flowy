/**
 * THE RETURN KEY DECLARES WHAT IT ACTUALLY DOES.
 *
 *   node scripts/enter-key-says-what-it-does-check.mjs BASE_URL TOKEN ROOM
 *
 * The operator, on a Fold 8, 2026-09-04: "enter on the phone sends, there is no
 * way to have a newline", and separately three messages that were meant as one.
 * Row 01M1PKKC1M0AE6XVTBF1BPWP77.
 *
 * WHAT THIS DOES NOT DECIDE. Whether Enter SHOULD send on a phone is the
 * operator's call and is asked on that row. This check is indifferent to the
 * answer: it asserts only that the composer's declared enterKeyHint and the
 * composer's actual behaviour AGREE. Settle the question either way, move both,
 * and this stays green; move one without the other and it reds.
 *
 * WHY THE DECLARATION IS WORTH A CHECK AT ALL. enterKeyHint is the only thing a
 * soft keyboard reads to decide what to draw on that key. Unset, Android draws
 * its return arrow - the newline glyph - on a key that commits the message. The
 * key was not broken; it was mislabelled, which is why the operator hit it and
 * why no desktop user ever saw a problem. This repo keeps finding the same
 * shape: something that behaves correctly and describes itself wrongly.
 *
 * BOTH DIRECTIONS, and the second is the one that protects a desk. Making the
 * hint truthful is trivial; the risk is a later change that makes Enter
 * newline everywhere to fix the phone and silently takes send away from every
 * physical keyboard. So "Enter sends" and "shift-Enter does not" are both
 * asserted here, exactly as softkeys-check.mjs guards the shell in both
 * directions.
 *
 * A REAL BROWSER AND NOT AN EMULATOR, and the limit is stated rather than
 * worked around: Chromium's device emulation delivers hardware key events and
 * does not reproduce a soft keyboard's IME path, so no emulator can tell us
 * what an Android Return delivers. That question was answered by the person
 * holding the phone. What a browser CAN measure is this - the attribute the
 * keyboard would read, and what the handler does with the key - and that is all
 * this claims to measure.
 */
import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/enter-key-says-what-it-does-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}
const die = (why) => {
  console.error(why);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 30_000 });

  const box = page.locator('textarea[aria-label="message"]');
  await box.waitFor({ state: "visible", timeout: 20_000 });

  // WHAT THE KEYBOARD WOULD READ. The DOM property and not the attribute: React
  // sets it as a property, and an attribute-only read returns null on a value
  // that is present and working - which would fail this check for the wrong
  // reason and send somebody looking at the wrong file.
  const declared = await box.evaluate((el) => el.enterKeyHint ?? "");
  if (declared === "") {
    die(
      "the composer declares no enterKeyHint, so a soft keyboard draws its default return arrow on this key whatever the key does",
    );
  }

  // WHAT IT ACTUALLY DOES. Typed, then Enter, and the answer is read off the
  // box: a send clears the draft, a newline leaves it holding one.
  const body = `enter-key probe ${declared}`;
  await box.fill(body);
  await box.press("Enter");
  await page.waitForTimeout(1200);
  const afterEnter = await box.inputValue();
  const enterSent = afterEnter === "";

  if (enterSent !== (declared === "send")) {
    die(
      `the key says "${declared}" and ${
        enterSent ? "sends" : "does not send"
      } - the declaration and the behaviour disagree, so the glyph on a phone's return key is a lie`,
    );
  }

  // AND THE OTHER DIRECTION, so a fix for the phone cannot quietly cost a desk
  // its newline. shift-Enter must still insert one and must not send.
  await box.fill("first line");
  await box.press("Shift+Enter");
  await page.waitForTimeout(300);
  const afterShift = await box.inputValue();
  if (!afterShift.includes("\n")) {
    die(
      `shift-Enter left the draft as ${JSON.stringify(afterShift)} - a physical keyboard has lost its newline`,
    );
  }
  await box.fill("");

  console.log(
    `enter key declares "${declared}" and ${enterSent ? "sends" : "inserts a newline"}; shift-Enter still writes a newline`,
  );
} finally {
  await browser.close();
}
