/**
 * Does a chat address say which project it means, and does the old form still
 * work?
 *
 * 01M10V97MD, the operator: "no project name in url". Every project has a
 * #general, so /chat/general names a room and not a place - the same defect
 * that put two messages into pa's #general instead of flowy's. The canonical
 * form is /p/<project>/chat/<room>.
 *
 * TWO THINGS ARE ASSERTED AND THEY FAIL DIFFERENTLY.
 *
 * 1. THE ROUTE RANKS ABOVE THE ARTIFACT ONE. /p/:project/:type/:id already
 *    existed and /p/flowy/chat/general matches it too - project=flowy,
 *    type=chat, id=general. React Router scores a static segment above a
 *    dynamic one so the chat route should win, but "should" is the word that
 *    makes this worth a check: if it loses, every link of this shape silently
 *    renders an artifact page that cannot exist, and the room is unreachable.
 *
 * 2. A BARE LINK UPGRADES ITSELF. /chat/general still resolves and rewrites to
 *    the canonical address, because 17 links across 8 files use the bare form
 *    and rewriting them all was the riskier way to do this. If the rewrite
 *    stops happening the old links keep working and the address bar quietly
 *    stops carrying the project, which is the whole point being lost without
 *    anything breaking.
 *
 * The rewrite must also be a REPLACE: going back from the room must leave the
 * room, not step onto the bare form and bounce forward again.
 */

import { chromium } from "playwright";

const [base, handle, password, project] = process.argv.slice(2);
if (!base || !handle || !password || !project) {
  console.error("usage: node scripts/chat-url-project-check.mjs BASE_URL HANDLE PASSWORD PROJECT");
  process.exit(2);
}

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

  const who = await page.evaluate(async () => {
    const res = await fetch("/api/whoami", { credentials: "same-origin" });
    return res.ok ? ((await res.json())?.user ?? "") : "";
  });
  if (!who) die("could not log in, so nothing below was measured");

  /**
   * ENTER THE PROJECT FIRST, and this is a precondition rather than a step.
   *
   * The rewrite deliberately does nothing while the session is in no project:
   * writing /p//chat/general into the address bar would put an empty project
   * there and read as a fact. So a check that never enters one measures the
   * guard and reports it as the feature being broken - which is what the first
   * version of this check did.
   */
  await page.goto(`${base}/projects`, { timeout: 30_000 }).catch(() => {});
  const enter = page.locator(`[data-enter-project="${project}"]`);
  await enter.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await enter.count()) === 0) die(`/projects offers no way into ${JSON.stringify(project)}`);
  if ((await enter.getAttribute("data-enter-current")) !== "yes") {
    await enter.click();
    await page
      .locator(`[data-enter-project="${project}"][data-enter-current="yes"]`)
      .waitFor({ state: "visible", timeout: 15_000 })
      .catch(() => die(`entering ${JSON.stringify(project)} was refused or never took`));
  }

  // 1. the canonical form reaches the ROOM, not the artifact page.
  await page.goto(`${base}/p/${project}/chat/general`, { timeout: 30_000 }).catch(() => {});
  const header = page.locator("h1", { hasText: "#general" });
  await header.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if (!(await header.isVisible().catch(() => false))) {
    die(
      `/p/${project}/chat/general did not draw the room - if it matched /p/:project/:type/:id ` +
        "instead, the artifact route outranks the chat one and every link of this shape is dead",
    );
  }

  // 2. the bare form still resolves, and upgrades itself.
  //
  // FROM A KNOWN PAGE, so the back assertion below means something. The first
  // version came here straight from the canonical address, so going back landed
  // on that identical path and the check called a working replace a push. The
  // previous entry has to be somewhere the room is not.
  await page.goto(`${base}/projects`, { timeout: 30_000 }).catch(() => {});
  await page.goto(`${base}/chat/general`, { timeout: 30_000 }).catch(() => {});
  await page
    .waitForURL((url) => url.pathname === `/p/${project}/chat/general`, { timeout: 15_000 })
    .catch(() => {});
  const landed = new URL(page.url()).pathname;
  if (landed !== `/p/${project}/chat/general`) {
    die(`/chat/general stayed at ${landed} - a bare link no longer carries the project`);
  }
  if (
    !(await page
      .locator("h1", { hasText: "#general" })
      .isVisible()
      .catch(() => false))
  ) {
    die("the upgraded address does not draw the room");
  }

  // The rewrite replaced rather than pushed: one back leaves the room entirely.
  await page.goBack({ timeout: 15_000 }).catch(() => {});
  const back = new URL(page.url()).pathname;
  if (back !== "/projects") {
    die(
      `back went to ${back} rather than /projects - the rewrite pushed a history entry, ` +
        "so the reader presses back twice to leave a room they entered once",
    );
  }

  if (crashes.length > 0) die(`the console threw: ${crashes.join(" | ")}`);
  console.log(
    `/p/${project}/chat/general draws the room, /chat/general upgrades to it, and the rewrite replaces`,
  );
} finally {
  await browser.close();
}
