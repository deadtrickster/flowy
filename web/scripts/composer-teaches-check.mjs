/**
 * THE COMPOSER ADVERTISES ONLY WHAT IT DOES.
 *
 *   node scripts/composer-teaches-check.mjs BASE_URL TOKEN [ROOM]
 *
 * UI row 01M173AT9V2PYK7XHG4GZDD1MJ, item 7: their composer teaches in the
 * placeholder - "@ for files/agents; / for commands and skills; ! for shell;
 * # for snippets" - and ours said "say something...".
 *
 * @ is not decoration here. mentions.go resolves a name AT WRITE TIME and
 * records the resolved pairs in meta.mentions, so a seat that was not named
 * with an @ is not notified: the affordance that decides whether a message
 * reaches anybody was the one nothing on screen named.
 *
 * WHAT THIS ASSERTS IS THE BOND, not the sentence. Checking that the
 * placeholder contains "@" is a string assertion - it passes just as well on a
 * placeholder that promises "/ for commands" to a composer with no commands,
 * which is a lie the reader only discovers by typing. So the check reads the
 * SYMBOLS OUT OF THE PLACEHOLDER, types each one, and requires the composer to
 * answer. Advertising something that does nothing fails here, and so does
 * teaching nothing at all.
 */

import { chromium } from "playwright";

const [base, token, room = "general"] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/composer-teaches-check.mjs BASE_URL TOKEN [ROOM]");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

// What each advertisable symbol must make the composer do. A symbol named in
// the placeholder with no entry here is an advertisement nobody can verify,
// and that is a failure too - it is how "/ for commands" would get in.
const ANSWERS = {
  "@": "[data-at-suggestions]",
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 950 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 }).catch(() => {});

  const box = page.locator('textarea[aria-label="message"]');
  await box.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await box.count()) === 0) die("the room drew no composer");
  if (crashes.length > 0) die(`the room threw: ${crashes.join("; ")}`);

  const placeholder = (await box.first().getAttribute("placeholder")) ?? "";
  if (!placeholder) die("the composer has no placeholder at all");

  // Signed in, the composer must not be sitting in its disabled copy - that
  // text teaches how to log in, which is a different claim than this one.
  if (/log in|paste a token/i.test(placeholder)) {
    die(`the composer is in its signed-out state (${JSON.stringify(placeholder)}), so this run
never saw the placeholder it is meant to judge`);
  }

  const advertised = Object.keys(ANSWERS).filter((symbol) => placeholder.includes(symbol));
  const unknown = [..."@/!#$"].filter(
    (symbol) => placeholder.includes(symbol) && !(symbol in ANSWERS),
  );
  if (unknown.length > 0) {
    die(`the placeholder advertises ${unknown.join(" ")} and this check has no way to prove any of
them does anything. Either the composer grew a feature and this list needs it, or the placeholder
promises something that is not there - and the reader finds that out by typing.`);
  }
  if (advertised.length === 0) {
    die(`the placeholder ${JSON.stringify(placeholder)} teaches no symbol. @ decides whether a
message reaches a seat at all, and nothing on screen says so.`);
  }

  for (const symbol of advertised) {
    await box.first().fill("");
    await box.first().click();
    await box.first().type(symbol, { delay: 40 });
    const answer = page.locator(ANSWERS[symbol]);
    await answer.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
    if ((await answer.count()) === 0) {
      die(`the placeholder advertises ${JSON.stringify(symbol)} and typing it does nothing -
${ANSWERS[symbol]} never appeared. A composer that teaches a gesture it does not have sends the
reader to find that out by hand.`);
    }
  }

  console.log(
    `the composer teaches ${advertised.join(" ")} and each one answers: ${JSON.stringify(placeholder)}`,
  );
} finally {
  await browser.close();
}
