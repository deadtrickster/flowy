/**
 * THE KEYS A PHONE SENDS MUST REACH THE SHELL, AND A REAL KEYBOARD MUST NOT
 * SEND EVERYTHING TWICE.
 *
 *   node scripts/softkeys-check.mjs
 *
 * 01M1558DPM1HRGZNJGMVW24DHF item 5. Android soft keyboards report keydown 229
 * for keys that are not composition - backspace above all - and ghostty-web's
 * handleKeyDown returns early on 229 while its own beforeinput listener calls
 * preventDefault. So on a phone, backspace reaches nothing whatever.
 *
 * BOTH DIRECTIONS ARE ASSERTED AND THE SECOND ONE IS THE DANGEROUS ONE. Making
 * the phone work is easy; doing it without breaking every physical keyboard is
 * the actual problem, because a real keystroke fires BOTH a keydown that
 * ghostty already sent and a beforeinput for the same character. The naive fix
 * types everything twice for every desktop user, which is a far worse bug than
 * the one being fixed - so "a real keystroke sends exactly once" is checked
 * here beside "a soft key sends at all".
 *
 * NO BROWSER, AND THAT IS NOT A COMPROMISE HERE. The module takes an element,
 * a send function and a clock, and everything under test is the decision it
 * makes between two events. A fake element that records listeners drives it
 * exactly, deterministically, in milliseconds - where a real browser could not
 * produce an Android keydown 229 at all without emulating a device.
 *
 * THE CLOCK IS INJECTED for the same reason: the guard is a time window, and a
 * check that slept through it would be slow and would still be a race. Here the
 * clock is a variable, so "the same keystroke" and "a new one much later" are
 * exact rather than probable.
 */

import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";

const here = dirname(fileURLToPath(import.meta.url));
const bundled = await build({
  entryPoints: [resolve(here, "..", "src", "lib", "softkeys.ts")],
  bundle: true,
  format: "esm",
  write: false,
  logLevel: "silent",
});
const { attachSoftKeyboard } = await import(
  `data:text/javascript;base64,${Buffer.from(bundled.outputFiles[0].text).toString("base64")}`
);

const bad = [];
const check = (what, ok, saw) => {
  if (!ok) bad.push(`${what}\n     saw: ${JSON.stringify(saw)}`);
};

// An element that is only a listener table - which is all the module uses it
// for, so the fake is complete rather than partial.
function fakeElement() {
  const on = {};
  return {
    addEventListener: (type, fn) => {
      if (!on[type]) on[type] = [];
      on[type].push(fn);
    },
    removeEventListener: (type, fn) => {
      on[type] = (on[type] || []).filter((f) => f !== fn);
    },
    fire: (type, event) => {
      for (const fn of on[type] || []) fn(event);
    },
    count: (type) => (on[type] || []).length,
  };
}

function harness() {
  const el = fakeElement();
  const sent = [];
  let clock = 1000;
  const detach = attachSoftKeyboard(
    el,
    (d) => sent.push(d),
    () => clock,
  );
  return {
    el,
    sent,
    detach,
    tick: (ms) => {
      clock += ms;
    },
    // The two events a physical keystroke produces, in the order the browser
    // dispatches them.
    realKey: (key, inputType = "insertText", data = key) => {
      el.fire("keydown", { key, keyCode: key.charCodeAt(0), isComposing: false });
      el.fire("beforeinput", { inputType, data });
    },
    // What a soft keyboard produces: a placeholder keydown ghostty drops, then
    // the beforeinput that carries the real intent.
    softKey: (inputType, data = null) => {
      el.fire("keydown", { key: "Unidentified", keyCode: 229, isComposing: false });
      el.fire("beforeinput", { inputType, data });
    },
  };
}

// THE BUG THE ITEM IS ABOUT.
{
  const h = harness();
  h.softKey("deleteContentBackward");
  check(
    "backspace from a soft keyboard reaches the shell as DEL",
    h.sent.join("") === "\x7f",
    h.sent,
  );
}
{
  const h = harness();
  h.softKey("insertLineBreak");
  h.tick(500);
  h.softKey("insertParagraph");
  check(
    "both spellings of enter reach the shell as carriage returns, which is what a pty expects",
    h.sent.join("") === "\r\r",
    h.sent,
  );
}
{
  const h = harness();
  h.softKey("insertText", "swipe");
  check(
    "text with no composition around it - swipe, autocorrect - is sent",
    h.sent.join("") === "swipe",
    h.sent,
  );
}

// THE REGRESSION THE FIX CAN CAUSE, which lands on every desktop user rather
// than on phones.
{
  const h = harness();
  h.realKey("a");
  check("a real keystroke is not sent a second time by this listener", h.sent.length === 0, h.sent);
}
{
  const h = harness();
  h.realKey("a");
  h.realKey("b");
  h.realKey("c");
  check("nor is a run of them", h.sent.length === 0, h.sent);
}

// A KEYDOWN THAT PRODUCES NO BEFOREINPUT MUST NOT SWALLOW THE NEXT ONE. Arrows,
// function keys and bare modifiers all do this, and a flag cleared only by the
// next beforeinput would be left standing by them - so the very next soft key
// would go missing, intermittently, which is the worst way for this to fail.
{
  const h = harness();
  h.el.fire("keydown", { key: "ArrowUp", keyCode: 38, isComposing: false });
  h.tick(400);
  h.softKey("deleteContentBackward");
  check(
    "a soft key still lands after an arrow key that produced no beforeinput",
    h.sent.join("") === "\x7f",
    h.sent,
  );
}

// COMPOSITION IS NOT THIS MODULE'S. ghostty-web's compositionend already calls
// onDataCallback with the finished text; acting on the intermediate events here
// would send every half-typed form as well as the result.
{
  const h = harness();
  h.el.fire("keydown", { key: "Unidentified", keyCode: 229, isComposing: true });
  h.el.fire("beforeinput", { inputType: "insertCompositionText", data: "korea" });
  check(
    "composition is left to the emulator, which already sends it on compositionend",
    h.sent.length === 0,
    h.sent,
  );
}

// An inputType nobody mapped must do nothing rather than send an empty string
// or a guess.
{
  const h = harness();
  h.softKey("formatBold");
  check("an inputType this does not map sends nothing", h.sent.length === 0, h.sent);
}

// DETACH, because the panel rebuilds its terminal on every run and a listener
// left on a discarded element is a second sender for the next session.
{
  const h = harness();
  h.detach();
  check("detach removes the keydown listener", h.el.count("keydown") === 0, h.el.count("keydown"));
  check(
    "detach removes the beforeinput listener",
    h.el.count("beforeinput") === 0,
    h.el.count("beforeinput"),
  );
  h.softKey("deleteContentBackward");
  check("and nothing is sent after it", h.sent.length === 0, h.sent);
}

if (bad.length > 0) {
  console.error(
    `soft keyboard input is wrong in ${bad.length} way(s):\n\n  - ${bad.join("\n  - ")}`,
  );
  process.exit(1);
}
console.log(
  "soft keys reach the shell (backspace, enter, uncomposed text), a real keyboard sends once, composition is left alone, and detach stops it",
);
