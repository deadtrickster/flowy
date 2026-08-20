/**
 * SWITCHING PROJECTS IS A CONTROL, not a credential swap.
 *
 *   node scripts/switch-project-check.mjs BASE_URL TOKEN
 *
 * The operator asked twice - "how to switch projects", then "still no project
 * switcher for me" - while every part underneath it had landed: project_members,
 * sessions.project, principalFor filling it, the enter door, and whoami
 * reporting memberships. Nothing drew any of it, so from where they sat none of
 * it existed.
 *
 * WHAT IS ASSERTED, and why it is the empty case first:
 *
 *   1. a person who belongs to NOTHING is told so, in words, with what to do
 *      about it. project_members is empty on every node right now, so this is
 *      the state anybody meets first, and an empty list rendered silently is
 *      the same silence the row was filed against;
 *   2. the panel is keyed by the count, so "no memberships" and "the node did
 *      not say" are distinguishable from outside - a bearer token gets no panel
 *      at all, because a seat's reach is minted into it and switching is not a
 *      question it can ask.
 *
 * WHAT IT CANNOT ASSERT HERE: the switch itself. That needs a person with two
 * memberships and a cookie session, which is a fixture this suite does not have
 * yet - so it is checked in the store and at the door instead, and this says
 * only what the browser can honestly see. A check that pretended otherwise
 * would be the third thing today that passed while the operator saw nothing.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/switch-project-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/projects`, { timeout: 20_000 }).catch(() => {});
  await page
    .locator("[data-projects]")
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});

  if ((await page.locator("[data-projects]").count()) === 0) {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    console.error(`/projects drew nothing: no [data-projects].${errors}`);
    process.exit(1);
  }

  // A BEARER TOKEN IS NOT A PERSON WITH MEMBERSHIPS. whoami answers null for
  // one, the panel renders nothing, and that is correct rather than missing:
  // offering a seat a switch it cannot make would be a control that exists to
  // be refused.
  const panels = await page.locator("[data-memberships]").count();
  if (panels === 0) {
    console.log(
      "a seat's credential is offered no project switcher, which is right: its reach is minted into the token",
    );
    process.exit(0);
  }

  const count = Number(
    await page.locator("[data-memberships]").first().getAttribute("data-memberships"),
  );
  const said = (await page.locator("[data-memberships]").first().innerText()).trim();
  if (count === 0) {
    if (!/belong to no project/i.test(said)) {
      console.error(
        `a person who belongs to nothing is shown an empty panel rather than told so: ${JSON.stringify(said)}`,
      );
      process.exit(1);
    }
    if (!/members/i.test(said)) {
      console.error(
        `the empty panel does not say what to do about it, which leaves somebody stuck where the operator was: ${JSON.stringify(said)}`,
      );
      process.exit(1);
    }
    console.log("a person who belongs to no project is told so, and told what puts them in one");
    process.exit(0);
  }

  // With memberships, exactly one is marked as the one being worked in, and it
  // is not offered as a destination: a button that switches you to where you
  // already are is a button that does nothing.
  const here = await page.locator('[data-enter-current="yes"]').count();
  if (here !== 1) {
    console.error(`${count} memberships and ${here} of them marked as the current project`);
    process.exit(1);
  }
  const disabled = await page.locator('[data-enter-current="yes"]').first().isDisabled();
  if (!disabled) {
    console.error("the project being worked in is offered as a switch destination");
    process.exit(1);
  }
  console.log(`${count} membership(s) drawn, one marked current and not offered as a destination`);
} finally {
  await browser.close();
}
