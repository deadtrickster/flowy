/**
 * A person who is logged in sees the console, not the signed-out screen.
 *
 *   node scripts/person-sees-the-console-check.mjs BASE_URL HANDLE PASSWORD PROJECT
 *
 * 01M0W7BADY. Twenty-five files asked `const { token } = useSession()` and read
 * it as "is anybody signed in". The token is the BEARER out of localStorage,
 * and since cookies landed it is one of two ways to be somebody - so a person
 * holding a session got the locked-shelf screen on page after page while being
 * perfectly well signed in. The operator found the first one as a chat box that
 * would not take typing; these are the other twenty-four.
 *
 * THE ASSERTION IS THE SENTENCE, NOT THE CONTENT. An empty board and a board
 * you may not see look identical if you count rows, which is why these pages
 * say which empty they are - "log in, or paste a token, to read the queue -
 * signed out, there is no reader to scope it to". That sentence is exactly
 * right for somebody signed out and exactly wrong for a person with a session,
 * so its ABSENCE while signed in is the fact this checks. A page with no rows
 * still passes; a page telling a logged-in person to log in does not.
 *
 * AND THE HEADING, so "no sentence" cannot be satisfied by a page that failed
 * to render at all. Absence of the wrong words is not presence of the right
 * page, and a blank screen would otherwise pass every arm of this.
 *
 * NO TOKEN IS EVER STORED. That is the whole point: every other console check
 * pastes one, which is the arm that always worked, which is why this survived
 * 773 green checks.
 */

import { chromium } from "playwright";

const [base, handle, password, project] = process.argv.slice(2);
if (!base || !handle || !password || !project) {
  console.error(
    "usage: node scripts/person-sees-the-console-check.mjs BASE_URL HANDLE PASSWORD PROJECT",
  );
  process.exit(2);
}

// THE SUBSTRING BOTH WORDINGS SHARE, and that is not a detail.
//
// This first read "log in, or paste a token" - the sentence as it is worded
// TODAY. The negative control then passed: reverting one page to the old gate
// also reverted its copy to "paste a token to see your memories", which does
// not contain the phrase, so the check looked straight past exactly the defect
// it exists for. A check keyed to the current wording of a sentence tests the
// wording, not the state.
//
// "paste a token" is in both, and in any future rewording that still tells
// somebody to supply a credential - which IS the fact being asserted. It is not
// in TokenBar, whose only use of the words is a placeholder attribute and not
// innerText.
const SIGNED_OUT = "paste a token";

/** The pages a person opens, and the heading each must draw. */
const PAGES = [
  { path: "/todos", heading: "todos" },
  { path: "/memory", heading: "memory" },
  { path: "/inbox", heading: "inbox" },
  { path: "/activity", heading: "activity" },
  { path: "/diagrams", heading: "diagrams" },
  { path: "/worklog", heading: "worklog" },
];

const die = (why) => {
  console.error(why);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 950 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  await page.goto(`${base}/login`, { timeout: 30_000 }).catch(() => {});
  await page.locator("[data-login-form]").waitFor({ state: "visible", timeout: 20_000 });
  await page.locator("[data-login-handle]").fill(handle);
  await page.locator("[data-login-password]").fill(password);
  await page.locator("[data-login-submit]").click();
  await page.waitForURL((url) => url.pathname === "/", { timeout: 15_000 }).catch(() => {});

  // DID THE LOGIN ACTUALLY WORK. Asked of the node, and it is not a formality.
  //
  // In the full suite this check failed with "/projects offers no way into pa",
  // which reads as a broken switcher and was nothing of the sort: /api/login
  // rate limits, several checks in this gate log in, and mine was refused. The
  // page then sat signed out, whoami had no memberships, and the switcher drew
  // nothing - a true statement about a person who is not logged in, reported as
  // a defect in the page.
  //
  // Everything below this line is about what a SIGNED-IN person sees, so a
  // failure to sign in has to stop here and say so in its own words rather than
  // become a confusing failure three steps later.
  const signedInAs = async () => {
    return await page.evaluate(async () => {
      const res = await fetch("/api/whoami", { credentials: "same-origin" });
      if (!res.ok) return "";
      const who = await res.json();
      return who?.user ?? "";
    });
  };
  if (!(await signedInAs())) {
    const refused = await page
      .locator("[data-login-refused]")
      .first()
      .innerText()
      .catch(() => "");
    // The limiter is a minute wide and other checks in this gate spend it, so
    // one wait is worth it before calling the run lost. See profile-check.
    if (/rate|429|too many/i.test(refused)) {
      console.log("  (waiting out the login limiter, 62s)");
      await page.waitForTimeout(62_000);
      await page.locator("[data-login-password]").fill(password);
      await page.locator("[data-login-submit]").click();
      await page.waitForURL((url) => url.pathname === "/", { timeout: 15_000 }).catch(() => {});
    }
    if (!(await signedInAs())) {
      die(`could not log ${handle} in, so nothing below was measured.
The page said ${JSON.stringify(refused || "nothing at all")}. This is not a finding about
any console page - it is this check failing to reach the state it tests.`);
    }
  }

  const stored = await page.evaluate(() => localStorage.getItem("flowy.token"));
  if (stored) die("this check is meant to run with no bearer token and localStorage holds one");

  // Into a project, through the control a person is told to use. Some of these
  // pages are scoped to where you write, so a session in no project would be a
  // second, unrelated reason for a thin page.
  await page.goto(`${base}/projects`, { timeout: 30_000 }).catch(() => {});
  const enter = page.locator(`[data-enter-project="${project}"]`);
  await enter.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await enter.count()) === 0) die(`/projects offers no way into ${JSON.stringify(project)}`);
  if ((await enter.getAttribute("data-enter-current")) !== "yes") {
    await enter.click();
    await page
      .locator(`[data-enter-project="${project}"][data-enter-current="yes"]`)
      .waitFor({ state: "visible", timeout: 15_000 })
      .catch(() => {});
  }

  const locked = [];
  const blank = [];
  for (const { path, heading } of PAGES) {
    await page.goto(`${base}${path}`, { timeout: 30_000 }).catch(() => {});
    await page
      .locator("h1", { hasText: heading })
      .first()
      .waitFor({ state: "visible", timeout: 20_000 })
      .catch(() => {});
    const drew = await page.locator("h1", { hasText: heading }).count();
    if (drew === 0) {
      blank.push(path);
      continue;
    }
    const body =
      (await page
        .locator("body")
        .innerText()
        .catch(() => "")) || "";
    if (body.includes(SIGNED_OUT)) locked.push(path);
  }

  // A PAGE THAT SAYS NOTHING WHEN IT IS LOCKED CANNOT BE JUDGED BY WHAT IT SAYS.
  //
  // The arm above asserts the signed-out sentence is absent, and that is the
  // whole test for five of these six. /memory has no signed-out copy at all -
  // gated out, it renders an empty list, which is indistinguishable from a
  // person who has written nothing. Measured: reverting Memory.tsx alone to the
  // old `!token` gate left this check green, twice, because there was no
  // sentence to look for. The silent pages are the WORSE case, and the sentence
  // arm is blind to exactly them.
  //
  // So this one is asserted on CONTENT. A row is written as this person,
  // through the browser's own cookie, and must then appear on the page. A page
  // that never fetches because it thinks nobody is signed in cannot show it.
  const mark = `person-sees-the-console ${Date.now().toString(36)}`;
  const wrote = await page.evaluate(async (title) => {
    const res = await fetch("/api/artifacts", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        type: "memory",
        kind: "note",
        title,
        body: "seeded",
        visibility: "personal",
      }),
    });
    return res.ok ? "" : `HTTP ${res.status} ${await res.text()}`;
  }, mark);
  if (wrote) die(`could not seed a memory as this person: ${wrote}`);

  await page.goto(`${base}/memory`, { timeout: 30_000 }).catch(() => {});
  const seeded = page.locator(`text=${mark}`).first();
  await seeded.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await seeded.count()) === 0) {
    die(`/memory does not show a note this person just wrote.
It draws an empty list rather than saying anything, so "no memories" and "you are not
signed in" look identical here - which is why this arm asserts a row and not a sentence.`);
  }

  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);
  if (blank.length > 0) {
    die(
      `these pages drew no heading at all, so this check could not judge them: ${blank.join(", ")}`,
    );
  }
  if (locked.length > 0) {
    die(`a person who is logged in is told to log in on: ${locked.join(", ")}
Each of those reads the bearer token in localStorage and calls it being signed in. Being
somebody is whoami answering - see useSignedIn in lib/session.`);
  }

  console.log(
    `a logged-in person with no token sees all ${PAGES.length} pages: ${PAGES.map((p) => p.path).join(" ")}`,
  );
} finally {
  await browser.close();
}
