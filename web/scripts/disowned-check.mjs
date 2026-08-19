/**
 * A disowned message reads as disowned, and the one beside it does not.
 *
 *   node scripts/disowned-check.mjs BASE_URL TOKEN ROOM DISOWNED_EVENT OTHER_EVENT
 *
 * The store half of repudiation landed first and nothing drew it, so a row
 * whose author had taken it back rendered exactly like one nobody had touched.
 * That is the gap the whole feature exists to close: the person who was
 * impersonated is the reader, and an API field they cannot see is not a
 * disclosure.
 *
 * BOTH ARMS ARE ON ONE PAGE, deliberately. A mark that draws on every message
 * is indistinguishable from a mark that works, from one screenshot - so the
 * control is not a second run against a clean room, it is the message NEXT TO
 * IT: the same speaker's earlier line, written BEFORE the window, which must
 * render unmarked. That arm fails loudly for a surface that draws the mark on
 * everything, and it exercises the window boundary on the drawn surface rather
 * than only in the store.
 *
 * The other control - another PRINCIPAL's row inside the same window - is
 * asserted in internal/store (TestTheMarkIsPutOnTheSubjectsRowsAndNobodyElses)
 * rather than here, because on a page it turns into a question about who may
 * read whose project and a failure would not say which of the two it was. Each
 * arm is measured where it is unambiguous.
 *
 * ASSERTED ON ELEMENTS, never on page text. The word "disowned" appears in the
 * repudiation's own title, in its body, and in whatever anybody happens to be
 * saying about it in the room - so `page has the word` passes with nothing
 * drawn. Each claim below reads a data attribute keyed by the event id.
 */

import { chromium } from "playwright";

const [base, token, room, disownedId, otherId] = process.argv.slice(2);
if (!base || !token || !room || !disownedId || !otherId) {
  console.error(
    "usage: node scripts/disowned-check.mjs BASE_URL TOKEN ROOM DISOWNED_EVENT OTHER_EVENT",
  );
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 }).catch(() => {});

  // THE WITNESS THAT THIS RUN MEASURED ANYTHING. A room that never painted
  // answers "not marked" to every question below, which is also what a
  // completely broken mark answers.
  const both = page.locator("[data-reply]");
  await both
    .first()
    .waitFor({ timeout: 20_000 })
    .catch(() => {});
  const messages = await both.count();
  if (messages < 2) {
    die(`${room} painted ${messages} messages - the check needs the disowned one and its control`);
  }

  // 1. THE DISOWNED LINE SAYS SO.
  const marked = page.locator(`[data-disowned="${disownedId}"]`);
  await marked
    .first()
    .waitFor({ timeout: 10_000 })
    .catch(() => {});
  if ((await marked.count()) === 0) {
    die(
      `message ${disownedId} is disowned by its speaker and the room draws nothing - the mark is in the API and not on the screen, which is where it has to be`,
    );
  }

  // 2. AND THE ONE BESIDE IT DOES NOT. Same speaker, same room, written before
  // the window opens - so this fails for any surface that draws the mark on
  // every message, which is the failure a single screenshot cannot tell from
  // success.
  const control = page.locator(`[data-disowned="${otherId}"]`);
  if ((await control.count()) > 0) {
    die(
      `message ${otherId} was written before the disowned window and is drawn as disowned - the mark is on messages the repudiation does not cover`,
    );
  }

  // 3. AND THE MARK DOES NOT REPLACE WHAT IT QUALIFIES. The signature really
  // did verify; the person whose key made it says the key was not theirs. A
  // surface that swapped one for the other loses the difference between a
  // stolen key and a forgery, so both readings have to survive on the row.
  const row = page.locator(`[data-reply="${disownedId}"]`);
  if ((await row.count()) > 0) {
    const said = ((await row.first().textContent()) || "").toLowerCase();
    if (!said.includes("signed") && !said.includes("attributed")) {
      die(
        `the disowned message shows the mark and no authorship reading - "authored, and its author disowns it" is the sentence, and half of it is missing`,
      );
    }
  }

  if (crashes.length) die(`the room threw while drawing the mark: ${crashes[0]}`);
  console.log(
    `${disownedId} draws as disowned, ${otherId} in the same window does not, across ${messages} messages`,
  );
} finally {
  await browser.close();
}
