/**
 * THE WAY IN IS REACHABLE FROM THE PAGE, not only from the URL bar.
 *
 *   node scripts/way-in-check.mjs BASE_URL [TOKEN]
 *
 * The operator, blocked: "fyi, i cant login because there is no login page".
 * There was one - App.tsx has routed /login for as long as Login.tsx has
 * existed, and it renders - and NOTHING IN THE CONSOLE LINKED TO IT. A page
 * reachable only by typing its address is a page the person it was built for
 * does not have.
 *
 * SO THIS CLICKS RATHER THAN NAVIGATES. Asserting that /login renders is what
 * every check already did implicitly and is exactly the claim that stayed true
 * while the operator could not get in: the question is whether somebody looking
 * at the console can FIND it.
 *
 * With no token the console is signed out, which is the state a person arrives
 * in. With one it is a seat, and the same rail must offer the way OUT - being
 * unable to leave is the same defect one step later.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base) {
  console.error("usage: node scripts/way-in-check.mjs BASE_URL [TOKEN]");
  process.exit(2);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  // SIGNED OUT FIRST: no token in storage, which is how a person arrives.
  await page.goto(`${base}/`, { timeout: 20_000 }).catch(() => {});
  await page.locator("[data-token-bar]").waitFor({ state: "visible", timeout: 20_000 });

  const link = page.locator("[data-log-in]");
  if ((await link.count()) === 0) {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    console.error(
      `a signed-out console offers no way to log in: nothing matches [data-log-in].
  /login renders when it is typed, which is what made this invisible.${errors}`,
    );
    process.exit(1);
  }
  await link.first().click();
  await page.waitForURL(/\/login$/, { timeout: 10_000 }).catch(() => {});
  if (!page.url().endsWith("/login")) {
    console.error(`the way-in link went to ${page.url()} rather than to /login`);
    process.exit(1);
  }
  // And the page it reaches is the one that takes a handle, not the token box
  // again - a link that lands somewhere without a password field would satisfy
  // the URL and not the person.
  if ((await page.locator('input[type="password"]').count()) === 0) {
    console.error("the login page has no password field, so the link goes somewhere else");
    process.exit(1);
  }

  if (!token) {
    console.log(
      "a signed-out console offers a way in, and it lands on a page that takes a password",
    );
    process.exit(0);
  }

  // AND THE WAY OUT, for a console that holds a credential.
  const seated = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await seated.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await seated.goto(`${base}/`, { timeout: 20_000 }).catch(() => {});
  await seated.locator("[data-token-bar]").waitFor({ state: "visible", timeout: 20_000 });
  if ((await seated.locator("[data-log-out]").count()) === 0) {
    console.error(
      "a console holding a credential offers no way out: nothing matches [data-log-out]",
    );
    process.exit(1);
  }
  console.log(
    "a signed-out console offers a way in that lands on a password page, and a seated one offers a way out",
  );
} finally {
  await browser.close();
}
