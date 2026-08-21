/**
 * Typing @ offers the names the node would resolve, and Enter takes one instead
 * of sending.
 *
 *   node scripts/at-suggest-check.mjs BASE_URL TOKEN ROOM
 *
 * THE OPERATOR: "no suggestions when I type @". The row (01M0GGSMBD) says why it
 * is more than convenience: a mention only becomes a mention if the name
 * resolves at write time - mentions.go records the resolved pairs in
 * meta.mentions, and a name that resolves to nobody is drawn as prose and
 * addresses no one. A typo produces a message that looks addressed and reaches
 * nobody.
 *
 * THE ARM THIS FILE EXISTS FOR IS THE ENTER KEY. This composer sends on Enter.
 * A suggestion list that let Enter through would send the half-typed name the
 * instant somebody tried to accept it - turning the feature into a machine for
 * producing exactly the defect it was built to prevent. That is asserted by
 * COUNTING THE MESSAGES IN THE ROOM across the keypress: a send is a row on the
 * node, so nothing here has to trust the DOM about whether one happened.
 */

import { chromium } from "playwright";

const [base, token, room] = process.argv.slice(2);
if (!base || !token || !room) {
  console.error("usage: node scripts/at-suggest-check.mjs BASE_URL TOKEN ROOM");
  process.exit(2);
}

const bearer = { Authorization: `Bearer ${token}` };
const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

const said = async () => {
  const r = await fetch(`${base}/api/chat/${encodeURIComponent(room)}?order=recent&limit=1`, {
    headers: bearer,
  });
  if (!r.ok) die(`reading #${room} answered ${r.status}`);
  const page = await r.json();
  return page.events?.[0]?.id ?? "";
};

// WHO THE NODE WOULD RESOLVE, asked of the same door the composer offers from.
const roster = await fetch(`${base}/api/presence`, { headers: bearer }).then((r) =>
  r.ok ? r.json() : die(`/api/presence answered ${r.status}`),
);
const names = (roster.members ?? []).map((m) => m.name).filter(Boolean);
if (names.length === 0) die("/api/presence named nobody, so there is nothing to suggest");

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 }).catch(() => {});
  const composer = page.locator('textarea[aria-label="message"]');
  await composer.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await composer.count()) === 0) die("no composer on the room page");
  if (crashes.length > 0) die(`the room threw: ${crashes.join("; ")}`);

  const list = page.locator("[data-at-suggestions]");

  // A BARE @ OFFERS EVERYBODY. This is the case the operator asked about: they
  // typed @ and nothing happened.
  await composer.click();
  await composer.type("@");
  await list.waitFor({ state: "visible", timeout: 8_000 }).catch(() => {});
  if ((await list.count()) === 0) {
    die(`typing @ offered nothing. The node knows ${names.length} names (${names.join(", ")}),
so there was something to offer and the composer did not.`);
  }

  // NARROWING. A fragment shows the names that start with it and hides the rest.
  const first = names[0];
  await composer.type(first.slice(0, 2));
  await page.waitForTimeout(300);
  const offered = await page
    .locator("[data-at-name]")
    .evaluateAll((els) => els.map((e) => e.getAttribute("data-at-name")));
  if (!offered.includes(first)) {
    die(
      `typing "@${first.slice(0, 2)}" did not offer ${first}. Offered: ${offered.join(", ") || "(nothing)"}`,
    );
  }

  // THE ENTER ARM. Count the room's newest message before and after: Enter must
  // take the suggestion and must NOT send.
  const before = await said();
  await composer.press("Enter");
  await page.waitForTimeout(1200);
  const after = await said();
  if (after !== before) {
    die(`Enter SENT the message while the suggestion list was open. The room's newest
message went ${before || "(none)"} -> ${after}. This composer sends on Enter, so an
open list has to take that key or the feature ships the defect it prevents.`);
  }

  const draft = await composer.inputValue();
  if (!draft.includes(`@${first} `)) {
    die(`Enter did not complete the name: the draft is ${JSON.stringify(draft)}, wanted it to
carry "@${first} ".`);
  }

  // ESCAPE LEAVES A LITERAL @word ALONE, so the list can never trap somebody
  // writing about an email address or a name that is not a person here.
  await composer.fill("");
  await composer.type("@");
  await page.waitForTimeout(300);
  await composer.press("Escape");
  await page.waitForTimeout(200);
  if ((await list.count()) > 0) die("Escape did not close the suggestion list");

  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);
  console.log(`@ offers ${names.length} names, narrows, completes on Enter, and does not send`);
} finally {
  await browser.close();
}
