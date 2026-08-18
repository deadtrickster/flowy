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
  await page.locator("[data-profile-password-input]").fill(password);
  const warning = page.locator("[data-profile-warning]");
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

  // ------------------------------------------------------- and the password works
  const good = await fetch(`${base}/api/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ handle: wanted, password }),
  });
  if (!good.ok) {
    die(`the password set in the panel does not log in: /api/login answered ${good.status}`);
  }
  const bad = await fetch(`${base}/api/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ handle: wanted, password: `${password}-wrong` }),
  });
  if (bad.ok) {
    die(`a WRONG password also logged in (${bad.status}), so the arm above proves nothing
about what the panel wrote`);
  }

  console.log(
    `the profile panel set the handle to ${wanted} and a password that logs in, and refused ${taken}`,
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
