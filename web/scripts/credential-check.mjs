/**
 * A dead credential says so, instead of drawing an empty console.
 *
 *   node scripts/credential-check.mjs BASE_URL GOOD_TOKEN
 *
 * 01M0K76WY4. The operator reported "ui stopped working". It was working: every
 * read was answering 401, every pane drew its own empty state, and the frame
 * around them looked exactly as it always does. Nothing anywhere said why.
 *
 * WHY NO AGENT COULD SEE IT. Every check in this suite authenticates with a
 * bearer token; a person's browser authenticates with a session. perm.go scopes
 * those two differently, so a session failure is invisible to a token test - it
 * took four agents an hour and a read of the sessions table to find, and the
 * table is not something the console can consult.
 *
 * THE FIXTURE IS THE POINT AND IT IS FREE: a credential the node rejects. This
 * check does not need a broken session, only a broken CREDENTIAL, which is one
 * string. 401 is 401 whether the cookie was swept, the session expired or the
 * token is nonsense.
 *
 * TWO ARMS, because one would pass on a console that showed the banner
 * permanently - which is the obvious wrong implementation and worse than the
 * bug, since a warning that is always on is one people stop reading.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/credential-check.mjs BASE_URL GOOD_TOKEN");
  process.exit(2);
}

const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const look = async (credential) => {
    const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
    const crashes = [];
    page.on("pageerror", (err) => crashes.push(String(err)));
    await page.addInitScript((t) => localStorage.setItem("flowy.token", t), credential);
    await page.goto(`${base}/memory`, { timeout: 30_000 }).catch(() => {});
    // Long enough for a read to come back and be noticed. The banner appears on
    // the FIRST 401, not on a timer, so this is not a race against a poll.
    await page.waitForTimeout(3000);
    const banner = page.locator("[data-credential-dead]");
    const got = {
      shown: (await banner.count()) > 0,
      words: (
        (await banner
          .first()
          .textContent()
          .catch(() => "")) ?? ""
      )
        .replace(/\s+/g, " ")
        .trim(),
      links: await page.locator('[data-credential-dead] a[href="/login"]').count(),
      crashes,
    };
    await page.close();
    return got;
  };

  // ARM ONE: a credential the node rejects. This is what the operator had.
  const dead = await look("not-a-real-credential-9f3");
  if (dead.crashes.length > 0)
    die(`the page threw with a dead credential: ${dead.crashes.join("; ")}`);
  if (!dead.shown) {
    die(`every read answered 401 and the console said nothing about it.
That is what the operator saw: the frame renders, every pane is empty, and the
page is indistinguishable from a node that lost its data. EMPTY, FORBIDDEN and
UNREACHABLE are three different answers and a blank page is how they become one.`);
  }
  if (dead.links === 0) {
    die(`the banner says the credential is dead and offers no way to sign in: ${JSON.stringify(dead.words)}
A message a person cannot act on is a better blank page, not a fix.`);
  }

  // ARM TWO: a credential that works. A banner that is always up is worse than
  // the bug - a warning nobody can clear is one everybody learns to ignore,
  // including the time it is right.
  const alive = await look(token);
  if (alive.crashes.length > 0)
    die(`the page threw with a good credential: ${alive.crashes.join("; ")}`);
  if (alive.shown) {
    die(`the banner is shown to somebody whose credential works: ${JSON.stringify(alive.words)}
It must clear on any read that succeeds.`);
  }

  console.log(`dead credential says so ("${dead.words.slice(0, 60)}…"), working one does not`);
} finally {
  await browser.close();
}
