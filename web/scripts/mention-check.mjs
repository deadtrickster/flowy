/**
 * The @names inside a message, in a real browser, asserted on the ELEMENTS.
 *
 *   node scripts/mention-check.mjs BASE_URL TOKEN ROOM MINE OTHER UNRESOLVED
 *
 * MINE is the name of the principal this token is, OTHER is somebody else, and
 * UNRESOLVED is an @word in the same room that names nobody on this node.
 *
 * Asserted on the elements and not on the page text, for the reason
 * browser-check.mjs is: a name that is drawn as a mention and a name that is
 * merely sitting in a body are the same string, and the room shows both - so
 * searching the page for "@alice" passes with the feature entirely absent. The
 * mention runs carry data-mention, so this finds those and reads them.
 *
 * Four claims, each of which a plausible half-built version gets wrong:
 *
 *   - a resolved name is drawn as a mention at all;
 *   - in the colour of whoever it names, which is the colour that person
 *     SPEAKS in - a mention coloured from its own string would look right and
 *     tie a message to the wrong person;
 *   - a mention of the reader stands out further than a mention of somebody
 *     else, which is the whole reason a room of five needs this;
 *   - and an @word that resolved to nobody is NOT drawn as one. A version that
 *     colours every @word passes the first three and lies about the fourth,
 *     which is worse than no feature: it says somebody was addressed when the
 *     node addressed nobody.
 */

import { chromium } from "playwright";

const [base, token, room, mine, other, unresolved] = process.argv.slice(2);
if (!base || !token || !room || !mine || !other || !unresolved) {
  console.error("usage: node scripts/mention-check.mjs BASE_URL TOKEN ROOM MINE OTHER UNRESOLVED");
  process.exit(2);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 }).catch(() => {});

  // Wait for the mention RUN, not for the room: the transcript is on screen one
  // fetch before the messages are, so reading it the moment it appears asserts
  // on an empty room and fails a feature that works.
  try {
    await page
      .locator("main [data-mention]")
      .first()
      .waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const shown = await page
      .locator("main")
      .innerText()
      .catch(() => "");
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    console.error(`no @name in this room is drawn as a mention: nothing carries data-mention.
The room says:
${shown}${errors}`);
    process.exit(1);
  }

  // Every mention run, with what a person would actually see of it: the text,
  // the colour it came out, the ring (tailwind draws one as a box shadow), and
  // the ordinary colour of the body it sits in, read from its own parent so a
  // theme change cannot make this pass by accident.
  const runs = await page.$$eval("main [data-mention]", (nodes) =>
    nodes.map((n) => ({
      text: (n.textContent || "").trim(),
      colour: getComputedStyle(n).color,
      ring: getComputedStyle(n).boxShadow,
      plain: n.parentElement ? getComputedStyle(n.parentElement).color : "",
    })),
  );

  // And the speaker tags, which is where the colour has to agree.
  const speakers = await page.$$eval("main span[title]", (nodes) =>
    nodes.map((n) => ({ name: (n.textContent || "").trim(), colour: getComputedStyle(n).color })),
  );

  const run = (name) => runs.find((r) => r.text === `@${name}`);
  const ofMine = run(mine);
  const ofOther = run(other);
  for (const [what, found] of [
    [mine, ofMine],
    [other, ofOther],
  ]) {
    if (!found) {
      console.error(
        `@${what} is not drawn as a mention. What is: ${
          runs.map((r) => r.text).join(", ") || "nothing"
        }`,
      );
      process.exit(1);
    }
    if (found.colour === found.plain) {
      console.error(
        `@${what} is drawn in the ordinary body colour (${found.plain}), so it is not tagged at all`,
      );
      process.exit(1);
    }
  }

  // The same person, the same colour. This is the claim that a mention is
  // coloured BY WHOM IT NAMES rather than by the word: the speaker tag for that
  // name is on the same page, and the two have to agree.
  const spoke = speakers.find((s) => s.name === mine);
  if (!spoke) {
    console.error(`nobody called ${mine} has spoken in ${room}, so the colour of a mention of them
could not be compared with the colour they speak in - the fixture is missing, not the feature`);
    process.exit(1);
  }
  if (spoke.colour !== ofMine.colour) {
    console.error(`a mention of ${mine} is ${ofMine.colour} and ${mine} speaks in ${spoke.colour}:
the same person is two colours, which is worse than one colour for everybody`);
    process.exit(1);
  }

  // A mention of you stands out further than a mention of somebody else. Both
  // halves, because "everything is ringed" tells nobody apart either.
  if (ofMine.ring === "none" || ofMine.ring === "") {
    console.error(`a mention of ${mine}, who is reading, is drawn exactly like a mention of anybody
else: no ring on it (box-shadow: ${ofMine.ring || "none"})`);
    process.exit(1);
  }
  if (ofOther.ring !== "none" && ofOther.ring !== "") {
    console.error(`a mention of ${other} is ringed too (box-shadow: ${ofOther.ring}), so the ring
says "somebody was named" rather than "you were named"`);
    process.exit(1);
  }

  // And the word that means nobody. It has to be on the page - "this is not
  // marked up" is trivially true of a message that never arrived - and it must
  // not be a mention.
  const body = await page.locator("main").innerText();
  if (!body.includes(`@${unresolved}`)) {
    console.error(`the message holding @${unresolved} is not on screen, so nothing was tested
about a name that resolves to nobody`);
    process.exit(1);
  }
  if (run(unresolved)) {
    console.error(`@${unresolved} names nobody on this node and is drawn as a mention anyway,
so the console is colouring @words rather than the people the node resolved`);
    process.exit(1);
  }

  console.log(
    `${runs.length} mention(s) in ${room}: @${mine} in the colour ${mine} speaks in and ringed, ` +
      `@${other} coloured and not ringed, @${unresolved} left as text`,
  );
} finally {
  await browser.close();
}
