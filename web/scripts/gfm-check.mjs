/**
 * Every chat body is GitHub-flavoured markdown, and the two things the plain
 * path used to carry survived the move.
 *
 *   node scripts/gfm-check.mjs BASE_URL TOKEN ROOM MINE OTHER
 *
 * MINE is the handle of the principal this token is; OTHER is somebody else on
 * the node. The room is seeded here rather than read from whatever the rest of
 * the run left lying around, because every assertion below is about the exact
 * words in the messages.
 *
 * WHY THIS EXISTS. The console rendered markdown only for bodies a heuristic
 * recognised - a fence, a list, a heading, a table pipe - and everything else
 * took a plain path. The operator typed a message with `backticks` in it, got
 * backticks, and said "just go full gh flavored markdown everywhere". So the
 * heuristic is gone and this is what says so: the FIRST claim below fails on
 * the build that shipped that heuristic, because a one-line message with an
 * inline code span in it was prose to it.
 *
 * Asserted on ELEMENTS, for the reason browser-check.mjs is. A page that
 * renders `git log` and a page that renders <code>git log</code> both contain
 * the string "git log", so searching the text passes with the feature absent.
 *
 * The claims, each of which a plausible half-built version gets wrong:
 *
 *   - an inline code span is a <code> element and its backticks are NOT on
 *     screen - the whole of what was reported;
 *   - a fenced block is still pre > code, so the fork was deleted rather than
 *     flipped to the other side;
 *   - a plain sentence is still a plain sentence, carrying no code, no list
 *     and no table - "everything renders as a code block" would pass claim one;
 *   - the GFM dialect specifically: a table, a strikethrough and a bare URL,
 *     because gfm is marked's default and a default is not a decision;
 *   - an outbound link carries noopener and noreferrer, which the plain path
 *     used to put on the links it made itself;
 *   - a mention still draws as a mention, in the colour of whoever it names and
 *     ringed when it names the reader, ON A BODY THAT TAKES THE MARKDOWN PATH -
 *     which is precisely the body that used to lose it;
 *   - and a span citation still quotes the bytes somebody selected, THROUGH the
 *     render. Two arms of one gesture, because one reading cannot tell "the
 *     offsets are mapped" from "spans were quietly dropped".
 *
 * The citation claim is settled on the NODE and not in the browser: the reply
 * is sent, and what the node derives from the stored byte offsets has to be the
 * words that were selected. The console can believe whatever it likes about a
 * span; the quote a reader sees is cut out of the source by the node.
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, token, room = "gfm", mine, other] = process.argv.slice(2);
if (!base || !token || !mine || !other) {
  console.error("usage: node scripts/gfm-check.mjs BASE_URL TOKEN ROOM MINE OTHER");
  process.exit(2);
}
refuseRemote(base, "gfm-check");

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const URL_IN_BODY = "https://example.invalid/gfm#tables";

// The fixtures. Each one is the smallest body that carries its claim.
const CODE_BODY = "run `flowy say --room general` first, then check the log";
const FENCE_BODY = "the command is\n\n```\nflowy serve --addr 127.0.0.1:8787\n```";
const PLAIN_BODY = "the coupling nut backed off overnight and nothing here is markup";
const GFM_BODY = `| part | state |\n| --- | --- |\n| gate | green |\n\n~~withdrawn~~ and ${URL_IN_BODY} is the spec`;
const MENTION_BODY = `@${mine} and @${other}: check \`git log\` before you land`;

const say = async (body) => {
  const said = await fetch(`${base}/api/chat/${room}/say`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
    body: JSON.stringify({ body }),
  }).catch((err) => ({ ok: false, status: 0, text: async () => String(err) }));
  if (!said.ok) {
    die(`the seed was refused: HTTP ${said.status} ${await said.text()}
  Nothing about markdown was tested.`);
  }
  return (await said.json()).id;
};

/** What the composer says the next message will answer, or null for nothing. */
const armed = (page) =>
  page.evaluate(() => {
    const block = document.querySelector("form [data-citation]");
    if (!block) return null;
    const quote = block.querySelector("[data-cite-text]");
    return {
      message: block.getAttribute("data-citation"),
      whole: block.getAttribute("data-cite-whole") === "true",
      text: (quote?.textContent || "").trim(),
    };
  });

/**
 * Select `needle` inside one message body and finish the selection the way a
 * mouse does.
 *
 * A built Range rather than a pointer drag, and deliberately: what is under
 * test here is the ARITHMETIC between the rendered text and the raw body, and a
 * drag that lands a few pixels off selects different words on every machine.
 * That the gesture itself still cites - a real pointer, pressed and moved and
 * released - is reply-check.mjs's claim and stays there.
 */
const select = (page, bodyId, needle) =>
  page.evaluate(
    ({ bodyId, needle }) => {
      const body = document.querySelector(`[data-body="${bodyId}"]`);
      if (!body) return { ok: false, why: "no such body on screen" };
      const walker = document.createTreeWalker(body, NodeFilter.SHOW_TEXT);
      const nodes = [];
      let text = "";
      for (let n = walker.nextNode(); n; n = walker.nextNode()) {
        nodes.push({ node: n, at: text.length });
        text += n.nodeValue;
      }
      const at = text.indexOf(needle);
      if (at < 0) return { ok: false, why: `the rendered body reads ${JSON.stringify(text)}` };
      // A boundary between two text nodes belongs to the node the selection is
      // INSIDE: the start goes to the node it opens, the end to the node it
      // closes. Putting the end at offset 0 of the next node reads as a
      // selection that crosses a block boundary, and the browser then puts the
      // block's own newlines into what was selected.
      const locate = (offset, ending) => {
        for (const held of nodes) {
          const len = held.node.nodeValue.length;
          const inside = ending
            ? offset > held.at && offset <= held.at + len
            : offset >= held.at && offset < held.at + len;
          if (inside) return { node: held.node, offset: offset - held.at };
        }
        return null;
      };
      const from = locate(at, false);
      const to = locate(at + needle.length, true);
      if (!from || !to) return { ok: false, why: "the words straddle no text node" };
      const range = document.createRange();
      range.setStart(from.node, from.offset);
      range.setEnd(to.node, to.offset);
      const selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
      const got = selection.toString();
      body.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
      return { ok: true, got };
    },
    { bodyId, needle },
  );

const browser = await chromium.launch();
try {
  const codeId = await say(CODE_BODY);
  await say(FENCE_BODY);
  await say(PLAIN_BODY);
  await say(GFM_BODY);
  await say(MENTION_BODY);

  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 }).catch(() => {});

  // Wait for the LAST body seeded, not for the room: the transcript is on
  // screen one fetch before the messages are, so asserting the moment it
  // appears asserts on an empty room and fails a feature that works.
  try {
    await page
      .locator("main [data-mention]")
      .first()
      .waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const shown = await page
      .locator("main")
      .innerText()
      .catch(() => "");
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(`#${room} never painted the seeded messages, so nothing was tested. It says:
${shown}${errors}`);
  }

  // 1. INLINE BACKTICKS. The report, exactly: a body with nothing structural in
  // it but a code span. On the build that decided markdown by looking for a
  // fence, a list, a heading or a table pipe, this body was prose - so the
  // <code> is absent and the backticks are on screen.
  const inline = page.locator(`main [data-body="${codeId}"] code`);
  if ((await inline.count()) === 0) {
    const shown = await page.locator(`main [data-body="${codeId}"]`).innerText();
    die(`a message with \`backticks\` in it drew no <code> element. It reads:
  ${JSON.stringify(shown)}
  ${shown.includes("`") ? "The backticks are on screen, so the body took a plain path." : "The body is not on screen at all - this is the seed, not the render."}`);
  }
  if ((await inline.first().innerText()).trim() !== "flowy say --room general") {
    die(`the code element holds ${JSON.stringify(await inline.first().innerText())} rather than
  the words between the backticks`);
  }
  const codeShown = await page.locator(`main [data-body="${codeId}"]`).innerText();
  if (codeShown.includes("`")) {
    die(
      `the code element rendered AND a backtick is still on screen: ${JSON.stringify(codeShown)}`,
    );
  }

  // 2. A FENCE IS STILL A BLOCK. The arm that stops this change trading one
  // path for the other: fenced blocks worked before and have to work after.
  const fenced = page.locator("main [data-body] pre code");
  if ((await fenced.count()) === 0) {
    die("a fenced block drew no pre > code - the fork was flipped rather than deleted");
  }

  // 3. A PLAIN SENTENCE IS STILL PLAIN. "Render everything as a code block"
  // passes claim 1 and is not what anybody asked for.
  const plain = page.locator("main [data-body]", { hasText: "the coupling nut backed off" });
  const decorated = await plain
    .first()
    .evaluate((n) => n.querySelectorAll("code, pre, ul, ol, table, blockquote").length);
  if (decorated !== 0) {
    die(`a message with no markup in it rendered ${decorated} structural element(s)`);
  }
  if ((await plain.first().innerText()).trim() !== PLAIN_BODY) {
    die(`a plain sentence reads back as ${JSON.stringify(await plain.first().innerText())}
  rather than as it was typed`);
  }

  // 4. THE GFM DIALECT. Named one by one, because "gfm: true" is marked's
  // default and a default is not a decision anybody made or can rely on.
  const gfm = page.locator("main [data-body]", { hasText: "withdrawn" }).first();
  for (const [what, selector] of [
    ["a table", "table th"],
    ["strikethrough", "del"],
    ["an autolinked bare URL", `a[href="${URL_IN_BODY}"]`],
  ]) {
    if ((await gfm.locator(selector).count()) === 0) {
      die(`GFM ${what} did not render: no ${selector} in the body that carries one.
  It reads ${JSON.stringify(await gfm.innerText())}`);
    }
  }

  // 5. AN OUTBOUND LINK IS STILL SAFE. A body is text a peer wrote, so the
  // target must not be handed a window handle it can navigate back. The plain
  // path put this on the links it made; the renderer has to as well.
  const rel = (await gfm.locator(`a[href="${URL_IN_BODY}"]`).first().getAttribute("rel")) || "";
  if (!rel.includes("noopener") || !rel.includes("noreferrer")) {
    die(`the autolinked URL has rel=${JSON.stringify(rel)} - it needs noopener and noreferrer`);
  }

  // 6. THE MENTION CHIPS, on a body carrying a code span - which is exactly the
  // body that took the markdown path before this change and therefore lost
  // them. Colour, ring and the difference between the two.
  const chips = await page.$$eval("main [data-mention]", (nodes) =>
    nodes.map((n) => ({
      text: (n.textContent || "").trim(),
      colour: getComputedStyle(n).color,
      ring: getComputedStyle(n).boxShadow,
      plain: n.parentElement ? getComputedStyle(n.parentElement).color : "",
    })),
  );
  const chip = (name) => chips.find((c) => c.text === `@${name}`);
  const ofMine = chip(mine);
  const ofOther = chip(other);
  if (!ofMine || !ofOther) {
    die(`a body rendered as markdown lost its mention chips: what is drawn is
  ${JSON.stringify(chips.map((c) => c.text))}. This is the regression this change is most
  likely to cause - the markdown path had no chips at all.`);
  }
  for (const [name, found] of [
    [mine, ofMine],
    [other, ofOther],
  ]) {
    if (found.colour === found.plain) {
      die(`@${name} is drawn in the ordinary body colour (${found.plain}), so it is not tagged`);
    }
  }
  if (ofMine.ring === "none" || ofMine.ring === "") {
    die(`a mention of ${mine}, who is reading, wears no ring (box-shadow: ${ofMine.ring || "none"}),
  so it is drawn exactly like a mention of anybody else`);
  }
  if (ofOther.ring !== "none" && ofOther.ring !== "") {
    die(`a mention of ${other} is ringed too (box-shadow: ${ofOther.ring}), so the ring says
  "somebody was named" rather than "you were named"`);
  }
  // A mention has no click behaviour of its own - nothing anywhere consumes
  // data-mention - so what is asserted is what is true: clicking one is
  // reading, and reading must not arm a reply or throw.
  await page.locator("main [data-mention]").first().click();
  await page.waitForTimeout(200);
  if (await armed(page)) die("clicking a mention chip armed a reply - reading is not answering");

  // 7a. A SPAN CITATION, THROUGH THE RENDER. The words are AFTER the code span
  // in the same body, so their offset in the rendered text and their offset in
  // the raw body differ by the two backticks - which is the arithmetic that
  // used to be counted off the screen and is now found in the body.
  const SPAN = "then check the log";
  const selected = await select(page, codeId, SPAN);
  if (!selected.ok) die(`could not select ${JSON.stringify(SPAN)}: ${selected.why}`);
  if (selected.got !== SPAN) {
    die(`the fixture selected ${JSON.stringify(selected.got)} rather than ${JSON.stringify(SPAN)}`);
  }
  await page.waitForTimeout(200);

  // SELECTING IS NOT CITING, and this check used to assert the opposite.
  //
  // It armed the citation from onMouseUp, so a reader who dragged across a
  // message to COPY it cited it instead - the operator, 2026-08-20: "why
  // whenever i select message text here it automatically becomes a citation? I
  // just wanted to copy it." The old assertion here pinned that behaviour as
  // the contract, so the fix could not land without changing this line.
  //
  // WHAT THE CHECK IS ACTUALLY FOR SURVIVES UNCHANGED: a span selected in a
  // RENDERED markdown body has to map to the right offsets in the RAW body -
  // the arithmetic that differs by the two backticks. That is asserted below,
  // through the control instead of through the drag.
  if (await armed(page)) {
    die(`selecting text armed a citation. Selecting is how a reader COPIES; citing is a control
  they press - see MessageList's cite button and cite-gesture-check.mjs.`);
  }
  await page.locator(`[data-cite="${codeId}"]`).first().click();
  await page.waitForTimeout(200);
  const bySpan = await armed(page);
  if (!bySpan || bySpan.message !== codeId) {
    die(`pressing cite with a span selected armed ${JSON.stringify(bySpan)} - the control no
  longer cites the message the selection is in`);
  }
  if (bySpan.whole) {
    die(`selecting ${JSON.stringify(SPAN)} cited the WHOLE message. Every body is markdown now,
  so a span citation that gives up on all of them is the feature quietly dropped.`);
  }

  // And the node settles it. The console can believe whatever it likes about
  // its offsets; what a reader sees is cut out of the source by the node, from
  // the bytes that were stored.
  const composer = page.locator('form:has(textarea[aria-label="message"])');
  await composer.locator('textarea[aria-label="message"]').fill("quoting the tail of that");
  await composer.getByRole("button", { name: "send" }).click();
  await page.waitForTimeout(1500);
  const log = await (
    await fetch(`${base}/api/chat/${room}?since=0&limit=200`, {
      headers: { Authorization: `Bearer ${token}` },
    })
  ).json();
  const reply = (log.events || []).filter((e) => e.body === "quoting the tail of that").pop();
  if (!reply) die("the reply carrying the span citation was never stored");
  if (!reply.citation || reply.citation.whole) {
    die(`the stored reply cites ${JSON.stringify(reply.meta?.cite)} - not a span of the message`);
  }
  if (reply.citation.text !== SPAN) {
    die(`THE OFFSETS ARE WRONG. The node derived ${JSON.stringify(reply.citation.text)} from the
  bytes this console stored (${JSON.stringify(reply.meta?.cite)}), and what was selected was
  ${JSON.stringify(SPAN)}. A citation that quotes bytes nobody selected misquotes the person
  being cited, which is the one failure this whole design exists to prevent.`);
  }

  // 7b. THE OTHER ARM OF THE SAME GESTURE. A selection that crosses the code
  // span has no counterpart in the raw body at all - what is on screen has no
  // backticks in it - so there is no span to be right about, and the honest
  // answer is the whole message. One reading cannot tell a mapping that works
  // from a feature that was deleted; these two can.
  const ACROSS = "run flowy say";
  const crossed = await select(page, codeId, ACROSS);
  if (!crossed.ok) die(`could not select across the code span: ${crossed.why}`);
  await page.waitForTimeout(200);
  // Through the control, like the span case above: selecting is copying now,
  // and pressing cite is what arms. What is being measured here is unchanged -
  // a selection that CANNOT be placed in the raw body still cites the message
  // rather than citing nothing.
  await page.locator(`[data-cite="${codeId}"]`).first().click();
  await page.waitForTimeout(200);
  const byCrossing = await armed(page);
  if (!byCrossing || byCrossing.message !== codeId) {
    die(`pressing cite with a selection across a code span armed ${JSON.stringify(byCrossing)} -
  the control must always cite that body, whatever grain it ends up citing`);
  }
  if (!byCrossing.whole) {
    die(`selecting ${JSON.stringify(ACROSS)}, which appears nowhere in the raw body
  ${JSON.stringify(CODE_BODY)}, armed a SPAN citation. There is no span there to be right
  about, so this one is quoting bytes it guessed at.`);
  }

  if (crashes.length > 0) die(`the page threw while this ran:\n  ${crashes.join("\n  ")}`);

  console.log(
    `ok  backticks are <code>, fences are pre>code, a plain sentence stays plain, GFM tables,
    strikethrough and autolinks render with rel="${rel}", @${mine} is ringed and @${other} is not,
    and the node derived ${JSON.stringify(reply.citation.text)} from the span that was selected
    while a selection crossing the code span cited the whole message`,
  );
} finally {
  await browser.close();
}
