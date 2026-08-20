/**
 * The merge queue tab counts what the node says, and calls unjudged rows
 * QUEUED rather than refused.
 *
 *   node scripts/merge-tab-check.mjs BASE_URL TOKEN
 *
 * THE OPERATOR: "merges tab is always 0". Measured on the live console before
 * anything was changed, the tab read "0 may land, 2 refused" - while both of
 * those rows carried "no gate has measured it" on their own cards and one of
 * them was gating. Nothing had refused either of them.
 *
 * The cause was `admissible === false` standing in for refused. On this wire
 * absent means nobody asked, false means asked and no, and the node answers
 * false for a row it has not measured yet. So on a working queue almost every
 * row was drawn red, and a reader who learns the red means nothing stops
 * reading the red at all.
 *
 * WHAT THIS ASSERTS IS AGREEMENT WITH THE NODE, not a number. The three counts
 * come from /api/merge-queue and must add up to the rows in it, with `blocked`
 * reserved for rows that actually carry the field. A check pinning literal
 * numbers would fail every time the queue moved, which on this node is
 * constantly, and would teach whoever hit it to delete the check.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/merge-tab-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

const queue = await fetch(`${base}/api/merge-queue`, {
  headers: { Authorization: `Bearer ${token}` },
}).then((r) => (r.ok ? r.json() : die(`/api/merge-queue answered ${r.status}`)));
const rows = queue.items ?? [];

// WHAT THE NODE SAYS, worked out here so the browser's numbers are compared
// against the source rather than against this file's idea of the queue.
const wantLand = rows.filter((r) => r.admissible === true).length;
const wantBlocked = rows.filter((r) => r.blocked).length;
const wantQueued = rows.filter((r) => r.admissible !== true && !r.blocked).length;
if (wantLand + wantBlocked + wantQueued !== rows.length) {
  die(`the three states do not partition the queue: ${wantLand}+${wantBlocked}+${wantQueued} != ${rows.length}.
That is a fault in this check, not in the console.`);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 950 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/todos`, { timeout: 30_000 }).catch(() => {});

  // WAITED FOR BY ITS OWN TEXT. The counts render only once the merges have
  // loaded - `mergesLoaded` - so reading the tab bar at first paint reads it
  // before the answer arrived, which is a check failing for the console being
  // fast. "may land" is the first stat and appears with the rest of them.
  const bar = page.getByText("may land", { exact: false }).first();
  await bar.waitFor({ state: "visible", timeout: 25_000 }).catch(() => {});
  if ((await bar.count()) === 0) {
    const main = await page
      .locator("main")
      .innerText()
      .catch(() => "");
    die("the merge queue tab never showed its counts:", main.slice(0, 500));
  }

  const text = (
    await page
      .locator("main")
      .innerText()
      .catch(() => "")
  ).replace(/\s+/g, " ");
  const readCount = (what) => {
    const m = text.match(new RegExp(`(\\d+)\\s+${what}`));
    return m ? Number(m[1]) : null;
  };

  // REFUSED MUST BE GONE, and this is the assertion the row is about. A row
  // nobody has judged is not a row somebody refused.
  if (/\d+\s+refused/.test(text)) {
    die(`the tab still says "refused". A row the drainer has not measured carries
"no gate has measured it" as its own reason - calling that refused is an
else-branch wearing an alarming word, which is what taught the operator to
ignore the tab.`);
  }

  for (const [what, want] of [
    ["may land", wantLand],
    ["blocked", wantBlocked],
    ["queued", wantQueued],
  ]) {
    const got = readCount(what);
    if (got === null) {
      die(
        `the tab has no "${what}" count. The three states have to be on screen together -
one of them missing is how "0 may land" came to be read as "the tab is always 0".`,
        text.slice(0, 400),
      );
    }
    if (got !== want) {
      die(
        `the tab says ${got} ${what} and /api/merge-queue says ${want}. The tab is counting
something other than what the node answered.`,
        text.slice(0, 400),
      );
    }
  }
  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);
  console.log(
    `the merge tab agrees with the node: ${wantLand} may land, ${wantBlocked} blocked, ${wantQueued} queued`,
  );
} finally {
  await browser.close();
}
