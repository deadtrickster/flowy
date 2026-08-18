/**
 * NOBODY IS OFFERED A SAVED PASSWORD OR A STORED CARD WHILE WRITING DOWN WORK.
 *
 *   node scripts/autofill-check.mjs BASE_URL TOKEN ROOM TITLE CARRIER [--fields-only]
 *
 * The bug this exists for was reported in the operator's own words: "raise todo
 * input makes my browser show password and credit card suggestion. chat input
 * doesn't". The raise box in the room's todo panel was one unnamed text input
 * with no type and no autocomplete, alone in a form with a submit button - which
 * is the exact shape a browser reads as a sign-in, so Chrome offered what it had
 * saved. The message box beside it never had the problem because what you type
 * into it is a textarea, and browsers do not offer a credential over one.
 *
 * WHAT IS ASSERTED, AND WHY IT IS NOT THE SUGGESTION ITSELF. A native autofill
 * dropdown is chrome's own UI: it is not in the page, no automation API reports
 * it, and a check that waited for it would be a check that always passes. What
 * decides whether it appears IS in the page, and it is the only input to that
 * decision this console controls: the type, the name and the autocomplete token
 * on the field, and the autocomplete on the form around it. So that is what is
 * measured, on the elements the browser would actually read, after the journey
 * that puts them on screen - not in the source, where a component nobody renders
 * passes just as well as one somebody uses.
 *
 * IT IS A FLOW, NOT AN ATTRIBUTE READ. Each field is reached the way a person
 * reaches it - open the page, click the control, type, submit, see the result -
 * because the failure next door was a New button disabled until a name box had
 * text, so the click never reached a handler and every test still passed. A
 * field that is annotated perfectly and cannot be typed into is not fixed, so
 * this check types into every field it makes a claim about.
 *
 * The flows, in the order they run:
 *
 *   1. raise a todo in a room. Open /chat/ROOM, click into the raise box, type
 *      TITLE, submit, and the row arrives in the panel - with the field state
 *      asserted before typing AND after the panel has refilled from the node,
 *      because a re-render that drops the attributes is the same bug an hour
 *      later;
 *   2. say who is carrying it. Click the assignee cell on the row just raised,
 *      which swaps it for a lone text input in its own form, and drive that;
 *   3. sweep the room page and every other page named on the command line: no
 *      text box a person can type into is left for the browser to guess about.
 *      The operator hit one field, and the next one they hit will be another.
 *
 * --fields-only drives 1 and 3 without writing anything, for pointing at a
 * stand-in that serves the bundle but cannot take a raise. It is for proving by
 * hand that this check goes red against an unfixed console. THE GATE MUST NOT
 * PASS IT: without the journey, this stops being a flow check.
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const args = process.argv.slice(2);
const fieldsOnly = args.includes("--fields-only");
const sweep = args.filter((a) => a.startsWith("--page=")).map((a) => a.slice("--page=".length));
const positional = args.filter((a) => !a.startsWith("--"));
const [base, token, room, title, carrier] = positional;

if (!base || !token || !room || !title || (!carrier && !fieldsOnly)) {
  console.error(
    "usage: node scripts/autofill-check.mjs BASE_URL TOKEN ROOM TITLE CARRIER [--page=/path]... [--fields-only]",
  );
  process.exit(2);
}

// This check raises a todo, so it must not be aimed at a live node by accident.
refuseRemote(base, "autofill-check");

if (fieldsOnly) {
  console.error(
    "autofill-check: --fields-only, so NOTHING IS RAISED AND NO JOURNEY IS DRIVEN.\n" +
      "  This mode exists to prove the check red against an unfixed bundle. A gate that\n" +
      "  passes it is not testing that anybody can raise a todo.",
  );
}

/** die prints what a person would have seen and fails the check. */
function die(message, shown) {
  console.error(shown ? `${message}\nThe screen shows:\n${shown}` : message);
  process.exit(1);
}

/** What one field tells the browser about itself, read off the live element. */
async function stateOf(locator) {
  return locator.evaluate((el) => ({
    tag: el.tagName.toLowerCase(),
    type: (el.getAttribute("type") || "").toLowerCase(),
    name: el.getAttribute("name") || "",
    autocomplete: (el.getAttribute("autocomplete") || "").toLowerCase(),
    form: el.form ? (el.form.getAttribute("autocomplete") || "").toLowerCase() : null,
    label: el.getAttribute("aria-label") || el.getAttribute("placeholder") || "",
    disabled: el.disabled,
  }));
}

/**
 * assertSafe fails unless this field has told the browser it is not one of its
 * own. `wantName` is required of the fields this row is about; the sweep asks
 * only for the autocomplete, which is the part that decides the suggestion.
 */
function assertSafe(where, state, { wantName = false } = {}) {
  const said = JSON.stringify(state);
  if (state.autocomplete !== "off") {
    die(
      `${where} carries autocomplete=${JSON.stringify(state.autocomplete)}, want "off".
An unannotated text box in a form with a submit button is the shape a browser
reads as a sign-in, and it offers a saved password or a stored card over it.
The element says: ${said}`,
    );
  }
  if (state.tag === "input" && state.type === "") {
    die(
      `${where} has no type attribute at all, so the browser has one less thing to go on
and falls back to guessing from the form. The element says: ${said}`,
    );
  }
  if (wantName && state.name === "") {
    die(`${where} has no name, so nothing on it says what it is for. The element says: ${said}`);
  }
  if (state.form !== null && state.form !== "off") {
    die(
      `the form around ${where} carries autocomplete=${JSON.stringify(state.form)}, want "off".
The browser reads the form to decide what the GROUP is for and the field to decide
what one box is for, and it was the group that looked like a sign-in here.
The element says: ${said}`,
    );
  }
}

const bearer = { Authorization: `Bearer ${token}` };
const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  const errors = () => (crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "");

  /**
   * Every fillable box on whatever page is open, with what it says about
   * itself. Checkboxes, radios and buttons are inputs too and are not what
   * autofill is about, so this reads the type rather than the tag - and an
   * input with NO type is exactly the case that started this, which is why a
   * missing type counts as text here.
   */
  async function sweepPage(where) {
    const found = await page.evaluate(() => {
      const fillable = (el) => {
        if (el.tagName === "TEXTAREA") return true;
        if (el.tagName !== "INPUT") return false;
        const t = (el.getAttribute("type") || "text").toLowerCase();
        return ![
          "checkbox",
          "radio",
          "button",
          "submit",
          "reset",
          "range",
          "color",
          "file",
          "hidden",
          "image",
        ].includes(t);
      };
      return Array.from(document.querySelectorAll("input, textarea"))
        .filter(fillable)
        .filter((el) => el.offsetParent !== null || el.getClientRects().length > 0)
        .map((el) => ({
          tag: el.tagName.toLowerCase(),
          type: (el.getAttribute("type") || "").toLowerCase(),
          name: el.getAttribute("name") || "",
          autocomplete: (el.getAttribute("autocomplete") || "").toLowerCase(),
          form: el.form ? (el.form.getAttribute("autocomplete") || "").toLowerCase() : null,
          label: el.getAttribute("aria-label") || el.getAttribute("placeholder") || "",
          disabled: el.disabled,
        }));
    });
    for (const state of found) {
      assertSafe(
        `the ${state.tag} ${JSON.stringify(state.label || state.name)} on ${where}`,
        state,
      );
    }
    return found.length;
  }

  // ------------------------------------------------- flow 1: raise a todo
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 20_000 }).catch(() => {});

  // The side column is a tab bar, and the todos tab happens to be the one it
  // opens on. Clicked anyway rather than assumed: a person who arrives on
  // another tab has to reach this pane, and a default is a thing that changes.
  const tab = page.locator('[data-room-pane="todos"]');
  if (await tab.count()) {
    await tab
      .first()
      .click({ timeout: 10_000 })
      .catch(() => {});
  }

  const panel = page
    .locator("aside section, section")
    .filter({ has: page.locator("h2", { hasText: /^todos$/ }) })
    .first();
  try {
    await panel.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    die(`the room has no todo panel, so there is no raise box to check.${errors()}`);
  }

  // Found by its accessible name, which is what a person reads, and NOT by a
  // marker attribute put there by this fix. A check that located the field by
  // something the fix added would go red on an unfixed console for the wrong
  // reason - "no such element" rather than "that element invites autofill" -
  // and would say nothing at all about a console where somebody kept the marker
  // and dropped the annotation.
  const raiseBox = panel
    .getByRole("textbox", { name: new RegExp(`^raise a todo in ${room}$`, "i") })
    .first();
  try {
    await raiseBox.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    die(
      `the todo panel has no box named "raise a todo in ${room}": the field a person types a
new todo into is not on the screen, so this check would have passed on its absence.${errors()}`,
      await panel.innerText().catch(() => ""),
    );
  }

  const before = await stateOf(raiseBox);
  if (before.disabled) {
    die(
      `the raise box is disabled on a freshly opened room, so nobody can raise a todo at all -
which is the New-button failure next door, where the control was never reachable and every
test still passed. The element says: ${JSON.stringify(before)}`,
    );
  }
  assertSafe("the raise-a-todo box", before, { wantName: true });

  // Reached and typed the way a person does: click the box, type with the
  // keyboard. fill() sets a value without ever proving the control takes focus.
  await raiseBox.click({ timeout: 10_000 });
  await raiseBox.pressSequentially(title, { delay: 5 });

  const raiseButton = panel.getByRole("button", { name: /^raise$/ }).first();
  if (!fieldsOnly) {
    if (await raiseButton.isDisabled()) {
      die(
        `the raise button is still disabled after typing ${JSON.stringify(title)} into the box,
so the journey ends before anything is raised`,
        await panel.innerText().catch(() => ""),
      );
    }
    await raiseButton.click();

    // The journey finishes where a person expects: the row is in the panel.
    try {
      await panel
        .locator("li")
        .filter({ hasText: title })
        .first()
        .waitFor({ state: "visible", timeout: 20_000 });
    } catch {
      die(
        `${JSON.stringify(title)} was typed into the raise box and submitted, and no row for it
reached the panel${errors()}`,
        await panel.innerText().catch(() => ""),
      );
    }

    // And the node holds it, because a row that is only in this tab is the same
    // bug one reload later.
    const held = await fetch(
      `${base}/api/artifacts?type=memory&kind=todo&room=${encodeURIComponent(room)}`,
      { headers: bearer },
    );
    const list = held.ok ? await held.json() : { artifacts: [] };
    const mine = (list.artifacts ?? []).find((a) => a.title === title);
    if (!mine) {
      die(`the panel drew ${JSON.stringify(title)} and the node has no todo by that title in
#${room}: the screen and the store disagree`);
    }

    // The panel has been refilled from the node since the first read. A field
    // that loses its annotation on re-render is the reported bug again.
    const after = await stateOf(raiseBox);
    assertSafe("the raise-a-todo box, after the panel refilled from the node", after, {
      wantName: true,
    });

    // ------------------------------------------- flow 2: say who carries it
    const row = panel.locator("li").filter({ hasText: title }).first();
    await row.locator("[data-assignee]").first().click({ timeout: 10_000 });
    const nameBox = row.getByRole("textbox").first();
    try {
      await nameBox.waitFor({ state: "visible", timeout: 10_000 });
    } catch {
      die(
        "clicking the assignee cell opened nothing to type a name into",
        await panel.innerText().catch(() => ""),
      );
    }
    assertSafe("the who-is-carrying-this box", await stateOf(nameBox), { wantName: true });
    await nameBox.pressSequentially(carrier, { delay: 5 });
    await nameBox.press("Enter");
    try {
      await row
        .locator("[data-assignee]")
        .filter({ hasText: carrier })
        .first()
        .waitFor({ state: "visible", timeout: 15_000 });
    } catch {
      die(
        `the panel did not take ${JSON.stringify(carrier)} as the carrier of the row just raised`,
        await panel.innerText().catch(() => ""),
      );
    }
  }

  // -------------------------------------------------------- flow 3: the sweep
  //
  // Each page is waited for by its OWN first text box rather than by network
  // idle: this console long-polls, so it is never idle, and a check that waited
  // for that would spend its timeout on every page and then count whatever had
  // happened to mount. A page with no text box at all is legal - the wait is
  // allowed to time out, and the count below is what says the sweep saw
  // something.
  let boxes = await sweepPage(`the room page /chat/${room}`);
  for (const path of sweep) {
    await page.goto(`${base}${path}`, { timeout: 20_000 }).catch(() => {});
    await page
      .locator("input, textarea")
      .first()
      .waitFor({ state: "visible", timeout: 10_000 })
      .catch(() => {});
    // The first box on any page is the token bar in the shell, which is up
    // before the route is, so a moment is given to the route's own fields. A
    // field that mounted after this would be counted by nobody, which is why
    // the two fields this row is actually about are driven above rather than
    // left to the sweep.
    await page.waitForTimeout(750);
    boxes += await sweepPage(path);
  }
  if (boxes === 0) {
    die("no text box was found on any page checked, so this check asserted nothing");
  }

  console.log(
    fieldsOnly
      ? `${boxes} text boxes across ${1 + sweep.length} pages all tell the browser not to fill them (fields only, nothing raised)`
      : `raised ${JSON.stringify(title)} through the panel and named its carrier, and all ${boxes} text boxes across ${1 + sweep.length} pages tell the browser not to fill them`,
  );
} finally {
  await browser.close();
}
