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
    "usage: node scripts/roster-check.mjs BASE_URL TOKEN reader=kind [reader=kind ...]",
  );
  process.exit(2);
}

const expected = want.map((pair) => {
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

  console.log(
    `the roster draws each listener's kind, distinctly, in a browser: ${seen
      .map((s) => `${s.kind} -> ${JSON.stringify(s.label)}`)
      .join(", ")}`,
  );
} finally {
  await browser.close();
}
