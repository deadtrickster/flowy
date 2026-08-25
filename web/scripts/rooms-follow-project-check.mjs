/**
 * The rooms rail follows a project switch, without a reload.
 *
 *   node scripts/rooms-follow-project-check.mjs BASE_URL HANDLE PASSWORD PROJECT_A PROJECT_B
 *
 * The operator, 2026-08-25, having just switched project from the console: "it
 * worked except rooms list didnt restore to the flowy project". The switch
 * worked. The rail did not follow it.
 *
 * useRoomList's effect carried `[]`, so the rooms were fetched once at mount and
 * never again. That was right when a console had one project for the life of a
 * page; `enter` made the project change underneath a mounted tree, and every
 * read keyed on nothing at all became wrong on the day it landed.
 *
 * NOT COSMETIC. A room belongs to a project - #general in flowy and #general in
 * Lab are two rooms with one name and neither reads the other - so a rail left
 * on the previous project offers links into rooms this session cannot write in.
 * The reader clicks one, types, and the message lands somewhere else or nowhere.
 *
 * THE ROOM IS SEEDED RATHER THAN ASSUMED. A room exists here as soon as somebody
 * speaks in one, so this makes a room in B with a name nothing else can produce
 * and then asks whether the rail shows it. Asserting on the rooms that happen to
 * exist would pass on a node where both projects hold the same names, which is
 * the normal case and the one that hides this.
 *
 * AND NO RELOAD BETWEEN THE TWO READS. That is the whole assertion: a reload
 * remounts the tree and refetches, so a check that reloads passes against the
 * broken build. The switch itself has to move the rail.
 */

import { chromium } from "playwright";

const [base, handle, password, projectA, projectB] = process.argv.slice(2);
if (!base || !handle || !password || !projectA || !projectB) {
  console.error(
    "usage: node scripts/rooms-follow-project-check.mjs BASE_URL HANDLE PASSWORD PROJECT_A PROJECT_B",
  );
  process.exit(2);
}

const die = (why) => {
  console.error(why);
  process.exit(1);
};

const only = `only-in-b-${Date.now().toString(36)}`;

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

  /** Click a project in the switcher and wait for the page to say it took. */
  const enter = async (project) => {
    await page.goto(`${base}/projects`, { timeout: 30_000 }).catch(() => {});
    const button = page.locator(`[data-enter-project="${project}"]`);
    await button.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
    if ((await button.count()) === 0)
      die(`/projects offers no way into ${JSON.stringify(project)}`);
    if ((await button.getAttribute("data-enter-current")) === "yes") return;
    await button.click();
    await page
      .locator(`[data-enter-project="${project}"][data-enter-current="yes"]`)
      .waitFor({ state: "visible", timeout: 15_000 })
      .catch(() => die(`entering ${JSON.stringify(project)} was refused or never took`));
  };

  /** The room names the rail is offering right now. */
  const rail = async () => {
    await page.locator("[data-room-list]").waitFor({ state: "visible", timeout: 20_000 });
    return await page.locator("[data-room-list] a[href^='/chat/']").allInnerTexts();
  };

  // A ROOM THAT ONLY B HAS. Said as this person, so it lands in whatever project
  // the session is in - which is why B is entered first.
  await enter(projectB);
  const said = await page.evaluate(async (room) => {
    const res = await fetch(`/api/chat/${room}/say`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ body: "seeded by rooms-follow-project-check" }),
    });
    return res.ok ? "" : `HTTP ${res.status} ${await res.text()}`;
  }, only);
  if (said) die(`could not seed a room in ${projectB}: ${said}`);

  // Into A, where that room does not exist.
  await enter(projectA);
  const inA = await rail();
  if (inA.some((name) => name.includes(only))) {
    die(`the rail in ${projectA} offers ${only}, which is a room in ${projectB}.
A room belongs to a project, so this is a link into a room this session cannot write in.`);
  }

  // And back to B, WITHOUT A RELOAD. This is the assertion.
  await enter(projectB);
  const inB = await rail();
  if (!inB.some((name) => name.includes(only))) {
    die(`after switching from ${projectA} to ${projectB} the rail still shows ${projectA}'s rooms.
It is missing ${only}, which exists in ${projectB} and was just written there.
The rooms are fetched once at mount; nothing refetches them when the project changes underneath.
Rail now: ${inB.join(", ") || "(empty)"}`);
  }

  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);
  console.log(`the rail followed ${projectA} -> ${projectB} with no reload, and ${only} appeared`);
} finally {
  await browser.close();
}
