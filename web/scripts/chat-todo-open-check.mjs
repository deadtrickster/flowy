/**
 * A todo raised in a room can be read there, and reached from there.
 *
 *   node scripts/chat-todo-open-check.mjs BASE_URL TOKEN ROOM
 *
 * THE OPERATOR, TWICE: "no way to go from chat todo to full todo card", then
 * "i want a quick view for chat todo when I click on it - quick summary card +
 * link to the full todo card".
 *
 * The panel beside a conversation had the id, the title, the status and the
 * assignee, and nothing else - so work raised out of a conversation was
 * readable there and nowhere else short of typing an id into a URL.
 *
 * The last arm is the one that matters and the easy one to leave out: the link
 * has to POINT AT THE ROW IT IS UNDER. A summary that opens somebody else's
 * todo is worse than no link, and it is exactly what a list-index bug produces.
 */

import { chromium } from "playwright";

const [base, token, room = "general"] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/chat-todo-open-check.mjs BASE_URL TOKEN [ROOM]");
  process.exit(2);
}

// die CLEARS BEFORE IT EXITS, and every call site awaits it.
//
// process.exit does not run a finally block, so the cleanup below would be
// skipped on exactly the runs that leave the most behind: a failing one, which
// is when somebody is looking at the board and when the check is most likely to
// be run again. It is async for that reason - an unawaited die would let the
// line after it run, which is the bug this shape usually has.
const die = async (message) => {
  console.error(message);
  await clearRaised();
  process.exit(1);
};

const BODY = "chat-todo-open-check: the body only the summary can show";

// THE ROWS THIS CHECK RAISES, so it can take them off the board again.
//
// On the gate this is free: the suite builds a database per run and throws it
// away, so a fixture dies with the run that made it. Against a LIVE node - which
// is how these checks get written and how a seat verifies a landing - the rows
// stay, unowned and open, and the board nag counts them as work waiting at every
// seat. Measured 2026-08-21: three of the twenty unowned rows on the dogfood
// board were fixtures from this file and from spread-unowned-check, one of them
// sitting there for six hours.
//
// BY ID, not by title: two runs raise two rows with the same words, and a
// cleanup that matched on the title would close somebody else's - including the
// one the other run is still looking at.
const raised = [];
const clearRaised = async () => {
  for (const id of raised) {
    await fetch(`${base}/api/artifact/${encodeURIComponent(id)}/status`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: JSON.stringify({
        status: "done",
        // A NOTE, BECAUSE THE NODE REFUSES A CLOSE WITHOUT ONE: "a row closed
        // with nothing said reads in a week exactly like one closed with a
        // measurement". The first version of this cleanup sent none, got a 400,
        // and left the row on the board - see the reporting below for why that
        // was silent.
        note: "closed by chat-todo-open-check: a fixture this check raised, cleaned up so it is not counted as work waiting",
      }),
    })
      .then((res) => {
        // FETCH DOES NOT THROW ON A 4xx, which is how the first draft reported
        // success while leaving the row it was written to remove. A cleanup
        // that fails quietly is worse than no cleanup: it makes the board look
        // tidy in the one place somebody would check.
        if (!res.ok) {
          console.error(`could not clear the fixture ${id}: ${res.status}`);
        }
      })
      .catch((err) => {
        console.error(`could not clear the fixture ${id}: ${err}`);
      });
  }
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 30_000 }).catch(() => {});

  // TWO rows, not one. With a single row a link that always points at the first
  // todo in the list passes, and that is the bug this check exists for.
  const made = await page.evaluate(
    async ([t, r, body]) => {
      const raise = async (title, withBody) => {
        const res = await fetch("/api/artifacts", {
          method: "POST",
          headers: { Authorization: `Bearer ${t}`, "Content-Type": "application/json" },
          body: JSON.stringify({
            type: "memory",
            kind: "todo",
            title,
            body: withBody,
            fields: { room: r },
          }),
        });
        if (!res.ok) return { error: `${res.status} ${await res.text()}` };
        return await res.json();
      };
      const first = await raise("chat-todo-open-check: the decoy", "not this one");
      if (first.error) return first;
      const second = await raise("chat-todo-open-check: the one to open", body);
      return second.error ? second : { first, second };
    },
    [token, room, BODY],
  );
  if (made.error) await die(`could not raise the rows: ${made.error}`);
  const { first, second } = made;
  raised.push(first.id, second.id);

  await page.goto(`${base}/chat/${room}`, { timeout: 30_000 }).catch(() => {});
  const opener = page.locator(`[data-todo-open="${second.id}"]`);
  await opener.waitFor({ state: "visible", timeout: 30_000 }).catch(() => {});
  if (crashes.length > 0) await die(`the room threw: ${crashes.join("; ")}`);
  if ((await opener.count()) === 0) {
    await die("the row just raised is not openable in the room's todo panel");
  }

  if ((await page.locator("[data-todo-summary]").count()) !== 0) {
    await die("a summary was open before anything was clicked");
  }
  await opener.click();

  const summary = page.locator(`[data-todo-summary="${second.id}"]`);
  await summary.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
  if ((await summary.count()) === 0) await die("clicking the title opened no summary");

  const shown = await summary.textContent();
  if (!shown?.includes(BODY)) {
    await die(`the summary does not show the row's body: ${JSON.stringify(shown?.slice(0, 120))}`);
  }

  // AND THE LINK POINTS AT THIS ROW. Scoped to the open summary, because the
  // panel may hold many and an unscoped selector would pass on the wrong one.
  const href = await summary.locator("[data-todo-full-card]").getAttribute("href");
  if (!href?.includes(second.id)) {
    await die(`the full-card link points at ${href}, not at ${second.id}`);
  }

  console.log(`a chat todo opens where it sits, shows its body, and links to ${second.id}`);
} finally {
  // IN THE FINALLY, so a check that fails still clears its fixtures. A failing
  // run is exactly when somebody is looking at the board, and it is the run
  // most likely to be repeated.
  await clearRaised();
  await browser.close();
}
