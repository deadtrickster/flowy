/**
 * The room's todo panel SETS an assignee, OVERRIDES one, and the value survives
 * the room's next poll. In a real browser, on the elements.
 *
 *   node scripts/assignee-check.mjs BASE_URL TOKEN ROOM UNOWNED OWNED STALE FIRST SECOND
 *
 * UNOWNED is the title of a todo nobody is carrying, OWNED is one whose body
 * carries `OWNER: STALE` - the convention the queue was written with before
 * there was a field - and FIRST and SECOND are the two names this drives
 * through the panel.
 *
 * Three claims, and the third is the one that catches the bug this feature
 * would otherwise have shipped with:
 *
 *   - the panel can SET an assignee on a todo that has none;
 *   - it can OVERRIDE one that is already there, including one that came off
 *     the body's OWNER line, because a field that can only be filled in once is
 *     not an assignee, it is a note;
 *   - and the value SURVIVES THE ROOM'S NEXT POLL. The panel is refilled from
 *     the node by every long poll that comes back, so an assignee held in the
 *     tab's own state looks perfect until somebody touches the room and then
 *     silently reverts. The poll is provoked here rather than waited for - a
 *     todo is raised from outside the browser, and the check waits for it to
 *     reach the PANEL, which it can only do by that reload happening, before
 *     re-reading the cells. "The poll came back" is asserted, not assumed.
 *
 * The last assertion is against the NODE as well as the screen: a value that is
 * right in this tab and absent from the store is the same bug one reload later.
 */

import { chromium } from "playwright";

const [base, token, room, unowned, owned, stale, first, second] = process.argv.slice(2);
if (!base || !token || !room || !unowned || !owned || !stale || !first || !second) {
  console.error(
    "usage: node scripts/assignee-check.mjs BASE_URL TOKEN ROOM UNOWNED OWNED STALE FIRST SECOND",
  );
  process.exit(2);
}

const bearer = { Authorization: `Bearer ${token}` };

/** die prints what a person would have seen and fails the check. */
function die(message, shown) {
  console.error(shown ? `${message}\nThe panel shows:\n${shown}` : message);
  process.exit(1);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 }).catch(() => {});

  const panel = page
    .locator("aside section")
    .filter({ has: page.locator("h2", { hasText: /^todos$/ }) })
    .first();
  try {
    await panel.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(`the room has no todo panel: no aside section headed "todos".${errors}`);
  }

  /** The row of one todo, by its title. */
  const row = (title) => panel.locator("li").filter({ hasText: title }).first();
  /** The cell that says who is carrying it, which is also the control. */
  const cell = (title) => row(title).locator("[data-assignee]");

  /** reads what the cell says now, once the row has arrived. */
  async function says(title) {
    try {
      await cell(title).waitFor({ state: "visible", timeout: 15_000 });
    } catch {
      const shown = await panel.innerText().catch(() => "");
      die(
        `no assignee cell on the row for ${JSON.stringify(title)}: nothing in it carries
data-assignee, so the panel has no control for who is carrying the work`,
        shown,
      );
    }
    return (await cell(title).innerText()).trim();
  }

  /** Drives the control: click it, type a name, commit. */
  async function assign(title, name) {
    await cell(title).click();
    const box = row(title).getByRole("textbox");
    try {
      await box.waitFor({ state: "visible", timeout: 5_000 });
    } catch {
      const shown = await panel.innerText().catch(() => "");
      die(
        `clicking the assignee of ${JSON.stringify(title)} opened nothing to type a name into,
so the cell reports who is carrying it and offers no way to say`,
        shown,
      );
    }
    await box.fill(name);
    await box.press("Enter");
    try {
      await cell(title).filter({ hasText: name }).waitFor({ state: "visible", timeout: 10_000 });
    } catch {
      const shown = await panel.innerText().catch(() => "");
      die(
        `the panel did not take ${JSON.stringify(name)} as the assignee of ${JSON.stringify(title)};
the cell still says ${JSON.stringify(await says(title))}`,
        shown,
      );
    }
  }

  // Nobody is carrying it to begin with, so setting one is a change rather than
  // a coincidence: a cell that already said FIRST would pass every assertion
  // below with no control on the page at all.
  const before = await says(unowned);
  if (before === first || before === second) {
    die(`the row for ${JSON.stringify(unowned)} already says ${JSON.stringify(before)} before
anything was set, so this check could not tell a working control from none`);
  }

  // SET, then OVERRIDE. The override is the half a version that writes once and
  // refuses to write again gets wrong.
  await assign(unowned, first);
  await assign(unowned, second);

  // And the override of an owner that came off the body's OWNER line, which is
  // how the whole queue was written before the field existed. The cell has to
  // be showing that name first - overriding a name the panel never displayed
  // would say nothing about the compatibility this depends on.
  const wasOwned = await says(owned);
  if (wasOwned !== stale) {
    die(`the row for ${JSON.stringify(owned)} says ${JSON.stringify(wasOwned)} rather than
${JSON.stringify(stale)}, which is the OWNER line its body carries`);
  }
  await assign(owned, first);

  // Now the room's poll, provoked from OUT HERE so it arrives the way anybody
  // else's work does: the console is blocked in a long poll, the node answers
  // it, and the panel is REFILLED from the node's list.
  //
  // What is raised is a todo rather than a message, because a todo is proof
  // about the panel. It reaches this browser only through the poll's reload of
  // the list - nothing in the tab asked for it - and that reload is precisely
  // the thing that would wipe an assignee the panel was holding itself. A
  // message appearing would only say the transcript moved.
  //
  // Raised more than once if it has to be. A poll that missed one window is not
  // the claim under test, and the claim needs A poll to have landed rather than
  // this particular one; what would be weakening is accepting that none did,
  // and the loop below fails rather than doing that.
  const provoked = "the assignee check provoked a poll";
  let landed = false;
  for (let attempt = 1; attempt <= 3 && !landed; attempt++) {
    const title = `${provoked} ${attempt}`;
    const raised = await fetch(`${base}/api/chat/${encodeURIComponent(room)}/todo`, {
      method: "POST",
      headers: { ...bearer, "Content-Type": "application/json" },
      body: JSON.stringify({ title }),
    });
    if (!raised.ok) {
      die(`could not raise a todo in #${room} to provoke the poll: ${raised.status}`);
    }
    landed = await panel
      .getByText(title, { exact: false })
      .first()
      .waitFor({ state: "visible", timeout: 20_000 })
      .then(
        () => true,
        () => false,
      );
  }
  if (!landed) {
    const shown = await panel.innerText().catch(() => "");
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(
      `three todos were raised in #${room} from outside the browser and none of them reached
the panel, so the room is not polling and nothing was learned about what a poll
does to the assignee.${errors}`,
      shown,
    );
  }

  const stillSecond = await says(unowned);
  const stillFirst = await says(owned);
  if (stillSecond !== second || stillFirst !== first) {
    die(`the room polled and the assignees moved: ${JSON.stringify(unowned)} says
${JSON.stringify(stillSecond)} (set to ${JSON.stringify(second)}) and ${JSON.stringify(owned)} says
${JSON.stringify(stillFirst)} (set to ${JSON.stringify(first)}). The panel is holding the assignee
in the tab rather than on the node.`);
  }

  // The same claim against the node, because a value that is right on this
  // screen and absent from the store is the same bug one reload later.
  const listed = await fetch(
    `${base}/api/artifacts?type=memory&kind=todo&room=${encodeURIComponent(room)}`,
    { headers: bearer },
  );
  if (!listed.ok) {
    die(`could not read #${room}'s todos back from the node: ${listed.status}`);
  }
  const { artifacts = [] } = await listed.json();
  for (const [title, want] of [
    [unowned, second],
    [owned, first],
  ]) {
    const item = artifacts.find((a) => a.title === title);
    const held = item?.fields?.assignee;
    if (held !== want) {
      die(`the node has ${JSON.stringify(held)} as the assignee of ${JSON.stringify(title)}, and
the panel says ${JSON.stringify(want)}: the panel wrote nowhere`);
    }
  }

  const q = (s) => JSON.stringify(s);
  console.log(
    `#${room}'s panel set ${q(first)}, overrode it with ${q(second)}, overrode the OWNER line ${q(
      stale,
    )} with ${q(first)}, and a poll of the room left both alone`,
  );
} finally {
  await browser.close();
}
