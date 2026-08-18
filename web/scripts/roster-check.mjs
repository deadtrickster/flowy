/**
 * What the room's roster says a listener can DO, in a real browser, on the
 * element rather than on the page's text.
 *
 *   node scripts/roster-check.mjs BASE_URL TOKEN reader=kind [reader=kind ...]
 *
 * A listener that hears the room and cannot wake anybody is the failure this
 * check exists for. It polls, it is attached, its last poll is seconds old, and
 * every one of those is true while the session behind it is deaf - so a roster
 * drawn from them reports healthy, which it did for 28 minutes with a person
 * waiting on the other end. The kind is the only field that answers the
 * question, so the assertion is that the kind is DRAWN, per listener, in words
 * a reader can tell apart.
 *
 * On the element for the reason browser-check.mjs is: a page-text search for
 * "forked" or "unknown" would pass with the listener lines entirely absent, and
 * would pass just as well if all three lines said the same thing. So this finds
 * the line for each named reader, reads the kind element inside it, and then
 * requires the three renderings to differ - a version of this feature that drew
 * one word for every kind would satisfy every check that only asked "is the
 * kind on the screen" and would tell nobody anything.
 */

import { chromium } from "playwright";

const [base, token, ...want] = process.argv.slice(2);
if (!base || !token || want.length === 0) {
  console.error(
    "usage: node scripts/roster-check.mjs BASE_URL TOKEN reader=kind [reader=kind ...] [--went-quiet=READER]",
  );
  process.exit(2);
}

// The reader that was armed and stopped, if the caller staged one. Its whole
// point is that it is NOT among the listeners above: it must be drawn, in its
// own right, as a seat that is not answering.
const wentQuiet = want
  .filter((arg) => arg.startsWith("--went-quiet="))
  .map((arg) => arg.slice("--went-quiet=".length));

const expected = want
  .filter((arg) => !arg.startsWith("--"))
  .map((pair) => {
    const [reader, kind] = pair.split("=");
    if (!reader || !kind) {
      console.error(`"${pair}" is not reader=kind`);
      process.exit(2);
    }
    return { reader, kind };
  });

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/general`, { timeout: 20_000 }).catch(() => {});

  // The roster refreshes on its own clock rather than with the room's messages,
  // so the first paint of the room has no listeners on it. Waiting for a LINE
  // and not for the panel is the same lesson browser-check.mjs learned about
  // todos: a panel is visible from mount with nothing in it, and reading it
  // then asserts on the empty state.
  const lines = page.locator("aside li[data-listener]");
  try {
    await lines.first().waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    console.error(
      `the room's roster has no listener lines: nothing matches aside li[data-listener].
Either the roster is not drawing its listeners or it does not mark them.${errors}`,
    );
    process.exit(1);
  }

  const seen = [];
  for (const { reader, kind } of expected) {
    const line = page.locator(`aside li[data-listener="${reader}"]`);
    try {
      await line.waitFor({ state: "visible", timeout: 20_000 });
    } catch {
      const listed = await lines.evaluateAll((nodes) =>
        nodes.map((n) => n.getAttribute("data-listener")),
      );
      console.error(
        `the roster has no line for the reader ${JSON.stringify(reader)}. It lists: ${listed.join(", ")}`,
      );
      process.exit(1);
    }
    const drawn = line.locator("[data-waiter-kind]").first();
    if ((await drawn.count()) === 0) {
      console.error(
        `the roster line for ${JSON.stringify(reader)} draws no kind at all. It says: ${JSON.stringify(await line.innerText())}`,
      );
      process.exit(1);
    }
    const got = await drawn.getAttribute("data-waiter-kind");
    const label = (await drawn.innerText()).trim();
    if (got !== kind) {
      console.error(
        `${reader} polled as ${kind} and the roster draws it as ${JSON.stringify(got)} (${JSON.stringify(label)})`,
      );
      process.exit(1);
    }
    if (label === "") {
      // A kind carried only in an attribute is a kind nobody reads. The
      // attribute is how this check finds it; the words are the feature.
      console.error(`${reader} is marked ${kind} in the markup and says nothing on the screen`);
      process.exit(1);
    }
    seen.push({ reader, kind, label });
  }

  // Three states have to read as three states. One word for all of them passes
  // every assertion above and is exactly the widget this replaces.
  const labels = new Set(seen.map((s) => s.label));
  if (labels.size !== seen.length) {
    console.error(
      `the kinds are not told apart on the screen: ${seen.map((s) => `${s.kind} -> ${JSON.stringify(s.label)}`).join(", ")}`,
    );
    process.exit(1);
  }

  // A BOOKMARK IS NOT AN EAR. The console declares a reader per room to keep a
  // human's unread place - console:general and friends - and those hold a
  // position without ever polling. They arrived in this pane as detached
  // listeners of unknown kind, six of them, until the operator said it was a
  // mess. The pane answers "is anybody hearing me", so a row that has never
  // called the inbox does not belong in it.
  const bookmarks = await lines.evaluateAll((nodes) =>
    nodes
      .map((n) => n.getAttribute("data-listener") || "")
      .filter((name) => name.startsWith("console:")),
  );
  if (bookmarks.length > 0) {
    console.error(
      `the listening pane lists readers that have never polled: ${bookmarks.join(", ")}.
  Those are the console's own unread bookmarks. They keep a place in a room and
  never call the inbox, so they cannot hear anything and must not be drawn as
  listeners - the pane's whole question is whether anybody is hearing you.`,
    );
    process.exit(1);
  }

  // ONE LINE PER PRINCIPAL. Two rows for one identity is a DOUBLED WAITER -
  // they share a server-side cursor, so each hears only part of the room while
  // both look healthy - and it must be visible rather than tidied into one
  // clean line. Deduping by NAME gets this inverted in both directions: two
  // names can be one principal, and one name can be two different users.
  const doubled = await lines.evaluateAll((nodes) =>
    nodes
      .map((n) => ({
        name: n.getAttribute("data-listener") || "",
        rows: Number(n.getAttribute("data-listener-rows") || "1"),
        says: n.textContent || "",
      }))
      .filter((l) => l.rows > 1),
  );
  for (const d of doubled) {
    if (!d.says.includes("doubled")) {
      console.error(
        `${d.name} is drawn from ${d.rows} polling rows and the line does not say so.
  One identity polling twice splits its wake-ups between two processes and both
  look healthy. Collapsing them silently hides the failure this pane is for.`,
      );
      process.exit(1);
    }
  }

  // A SEAT THAT WENT QUIET IS ON THE SCREEN, SAYING SO.
  //
  // This is the row the operator asked about twice: an agent had not polled in
  // six hours, its poll counter had been left up by a decrement that never ran,
  // and every surface here drew it as attached and polling. The obvious repair -
  // window it out with the dead cursors - would have made this panel tidy and
  // deleted the only record that the seat had ever gone deaf.
  //
  // So the assertion is not that it is gone. It is that it is DRAWN, marked
  // lost, in words, with how long ago it was last heard from - and that it is
  // not sitting up among the listeners claiming to be polling.
  for (const reader of wentQuiet) {
    const line = page.locator(`aside li[data-listener="${reader}"]`);
    if ((await line.count()) === 0) {
      console.error(
        `${reader} stopped mid-poll six hours ago and the roster draws no line for it at all.
  Dropping the row tidies the panel and destroys the only evidence that the seat
  went deaf - which is the thing somebody is looking at this panel to find out.`,
      );
      process.exit(1);
    }
    const state = await line.first().getAttribute("data-listener-state");
    if (state !== "lost") {
      console.error(
        `${reader} has not polled in six hours and its line is marked ${JSON.stringify(state)}.
  A poll that started six hours ago is not in flight: the server blocks for
  twenty-five seconds at a time, so the counter that says otherwise is a
  decrement that never ran.`,
      );
      process.exit(1);
    }
    const says = (await line.first().innerText()).trim();
    if (!/not answering/i.test(says) || !/\d+h/.test(says)) {
      console.error(
        `${reader} is marked lost in the markup and the line says ${JSON.stringify(says)}.
  It has to say, in words, that the seat is not answering and how long it has
  been since anything was heard from it. A state carried only in an attribute
  is a state nobody reads.`,
      );
      process.exit(1);
    }
  }

  console.log(
    `the roster draws each listener's kind, distinctly, in a browser: ${seen
      .map((s) => `${s.kind} -> ${JSON.stringify(s.label)}`)
      .join(", ")}; no never-polled bookmarks drawn as listeners${
      doubled.length ? `; ${doubled.length} doubled identity named as doubled` : ""
    }${wentQuiet.length ? `; ${wentQuiet.join(", ")} drawn as not answering` : ""}`,
  );
} finally {
  await browser.close();
}
