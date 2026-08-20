/**
 * The sidebar says how much is waiting, and its numbers agree with each other.
 *
 *   node scripts/rail-counters-check.mjs BASE_URL TOKEN
 *
 * THE OPERATOR: "our panel switcher on the left looks dead - no counters
 * nothing says how many projects there is or how many reports were filed. or
 * maybe a new one came i didnt read yet". The second sentence is the one this
 * closes: the rail could not say that anything had changed since they last
 * looked.
 *
 * They placed both numbers themselves - "we can have global unread counter at
 * the right of ROOMS", then "closed accordeon tiitlle can have closed counter" -
 * and ruled on the hard question when it came up: "global is global", so the
 * heading counts EVERY room including the ones they have closed.
 *
 * WHAT THIS ASSERTS IS AGREEMENT, not a number. The counts come from one place
 * and are summed twice, so the failure worth catching is not "the total is
 * wrong" - it is the two totals drifting apart from the per-room dots they are
 * built from, which is what happens the day somebody sums the room LIST instead
 * of the counts.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/rail-counters-check.mjs BASE_URL TOKEN");
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
  await page.goto(`${base}/`, { timeout: 30_000 }).catch(() => {});
  await page
    .locator("[data-room-list]")
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if (crashes.length > 0) die(`the shell threw: ${crashes.join("; ")}`);

  // Something unread has to exist, or every assertion below is satisfied by a
  // rail with no numbers on it - which is the state being fixed.
  const said = await page.evaluate(
    async ([t]) => {
      const res = await fetch("/api/chat/general/say", {
        method: "POST",
        headers: { Authorization: `Bearer ${t}`, "Content-Type": "application/json" },
        body: JSON.stringify({ body: "rail-counters-check: something to be unread" }),
      });
      return res.ok ? await res.json() : { error: `${res.status} ${await res.text()}` };
    },
    [token],
  );
  if (said.error) die(`could not write a message to be unread: ${said.error}`);

  await page.goto(`${base}/profile`, { timeout: 30_000 }).catch(() => {});
  await page.goto(`${base}/`, { timeout: 30_000 }).catch(() => {});
  await page
    .locator("[data-rooms-unread]")
    .waitFor({ state: "visible", timeout: 30_000 })
    .catch(() => {});

  const seen = await page.evaluate(() => {
    const num = (el, attr) => (el ? Number(el.getAttribute(attr)) : null);
    // data-unread-count, never the text: the badge caps at "99+" so summing
    // what a person reads would lose everything past a hundred.
    const dots = Array.from(document.querySelectorAll("[data-unread]")).map((el) =>
      Number(el.getAttribute("data-unread-count") ?? 0),
    );
    return {
      total: num(document.querySelector("[data-rooms-unread]"), "data-rooms-unread"),
      closed: num(document.querySelector("[data-closed-unread]"), "data-closed-unread"),
      dots,
      dotSum: dots.reduce((a, b) => a + b, 0),
      hasClosedList: document.querySelectorAll("[data-closed-rooms]").length > 0,
    };
  });

  if (seen.total === null) {
    die("the ROOMS heading carries no unread total, with something unread on the node");
  }
  if (seen.total <= 0) die(`the heading total reads ${seen.total} with something unread`);

  // THE AGREEMENT. Every visible dot is a room in the open list, so the global
  // total must be at least their sum - and strictly more when closed rooms hold
  // unread, which is what "global is global" means.
  if (seen.total < seen.dotSum) {
    die(`the heading says ${seen.total} while the visible dots sum to ${seen.dotSum}`);
  }
  if (seen.closed !== null && seen.total < seen.closed) {
    die(`the heading says ${seen.total} while the closed pile alone says ${seen.closed}`);
  }
  if (seen.closed !== null && seen.dotSum + seen.closed !== seen.total) {
    die(
      `open dots ${seen.dotSum} + closed ${seen.closed} = ${seen.dotSum + seen.closed}, but the heading says ${seen.total}`,
    );
  }

  console.log(
    `rail totals agree: heading ${seen.total}, open dots ${seen.dotSum}, closed ${seen.closed ?? 0}`,
  );
} finally {
  await browser.close();
}
