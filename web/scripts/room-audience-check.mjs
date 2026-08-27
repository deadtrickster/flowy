/**
 * A room says its project, the @ list offers only names that can hear it, and a
 * name typed in full that cannot is said at compose time.
 *
 *   node scripts/room-audience-check.mjs BASE_URL TOKEN_A TOKEN_B USER_A USER_B PROJECT_A PROJECT_B
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
 * THE FIXTURE EARNS ITS NEGATIVES WITHOUT WRITING A GRANT. Alice (pa) and bob
 * (pb) each speak in a room called audroom - one room name, two rooms, the
 * failure the row describes. The browser reads as BOB, and bob can SEE alice
 * because the suite's own grant checks have already opened pa to pb by the
 * time console checks run (run-tests.sh: "A opens pa up to pb"). A check that
 * earned its own grant would leave the node's grant table changed for every
 * later permission check - measured, the hard way: a pa->pb grant of the
 * check's own turned six later checks red, each one saying what a token could
 * now read. Standing on the suite's grant means the fixture is real and the
 * node is left exactly as it was found.
 *
 * AND IT DELETES THE CONSOLE'S READER ROWS. One shell load declares
 * console:<room> rows for the sidebar and console-dm for the direct rail
 * under the browser's principal (user-first), and inbox_readers keys on the
 * principal - a row left behind is a second row with the same first part for
 * the suite's own reader-row check, whose arithmetic then sees two marks where
 * it read one. The browser closes before the deletes (a refresh in flight
 * could re-declare), and the empty state is CHECKED afterwards, the way the
 * unread check taught: a delete that quietly did not happen leaves a reader
 * that is poisoned.
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, tokenA, tokenB, userA, userB, projectA, projectB] = process.argv.slice(2);
if (!base || !tokenA || !tokenB || !userA || !userB || !projectA || !projectB) {
  console.error(
    "usage: node scripts/room-audience-check.mjs BASE_URL TOKEN_A TOKEN_B USER_A USER_B PROJECT_A PROJECT_B",
  );
  process.exit(2);
}
refuseRemote(base, "room-audience-check");

const ROOM = "audroom";

const die = (message) => {
  throw new Error(message);
};

const call = async (token, path, init = {}) => {
  const r = await fetch(`${base}${path}`, {
    ...init,
    headers: { Authorization: `Bearer ${token}`, ...(init.headers ?? {}) },
  });
  return r;
};

const say = async (token, body) => {
  const r = await call(token, `/api/chat/${encodeURIComponent(ROOM)}/say`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ body }),
  });
  if (!r.ok) die(`seeding "${body}" was refused: HTTP ${r.status} ${await r.text()}`);
};

await say(tokenA, "alice is in pa audroom");
await say(tokenB, "bob is in pb audroom");

// WHO THE ROSTERS SAY, asked of the door the page offers from. The member names
// are the node's own - a check that hardcoded a handle would assert about a
// name the page never drew.
const presence = await call(tokenB, "/api/presence").then((r) =>
  r.ok ? r.json() : die(`/api/presence answered ${r.status}`),
);
const here = (presence.members ?? []).find((m) => m.actor === userB);
const away = (presence.members ?? []).find((m) => m.actor === userA);
if (!here?.name) die("the pb speaker is not in the roster - the fixture said nothing");
if (!away?.name) {
  die(`the pa speaker is not in the roster - the suite's pa->pb grant is not
standing, so the fixture is blind. Nothing about who can hear the room was tested.`);
}

const newest = async () => {
  const r = await call(tokenB, `/api/chat/${encodeURIComponent(ROOM)}?order=recent&limit=1`);
  if (!r.ok) die(`reading #${ROOM} answered ${r.status}`);
  const page = await r.json();
  return page.events?.[0];
};

const browser = await chromium.launch();
// Set only once the shell is up: the cleanup below must not delete reader rows
// when no console of this check ever declared any - on a failed launch the rows
// present belong to an earlier check.
let mounted = false;
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), tokenB);
  await page.goto(`${base}/chat/${encodeURIComponent(ROOM)}`, { timeout: 30_000 }).catch(() => {});
  const composer = page.locator('textarea[aria-label="message"]');
  await composer.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await composer.count()) === 0) die("no composer on the room page");
  mounted = true;
  if (crashes.length > 0) die(`the room threw: ${crashes.join("; ")}`);

  // ARM 1: THE PAGE SAYS WHICH PROJECT THE ROOM BELONGS TO. Two projects both
  // have #audroom, and a page that says neither tells nobody which one it is.
  const badge = page.locator("[data-room-project]");
  if ((await badge.count()) === 0) {
    die(`the room page does not say which project the room belongs to - two projects
both have #${ROOM}, and a page that says neither tells nobody which one it is`);
  }
  const stated = (await badge.first().textContent()) ?? "";
  if (!stated.includes(projectB)) {
    die(`the room badge says ${JSON.stringify(stated)}, wanted it to name ${projectB}`);
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
    die(`typing @ offered ${away.name}, who speaks in ${projectA} and cannot hear this
room. The list must offer only names that can hear it.`);
  }

  // ARM 3: A NAME TYPED IN FULL IS SAID AT COMPOSE TIME, AND THE SEND IS NOT
  // REFUSED. The row judged refusing the send wrong - "I mean @alice" is a thing
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
  if (!warnText.includes(projectA)) {
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
  if (!elseText.includes(projectA)) {
    die(`the elsewhere badge does not say which project: ${JSON.stringify(elseText)}`);
  }
  const inRoom = await page
    .locator("[data-member]")
    .evaluateAll((els) => els.map((e) => e.getAttribute("data-member")));
  if (inRoom.includes(userA)) {
    die(`${away.name} is drawn as in the room while their projects name only ${projectA}`);
  }

  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);
  console.log(
    `#${ROOM} says it belongs to ${projectB}, @ offers ${here.name} and not ${away.name}, @${away.name} is warned at compose time and still sends, and the roster puts ${away.name} in ${projectA}`,
  );
} catch (err) {
  console.error(err instanceof Error ? err.message : String(err));
  process.exitCode = 1;
} finally {
  // Leave the node's inbox_readers as the check found it. The browser closes
  // FIRST - the shell re-declares its reader rows on a tick, so deleting while
  // it lives could lose a row to a refresh in flight. Runs on arm failures
  // too: a red run must not leave a poisoned reader for the rest of the suite.
  await browser.close();
  if (mounted) {
    // Every row this browser's shell can have declared: console:<room> for the
    // sidebar and console-dm for the direct rail (a dash, deliberately - a
    // colon there would collide with a room some day). No other console check
    // drives a TOKEN_B browser, so under this token they are all this one's.
    const held = await call(tokenB, "/api/inbox/readers").then((r) =>
      r.ok ? r.json() : die(`/api/inbox/readers answered ${r.status}`),
    );
    const mine = (held.readers ?? []).filter(
      (row) => row.reader === "console-dm" || row.reader.startsWith("console:"),
    );
    for (const row of mine) {
      const r = await call(tokenB, `/api/inbox/reader/${encodeURIComponent(row.reader)}`, {
        method: "DELETE",
      });
      // 404 is the goal state reached another way - the row vanished between
      // the list and the delete. Anything else must be said.
      if (!r.ok && r.status !== 404) {
        console.error(`deleting the ${row.reader} reader row was refused: HTTP ${r.status}`);
        process.exitCode = 1;
      }
    }
    // AND THE RESULT IS CHECKED, the way the unread check taught: a delete
    // that quietly did not happen leaves a reader that is poisoned.
    const after = await call(tokenB, "/api/inbox/readers").then((r) =>
      r.ok ? r.json() : die(`/api/inbox/readers answered ${r.status}`),
    );
    const left = (after.readers ?? []).filter(
      (row) => row.reader === "console-dm" || row.reader.startsWith("console:"),
    );
    if (left.length > 0) {
      console.error(
        `the console's reader rows survived the cleanup: ${left.map((row) => row.reader).join(", ")}`,
      );
      process.exitCode = 1;
    }
  }
}
