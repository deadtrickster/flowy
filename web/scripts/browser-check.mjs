/**
 * What a person actually sees, in a real browser, against the live node.
 *
 * render-check.mjs already paints the shipped bundle in jsdom, which catches a
 * console that throws on mount. This one is a layer past that: a real engine,
 * real layout, real event loop, and assertions on ELEMENTS rather than on the
 * page's text.
 *
 * The element part is the lesson, not a detail. Checking the room's todo panel
 * by searching the page for "todos" passes with the panel entirely absent,
 * because the word is also in the global navigation - which is exactly what
 * happened the first time this was checked by hand. A string that appears in
 * two places is not evidence about either. So: find the panel, then read it.
 *
 *   node scripts/browser-check.mjs BASE_URL TOKEN EXPECTED_TEXT
 *
 * EXPECTED_TEXT has to appear INSIDE the room's todo panel, which is the aside
 * section headed "todos" - not anywhere on the page.
 *
 * It also drives the side column's TABS, because that column is where the todo
 * panel lives and there are four of them now - todos, merges, thread, worklog -
 * where there were two tabs and two stacked panes. Every one of them is clicked,
 * the body that comes up is read off the element rather than assumed from the
 * button, and every title is read for the coloured count that is the reason the
 * bar exists. See the tab section below.
 */

import { chromium } from "playwright";

const [base, token, expected, absent] = process.argv.slice(2);

if (!base || !token || !expected) {
  console.error("usage: node scripts/browser-check.mjs BASE_URL TOKEN EXPECTED_TEXT [ABSENT_TEXT]");
  process.exit(2);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/general`, { timeout: 20_000 }).catch(() => {});

  const panel = page
    .locator("aside section")
    .filter({ has: page.locator("h2", { hasText: /^todos$/ }) })
    .first();

  try {
    await panel.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    console.error(
      `the room has no todo panel: no aside section headed "todos".
The word appears in the global nav too, so this looks for the ELEMENT.${errors}`,
    );
    process.exit(1);
  }

  // Wait for the ROW, not for the panel. The panel is visible from mount with
  // an empty list, and its todos arrive one fetch later - so reading its text
  // the moment it appears asserts on the empty state and fails a feature that
  // works. That is what this check did first: the API had all thirteen and the
  // assertion saw none of them, because it asked too early rather than because
  // anything was wrong.
  try {
    await panel.getByText(expected, { exact: false }).first().waitFor({
      state: "visible",
      timeout: 15_000,
    });
  } catch {
    const shown = await panel.innerText();
    console.error(`the room's todo panel does not show ${JSON.stringify(expected)}. It shows:
${shown}`);
    process.exit(1);
  }

  // What must NOT be on it. A panel is as much what it does not say: two words
  // for one state read as two states, and a reader goes looking for a
  // distinction that is not there.
  if (absent) {
    // Scoped to the OWNER CELLS, not the whole panel. The first version searched
    // all of the text and reported a failure against a todo whose TITLE quoted
    // the word - the user's own "todo list has unowned and unassigned - looks
    // identical". The app was right and the check was wrong: a word banned from
    // a column is not a word banned from the page, and content is allowed to
    // discuss the thing the column must not say.
    //
    // By the attribute rather than by position in the row: the cell became the
    // control that sets the assignee, and a check pinned to "the second child"
    // silently moves to whatever is second next time the row is rearranged.
    const owners = await panel
      .locator("li [data-assignee]")
      .evaluateAll((nodes) => nodes.map((n) => (n.textContent || "").trim()));
    // A selector that matches nothing would report "never says it" about a
    // column it never found - the same shape as counting zero requests from a
    // page that never loaded. So the cells have to exist before their contents
    // mean anything.
    if (owners.length === 0) {
      console.error(
        "no owner cells were found, so nothing was checked - the panel's row layout has moved",
      );
      process.exit(1);
    }
    const offenders = owners.filter((o) => o === absent);
    if (offenders.length > 0) {
      console.error(
        `${offenders.length} todo(s) still name the owner ${JSON.stringify(absent)}; owners are: ${[...new Set(owners)].join(", ")}`,
      );
      process.exit(1);
    }
  }

  // ------------------------------------------------- the side column's tabs
  //
  // FOUR PANES, ONE BAR, AND A NUMBER ON EVERY TITLE. The column used to be two
  // tabs with the thread stacked permanently underneath them and the worklog a
  // whole page away; it is four tabs now, and the counts in the titles are the
  // reason the operator asked for tabs in the first place - "both tab titles
  // have basic colored stats", so a person can see whether a pane wants them
  // WITHOUT opening it.
  //
  // So this drives all four and asserts both halves. A bar whose buttons all
  // opened the same pane looks right in a screenshot, and a tab whose title
  // lost its count looks right in a click-through - which is why the body says
  // which pane it is on the element, and why every title is read for a number
  // in a colour of its own.
  // FIVE NOW, and the fifth is the rule this strip exists to enforce. The
  // roster was a header ABOVE the strip - permanently drawn, taking height from
  // the queue and taking more of it as the fleet grew, which is the operator's
  // "listening panel grew so much i cant see todos anymore". A list of rows is
  // a tab; there is no second place to put one.
  const PANES = ["todos", "merges", "thread", "worklog", "listening"];

  const named = await page
    .locator("aside [data-room-pane]")
    .evaluateAll((nodes) => nodes.map((n) => n.getAttribute("data-room-pane")));
  if (named.join(",") !== PANES.join(",")) {
    console.error(
      `the room's side column has tabs [${named.join(", ")}], and this check is about
[${PANES.join(", ")}]`,
    );
    process.exit(1);
  }

  // AND NOTHING IS DRAWN ABOVE THE STRIP. This is the rule rather than one more
  // pane: every feature that shipped into this column appended a panel above or
  // below the tabs, because appending is the smaller diff, and four features
  // later the queue the column exists for was off the screen and the operator
  // had asked for the same thing in five different words. A list of rows is a
  // tab; there is no second place to put one. Asserted structurally, so the
  // sixth feature cannot quietly reopen the stack - the strip is the column's
  // first child, and anything before it is the stack coming back.
  const first = await page.evaluate(() => {
    const column = document.querySelector("aside [role=tablist]")?.parentElement;
    const head = column?.firstElementChild;
    if (!head) return "the side column has no tab strip at all";
    return head.getAttribute("role") === "tablist"
      ? ""
      : `the side column draws <${head.tagName.toLowerCase()}> above its tab strip: ` +
          `${(head.textContent || "").trim().slice(0, 80)}`;
  });
  if (first) {
    console.error(first);
    process.exit(1);
  }

  // The worklog's count is checked against the NODE, because it is the one
  // number on the bar that is not derived from something already on screen -
  // and because the log is not per-room: an entry is written into a room of its
  // own, so this tab shows the whole log, and a build that "narrowed" it to
  // #general would draw 0 entries here and be indistinguishable from an empty
  // log. The node is asked the same way the console asks.
  const answered = await fetch(`${base}/api/activity?kind=worklog&order=recent`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!answered.ok) {
    console.error(`the node will not answer for the worklog: ${answered.status}`);
    process.exit(1);
  }
  const entries = ((await answered.json()).items ?? []).length;

  // The bar is drawn from the moment the room mounts and the worklog arrives a
  // fetch later, so the number in the title settles after the title does.
  // Waited for rather than read once: reading it the moment the bar appears
  // asserts against "0 entries" and fails a tab that works, which is the
  // mistake the todo half of this check already made once.
  await page
    .waitForFunction(
      (want) =>
        document.querySelector('[data-room-pane="worklog"] [data-room-count]')?.textContent ===
        `${want} entr${want === 1 ? "y" : "ies"}`,
      entries,
      { timeout: 15_000 },
    )
    .catch(() => {});

  for (const name of PANES) {
    const tab = page.locator(`aside [data-room-pane="${name}"]`);

    // The count first, while the tab is whatever it is: a title only carries a
    // number for the reader if it carries it CLOSED, which is the whole point
    // of putting it there.
    const counts = await tab.locator("[data-room-count]").evaluateAll((nodes) =>
      nodes.map((n) => ({
        text: (n.textContent || "").trim(),
        colour: getComputedStyle(n).color,
      })),
    );
    if (counts.length === 0) {
      console.error(`the ${name} tab carries no count, so it says nothing about its pane:
${await tab.innerText()}`);
      process.exit(1);
    }
    const wordy = counts.filter((c) => !/^\d+\s+\S/.test(c.text));
    if (wordy.length > 0) {
      console.error(
        `the ${name} tab has a stat that is not a number and a word: ${wordy
          .map((c) => JSON.stringify(c.text))
          .join(", ")}`,
      );
      process.exit(1);
    }
    // Coloured, and measured rather than assumed - a stylesheet that defines a
    // palette and a title that never uses it look identical from every angle
    // except a rendered page. The tab's own text colour is the baseline, so
    // this stays true on either theme.
    const plain = await tab.evaluate((n) => getComputedStyle(n).color);
    if (counts.every((c) => c.colour === plain)) {
      console.error(
        `every stat in the ${name} tab is drawn in the tab's own colour (${plain}), so the
numbers do not stand out from the title: ${counts.map((c) => c.text).join(", ")}`,
      );
      process.exit(1);
    }

    if (name === "worklog") {
      const said = counts.map((c) => c.text).join(" ");
      const shown = Number.parseInt(said, 10);
      if (shown !== entries) {
        console.error(
          `the worklog tab says ${JSON.stringify(said)} and the node holds ${entries} entr${
            entries === 1 ? "y" : "ies"
          } - the tab is counting something else, or it narrowed a log that is not per-room`,
        );
        process.exit(1);
      }
    }

    // And now open it. The body carries its own name, so this is an assertion
    // about which pane rendered rather than about which button was clicked.
    await tab.click();
    // Waited for, not read: a click is a React state change, and reading the
    // column in the same turn asks it what it looked like before.
    await page
      .locator(`aside [data-room-pane-body="${name}"]`)
      .waitFor({ state: "visible", timeout: 15_000 })
      .catch(() => {});
    const bodies = await page
      .locator("aside [data-room-pane-body]")
      .evaluateAll((nodes) => nodes.map((n) => n.getAttribute("data-room-pane-body")));
    if (bodies.join(",") !== name) {
      console.error(
        `clicking the ${name} tab left [${bodies.join(", ")}] on screen, and there must be
exactly one body and it must be the ${name} one`,
      );
      process.exit(1);
    }
    if ((await tab.getAttribute("aria-selected")) !== "true") {
      console.error(`the ${name} tab draws its body without marking itself selected`);
      process.exit(1);
    }

    // AND THE PANE IS A PLACE. Choosing one has to be somewhere a person can
    // come back from and somewhere they can send somebody else - held as
    // component state it was neither, and "look at the listening tab in
    // #general" was a sentence rather than a link.
    const where = new URL(page.url()).pathname;
    if (!where.endsWith(`/${name}`)) {
      console.error(
        `clicking the ${name} tab left the address at ${where}, so the pane is not in the URL
and neither the back button nor a link can reach it`,
      );
      process.exit(1);
    }
  }

  // BACK GOES BACK ONE PANE, not out of the room. Each tab above pushed an
  // entry, so the previous one is the pane chosen before it - which is the
  // operator's actual complaint about this console: the button leaves.
  await page.goBack();
  await page
    .locator(`aside [data-room-pane-body="${PANES[PANES.length - 2]}"]`)
    .waitFor({ state: "visible", timeout: 15_000 })
    .catch(() => {});
  const back = new URL(page.url()).pathname;
  if (!back.endsWith(`/${PANES[PANES.length - 2]}`)) {
    console.error(
      `going back from the ${PANES[PANES.length - 1]} pane landed at ${back}, want the
${PANES[PANES.length - 2]} pane - the back button leaves the room instead of undoing the tab`,
    );
    process.exit(1);
  }

  // AND A LINK OPENS IT COLD. Loaded fresh rather than navigated to, because
  // the two fail differently: a route the app knows and the node does not is a
  // 404 for everybody the link is sent to, and clicking through this console
  // would never show it.
  const deep = `${base}/chat/general/${PANES[PANES.length - 1]}`;
  const landed = await page.goto(deep, { timeout: 20_000 }).catch(() => null);
  if (!landed || !landed.ok()) {
    console.error(`${deep} answers ${landed ? landed.status() : "nothing"} - a pane somebody
links to has to be a page the node serves`);
    process.exit(1);
  }
  const opened = page.locator(`aside [data-room-pane-body="${PANES[PANES.length - 1]}"]`);
  try {
    await opened.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const drawn = await page
      .locator("aside [data-room-pane-body]")
      .evaluateAll((nodes) => nodes.map((n) => n.getAttribute("data-room-pane-body")));
    console.error(
      `${deep} loaded and drew [${drawn.join(", ")}] - a link to a pane opens a different one`,
    );
    process.exit(1);
  }

  // ------------------------------------------ the thread, inside its own tab
  //
  // The thread moved into a tab from a section that was always on screen, and
  // the two things that could have been dropped on the way are the ones the
  // reader uses: the count in the tab has to be the thread that is drawn, and
  // the graph/list toggle - the button and the `d` it promises - has to still
  // work in here.
  const thread = page.locator('aside [data-room-pane-body="thread"]');
  await page.locator('aside [data-room-pane="thread"]').click();
  await thread.waitFor({ state: "visible", timeout: 15_000 });

  const threadTab = await page.locator('aside [data-room-pane="thread"]').innerText();
  const events = Number.parseInt(threadTab.replace(/^\D+/, ""), 10);
  const rows = await thread.locator("ul li").count();
  if (!Number.isFinite(events) || events !== rows) {
    console.error(`the thread tab says ${JSON.stringify(threadTab.replace(/\n/g, " "))} and its
pane draws ${rows} message(s) - the count in the title is not the thread on screen`);
    process.exit(1);
  }

  if (rows === 0) {
    // Said out loud rather than passed quietly: an empty thread cannot show
    // that the two views are told apart.
    console.log("the thread pane is empty in this room, so the graph toggle was not driven");
  } else {
    const toggle = thread.getByRole("button", { name: /^(graph|list)$/ });
    await toggle.click();
    await thread
      .locator(".react-flow")
      .waitFor({ state: "attached", timeout: 15_000 })
      .catch(() => {});
    if ((await thread.locator(".react-flow").count()) === 0) {
      console.error(`the graph button did not draw the DAG in the thread pane. It shows:
${await thread.innerText()}`);
      process.exit(1);
    }
    // `d` is the binding the ThreadList advertises - "press d for the graph" -
    // and a promise in one component with no binding anywhere is the affordance
    // lying about itself. It has to still be a promise from inside a tab.
    await page.keyboard.press("d");
    await thread
      .locator("ul li")
      .first()
      .waitFor({ state: "visible", timeout: 15_000 })
      .catch(() => {});
    if ((await thread.locator("ul li").count()) !== rows) {
      console.error(`pressing d did not bring the thread back to its list of ${rows}. It shows:
${await thread.innerText()}`);
      process.exit(1);
    }
  }

  console.log(
    `the room's side column: ${PANES.join(", ")}, each with a coloured count, each drawing its own body`,
  );

  console.log(
    `the room's todo panel shows ${JSON.stringify(expected)}${absent ? ` and never ${JSON.stringify(absent)}` : ""}, in a browser`,
  );
} finally {
  await browser.close();
}
