/**
 * A citation on a message, in a real browser, asserted on the ELEMENTS.
 *
 *   node scripts/cite-check.mjs BASE_URL TOKEN ROOM SPEAKER SPAN_TEXT ABSENT_TEXT
 *
 * SPEAKER is the handle of whoever said the message being cited - somebody
 * other than the person whose replies carry the citations, or the colour claim
 * below would be true of any implementation. SPAN_TEXT is the span one of those
 * replies cites, and ABSENT_TEXT is a phrase of the cited message that is
 * OUTSIDE that span.
 *
 * On the elements and not on the page text, for the reason browser-check.mjs
 * is: the quoted words are also in the message being quoted, three rows up the
 * same transcript, so searching the page for them passes with the feature
 * entirely absent. The citation block carries data-citation, its attribution
 * data-cite-speaker and its quote data-cite-text, so this finds those.
 *
 * Three claims, each of which a plausible half-built version gets wrong:
 *
 *   - a reply draws what it is answering at all, rather than an id;
 *   - attributed to WHOEVER WAS QUOTED, in the colour that person speaks in on
 *     the same page - a citation drawn in the citing speaker's colour, or in
 *     none, reads as the citing speaker's own words;
 *   - and a citation of a SPAN quotes the span. Deriving the whole body for a
 *     part citation is the failure that looks like a working feature until
 *     somebody quotes one clause of a long message.
 */

import { chromium } from "playwright";

const [base, token, room, speaker, span, absent] = process.argv.slice(2);
if (!base || !token || !room || !speaker || !span || !absent) {
  console.error(
    "usage: node scripts/cite-check.mjs BASE_URL TOKEN ROOM SPEAKER SPAN_TEXT ABSENT_TEXT",
  );
  process.exit(2);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 }).catch(() => {});

  // Wait for the citation block, not for the room: the transcript is on screen
  // one fetch before the messages are, so reading it the moment it appears
  // asserts on an empty room and fails a feature that works.
  try {
    await page
      .locator("main [data-citation]")
      .first()
      .waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const shown = await page
      .locator("main")
      .innerText()
      .catch(() => "");
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    console.error(`no reply in this room draws what it is answering: nothing carries data-citation.
The room says:
${shown}${errors}`);
    process.exit(1);
  }

  // Every citation, with what a person would actually see of it: who it says
  // was quoted, the colour that attribution came out, the quoted words, and
  // the ordinary text colour of the block it sits in, read from its own parent
  // so a theme change cannot make this pass by accident.
  const cites = await page.$$eval("main [data-citation]", (nodes) =>
    nodes.map((n) => {
      const who = n.querySelector("[data-cite-speaker]");
      const quote = n.querySelector("[data-cite-text]");
      return {
        whole: n.getAttribute("data-cite-whole") === "true",
        name: (who?.textContent || "").trim(),
        colour: who ? getComputedStyle(who).color : "",
        text: (quote?.textContent || "").trim(),
        plain: n.parentElement ? getComputedStyle(n.parentElement).color : "",
      };
    }),
  );

  // And the speaker tags, which is where the colour has to agree.
  const speakers = await page.$$eval("main span[title]", (nodes) =>
    nodes.map((n) => ({ name: (n.textContent || "").trim(), colour: getComputedStyle(n).color })),
  );

  const whole = cites.find((c) => c.whole);
  const part = cites.find((c) => !c.whole);
  if (!whole || !part) {
    console.error(`this room should hold a citation of a whole message and a citation of one span
of it. What is drawn: ${JSON.stringify(cites, null, 2)}`);
    process.exit(1);
  }

  // Who was quoted, said on the citation itself. A block that shows the words
  // and not the name is the thing this feature exists to stop being retyped.
  for (const cite of [whole, part]) {
    if (cite.name !== speaker) {
      console.error(`a citation is attributed to ${JSON.stringify(cite.name)}, and the message it
quotes was said by ${JSON.stringify(speaker)} - a quotation under the wrong name`);
      process.exit(1);
    }
    if (!cite.colour || cite.colour === cite.plain) {
      console.error(`the attribution on a citation is drawn in the ordinary text colour
(${cite.plain}), so it is not in the quoted speaker's colour at all`);
      process.exit(1);
    }
  }

  // The same person, the same colour. This is the claim that the citation is
  // coloured BY WHOM IT QUOTES: that person's speaker tag is on the same page,
  // and the two have to agree.
  const spoke = speakers.find((s) => s.name === speaker);
  if (!spoke) {
    console.error(`nobody called ${speaker} has spoken in ${room}, so the colour of a citation of
them could not be compared with the colour they speak in - the fixture is missing, not the feature`);
    process.exit(1);
  }
  if (spoke.colour !== whole.colour) {
    console.error(`a citation of ${speaker} is ${whole.colour} and ${speaker} speaks in
${spoke.colour}: the same person is two colours, which is worse than one colour for everybody`);
    process.exit(1);
  }

  // The span citation quotes the span. Both halves: it has to carry the words
  // it cites, and it must NOT carry the part of the message outside them.
  if (!part.text.includes(span)) {
    console.error(`the span citation quotes ${JSON.stringify(part.text)}, which does not contain
the span it cites, ${JSON.stringify(span)}`);
    process.exit(1);
  }
  if (part.text.includes(absent)) {
    console.error(`the span citation quotes ${JSON.stringify(part.text)}, which carries
${JSON.stringify(absent)} - text outside the span it cites, so it is rendering the whole message`);
    process.exit(1);
  }
  if (!whole.text.includes(absent)) {
    console.error(`the whole-message citation quotes ${JSON.stringify(whole.text)}, which is
missing ${JSON.stringify(absent)} - so whole and part are not two different grains at all`);
    process.exit(1);
  }

  console.log(
    `${cites.length} citation(s) in ${room}: attributed to ${speaker} in the colour ${speaker} ` +
      `speaks in, the span one quoting ${JSON.stringify(span)} and nothing outside it`,
  );
} finally {
  await browser.close();
}
