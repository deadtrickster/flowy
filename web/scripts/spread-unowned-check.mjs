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

const die = (message) => {
  console.error(message);
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
  if (made.error) die(`could not write an unowned row: ${made.error}`);

  await page.goto(`${base}/`, { timeout: 30_000 }).catch(() => {});
  const card = page.locator("[data-spread-unowned]");
  await card.waitFor({ state: "visible", timeout: 30_000 }).catch(() => {});
  if (crashes.length > 0) die(`the overview threw: ${crashes.join("; ")}`);
  if ((await card.count()) === 0) {
    die("the spread card draws no unowned slice, with a row nobody is carrying on the board");
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
    die(`the card draws ${seen.drawnUnowned} unowned, the node says ${seen.unowned}`);
  }
  if (seen.seatSum + seen.drawnUnowned !== seen.open) {
    die(
      `seats ${seen.seatSum} + nobody ${seen.drawnUnowned} = ${seen.seatSum + seen.drawnUnowned}, but ${seen.open} rows are open`,
    );
  }

  console.log(
    `the card accounts for every open row: ${seen.seatSum} carried by ${seen.drawnSeats.length} seats, ${seen.drawnUnowned} by nobody, ${seen.open} open`,
  );
} finally {
  await browser.close();
}
