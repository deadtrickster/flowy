/**
 * What a selected range of terminal reads like when it reaches a room.
 *
 *   node scripts/termselect-check.mjs
 *
 * 01M1558DPM1HRGZNJGMVW24DHF item 4. Telling another agent what happened meant
 * retyping the screen or describing it, and both lose the thing that matters -
 * the exact bytes and which lines they were.
 *
 * NO BROWSER, AND THAT IS NOT LAZINESS. The obvious check - open /vms, drag
 * over the terminal, press the button - cannot run: /api/agent/socket is
 * operator-only and refused for a token-only console, so the terminal has no
 * output on it and there is nothing to select. A check that pressed Run and
 * dragged over an empty black rectangle would assert that zero lines format
 * correctly, which is true of a function that does nothing.
 *
 * So this drives the pure part directly, the way clock-check.mjs next door does
 * - esbuild the real module and import it, rather than pasting a copy of the
 * function into this file where it would stop being the thing that ships.
 */

import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";

const here = dirname(fileURLToPath(import.meta.url));
const bundled = await build({
  entryPoints: [resolve(here, "..", "src", "lib", "termselect.ts")],
  bundle: true,
  format: "esm",
  write: false,
  logLevel: "silent",
});
const { selectionMessage } = await import(
  `data:text/javascript;base64,${Buffer.from(bundled.outputFiles[0].text).toString("base64")}`
);

const bad = [];
const check = (what, ok, saw) => {
  if (!ok) bad.push(`${what}\n     saw: ${JSON.stringify(saw)}`);
};

const two = selectionMessage("first\nsecond");
check(
  "the lines themselves are carried",
  two.body.includes("first") && two.body.includes("second"),
  two.body,
);
check("they are fenced, so a room renders them as output", two.body.includes("```"), two.body);
check("it counts what it is sending", two.lines === 2, two.lines);

// THE NUMBERS ARE THE POINT. Output with no line numbers is output somebody has
// to count by hand to answer "which line".
const known = selectionMessage("alpha\nbeta", { from: 41 });
check(
  "numbered from the terminal's own row when it knows one",
  known.body.includes("42  alpha") && known.body.includes("43  beta"),
  known.body,
);

// AND IT DOES NOT INVENT ONE. A number that pretends to be a screen row sends
// the reader to that row to look at something else.
const guessed = selectionMessage("alpha\nbeta");
check(
  "numbered from 1 when the terminal did not say where",
  guessed.body.includes("1  alpha") && guessed.body.includes("2  beta"),
  guessed.body,
);

check(
  "says how many and which machine",
  /^1 line from the host shell/.test(selectionMessage("only one", { where: "host" }).body),
  selectionMessage("only one", { where: "host" }).body.split("\n")[0],
);
check(
  "names the project when there is one",
  /^2 lines from the microVM shell in flowy/.test(
    selectionMessage("a\nb", { where: "vm", project: "flowy" }).body,
  ),
  selectionMessage("a\nb", { where: "vm", project: "flowy" }).body.split("\n")[0],
);

// Dragging past the last row is how a selection picks up trailing blanks, and
// nobody means to send them.
check(
  "drops the blank lines a drag picks up at the end",
  selectionMessage("real\n\n\n").lines === 1,
  selectionMessage("real\n\n\n").lines,
);
// But a blank line INSIDE the selection is content and stays.
check(
  "keeps a blank line inside the selection",
  selectionMessage("top\n\nbottom").lines === 3,
  selectionMessage("top\n\nbottom").lines,
);

if (bad.length > 0) {
  console.error(`a terminal selection does not read as it should:\n  - ${bad.join("\n  - ")}`);
  process.exit(1);
}
console.log(
  `a terminal selection becomes a numbered, fenced message that says how many lines and which machine - ${two.lines} and ${known.body.split("\n")[0]}`,
);
