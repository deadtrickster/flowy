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
  // EVERY REFUSAL THE PAGE MET, so a failure says WHY rather than only that the
  // room is still there. The first gate run of this check reported "#general is
  // still in the sidebar after it was closed" and nothing else - true, and it
  // named the symptom of a write nobody could see. A preference that fails to
  // save looks exactly like a click that did nothing.
  const refused = [];
  page.on("response", (r) => {
    if (r.url().includes("/api/") && r.status() >= 400) {
      refused.push(`${r.request().method()} ${r.status()} ${r.url()}`);
    }
  });
  const why = () => (refused.length > 0 ? `\n  the node refused: ${refused.join("; ")}` : "");
  // AND WHAT THE PAGE LOOKED LIKE, because a failure with no refusal behind it
  // is the harder one: the first two gate runs both said "still in the sidebar"
  // with nothing else, and neither said whether the click had landed, whether
  // the closed list existed, or whether the console was even built. This runs
  // in the browser at the moment of failure and costs nothing until then.
  const seen = async () => {
    try {
      const state = await page.evaluate(() => ({
        rooms: document.querySelectorAll('a[href^="/chat/"]').length,
        closers: document.querySelectorAll("[data-close-room]").length,
        closedBlock: document.querySelectorAll("[data-closed-rooms]").length,
        head: document.body.innerText.slice(0, 120).replace(/\s+/g, " "),
      }));
      return `\n  the page had ${state.rooms} rooms, ${state.closers} close controls, ${state.closedBlock} closed-lists; body starts ${JSON.stringify(state.head)}`;
    } catch {
      return "\n  the page could not be read at all";
    }
  };
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/`, { timeout: 20_000 }).catch(() => {});

  const link = page.locator(`a[href="/chat/${room}"]`);
  await link
    .first()
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if (crashes.length > 0) die(`the shell threw: ${crashes.join("; ")}`);
  // START FROM A KNOWN STATE. This preference is per principal and lives on the
  // node, so a room left closed by an earlier run - or by whoever holds this
  // token - is still closed when the check arrives, and the check then fails at
  // the first arm saying the sidebar "offers no way to close" it. True and
  // useless: the room is not there to close. Measured by running this twice
  // against one node.
  if ((await link.count()) === 0) {
    const closed = page.locator("[data-closed-rooms]");
    if ((await closed.count()) === 0) {
      die(`#${room} is not in the sidebar and nothing says it is closed${why()}${await seen()}`);
    }
    await closed.locator("summary").click();
    const back = page.locator(`[data-reopen-room="${room}"]`);
    if ((await back.count()) === 0) {
      die(`#${room} is neither in the sidebar nor in the closed list`);
    }
    await back.click();
    await page
      .waitForFunction((r) => !!document.querySelector(`a[href="/chat/${r}"]`), room, {
        timeout: 10_000,
      })
      .catch(() => die(`#${room} was already closed and would not reopen${why()}`));
  }

  const closer = page.locator(`[data-close-room="${room}"]`);
  // WAITED FOR, not counted once. When the room had to be reopened above, the
  // link appears the instant React commits and this counted before the row it
  // belongs to was in the document - so the first run against an already-closed
  // room reported "the sidebar offers no way to close it" about a control that
  // arrived a moment later. A count is a sample; a wait is a state.
  await closer
    .first()
    .waitFor({ state: "attached", timeout: 10_000 })
    .catch(() => {});
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
    .catch(async () =>
      die(`#${room} is still in the sidebar after it was closed${why()}${await seen()}`),
    );

  // IT SURVIVES A RELOAD, which is the arm that says the node holds it.
  await page.reload({ timeout: 20_000 }).catch(() => {});
  await page.waitForTimeout(3000);
  if ((await page.locator(`a[href="/chat/${room}"]`).count()) !== 0) {
    die(
      `#${room} came back after a reload - the preference did not reach the node${why()}${await seen()}`,
    );
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
