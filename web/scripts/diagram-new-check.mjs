/**
 * The new button on /diagrams, driven in a real browser.
 *
 *   node scripts/diagram-new-check.mjs BASE_URL TOKEN
 *
 * This exists because "cant create a diagram - new button doesnt work" was
 * reported by an operator against a console where every unit test passed and
 * the node's write door was fine. Nothing anywhere exercised the CLICK, so the
 * one thing that was broken was the one thing nothing looked at: the button
 * was disabled until the box beside it had a name in it, and a disabled button
 * on this page has no cursor, no hover, no message and no navigation. A dead
 * button and a dead page are the same thing from the outside.
 *
 * So the check clicks new with the name box EMPTY, which is the state the
 * report came from. It is deliberately not "type a name and click", because
 * that path already worked and a check that took it would have passed against
 * the broken console.
 *
 * Four things are asserted, because the four ways this can fail want telling
 * apart in the output rather than in somebody's head afterwards:
 *
 *   1. the click reaches the handler        - a POST leaves the page
 *   2. the node accepts the write           - that POST is a 2xx
 *   3. the console navigates to the diagram - the url becomes /diagrams/<id>
 *   4. the editor loads                     - drawio reports itself ready
 *
 * Then the title is edited, because a diagram created with no name has to be
 * nameable afterwards or the fix to 1 has only moved the problem.
 *
 * The node is asked at the end as well as the page: a console that navigated
 * to a plausible url without the row landing would satisfy every assertion
 * about the page and none about the diagram.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);

if (!base || !token) {
  console.error("usage: node scripts/diagram-new-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const die = (why) => {
  console.error(why);
  process.exit(1);
};

const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1600, height: 1000 } });
const page = await context.newPage();

const crashes = [];
page.on("pageerror", (err) => crashes.push(String(err)));

// Every write the page makes, with its status, so a refused POST is reported
// as a refused POST rather than as "the url did not change".
const writes = [];
page.on("response", (res) => {
  const req = res.request();
  if (req.method() === "POST" && new URL(res.url()).pathname === "/api/artifacts") {
    writes.push(res.status());
  }
});

await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
await page
  .goto(`${base}/diagrams`, { timeout: 30_000 })
  .catch((err) => die(`the page did not load: ${err}`));

const box = page.locator('input[aria-label="new diagram title"]');
const button = page.getByRole("button", { name: /^(new|creating…)$/ });

try {
  await button.waitFor({ state: "visible", timeout: 15_000 });
} catch {
  die("there is no new button on /diagrams");
}

if (await box.inputValue()) die("the name box is not empty - this check has to start from empty");

if (await button.isDisabled()) {
  die(
    "the new button is disabled with the name box empty: an operator who does not " +
      "read the box as required clicks it, and is told nothing at all",
  );
}

const listed = page.url();
await button
  .click({ timeout: 10_000 })
  .catch((err) => die(`the new button did not take a click: ${err}`));

// The navigation is the console's own, so it is waited for as a url change
// rather than as a load.
await page
  .waitForURL(/\/diagrams\/[^/]+$/, { timeout: 20_000 })
  .catch(() =>
    die(
      writes.length === 0
        ? `clicking new wrote nothing - the handler never ran (still at ${page.url()})`
        : `the node answered ${writes.join(",")} to the write and the console stayed at ${page.url()}`,
    ),
  );

if (page.url() === listed) die("the url did not change");
if (!writes.some((status) => status >= 200 && status < 300)) {
  die(`no write succeeded: the node answered ${writes.join(",") || "nothing"}`);
}

const id = decodeURIComponent(page.url().split("/").pop());
if (!id) die("the diagram url carries no id");

// The editor, not a page that merely routed. data-drawio-ready is set by the
// host when drawio has announced itself over the embed protocol, so this is
// the editor saying it is up rather than an iframe element existing.
const holder = page.locator("[data-drawio-ready]");
try {
  await holder.waitFor({ state: "visible", timeout: 20_000 });
  await page.waitForFunction(
    () =>
      document.querySelector("[data-drawio-ready]")?.getAttribute("data-drawio-ready") === "yes",
    null,
    { timeout: 40_000 },
  );
} catch {
  die(`the diagram opened at ${id} but the draw.io editor never became ready`);
}

// A diagram made with no name has to be nameable, or the default is permanent.
const named = `named in the editor ${Date.now()}`;
const titleBox = page.locator('[data-testid="diagram-title"]');
try {
  await titleBox.waitFor({ state: "visible", timeout: 10_000 });
} catch {
  die("the diagram has no title box, so a diagram created unnamed can never be named");
}
await titleBox.fill(named);
await titleBox.press("Enter");

/** The diagram as the node holds it - the only version that outlives the tab. */
const fromTheNode = async () => {
  const answer = await fetch(`${base}/api/artifact/${encodeURIComponent(id)}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!answer.ok) {
    die(`the node does not hold the diagram the console navigated to: ${answer.status}`);
  }
  return answer.json();
};

// Polled rather than waited for on the page: the save indicator reads "saved"
// before a rename as well as after one, so a page assertion here would pass
// against a console that never wrote anything.
let held = await fromTheNode();
for (let tries = 0; tries < 30 && held.title !== named; tries++) {
  await page.waitForTimeout(500);
  held = await fromTheNode();
}
if (held.title !== named) {
  die(`the node holds the title ${JSON.stringify(held.title)}, want ${JSON.stringify(named)}`);
}
if (!held.body.includes("mxGraphModel")) {
  die(
    `the node holds a body that is not a drawio document: ${JSON.stringify(held.body.slice(0, 120))}`,
  );
}

if (crashes.length) die(`the page threw: ${crashes.join(" | ")}`);

console.log(`new with an empty name made ${id}, the editor came up, and the node holds it renamed`);

await browser.close();
