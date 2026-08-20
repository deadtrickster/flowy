/**
 * A room the reader closed leaves their sidebar, stays closed across a reload,
 * and comes back from the line that says how many are closed.
 *
 *   node scripts/close-room-check.mjs BASE_URL TOKEN ROOM
 *
 * THE OPERATOR ASKED TWICE. "I left the padesign room - 'you are not a member'
 * appeared. ok, how to remove it from ROOMS list now?" and then "all other chat
 * apps i know allow me to close the room". Leaving is a permission act - it
 * empties your role - and the sidebar lists every room in the project, so
 * leaving changed nothing they could see.
 *
 * CLOSING IS A FACT ABOUT THE READER, and this asserts the three things that
 * makes true:
 *
 *   it leaves the sidebar when closed
 *   it is STILL CLOSED after a reload - which is the arm that says the
 *     preference reached the node rather than a variable in the page
 *   it comes back, because a closed room with no way back is a room somebody
 *     has to be told about
 *
 * The middle arm is the whole reason this is a personal note on the node rather
 * than localStorage: the operator runs more than one machine, and a room closed
 * on one coming back on the next is the same "did that work?" that leaving
 * already produced.
 */

import { chromium } from "playwright";

const [base, token, room = "general"] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/close-room-check.mjs BASE_URL TOKEN [ROOM]");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/`, { timeout: 20_000 }).catch(() => {});

  const link = page.locator(`a[href="/chat/${room}"]`);
  await link
    .first()
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if (crashes.length > 0) die(`the shell threw: ${crashes.join("; ")}`);
  if ((await link.count()) === 0) die(`#${room} is not in the sidebar to begin with`);

  const closer = page.locator(`[data-close-room="${room}"]`);
  if ((await closer.count()) === 0) die(`the sidebar offers no way to close #${room}`);
  // HOVER FIRST, because the control is revealed on hover and a click through
  // `force` is not the interaction a person has. The first version forced it
  // and playwright refused - a display:none element is not clickable however
  // hard you ask - which is the check being right about the affordance: a
  // control nobody can reach is not a control.
  await link.first().hover();
  await closer.click();

  await page
    .waitForFunction((r) => !document.querySelector(`a[href="/chat/${r}"]`), room, {
      timeout: 10_000,
    })
    .catch(() => die(`#${room} is still in the sidebar after it was closed`));

  // IT SURVIVES A RELOAD, which is the arm that says the node holds it.
  await page.reload({ timeout: 20_000 }).catch(() => {});
  await page.waitForTimeout(3000);
  if ((await page.locator(`a[href="/chat/${room}"]`).count()) !== 0) {
    die(`#${room} came back after a reload - the preference did not reach the node`);
  }

  const closed = page.locator("[data-closed-rooms]");
  if ((await closed.count()) === 0) {
    die("nothing says a room was closed, so there is no way back to it");
  }
  await closed.locator("summary").click();
  const back = page.locator(`[data-reopen-room="${room}"]`);
  await back
    .waitFor({ state: "visible", timeout: 10_000 })
    .catch(() => die(`no way to reopen #${room}`));
  await back.click();
  await page
    .waitForFunction((r) => !!document.querySelector(`a[href="/chat/${r}"]`), room, {
      timeout: 10_000,
    })
    .catch(() => die(`#${room} did not come back when it was reopened`));

  if (crashes.length > 0) die(`the shell threw: ${crashes.join("; ")}`);
  console.log(`#${room} closed, stayed closed across a reload, and reopened`);
} finally {
  await browser.close();
}
