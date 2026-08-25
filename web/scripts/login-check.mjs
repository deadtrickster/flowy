/**
 * A person logs in, in a browser, and the console believes the node.
 *
 *   node scripts/login-check.mjs BASE_URL HANDLE PASSWORD [TOKEN]
 *
 * The operator's words were "i dont want to bother with token. token is for
 * api, not for me". The node answers a session cookie now; this is the screen
 * that gets one, and the check drives the journey a person takes rather than
 * the handler underneath it.
 *
 * THE COOKIE IS httpOnly, WHICH IS THE WHOLE DESIGN. Nothing in the console can
 * read it and nothing stores it, so "am I signed in" is /api/whoami answering -
 * and that is what makes the second arm below the discriminating one: the
 * browser is reloaded with NO token in localStorage and has to still be
 * somebody. A console that kept its own flag would pass every click-through
 * and fail that reload.
 *
 * The refusal is asserted as the NODE'S sentence, not a phrase composed here.
 * It says one thing for a wrong handle and a wrong password on purpose - which
 * of the two was wrong is an oracle for which accounts exist.
 */

import { chromium } from "playwright";

const [base, handle, password, token] = process.argv.slice(2);
if (!base || !handle || !password) {
  console.error("usage: node scripts/login-check.mjs BASE_URL HANDLE PASSWORD [TOKEN]");
  process.exit(2);
}

const die = (why) => {
  console.error(why);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1200, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  // NO TOKEN ANYWHERE. This is a person opening the console for the first time,
  // which is the state the whole row is about.
  await page.goto(`${base}/login`, { timeout: 30_000 }).catch(() => {});
  const form = page.locator("[data-login-form]");
  try {
    await form.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(`/login draws no form at all.${errors}`);
  }

  // 1. THE WRONG PASSWORD IS REFUSED, in the node's words.
  await page.locator("[data-login-handle]").fill(handle);
  await page.locator("[data-login-password]").fill(`${password}-wrong`);
  await page.locator("[data-login-submit]").click();
  const refused = page.locator("[data-login-refused]");
  try {
    await refused.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    die("a wrong password was not refused on the page - the form said nothing at all");
  }
  const said = ((await refused.innerText()) || "").toLowerCase();
  if (!said.includes("password")) {
    die(`the refusal reads ${JSON.stringify(said)}, which does not tell the person what failed`);
  }
  if (said.includes("no such") || said.includes("unknown handle")) {
    die(`the refusal reads ${JSON.stringify(said)} - it says which of the two was wrong, which is
an oracle for which accounts exist`);
  }

  // 2. THE RIGHT ONE GETS IN, and the console says who the NODE says you are.
  await page.locator("[data-login-password]").fill(password);
  await page.locator("[data-login-submit]").click();
  await page.waitForURL((url) => url.pathname === "/", { timeout: 15_000 }).catch(() => {});
  await page.goto(`${base}/login`, { timeout: 20_000 }).catch(() => {});
  const who = page.locator("[data-login-as]");
  try {
    await who.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    die(
      "logging in with the right password left the page still offering a form and nobody signed in",
    );
  }

  // 3. AND IT SURVIVES A RELOAD WITH NOTHING STORED. The cookie is the only
  // credential this browser has: localStorage is emptied first, so a console
  // keeping its own idea of signed-in fails here and only here.
  await page.evaluate(() => localStorage.clear());
  await page.reload({ timeout: 20_000 });
  try {
    await page.locator("[data-login-as]").waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    die(`after a reload with an empty localStorage the console does not know who it is - the
session cookie is the credential and something here is reading a stored flag instead`);
  }

  // 4. AND LOGGING OUT ENDS IT AT THE NODE. The form comes back, which is the
  // console asking whoami again rather than forgetting locally.
  await page.locator("[data-logout]").click();
  try {
    await page.locator("[data-login-as]").waitFor({ state: "detached", timeout: 15_000 });
  } catch {
    die("after logging out the page still says somebody is signed in");
  }

  // 5. AND LOGGING OUT WITH A TOKEN IN HAND CLEARS THAT TOO.
  //
  // The operator, 2026-08-25: "logout doesnt work and i cant clean cache on
  // android". Step 4 above passed throughout, because it never stores a token -
  // it logs in with a password and out again, and the only credential in play
  // is the cookie the button was already ending. A browser holding BOTH signed
  // out of the half it was not using and stayed signed in as the bearer, since
  // authenticate() tries the bearer first.
  //
  // On a desktop that is recoverable by clearing localStorage by hand. On a
  // phone it is not reachable at all, so the only signed-out state available
  // was one this console would not produce.
  //
  // This arm is the difference between the two, and it is why the whole check
  // now takes a token: an assertion about logging out that never logs in the
  // other way is an assertion about one of the two doors.
  if (token) {
    await page.goto(`${base}/login`, { timeout: 20_000 }).catch(() => {});
    await page.evaluate((t) => localStorage.setItem("flowy.token", t), token);
    await page.reload({ timeout: 20_000 });
    try {
      await page.locator("[data-login-as]").waitFor({ state: "visible", timeout: 15_000 });
    } catch {
      die(
        "a pasted token did not sign the console in, so the logout arm below would prove nothing",
      );
    }
    await page.locator("[data-logout]").click();
    // WAITED FOR, NOT SAMPLED ONCE. logOut is async - it ends the session at the
    // node and clears the token after that await resolves - so reading
    // localStorage in the same tick as the click reads before the work is done.
    // Measured: this arm failed against the FIXED build for that reason alone.
    // A timeout keeps it honest: on the broken build the token never goes, so
    // this still fails there, it just stops failing on the correct one.
    let left = null;
    for (let i = 0; i < 20; i++) {
      left = await page.evaluate(() => localStorage.getItem("flowy.token")).catch(() => null);
      if (!left) break;
      await page.waitForTimeout(250);
    }
    if (left) {
      die(`logging out left the bearer token in localStorage.
The session ended at the node and the console is still signed in as the token, because
authenticate() tries the bearer first - so the button appears to do nothing. There is no
way to clear it on a phone, which is where this was reported.`);
    }
    try {
      await page.locator("[data-login-as]").waitFor({ state: "detached", timeout: 15_000 });
    } catch {
      die("after logging out with a token in hand the page still says somebody is signed in");
    }
  }

  if (crashes.length) die(`the console threw during the login journey: ${crashes.join("; ")}`);
  console.log(
    "a person logs in: a wrong password is refused in the node's words, the right one signs in, it survives a reload with nothing stored, logging out ends it, and logging out with a token in hand clears that too",
  );
} finally {
  await browser.close();
}
