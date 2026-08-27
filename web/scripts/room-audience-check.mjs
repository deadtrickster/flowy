/**
 * A room says its project, the @ list offers only names that can hear it, and a
 * name typed in full that cannot is said at compose time.
 *
 *   node scripts/room-audience-check.mjs BASE_URL TOKEN_A TOKEN_A_AGENT TOKEN_B USER_A USER_B PROJECT_A PROJECT_B
 *
 * The operator, 2026-08-25 (row 01M0X22ECZ4): "we should be clear who is in the
 * rooms". Two projects both have a room called general, so agents addressed
 * people in the other project's room - and a mention RESOLVES at write time, so
 * the message looked addressed while it reached nobody in the room.
 *
 * WHAT THE PAGE MUST DO, in the row's own order: the room says which project it
 * belongs to (arm 1), the @ list offers only names that can hear this room
 * (arm 2), a name typed in full that cannot hear is said at compose time and
 * the send is NOT refused (arm 3 - the row judged refusing the send wrong), and
 * the roster draws the elsewhere speaker under their own heading with the
 * project named (arm 4).
 *
 * THE FIXTURE EARNS ITS NEGATIVES. Alice (pa) and bob (pb) each speak in a room
 * called audroom - one room name, two rooms, the failure the row describes.
 * Bob then grants pa read of pb, so alice's AGENT - the browser's identity -
 * can SEE bob in the roster: without the grant, "bob is not offered" would be
 * true by blindness, which is not a test.
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, tokenA, tokenAAgent, tokenB, userA, userB, projectA, projectB] = process.argv.slice(2);
if (!base || !tokenA || !tokenAAgent || !tokenB || !userA || !userB || !projectA || !projectB) {
  console.error(
    "usage: node scripts/room-audience-check.mjs BASE_URL TOKEN_A TOKEN_A_AGENT TOKEN_B USER_A USER_B PROJECT_A PROJECT_B",
  );
  process.exit(2);
}
refuseRemote(base, "room-audience-check");

const ROOM = "audroom";
const die = (message) => {
  console.error(message);
  process.exit(1);
};

const say = async (token, body) => {
  const r = await fetch(`${base}/api/chat/${encodeURIComponent(ROOM)}/say`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
    body: JSON.stringify({ body }),
  });
  if (!r.ok) die(`seeding "${body}" was refused: HTTP ${r.status} ${await r.text()}`);
};

await say(tokenA, "alice is in pa audroom");
await say(tokenB, "bob is in pb audroom");

// THE GRANT. Bob is a pb principal, so he opens pb up to pa - the same shape
// the suite's own grant checks use. This is what makes the browser able to see
// the name it must not offer.
const grant = await fetch(`${base}/api/grants`, {
  method: "POST",
  headers: { "Content-Type": "application/json", Authorization: `Bearer ${tokenB}` },
  body: JSON.stringify({ from_project: projectA, to_project: projectB }),
});
if (!grant.ok) {
  die(`the fixture grant was refused: HTTP ${grant.status} ${await grant.text()}
Nothing about who can hear the room was tested - the negative arms need a
cross-project reader and only the grant makes one.`);
}

// WHO THE ROSTERS SAY, asked of the door the page offers from. The member names
// are the node's own - a check that hardcoded a handle would assert about a
// name the page never drew.
const presence = await fetch(`${base}/api/presence`, {
  headers: { Authorization: `Bearer ${tokenAAgent}` },
}).then((r) => (r.ok ? r.json() : die(`/api/presence answered ${r.status}`)));
const here = (presence.members ?? []).find((m) => m.actor === userA);
const away = (presence.members ?? []).find((m) => m.actor === userB);
if (!here?.name) die("the pa speaker is not in the roster - the fixture said nothing");
if (!away?.name) die("the pb speaker is not in the roster - the grant did not open pb to pa");
// WHERE THEY SPOKE is asserted by the page arms, not here: on an old node the
// door answers no projects at all, and that is itself the defect the arms name.
// Dying here would make the red run die about the fixture instead of the page.

const newest = async () => {
  const r = await fetch(`${base}/api/chat/${encodeURIComponent(ROOM)}?order=recent&limit=1`, {
    headers: { Authorization: `Bearer ${tokenAAgent}` },
  });
  if (!r.ok) die(`reading #${ROOM} answered ${r.status}`);
  const page = await r.json();
  return page.events?.[0];
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), tokenAAgent);
  await page.goto(`${base}/chat/${encodeURIComponent(ROOM)}`, { timeout: 30_000 }).catch(() => {});
  const composer = page.locator('textarea[aria-label="message"]');
  await composer.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await composer.count()) === 0) die("no composer on the room page");
  if (crashes.length > 0) die(`the room threw: ${crashes.join("; ")}`);

  // ARM 1: THE PAGE SAYS WHICH PROJECT THE ROOM BELONGS TO. Two projects both
  // have #audroom, and a page that says neither tells nobody which one it is.
  const badge = page.locator("[data-room-project]");
  if ((await badge.count()) === 0) {
    die(`the room page does not say which project the room belongs to - two projects
both have #${ROOM}, and a page that says neither tells nobody which one it is`);
  }
  const stated = (await badge.first().textContent()) ?? "";
  if (!stated.includes(projectA)) {
    die(`the room badge says ${JSON.stringify(stated)}, wanted it to name ${projectA}`);
  }

  // ARM 2: TYPING @ OFFERS ONLY NAMES THAT CAN HEAR THIS ROOM.
  await composer.click();
  await composer.type("@");
  const list = page.locator("[data-at-suggestions]");
  await list.waitFor({ state: "visible", timeout: 8_000 }).catch(() => {});
  if ((await list.count()) === 0) {
    die(`typing @ offered nothing - ${here.name} is in the roster and should be offered`);
  }
  const offered = await page
    .locator("[data-at-name]")
    .evaluateAll((els) => els.map((e) => e.getAttribute("data-at-name")));
  if (!offered.includes(here.name)) {
    die(`typing @ did not offer ${here.name}. Offered: ${offered.join(", ") || "(nothing)"}`);
  }
  if (offered.includes(away.name)) {
    die(`typing @ offered ${away.name}, who speaks in ${projectB} and cannot hear this
room. The list must offer only names that can hear it.`);
  }

  // ARM 3: A NAME TYPED IN FULL IS SAID AT COMPOSE TIME, AND THE SEND IS NOT
  // REFUSED. The row judged refusing the send wrong - "I mean @bob" is a thing
  // a person can know - so the box says so BEFORE the send, and Enter stays a
  // send. The second half is asserted by the message landing.
  await composer.fill("");
  await composer.type(`@${away.name}`);
  await page.waitForTimeout(600);
  const warn = page.locator(`[data-at-warn="${away.name}"]`);
  if ((await warn.count()) === 0) {
    die(`typing @${away.name} in full was not said at compose time - the name cannot
hear this room, and the box must say so BEFORE the send, not after it`);
  }
  const warnText = (await warn.first().textContent()) ?? "";
  if (!warnText.includes(projectB)) {
    die(`the warning does not say where ${away.name} is: ${JSON.stringify(warnText)}`);
  }
  const before = (await newest())?.id ?? "";
  await composer.press("Enter");
  await page.waitForTimeout(1500);
  const after = await newest();
  if (!after || after.id === before) {
    die(
      "Enter did not send - the warning is meant to say so at compose time, not to block the send",
    );
  }
  if (!(after.body ?? "").includes(`@${away.name}`)) {
    die(`the sent message does not carry @${away.name}: ${JSON.stringify(after.body)}`);
  }

  // ARM 4: THE ROSTER DRAWS THE ELSEWHERE SPEAKER UNDER THEIR OWN HEADING, with
  // the project named, and not as in the room.
  await page.locator('[data-room-pane="listening"]').click();
  await page.waitForTimeout(800);
  const elsewhereBadge = page.locator(`[data-elsewhere-name="${away.name}"]`);
  if ((await elsewhereBadge.count()) === 0) {
    die(`the roster does not name ${away.name} as elsewhere on this node - it draws
every readable speaker as in the room, which is the flat roster the row filed`);
  }
  const elseText = (await elsewhereBadge.first().textContent()) ?? "";
  if (!elseText.includes(projectB)) {
    die(`the elsewhere badge does not say which project: ${JSON.stringify(elseText)}`);
  }
  const inRoom = await page
    .locator("[data-member]")
    .evaluateAll((els) => els.map((e) => e.getAttribute("data-member")));
  if (inRoom.includes(userB)) {
    die(`${away.name} is drawn as in the room while their projects name only ${projectB}`);
  }

  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);
  console.log(
    `#${ROOM} says it belongs to ${projectA}, @ offers ${here.name} and not ${away.name}, @${away.name} is warned at compose time and still sends, and the roster puts ${away.name} in ${projectB}`,
  );
} finally {
  await browser.close();
}
