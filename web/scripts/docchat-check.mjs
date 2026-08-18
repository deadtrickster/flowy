/**
 * The message box beside a document, driven in a real browser at two lengths of
 * conversation.
 *
 *   node scripts/docchat-check.mjs BASE_URL TOKEN PROJECT
 *
 * THE REPORT THIS EXISTS FOR: "reports chat has send button but no text area".
 * The textarea was in the DOM the whole time, correctly sized and not disabled.
 * It was COVERED: DocumentPanes wrapped MessageList in a plain block div, so the
 * transcript sized to its content instead of to the pane, overflowed downwards,
 * and - being position:relative - painted over the static form under it. A
 * person saw the messages, a sliver of the send button, and no box.
 *
 * So presence is not the assertion. `document.querySelector("textarea")` was
 * true against the broken console, and so was every reading of its width, its
 * height and its disabled flag. What was false was that a person could put a
 * caret in it.
 *
 * TWO ARMS, VARYING ONLY THE LENGTH OF THE CONVERSATION, because one reading
 * cannot tell "the box works" from "the box works until somebody says three
 * things in this room":
 *
 *   short - a couple of messages, the transcript is shorter than the pane. The
 *           box was always reachable here, which is exactly why a check written
 *           against a fresh document room would have passed on the broken code.
 *   long  - enough messages to overflow the pane. This is the arm the defect
 *           lives in and the arm that must be red before the fix.
 *
 * Each arm is a person's journey and nothing else: click into the box, type,
 * press send, then ask the NODE what it holds. The transcript is not evidence
 * on its own - the console merges an optimistic copy of its own send into the
 * list, so a console talking to itself satisfies "the message appeared".
 *
 * The click is the load-bearing step. Playwright's click waits for the element
 * to be hit-testable, so a covered box fails here with the covering element
 * named, which is the diagnosis rather than a symptom.
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, token, project] = process.argv.slice(2);

if (!base || !token || !project) {
  console.error("usage: node scripts/docchat-check.mjs BASE_URL TOKEN PROJECT");
  process.exit(2);
}

// This one writes a report and forty-odd messages, none of which can be deleted.
// Pointed at the dogfood node it would fill a room nobody asked for.
refuseRemote(base, "docchat-check");

const die = (why) => {
  console.error(why);
  process.exit(1);
};

/** ask talks to the node directly, for the seeding and for the read-back. */
const ask = async (path, init = {}) => {
  const res = await fetch(`${base}${path}`, {
    ...init,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${init.method ?? "GET"} ${path} -> ${res.status} ${text}`);
  return text ? JSON.parse(text) : null;
};

/**
 * A REPORT OF ITS OWN, made here rather than taken as an argument, because the
 * short arm is only short in a room nobody has spoken in. Handed a report the
 * rest of the gate uses, or run twice against the same one, the first arm
 * inherits a full transcript and quietly becomes a second long arm - the check
 * stops being able to tell the two apart, which is the whole of its value.
 */
const report = await ask("/api/artifacts", {
  method: "POST",
  body: JSON.stringify({
    type: "report",
    project,
    title: `a document with a conversation ${Date.now()}`,
    body: "# a document\n\nSomething to stand under the discussion pane.",
    visibility: "project",
  }),
});
const reportID = report.id;
const room = `doc-${reportID}`;

const seed = (body) =>
  ask(`/api/chat/${encodeURIComponent(room)}/say`, {
    method: "POST",
    body: JSON.stringify({ body, parents: [] }),
  });

/** roomHolds asks the node whether a body of exactly these words is in the log. */
const roomHolds = async (body) => {
  const page = await ask(`/api/chat/${encodeURIComponent(room)}?since=0`);
  return (page.events ?? []).some((event) => event.body === body);
};

const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1600, height: 1000 } });

/**
 * arm opens the report, sends `line` through the box the way a person does, and
 * fails naming which of the four steps did not happen.
 */
const arm = async (name, line, least = 0) => {
  const page = await context.newPage();
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page
    .goto(`${base}/p/${project}/report/${reportID}`, { timeout: 30_000 })
    .catch((err) => die(`${name}: the report page did not load: ${err}`));

  const box = page.locator('textarea[aria-label="message"]');
  const send = page.locator('form:has(textarea[aria-label="message"]) button[type="submit"]');

  try {
    await box.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    die(`${name}: there is no message box beside the document`);
  }

  // The transcript has to be the length this arm is about, or the arm is not
  // the arm. A long arm that opened before the messages arrived would be a
  // second short arm with a longer name.
  if (least > 0) {
    await page
      .waitForFunction((n) => document.querySelectorAll("aside [data-body]").length >= n, least, {
        timeout: 15_000,
      })
      .catch(() =>
        die(
          `${name}: fewer than ${least} messages were drawn - this arm is about a transcript of that length, and what loaded is not one`,
        ),
      );
  }
  const drawn = await page.locator("aside [data-body]").count();

  // THE STEP THE DEFECT FAILS. Not box.fill(), which sets the value through the
  // DOM and never asks whether anything could reach the element - it passed
  // against the broken console in the shell of this check.
  try {
    await box.click({ timeout: 8_000 });
  } catch (err) {
    const over = await box
      .evaluate((el) => {
        const r = el.getBoundingClientRect();
        const on = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
        if (!on || on === el || el.contains(on)) return "nothing";
        return `${on.tagName.toLowerCase()}.${String(on.className || "").slice(0, 70)}`;
      })
      .catch(() => "(could not be read)");
    die(
      `${name}: the message box cannot be clicked - ${over} is over it.\n` +
        `the box is in the page and a person cannot put a caret in it. (${drawn} messages drawn)\n` +
        `${err}`,
    );
  }

  await page.keyboard.type(line);
  const typed = await box.inputValue();
  if (typed !== line) die(`${name}: typing reached ${JSON.stringify(typed)}, not the box`);

  try {
    await send.click({ timeout: 8_000 });
  } catch (err) {
    die(`${name}: the send button cannot be clicked: ${err}`);
  }

  await page
    .locator("aside")
    .filter({ hasText: line })
    .first()
    .waitFor({ state: "visible", timeout: 10_000 })
    .catch(() => die(`${name}: what was sent never appeared in the transcript`));

  await page
    .waitForFunction(
      () => document.querySelector('textarea[aria-label="message"]')?.value === "",
      null,
      { timeout: 10_000 },
    )
    .catch(() => die(`${name}: the box did not empty, so the send did not complete`));

  if (!(await roomHolds(line))) {
    die(
      `${name}: the node's log for ${room} does not hold what was typed - the console said it did`,
    );
  }

  if (crashes.length > 0) die(`${name}: the page threw: ${crashes.join(" | ")}`);
  console.log(
    `${name}: clicked into the box over ${drawn} messages, typed, sent, and ${room} holds it`,
  );
  await page.close();
};

try {
  const stamp = Date.now();

  // ARM ONE. Two messages: the transcript is shorter than the pane.
  await seed("the survey opens");
  await seed("and somebody answers it");
  await arm(
    "short conversation",
    `a person types into the box with a short transcript ${stamp}`,
    2,
  );

  // ARM TWO. The same room, filled past the height of the pane. Nothing else
  // about the journey changes, so a difference between the arms is the length
  // of the conversation and cannot be anything else.
  for (let i = 0; i < 40; i++) {
    await seed(`filler line ${i} - said so that the transcript is taller than the pane it is in`);
  }
  await arm("long conversation", `a person types into the box with a long transcript ${stamp}`, 30);
} finally {
  await browser.close();
}
