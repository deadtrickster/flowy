/**
 * A person sets their own handle and password from the console, in a real
 * browser, and the node agrees afterwards.
 *
 *   node scripts/profile-check.mjs BASE_URL TOKEN WANTED_HANDLE TAKEN_HANDLE PASSWORD
 *
 * The operator asked for this panel in these words: "not doing that cli
 * commands - im logged in via token - give me profile panel - i will change my
 * password here." So the claims are theirs, in the order they would break:
 *
 *   - the panel renders the handle the node holds, as a value. An empty box
 *     cannot tell "no handle is set" from "not loaded", and those two want
 *     different things from the reader;
 *   - typing a handle and saving changes it AT THE NODE, and the panel shows
 *     the new one without a reload. A page that echoed what it posted would
 *     look identical whether or not the write took;
 *   - a handle somebody else holds is refused in a sentence, and the old one
 *     survives. The store's UNIQUE constraint is what refuses it; this is about
 *     the panel turning that into words rather than a 500;
 *   - and a password set here WORKS, which is two arms and not one: the new
 *     password logs in and a wrong one does not. A panel that says "saved"
 *     while writing nothing passes a one-armed check.
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, token, wanted, taken, password] = process.argv.slice(2);
if (!base || !token || !wanted || !taken || !password) {
  console.error(
    "usage: node scripts/profile-check.mjs BASE_URL TOKEN WANTED_HANDLE TAKEN_HANDLE PASSWORD",
  );
  process.exit(2);
}
if (wanted === taken) {
  console.error(
    `the wanted and taken handles are both ${JSON.stringify(wanted)}, so the refusal arm
could not tell a panel that refuses a duplicate from one that refuses everything`,
  );
  process.exit(2);
}

refuseRemote(base, "profile-check");

const bearer = { Authorization: `Bearer ${token}` };

function die(message, shown) {
  console.error(shown ? `${message}\nThe screen shows:\n${shown}` : message);
  process.exit(1);
}

/** me is what the node says this token's row is, asked fresh every time. */
async function me() {
  const answer = await fetch(`${base}/api/me`, { headers: bearer });
  if (!answer.ok) die(`GET /api/me answered ${answer.status}, so there is nothing to render`);
  return answer.json();
}

const before = await me();
const startingHandle = before.user?.handle ?? "";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  // ------------------------------------------------------------ what it shows
  await page.goto(`${base}/profile`, { timeout: 20_000 }).catch(() => {});
  const current = page.locator("[data-profile-current]");
  try {
    await current.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(`/profile drew nothing with data-profile-current, so the panel never said who
you are before offering to change it.${errors}`);
  }
  const shownHandle = await page
    .locator("[data-profile-handle]")
    .first()
    .getAttribute("data-profile-handle");
  if ((shownHandle ?? "") !== startingHandle) {
    die(
      `the panel shows handle ${JSON.stringify(shownHandle)} and the node holds
${JSON.stringify(startingHandle)}`,
      await current.innerText().catch(() => ""),
    );
  }

  // ------------------------------------------------------------ a taken one
  await page.locator("[data-profile-handle-input]").fill(taken);
  await page.locator("[data-profile-save]").click();
  const refused = page.locator("[data-profile-refused]");
  try {
    await refused.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    die(`saving the handle ${JSON.stringify(taken)}, which another user holds, drew no
refusal - so either it was taken or the panel swallowed the answer`);
  }
  const afterRefusal = await me();
  if ((afterRefusal.user?.handle ?? "") !== startingHandle) {
    die(`a refused save changed the handle at the node to
${JSON.stringify(afterRefusal.user?.handle)} - the refusal was cosmetic`);
  }

  // ------------------------------------------------------------ and a free one
  await page.locator("[data-profile-handle-input]").fill(wanted);
  // TYPED TWICE NOW. The panel refuses a password that is not confirmed, so a
  // check that fills one box is checking that the refusal works, by accident.
  await page.locator("[data-profile-password-input]").fill(password);
  await page.locator("[data-profile-password-again]").fill(password);
  // WAITED FOR, NOT COUNTED. The warning renders on the state change the fill
  // above causes, and React has not necessarily rendered it by the time the
  // fill() promise resolves. Latent here rather than red, and found by sweeping
  // every check in web/scripts for this shape after it cost two gate passes in
  // way-in-check tonight - twice, in one file, twenty lines apart.
  const warning = page.locator("[data-profile-warning]");
  await warning
    .first()
    .waitFor({ state: "visible", timeout: 10_000 })
    .catch(() => {});
  if ((await warning.count()) === 0) {
    die(`typing a new password drew no warning that other sessions end - the panel has to
say what a save costs before the save, not after`);
  }
  await page.locator("[data-profile-save]").click();
  const saved = page.locator("[data-profile-saved]");
  try {
    await saved.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const shownRefusal = await refused.innerText().catch(() => "");
    die(
      `the save drew no confirmation${shownRefusal ? `, and refused with: ${shownRefusal}` : ""}`,
    );
  }

  // The node, not the screen. This is the assertion the screen is evidence for.
  const after = await me();
  if ((after.user?.handle ?? "") !== wanted) {
    die(`the panel said saved and the node still holds ${JSON.stringify(after.user?.handle)}`);
  }
  const redrawn = await page
    .locator("[data-profile-handle]")
    .first()
    .getAttribute("data-profile-handle");
  if ((redrawn ?? "") !== wanted) {
    die(`the node holds ${JSON.stringify(wanted)} and the panel still draws
${JSON.stringify(redrawn)} - it did not re-read after the write`);
  }

  // ------------------------------------------- the password works, and replaces
  //
  // THREE LOGINS, WHICH IS THE WHOLE BUDGET. /api/login is rate limited at three
  // attempts a minute per client (api_join.go, joinBurst) because bcrypt at cost
  // 12 makes guessing expensive for the NODE as well. A fourth arm here answers
  // 429, and a 429 read as a refusal would say the panel broke logging in.
  //
  // So the three are chosen to be the strongest three, and the wrong-password
  // control this used to make is subsumed: the OLD password is a wrong password
  // after the change, and refusing it also proves the panel REPLACED rather than
  // added. A store that accepted everything would still let the first one in.
  // That is flow 4 of 01M0B30FP2 - "a panel that says saved while nothing
  // changed looks identical to one that worked".
  // THE LIMITER IS REAL AND THIS CHECK WAITS FOR IT rather than asking to be
  // scheduled around. /api/login allows three attempts a minute per client
  // (api_join.go, joinBurst) because bcrypt at cost 12 makes guessing expensive
  // for the node as well - and the sign-in check runs just before this one
  // against the same client, so the budget is shared and this check went red on
  // a 429 the first time it ran in a gate.
  //
  // "Space the checks apart" is advice nobody can enforce from inside one of
  // them. Waiting out the window is a minute, once, only when it actually
  // trips, and it makes this check's result depend on the panel rather than on
  // what ran before it.
  //
  // A second 429 after the wait is a real refusal to report: something is
  // spending the budget continuously, and nothing was measured either way.
  const login = async (pw, retried = false) => {
    const answer = await fetch(`${base}/api/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ handle: wanted, password: pw }),
    });
    if (answer.status !== 429) return answer;
    if (retried) {
      die(`/api/login is still rate limiting after a full window (429). Nothing was
measured about the panel - something in this gate is spending three login
attempts a minute continuously.`);
    }
    console.log("  (waiting out the login limiter, 62s)");
    await new Promise((resolve) => setTimeout(resolve, 62000));
    return login(pw, true);
  };

  const good = await login(password);
  if (!good.ok) {
    die(`the password set in the panel does not log in: /api/login answered ${good.status}`);
  }

  // NOW REPLACE IT FROM THE PANEL, and the first one must stop working.
  const second = `${password}-again`;
  await page.locator("[data-profile-password-input]").fill(second);
  await page.locator("[data-profile-password-again]").fill(second);
  await page.locator("[data-profile-save]").click();
  await saved.first().waitFor({ timeout: 10000 });

  const stale = await login(password);
  if (stale.ok) {
    die(`the password the panel replaced STILL LOGS IN (${stale.status}) - the panel said
saved and the old credential outlived it`);
  }
  const fresh = await login(second);
  if (!fresh.ok) {
    die(`the second password the panel set does not log in (${fresh.status}), so the arm
above proves the panel broke logging in rather than replacing a password`);
  }

  // A PASSWORD TYPED WRONG THE SECOND TIME IS NOT SAVED, and this is the arm
  // the panel was missing entirely. The node cannot refuse it - a typo is a
  // valid password - so it would be stored, and found at the next login, which
  // for a person whose handle is also their login name and who has no reset
  // path is a lockout. See routes/Profile.tsx.
  const typo = `${second}-typo`;
  await page.locator("[data-profile-password-input]").fill(typo);
  await page.locator("[data-profile-password-again]").fill(`${typo}X`);
  // SAID WHILE TYPING, before anybody presses save.
  const mismatch = page.locator("[data-profile-password-mismatch]");
  if ((await mismatch.count()) === 0) {
    die("two different passwords are typed and nothing on the page says they differ");
  }
  await page.locator("[data-profile-save]").click();
  await page.waitForTimeout(500);
  const refusedText = await page.locator("[data-profile-refused]").first().textContent();
  if (!refusedText || !/not the same/i.test(refusedText)) {
    die(`a mismatched password was not refused - the panel said ${JSON.stringify(refusedText)}`);
  }
  // AND NOTHING WAS SAVED, which is the assertion that matters: a refusal that
  // still wrote would be worse than no refusal, because the reader believes it.
  const unchanged = await login(second);
  if (!unchanged.ok) {
    die(`the mismatched submit changed the password anyway - ${second} no longer logs in`);
  }

  // THE FLOOR IS ON THE PAGE, not discovered by being refused. Both of the
  // node's numbers were invisible: login.go refuses under 8 and over 72, and
  // nothing in the panel said either until it answered.
  await page.locator("[data-profile-password-input]").fill("short");
  await page.locator("[data-profile-password-again]").fill("short");
  const rule = await page.locator("[data-profile-password-rule]").first().textContent();
  if (!rule || !/8/.test(rule)) {
    die(`the panel does not say how long a password has to be - it reads ${JSON.stringify(rule)}`);
  }

  console.log(
    `the profile panel set the handle to ${wanted}, set a password that logs in, replaced it so the first stopped working, refused ${taken}, refused a mismatch without saving it, and says the floor`,
  );
} finally {
  // PUT THE NAME BACK. This check renames a seat the rest of the gate addresses
  // by name - the room checks say "@$HANDLE_A" and would start failing about
  // something else entirely. The restore is in `finally` rather than at the end
  // of the happy path, because a check that leaves the fixture changed when it
  // fails is a check that turns one red into several.
  if (startingHandle) {
    await fetch(`${base}/api/me`, {
      method: "PUT",
      headers: { ...bearer, "Content-Type": "application/json" },
      body: JSON.stringify({ handle: startingHandle }),
    }).catch(() => {});
  }
  await browser.close();
}
