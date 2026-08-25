/**
 * A person who is logged in can say something in a room.
 *
 *   node scripts/person-can-post-check.mjs BASE_URL HANDLE PASSWORD PROJECT
 *
 * THE OPERATOR, on 2026-08-25, having just cleared their token so the project
 * switcher would work: "cant post to chat - that red cirlce pointer". The
 * composer was gated on `!token` - the bearer in localStorage - while `enter`
 * is a session act that a bearer cannot perform. So the console had no state in
 * which both worked:
 *
 *     token only    post yes    switch no
 *     cookie only   post no     switch yes
 *
 * and a person following the console's own instructions to get into a project
 * arrived in it unable to speak. The placeholder even read "paste a token to
 * say something", which is the wrong instruction for somebody who is already
 * signed in.
 *
 * WHY A COOKIE AND NOT A TOKEN. The token arm is covered everywhere else -
 * every other console check pastes one - so a check that pasted a token would
 * have passed against the broken build. The credential IS the defect, which is
 * why this one logs in and NEVER puts anything in localStorage.
 *
 * THE WHOLE JOURNEY, not the handler. Log in, choose a project on the page that
 * offers it, open the room, type, send. `session.tsx` says it already: whoami
 * answering is the console's whole idea of being signed in - so the flow that
 * catches this has to be one where whoami answers and the token does not exist.
 *
 * ASSERTED AT THE NODE. The message is read back through the browser's own
 * cookie, same origin, after the send - a console that draws an enabled box and
 * posts nothing would pass on the screen alone, and that is the likelier of the
 * two bugs.
 */

import { chromium } from "playwright";

const [base, handle, password, project] = process.argv.slice(2);
if (!base || !handle || !password || !project) {
  console.error("usage: node scripts/person-can-post-check.mjs BASE_URL HANDLE PASSWORD PROJECT");
  process.exit(2);
}

const die = (why, shown = "") => {
  console.error(shown ? `${why}\n${shown}` : why);
  process.exit(1);
};

const room = "general";
const mark = `person-can-post ${Date.now().toString(36)}`;

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 950 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  // A PERSON, WITH NO TOKEN ANYWHERE. Nothing below may put one in.
  await page.goto(`${base}/login`, { timeout: 30_000 }).catch(() => {});
  await page.locator("[data-login-form]").waitFor({ state: "visible", timeout: 20_000 });
  await page.locator("[data-login-handle]").fill(handle);
  await page.locator("[data-login-password]").fill(password);
  await page.locator("[data-login-submit]").click();
  await page.waitForURL((url) => url.pathname === "/", { timeout: 15_000 }).catch(() => {});

  const stored = await page.evaluate(() => localStorage.getItem("flowy.token"));
  if (stored) die("this check is meant to run with no bearer token and localStorage holds one");

  // INTO A PROJECT, through the control a person is told to use. A write lands
  // in the principal's home project, so a session with none has nowhere to put
  // a message and the composer would be right to refuse.
  await page.goto(`${base}/projects`, { timeout: 30_000 }).catch(() => {});
  const enter = page.locator(`[data-enter-project="${project}"]`);
  try {
    await enter.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    die(`/projects offers no way into ${JSON.stringify(project)} - the switcher lists only
memberships, so either this person is in no project or the page did not draw them.`);
  }
  if ((await enter.getAttribute("data-enter-current")) !== "yes") {
    await enter.click();
    await page
      .locator(`[data-enter-project="${project}"][data-enter-current="yes"]`)
      .waitFor({ state: "visible", timeout: 15_000 })
      .catch(() => {});
  }

  // THE ROOM, AND THE BOX.
  await page.goto(`${base}/chat/${room}`, { timeout: 30_000 }).catch(() => {});
  const box = page.locator('[aria-label="message"]').first();
  await box.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await box.count()) === 0) die("the room draws no composer at all");
  if (await box.isDisabled()) {
    const placeholder = (await box.getAttribute("placeholder")) ?? "";
    die(`a person who is logged in and in ${JSON.stringify(project)} cannot type in the room.
The box is disabled and offers ${JSON.stringify(placeholder)} - which is the console asking a
signed-in person for a credential they were told not to need. Being somebody is whoami
answering, not a token sitting in localStorage.`);
  }

  await box.fill(mark);
  await box.press("Enter");

  // THE NODE IS THE WITNESS, read through this browser's own cookie.
  let landed = false;
  for (let i = 0; i < 20; i++) {
    const said = await page.evaluate(async (r) => {
      const res = await fetch(`/api/chat/${r}?limit=30`, { credentials: "same-origin" });
      if (!res.ok) return { error: res.status };
      return await res.json();
    }, room);
    const events = said?.events ?? said?.messages ?? [];
    if (Array.isArray(events) && events.some((e) => (e?.body ?? "").includes(mark))) {
      landed = true;
      break;
    }
    await page.waitForTimeout(500);
  }
  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);
  if (!landed) {
    // THE CONSOLE'S OWN REFUSAL, if it has one. Without it the failure reads
    // "nothing arrived", which is the symptom rather than the cause - and the
    // cause is already on the screen, put there by the box for the person who
    // pressed enter. A check that has the answer and prints the symptom wastes
    // the run it took to get it.
    const said = await page
      .locator("[data-send-error]")
      .first()
      .innerText()
      .catch(() => "");
    const shown = said
      ? `the box said: ${JSON.stringify(said)}`
      : "and the box reported no error at all, so the send was believed to have worked";
    die(`the box took the message and the node never got it. The screen may have cleared;
the room did not receive anything.
${shown}`);
  }

  console.log(`a logged-in person posted in ${project}/#${room} with no token anywhere`);
} finally {
  await browser.close();
}
