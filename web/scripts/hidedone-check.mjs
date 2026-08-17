/**
 * The room's todo panel HIDES THE FINISHED ONES, says how many it is hiding,
 * and remembers the answer. In a real browser, on the elements.
 *
 *   node scripts/hidedone-check.mjs BASE_URL TOKEN ROOM DONE_TITLE OPEN_TITLE
 *
 * DONE_TITLE is a todo whose status is done and OPEN_TITLE is one that is not,
 * both raised in ROOM before this runs. Four claims:
 *
 *   - with the box ticked, the finished todo is GONE FROM THE PANEL and the
 *     open one is still in it. Asserting that a checkbox exists tests nothing;
 *     the claim is about rows.
 *   - the NUMBER HIDDEN is on screen the whole time anything is hidden, and it
 *     is the real number. This is the honesty half: a panel showing four rows
 *     with no sign that sixteen are behind it lies about the size of the queue,
 *     and a filter that silently removes rows is how somebody concludes a todo
 *     does not exist.
 *   - the setting SURVIVES A RELOAD. A preference re-set on every visit is not
 *     a preference.
 *   - and it SURVIVES THE ROOM'S POLL. The panel is refilled from the node
 *     every time the long poll comes back, which is what silently reverted the
 *     assignee an hour ago. The poll is provoked here rather than waited for -
 *     a todo is raised from outside the browser and has to REACH THE PANEL
 *     before anything is re-read, so "the poll came back" is asserted.
 *
 * Everything is read inside the PANEL, never off the page. Raising a todo puts
 * its title in the room as a message too, so a page-text search for the
 * finished one finds it in the transcript and reports it visible with the panel
 * filtering perfectly.
 */

import { chromium } from "playwright";

const [base, token, room, doneTitle, openTitle] = process.argv.slice(2);
if (!base || !token || !room || !doneTitle || !openTitle) {
  console.error("usage: node scripts/hidedone-check.mjs BASE_URL TOKEN ROOM DONE_TITLE OPEN_TITLE");
  process.exit(2);
}

const bearer = { Authorization: `Bearer ${token}` };

/** die prints what a person would have seen and fails the check. */
function die(message, shown) {
  console.error(shown ? `${message}\nThe panel shows:\n${shown}` : message);
  process.exit(1);
}

// What the NODE holds for this room, first. An absence assertion against a
// store that never had the row is the same shape as counting zero requests from
// a page that never loaded: it passes against a panel that hides nothing, and
// against one that has no todos at all.
const listed = await fetch(
  `${base}/api/artifacts?type=memory&kind=todo&room=${encodeURIComponent(room)}`,
  { headers: bearer },
);
if (!listed.ok) {
  die(`could not read #${room}'s todos back from the node: ${listed.status}`);
}
const { artifacts = [] } = await listed.json();
const isDone = (a) => (a.status || "").trim().toLowerCase() === "done";
const finished = artifacts.filter(isDone);
if (!finished.some((a) => a.title === doneTitle)) {
  die(`the node has no done todo titled ${JSON.stringify(doneTitle)} in #${room}, so hiding the
finished ones could not be told from hiding nothing. #${room} holds: ${artifacts
    .map((a) => `${JSON.stringify(a.title)} (${a.status || "todo"})`)
    .join(", ")}`);
}
if (!artifacts.some((a) => a.title === openTitle && !isDone(a))) {
  die(`the node has no unfinished todo titled ${JSON.stringify(openTitle)} in #${room}, so this
check could not tell a filter from an empty panel`);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  // The token and NOTHING ELSE: the preference is the app's to store, so a
  // browser that has never seen this console is what the default is read from.
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

  const box = panel.locator("[data-hide-done]");
  const hiddenCount = panel.locator("[data-hidden-count]");
  /** The row of one todo, by its title - a row of the PANEL, not a line of the room. */
  const row = (title) => panel.locator("li").filter({ hasText: title });

  try {
    await box.waitFor({ state: "visible", timeout: 10_000 });
  } catch {
    const shown = await panel.innerText().catch(() => "");
    die(
      `the panel has no control carrying data-hide-done, so there is no way to put the
finished work away`,
      shown,
    );
  }

  /**
   * Waits for the list to have arrived before reading what is not in it.
   *
   * The panel is on screen from mount with an empty list and its todos land one
   * fetch later, so "the finished one is absent" is true of a panel that has
   * not loaded yet - it would pass against every build there has ever been.
   * The open todo is the proof the read happened.
   */
  async function listLoaded(where) {
    try {
      await row(openTitle).first().waitFor({ state: "visible", timeout: 15_000 });
    } catch {
      const shown = await panel.innerText().catch(() => "");
      const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
      die(
        `${where}: the panel never showed ${JSON.stringify(openTitle)}, which is an UNFINISHED
todo of #${room}. Either the list did not load or the filter is eating live work.${errors}`,
        shown,
      );
    }
  }

  /** The finished one is not among the panel's rows, and the count says so. */
  async function assertHiding(where) {
    await listLoaded(where);
    if ((await row(doneTitle).count()) > 0) {
      const shown = await panel.innerText().catch(() => "");
      die(
        `${where}: ${JSON.stringify(doneTitle)} is a finished todo and it is still a row of the
panel with the box ticked`,
        shown,
      );
    }
    if ((await hiddenCount.count()) === 0) {
      const shown = await panel.innerText().catch(() => "");
      die(
        `${where}: ${finished.length} finished todo(s) are hidden and the panel does not say so -
nothing carries data-hidden-count. A panel that drops rows without a number on
screen lies about the size of the queue`,
        shown,
      );
    }
    // The real number, not merely a number. "1 done hidden" over sixteen hidden
    // rows is the same lie in smaller type.
    const said = (await hiddenCount.innerText()).trim();
    const digits = said.match(/\d+/);
    if (!digits || Number(digits[0]) !== finished.length) {
      const shown = await panel.innerText().catch(() => "");
      die(
        `${where}: ${finished.length} finished todo(s) are hidden and the panel says
${JSON.stringify(said)}`,
        shown,
      );
    }
  }

  /** The finished one is back, and nothing claims to be hiding anything. */
  async function assertShowing(where) {
    await listLoaded(where);
    try {
      await row(doneTitle).first().waitFor({ state: "visible", timeout: 10_000 });
    } catch {
      const shown = await panel.innerText().catch(() => "");
      die(
        `${where}: the box is unticked and ${JSON.stringify(doneTitle)} is still not a row of
the panel, so unticking it does not bring the finished work back`,
        shown,
      );
    }
    if ((await hiddenCount.count()) > 0) {
      const shown = await panel.innerText().catch(() => "");
      die(
        `${where}: nothing is hidden and the panel still says
${JSON.stringify((await hiddenCount.innerText()).trim())}`,
        shown,
      );
    }
  }

  // A browser that has never seen this console. Whatever the default is, it has
  // to be the one the code documents, and the rows have to agree with the box.
  if (!(await box.isChecked())) {
    die(`the box is unticked on a browser with nothing stored, so the default is to show the
finished work. todos.ts documents the default as hidden - one of the two is wrong`);
  }
  await assertHiding("on first load, with nothing stored");

  // Unticked: the work comes back. A filter you cannot turn off is a deletion.
  await box.uncheck();
  await assertShowing("with the box unticked");

  // And it is remembered. Same tab, fresh page: nothing in this browser sets
  // the preference except the console itself.
  await page.reload({ timeout: 20_000 });
  await panel.waitFor({ state: "visible", timeout: 15_000 });
  if (await box.isChecked()) {
    die(`the box was unticked and came back ticked after a reload, so the setting is not
remembered and has to be set again on every visit`);
  }
  await assertShowing("after a reload with the box unticked");

  // The other direction, because a preference that only remembers one of its
  // two values is a default with extra steps.
  await box.check();
  await assertHiding("with the box ticked again");
  await page.reload({ timeout: 20_000 });
  await panel.waitFor({ state: "visible", timeout: 15_000 });
  if (!(await box.isChecked())) {
    die("the box was ticked and came back unticked after a reload");
  }
  await assertHiding("after a reload with the box ticked");

  // Now the room's poll, provoked from OUT HERE so it arrives the way anybody
  // else's work does. A todo rather than a message: it reaches this browser
  // only through the poll's RELOAD OF THE LIST, which is the thing that would
  // wipe a setting the panel was deriving from the node's answer.
  //
  // Raised more than once if it has to be - a poll that missed one window is
  // not the claim under test, and what would be weakening is accepting that
  // none landed. The loop fails rather than doing that.
  const provoked = "the hide-done check provoked a poll";
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
    landed = await row(title)
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
does to the setting.${errors}`,
      shown,
    );
  }

  if (!(await box.isChecked())) {
    die(`the room polled and the box unticked itself: the panel is taking the setting from the
node's answer rather than holding its own`);
  }
  await assertHiding("after the room polled");

  console.log(
    `#${room}'s panel hid ${finished.length} finished todo(s) and said so, kept ${JSON.stringify(
      openTitle,
    )} on screen, remembered the box across two reloads, and a poll of the room left it ticked`,
  );
} finally {
  await browser.close();
}
