/**
 * The spread card shows the work nobody is carrying.
 *
 *   node scripts/spread-unowned-check.mjs BASE_URL TOKEN
 *
 * THE OPERATOR, LOOKING AT THE CARD: "why it doesnt sum up to 100%?"
 *
 * Every percentage was correct. Each is a seat's share of ALL open rows, and
 * the bars summed to about half because the list draws `shares` - which is per
 * assignee - and nothing drew the unowned rows that were the rest. Measured at
 * the time: open 35, unowned 17, four seats holding 18 between them.
 *
 * SO THIS ASSERTS THE SUM, and it is the one assertion that would have caught
 * the original: every drawn slice, seats plus nobody, accounts for every open
 * row. It also guards the tempting wrong fix - renormalising over the assigned
 * rows so the bars add up, which doubles every seat and deletes the fact the
 * operator was asking about.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/spread-unowned-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

// THE ROW THIS CHECK RAISES, so it can take it off the board again.
//
// Its fixture is the sharpest case of the whole class: a row that must be
// UNOWNED for the check to mean anything, so once left behind it cannot be
// cleared by anybody doing any work. One sat on the dogfood board for six hours
// and the nag - which wakes on the unowned pile - counted it at every seat the
// whole time.
//
// By id and not by title, because two runs raise two rows with the same words.
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
        note: "closed by spread-unowned-check: a fixture this check raised, cleaned up so it is not counted as work waiting",
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

// die CLEARS BEFORE IT EXITS, and every call site awaits it: process.exit does
// not run a finally block, so without this the runs that leave a fixture behind
// are exactly the failing ones - which is when somebody is looking at the board.
const die = async (message) => {
  console.error(message);
  await clearRaised();
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/`, { timeout: 30_000 }).catch(() => {});

  // A row nobody is carrying, written here so the check does not depend on the
  // board happening to have one. A card with nothing unowned satisfies every
  // assertion below by drawing no slice - which is the state being fixed.
  const made = await page.evaluate(
    async ([t]) => {
      const res = await fetch("/api/artifacts", {
        method: "POST",
        headers: { Authorization: `Bearer ${t}`, "Content-Type": "application/json" },
        body: JSON.stringify({
          type: "memory",
          kind: "todo",
          title: "spread-unowned-check: a row nobody is carrying",
          body: "written by the check that counts it",
        }),
      });
      if (!res.ok) return { error: `${res.status} ${await res.text()}` };
      return await res.json();
    },
    [token],
  );
  if (made.error) await die(`could not write an unowned row: ${made.error}`);
  raised.push(made.id);

  await page.goto(`${base}/`, { timeout: 30_000 }).catch(() => {});
  const card = page.locator("[data-spread-unowned]");
  await card.waitFor({ state: "visible", timeout: 30_000 }).catch(() => {});
  if (crashes.length > 0) await die(`the overview threw: ${crashes.join("; ")}`);
  if ((await card.count()) === 0) {
    await die("the spread card draws no unowned slice, with a row nobody is carrying on the board");
  }

  // THE SUM. Seats plus nobody must account for every open row, which is what
  // "why doesn't it add up" was asking.
  const seen = await page.evaluate(() =>
    fetch("/api/nag", {
      headers: { Authorization: `Bearer ${localStorage.getItem("flowy.token")}` },
    })
      .then((r) => r.json())
      .then((nag) => {
        const w = nag.workload ?? {};
        const drawnSeats = Array.from(document.querySelectorAll("[data-spread-open]")).map((el) =>
          Number(el.getAttribute("data-spread-open")),
        );
        const drawnUnowned = Number(
          document.querySelector("[data-spread-unowned]")?.getAttribute("data-spread-unowned") ?? 0,
        );
        return {
          open: w.open,
          unowned: w.unowned,
          drawnSeats,
          seatSum: drawnSeats.reduce((a, b) => a + b, 0),
          drawnUnowned,
        };
      }),
  );

  if (seen.drawnUnowned !== seen.unowned) {
    await die(`the card draws ${seen.drawnUnowned} unowned, the node says ${seen.unowned}`);
  }
  if (seen.seatSum + seen.drawnUnowned !== seen.open) {
    await die(
      `seats ${seen.seatSum} + nobody ${seen.drawnUnowned} = ${seen.seatSum + seen.drawnUnowned}, but ${seen.open} rows are open`,
    );
  }

  console.log(
    `the card accounts for every open row: ${seen.seatSum} carried by ${seen.drawnSeats.length} seats, ${seen.drawnUnowned} by nobody, ${seen.open} open`,
  );
} finally {
  // In the finally as well as in die: a crash that is neither is still a run
  // that raised a row.
  await clearRaised();
  await browser.close();
}
