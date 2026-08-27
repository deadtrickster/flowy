/**
 * Whether a time label says WHICH DAY it means.
 *
 * 01M10Y3JBD, the operator: "all time labels must show the date 'if not today'
 * chats - memoryies -todos". A bare "23:14:02" is unambiguous for about a day
 * and then lies by omission - a row from Tuesday and one from ten minutes ago
 * render identically. Every list in this console renders its stamp through one
 * helper, lib/utils clock(), so this drives that helper directly.
 *
 * THE ASSERTION IS AGAINST THE BARE TIME, not against letters or spaces in the
 * output. The first version of this check looked for a space or a letter and
 * failed three CORRECT answers, because "9:05:07 AM" has both under a 12-hour
 * locale. A date is present when the rendering differs from what
 * toLocaleTimeString gives for the same instant, and that holds in every locale
 * the reader might have.
 *
 * The two same-day extremes are here on purpose: 00:05 and 23:55 are twenty
 * minutes either side of a boundary, and they are what separates "same calendar
 * day" from "less than N hours ago". An elapsed-hours rule passes the easy
 * cases and gets both of these wrong.
 */

import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";

// BUNDLED, not merely transformed. api-error-check.mjs next door imports its
// module as a data url after a bare transform, and that works because api.ts
// imports nothing - utils.ts imports clsx and tailwind-merge for cn(), and a
// data url has no base to resolve a bare specifier against. Bundling inlines
// them, so the thing under test is the real source rather than a copy of the
// function pasted into this file.
const here = dirname(fileURLToPath(import.meta.url));
const source = resolve(here, "..", "src", "lib", "utils.ts");
const bundled = await build({
  entryPoints: [source],
  bundle: true,
  format: "esm",
  write: false,
  logLevel: "silent",
});
const { clock } = await import(
  `data:text/javascript;base64,${Buffer.from(bundled.outputFiles[0].text).toString("base64")}`
);

const now = new Date();
const at = (mutate) => {
  const d = new Date(now);
  mutate(d);
  return d.toISOString();
};

const cases = [
  ["today, mid-morning", at((d) => d.setHours(9, 5, 7, 0)), false],
  ["today, 00:05", at((d) => d.setHours(0, 5, 0, 0)), false],
  ["today, 23:55", at((d) => d.setHours(23, 55, 0, 0)), false],
  ["yesterday", at((d) => d.setDate(d.getDate() - 1)), true],
  ["last year", at((d) => d.setFullYear(d.getFullYear() - 1)), true],
];

const failures = [];
for (const [label, iso, wantDate] of cases) {
  const out = clock(iso);
  const hasDate = out !== new Date(iso).toLocaleTimeString();
  if (hasDate !== wantDate) {
    failures.push(
      `${label}: ${JSON.stringify(out)} ${hasDate ? "carries" : "omits"} a date, wanted it ${wantDate ? "carried" : "omitted"}`,
    );
  }
}

// The year appears only where it disambiguates, or every line of a live room
// grows a "2026" that never changes.
const lastYear = clock(at((d) => d.setFullYear(d.getFullYear() - 1)));
if (!lastYear.includes(String(now.getFullYear() - 1))) {
  failures.push(`a stamp from another year does not name it: ${JSON.stringify(lastYear)}`);
}
const yesterday = clock(at((d) => d.setDate(d.getDate() - 1)));
if (yesterday.includes(String(now.getFullYear()))) {
  failures.push(`a stamp from this year names the year anyway: ${JSON.stringify(yesterday)}`);
}

// An unparseable stamp is still the empty string, not "Invalid Date".
if (clock("not a date") !== "") {
  failures.push(`an unparseable stamp rendered ${JSON.stringify(clock("not a date"))}`);
}

if (failures.length > 0) {
  console.error(`time labels do not say which day they mean:\n${failures.join("\n")}`);
  process.exit(1);
}

console.log(
  "time labels carry the date when it is not today, and the year when it is not this one",
);
process.exit(0);
