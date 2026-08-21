/**
 * Every control the console offers acknowledges the pointer.
 *
 *   node scripts/pointer-check.mjs BASE_URL TOKEN
 *
 * THE OPERATOR SAID "cursor doens change on hover", about the close x that had
 * landed twenty seconds earlier. The x was the one they happened to be pointing
 * at; the cause was the whole console. Tailwind v4's preflight sets no cursor at
 * all - grepped, the only cursor line in preflight.css is a comment about Safari
 * spinners - so every <button> falls back to the UA default, which is an arrow.
 * Four `cursor-pointer` classes existed in web/src against dozens of buttons.
 *
 * So this asks the BROWSER for the computed cursor rather than reading the
 * stylesheet: a rule that is present in index.css and dropped by the build, or
 * beaten by a more specific class, is exactly the failure a text grep calls a
 * pass. A control that does not change the pointer reads as decoration, and the
 * operator finds it by asking the person who built it.
 *
 * Disabled controls are excluded on purpose, and asserted rather than skipped:
 * a button that will refuse the click should not invite it.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/pointer-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

/**
 * The cursors a control is allowed to have, and it is a NAMED SET rather than
 * "anything but default".
 *
 * The check exists because Tailwind v4's preflight sets no cursor at all, so a
 * button reads as decoration unless somebody CHOSE otherwise. "Not default" is
 * the wrong test for that: a control that inherited `text` or `auto` by accident
 * would pass it, and an accident is exactly what this is looking for.
 *
 * col-resize and row-resize are here because a drag handle that says `pointer`
 * is lying about what it does - this check refused a branch over one on
 * 2026-08-21, and the handle was right and the check was wrong. ew-resize and
 * ns-resize are the generic spellings of the same handle.
 *
 * Adding to this list is the correct response to a new kind of control. Widening
 * it to a wildcard is not.
 */
const CHOSEN_CURSORS = ["pointer", "col-resize", "row-resize", "ew-resize", "ns-resize"];

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/`, { timeout: 20_000 }).catch(() => {});
  await page
    .locator("[data-room-list]")
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if (crashes.length > 0) die(`the shell threw: ${crashes.join("; ")}`);

  const read = () =>
    // CHOSEN IS PASSED IN, not closed over. This callback runs IN THE PAGE, so a
    // module-level constant is simply not in scope there - it would arrive as a
    // ReferenceError inside the browser, which surfaces as a crashed evaluate
    // rather than as a failing assertion. Named by orchestrator before either of
    // us ran it; the second argument is the only way across that boundary.
    page.evaluate((chosen) => {
      const label = (el) =>
        (el.getAttribute("aria-label") || el.textContent || "")
          .trim()
          .replace(/\s+/g, " ")
          .slice(0, 40) ||
        el.className.toString().slice(0, 40) ||
        "<unlabelled>";
      const out = { total: 0, arrows: [], disabled: 0, disabledWrong: [] };
      for (const el of document.querySelectorAll('button, [role="button"]')) {
        const cursor = getComputedStyle(el).cursor;
        if (el.disabled) {
          out.disabled += 1;
          // A DISABLED CONTROL STILL MUST NOT SAY "pointer", and that stays
          // exact rather than becoming !dead(): `not-allowed` is the honest
          // cursor for one, and this arm is about the one value that promises
          // a click that will not happen.
          if (cursor === "pointer") out.disabledWrong.push(label(el));
          continue;
        }
        out.total += 1;
        if (!chosen.includes(cursor)) out.arrows.push(`${label(el)} [${cursor}]`);
      }
      return out;
    }, CHOSEN_CURSORS);

  // THE POSITIVE CONTROL. A page with no buttons on it passes every assertion
  // below, and that is how a check reports green on a console that failed to
  // build. The floor is three rather than the six the dogfood node carries,
  // because half of those six are per-room close controls and the gate's node
  // has far fewer rooms - a floor set from a busy node reports "did not render"
  // against a quiet one, which is a false red about the wrong thing.
  const overview = await read();
  if (overview.total < 3) {
    die(`only ${overview.total} enabled controls on the overview - the console did not render`);
  }
  if (overview.arrows.length > 0) {
    die(
      `${overview.arrows.length} of ${overview.total} controls on the overview do not change the pointer:\n  ${overview.arrows.join("\n  ")}`,
    );
  }
  if (overview.disabledWrong.length > 0) {
    die(
      `${overview.disabledWrong.length} disabled controls invite a click they will refuse:\n  ${overview.disabledWrong.join("\n  ")}`,
    );
  }

  // AND THE ROW THE OPERATOR WAS POINTING AT, by name, because that is the one
  // that started this and a console-wide count can be green while it is not.
  await page.locator("[data-close-room]").first().waitFor({ state: "attached", timeout: 10_000 });
  const closer = await page
    .locator("[data-close-room]")
    .first()
    .evaluate((el) => getComputedStyle(el).cursor);
  if (closer !== "pointer") die(`the close-room control is still ${closer}`);

  console.log(
    `${overview.total} enabled controls all point, ${overview.disabled} disabled ones do not, close-room included`,
  );
} finally {
  await browser.close();
}
