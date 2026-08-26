/**
 * A row id somebody pasted into a message is a link, and clicking it lands on
 * the row; a nearly-right string is not a link; and the resolver answers
 * honestly for an id that links but names no row.
 *
 * The resolver route GET /a/{ulid} exists so a renderer can link a bare id
 * without a lookup: it 302s to the row's own page. The pattern that links is
 * the strict ULID - 26 characters, 01 plus 24 Crockford base32 characters,
 * which excludes I, L, O and U - because a wider one turns arbitrary tokens
 * into dead links. So the two fakes below carry the two ways a nearly-right
 * pattern would widen: an I inside the id, and a lowercase one.
 *
 *   node scripts/rowlink-check.mjs BASE_URL TOKEN
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/rowlink-check.mjs BASE_URL TOKEN");
  process.exit(2);
}
refuseRemote(base, "rowlink-check");

const ROOM = "rowlinks";
const AUTH = { Authorization: `Bearer ${token}` };
// 26 characters, but no row id ever contains an I, and row ids are uppercase.
const FAKE_I = "01M0IIIIIIIIIIIIIIIIIIIIII";
const FAKE_LC = "01m0ABCDEFGHJKMNPQRSTVWXY";
// The right shape, and absent from the store: the resolver must say 404.
const UNKNOWN = "01ZZZZZZZZZZZZZZZZZZZZZZZZ";

// A REAL row, filed through the door so the id is minted by the node and the
// row's project comes back with it.
const filed = await fetch(`${base}/api/artifacts`, {
  method: "POST",
  headers: { "Content-Type": "application/json", ...AUTH },
  body: JSON.stringify({
    type: "memory",
    kind: "todo",
    title: "rowlink fixture: the row a pasted id lands on",
    body: "Filed by rowlink-check.mjs so a message can link its id.",
  }),
});
if (!filed.ok) {
  console.error(`the seed row was refused: HTTP ${filed.status} ${await filed.text()}
  Nothing about row links was tested.`);
  process.exit(1);
}
const row = await filed.json();
if (!row.id || !row.project) {
  console.error(`the seed row came back without an address: id=${row.id} project=${row.project}
  Nothing about row links was tested.`);
  process.exit(1);
}

const said = await fetch(`${base}/api/chat/${ROOM}/say`, {
  method: "POST",
  headers: { "Content-Type": "application/json", ...AUTH },
  body: JSON.stringify({ body: `see ${row.id} and not ${FAKE_I} and not ${FAKE_LC}.` }),
}).catch((err) => ({ ok: false, status: 0, text: async () => String(err) }));
if (!said.ok) {
  console.error(`the seed message was refused: HTTP ${said.status} ${await said.text()}
  Nothing about row links was tested.`);
  process.exit(1);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${ROOM}`, { timeout: 20_000 }).catch(() => {});
  await page.waitForSelector("main [data-body]", { timeout: 20_000 }).catch(() => {});

  // ARM 1: the real id renders as an anchor to the resolver, and clicking it
  // lands on the row's own page.
  const anchor = page.locator(`main a[href="/a/${row.id}"]`).last();
  try {
    await anchor.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    console.error(`the pasted row id is not a link
  wanted an anchor for /a/${row.id} in the room, and the message renders without one.
  A row id in a message must be a link.`);
    process.exit(1);
  }
  const [popup] = await Promise.all([
    page.waitForEvent("popup", { timeout: 15_000 }),
    anchor.click(),
  ]);
  const rowUrl = new RegExp(`/p/${row.project}/memory/${row.id}$`);
  try {
    await popup.waitForURL(rowUrl, { timeout: 15_000 });
  } catch {
    console.error(`clicking the row id did not land on the row
  the resolver sent the browser to ${popup.url()}, wanted ${rowUrl}.
  A clicked id must land on the row it names.`);
    process.exit(1);
  }
  try {
    await popup.getByText(row.title, { exact: false }).first().waitFor({
      state: "visible",
      timeout: 15_000,
    });
  } catch {
    console.error(`the row page does not show the row the id named
  ${popup.url()} never drew the title "${row.title}".
  A clicked id must land on the row it names.`);
    process.exit(1);
  }
  await popup.close();

  // ARM 2: the near-misses stay text. The strict pattern is the whole
  // negative arm - a pattern that links FAKE_I or FAKE_LC has widened.
  const hrefs = await page
    .locator('main a[href^="/a/"]')
    .evaluateAll((els) => els.map((el) => el.getAttribute("href")));
  const widened = hrefs.filter((h) => h === `/a/${FAKE_I}` || h === `/a/${FAKE_LC}`);
  if (widened.length > 0) {
    console.error(`a 26-character string that is not a row id became a link
  the message links ${widened.join(" ")} - an I no id has, or a lowercase one.
  Only the strict ULID pattern may link; a nearly-right string must stay text.`);
    process.exit(1);
  }

  // ARM 3: the resolver's honesty. An id that links but names no row answers
  // 404, not a redirect to nowhere, and a path that is not even shaped like
  // an id answers the same. The real id answers 302 to the row's own address.
  const unknown = await fetch(`${base}/a/${UNKNOWN}`, {
    redirect: "manual",
    headers: AUTH,
  });
  if (unknown.status !== 404) {
    console.error(`the resolver did not 404 for an id that names no row
  GET /a/${UNKNOWN} answered ${unknown.status}, wanted 404.
  A link that names no row must stop at an honest not-found.`);
    process.exit(1);
  }
  const malformed = await fetch(`${base}/a/${FAKE_I}`, { redirect: "manual", headers: AUTH });
  if (malformed.status !== 404) {
    console.error(`the resolver did not 404 for a path that is not a row id
  GET /a/${FAKE_I} answered ${malformed.status}, wanted 404.
  /a/ only ever answers 302 or 404.`);
    process.exit(1);
  }
  const real = await fetch(`${base}/a/${row.id}`, { redirect: "manual", headers: AUTH });
  if (real.status !== 302) {
    console.error(`the resolver did not redirect for the row id it was made for
  GET /a/${row.id} answered ${real.status}, wanted 302.
  A pasted id must resolve to its row.`);
    process.exit(1);
  }
  const location = real.headers.get("location");
  if (location !== `/p/${row.project}/memory/${row.id}`) {
    console.error(`the resolver sent the id to the wrong address
  wanted /p/${row.project}/memory/${row.id}, got ${location}.
  A pasted id must resolve to the row it names.`);
    process.exit(1);
  }
} finally {
  await browser.close();
}
