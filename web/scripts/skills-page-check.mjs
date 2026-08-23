/**
 * The skills shelf, driven in a real browser.
 *
 *   node scripts/skills-page-check.mjs BASE_URL TOKEN
 *
 * The diagrams page learned the hard way that a create button nobody can
 * press fails silently: every unit test passed while the button sat disabled
 * waiting for a name, and a disabled button looks exactly like a dead page
 * from outside. The skills page is the same shape, so this check asks the
 * same questions plus the one skills add:
 *
 *   1. clicking new with BOTH boxes empty refuses loudly   - an error line, no write
 *   2. clicking new with both filled writes                - a POST leaves the page
 *   3. the node accepts the write                          - that POST is a 2xx
 *   4. the console navigates to the row                    - the url is /p/.../memory/<id>
 *   5. the node holds a skill                              - type memory, kind skill
 *   6. the body renders as markdown                        - an <h1>, not a <pre> dump
 *   7. the shelf lists it                                  - back on /skills, the row is there
 *   8. the kind filter the shelf is built on answers it    - GET ?type=memory&kind=skill
 *
 * The node is asked as well as the page, because a console that navigated to
 * a plausible url without the row landing would satisfy every assertion
 * about the page and none about the skill.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);

if (!base || !token) {
  console.error("usage: node scripts/skills-page-check.mjs BASE_URL TOKEN");
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
  .goto(`${base}/skills`, { timeout: 30_000 })
  .catch((err) => die(`the page did not load: ${err}`));

const titleBox = page.locator('input[aria-label="new skill title"]');
const bodyBox = page.locator('textarea[aria-label="new skill body"]');
const button = page.getByRole("button", { name: /^(new|creating…)$/ });

try {
  await button.waitFor({ state: "visible", timeout: 15_000 });
} catch {
  die("there is no new button on /skills");
}

if (await button.isDisabled()) {
  die("the new button is disabled - a disabled button and a dead page look the same from outside");
}

// 1. Empty boxes refuse loudly, and write nothing. The diagrams bug this
//    guards against: the click must always answer, with a sentence if not a
//    row.
await button
  .click({ timeout: 10_000 })
  .catch((err) => die(`the new button did not take a click: ${err}`));
const refused = page.locator("p.text-destructive");
try {
  await refused.waitFor({ state: "visible", timeout: 10_000 });
} catch {
  die("clicking new with both boxes empty wrote nothing and said nothing - the click must answer");
}
if (writes.length !== 0) {
  die(`clicking new with both boxes empty wrote to the node: ${writes.join(",")}`);
}
if (page.url() !== `${base}/skills`) {
  die(`the empty click navigated away: ${page.url()}`);
}

// 2-4. Filled boxes write, the node accepts, and the console follows the row.
const named = `a skill from the check ${Date.now()}`;
const body = `# ${named}\n\nthe procedure: do the thing, then measure it.`;
await titleBox.fill(named);
await bodyBox.fill(body);
await button
  .click({ timeout: 10_000 })
  .catch((err) => die(`the filled new button did not take a click: ${err}`));

await page
  .waitForURL(/\/p\/[^/]+\/memory\/[^/]+$/, { timeout: 20_000 })
  .catch(() =>
    die(
      writes.length === 0
        ? `clicking new wrote nothing - the handler never ran (still at ${page.url()})`
        : `the node answered ${writes.join(",")} to the write and the console stayed at ${page.url()}`,
    ),
  );

if (!writes.some((status) => status >= 200 && status < 300)) {
  die(`no write succeeded: the node answered ${writes.join(",") || "nothing"}`);
}

const id = decodeURIComponent(page.url().split("/").pop());
if (!id) die("the skill url carries no id");

/** The skill as the node holds it - the only version that outlives the tab. */
const fromTheNode = async () => {
  const answer = await fetch(`${base}/api/artifact/${encodeURIComponent(id)}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!answer.ok) {
    die(`the node does not hold the skill the console navigated to: ${answer.status}`);
  }
  return answer.json();
};

const held = await fromTheNode();
if (held.type !== "memory") die(`the node holds type ${JSON.stringify(held.type)}, want memory`);
if (held.kind !== "skill") die(`the node holds kind ${JSON.stringify(held.kind)}, want skill`);
if (held.title !== named) {
  die(`the node holds the title ${JSON.stringify(held.title)}, want ${JSON.stringify(named)}`);
}
if (held.body !== body) {
  die(
    `the node holds a body that is not the one written: ${JSON.stringify(held.body?.slice(0, 80))}`,
  );
}

// 6. Rendered, not dumped: the body's own heading is an h1 element. A body
//    drawn as a <pre> would still match [data-artifact-body] but carries no
//    h1, so this tells "markdown rendered" from "text shown".
const heading = page.locator("[data-artifact-body] h1");
try {
  await heading.waitFor({ state: "visible", timeout: 10_000 });
} catch {
  die(`the skill opened at ${id} but its body did not render as markdown`);
}
if ((await heading.textContent()) !== named) {
  die(
    `the rendered heading is ${JSON.stringify(await heading.textContent())}, want ${JSON.stringify(named)}`,
  );
}

// 7. The shelf lists it - the page the operator reads the collection from.
await page.goto(`${base}/skills`, { timeout: 30_000 });
const row = page.locator(`li[data-skill="${id}"] a`);
try {
  await row.waitFor({ state: "visible", timeout: 10_000 });
} catch {
  die(`the skill ${id} is not on the /skills shelf`);
}

// 8. The kind filter the shelf is built on answers it, so the shelf is the
//    door an agent uses too, not a page with its own private index.
const listed = await fetch(`${base}/api/artifacts?type=memory&kind=skill&limit=200`, {
  headers: { Authorization: `Bearer ${token}` },
});
if (!listed.ok) die(`the kind filter the shelf is built on answered ${listed.status}`);
const page_ = await listed.json();
const ids = (page_.artifacts ?? []).map((a) => a.id);
if (!ids.includes(id)) {
  die("the node's kind filter does not list the skill the shelf shows");
}

if (crashes.length) die(`the page threw: ${crashes.join(" | ")}`);

console.log(
  `new with both boxes empty refused loudly and wrote nothing; a filled create made ${id}, rendered its markdown, and the shelf lists it`,
);

await browser.close();
