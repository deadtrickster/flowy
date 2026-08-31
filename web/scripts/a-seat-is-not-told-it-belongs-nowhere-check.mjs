/**
 * A SEAT IS NOT TOLD IT BELONGS TO NO PROJECT.
 *
 *   node scripts/a-seat-is-not-told-it-belongs-nowhere-check.mjs BASE_URL PERSON_TOKEN AGENT_TOKEN
 *
 * `memberships` was carrying three facts with two values. A person who belongs
 * to nothing is [], a list the node could not read is null, and an AGENT - for
 * whom the question does not apply, because a seat's reach is minted into its
 * token and no owner can add to it - answered [] as well. So this console keyed
 * its rail on the list and told every agent seat "you belong to no project
 * yet", in the sidebar, under the name of the project it was writing into.
 *
 * Measured on the dogfood node 2026-08-31, filed as 01M1BW5G028XX66GKVXYNE0T9X.
 * The door's own comment described the mistake its initialiser was making.
 *
 * ASSERTED AS A DIFFERENCE, not as an absolute. "The rail does not say you
 * belong to no project" passes on a console that renders nothing at all, and it
 * would have passed on every build before the sentence was written. So the same
 * page is loaded twice, varying only the credential, and the two readings have
 * to differ: the person who belongs to nothing is still told so, because that
 * is true of them and actionable - an owner can put them in a project - and the
 * seat is not.
 *
 * BOTH ARE STILL TOLD WHERE THEIR WRITES LAND. The fix for a false sentence is
 * not silence: a seat that cannot see its project in the rail is the defect the
 * operator screenshotted twice.
 */

import { chromium } from "playwright";

const [base, personToken, agentToken] = process.argv.slice(2);
if (!base || !personToken || !agentToken) {
  console.error(
    "usage: node scripts/a-seat-is-not-told-it-belongs-nowhere-check.mjs BASE_URL PERSON_TOKEN AGENT_TOKEN",
  );
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

/** The sentence under test, as the console words it. */
const NOWHERE = "you belong to no project yet";

const browser = await chromium.launch();
try {
  /** Load the rail with one credential and report what its project control says. */
  const read = async (token, who) => {
    const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
    const crashes = [];
    page.on("pageerror", (err) => crashes.push(String(err)));
    await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
    await page.goto(`${base}/`, { timeout: 30_000 });
    const control = page.locator("[data-rail-project]");
    await control
      .waitFor({ state: "visible", timeout: 20_000 })
      .catch(() => die(`the rail drew no project control at all for ${who}`));
    if (crashes.length > 0) die(`the console threw for ${who}: ${crashes.join("; ")}`);
    const said = (await control.innerText()).trim();
    const project = (await control.getAttribute("data-rail-project")) ?? "";
    const reach = await page.evaluate(async () => {
      const token = localStorage.getItem("flowy.token");
      const r = await fetch("/api/whoami", { headers: { Authorization: `Bearer ${token}` } });
      const body = await r.json();
      return { reach: body.reach ?? null, memberships: body.memberships ?? null };
    });
    await page.close();
    return { said, project, ...reach };
  };

  const person = await read(personToken, "the person");
  const seat = await read(agentToken, "the seat");

  // THE FIXTURE HAS TO BE THE ONE THIS CHECK IS ABOUT. A person who already
  // belongs somewhere would never see the sentence, so its absence for the seat
  // would prove nothing at all.
  if (person.reach !== "memberships" || (person.memberships ?? []).length !== 0) {
    die(`this run's person answers reach=${JSON.stringify(person.reach)} with ${JSON.stringify(
      person.memberships,
    )} memberships, so they are not the "belongs to nothing" case and the
comparison this check makes has nothing to compare against`);
  }
  if (seat.reach !== "token") {
    die(`this run's agent token answers reach=${JSON.stringify(seat.reach)}, not "token" - the
door is not naming a seat's mechanism, which is the fix this check stands on`);
  }

  if (!person.said.includes(NOWHERE)) {
    die(`the person who belongs to no project was NOT told so - the rail said
${JSON.stringify(person.said)}. That sentence is true of them and actionable, and dropping it
is the other way to get this wrong.`);
  }
  if (seat.said.includes(NOWHERE)) {
    die(`the seat was told ${JSON.stringify(NOWHERE)} - the rail said ${JSON.stringify(seat.said)}.
A seat's reach is minted into its token, so belonging to a project is not an act available to
it and the sentence describes somebody else.`);
  }

  // AND THE SEAT STILL KNOWS WHERE IT IS WRITING.
  if (!seat.project || seat.project === "none" || !seat.said.includes(seat.project)) {
    die(
      `the seat's rail does not name the project it writes into: data-rail-project=${JSON.stringify(
        seat.project,
      )}, control said ${JSON.stringify(seat.said)}. Silence is not the fix for a false sentence.`,
    );
  }

  console.log(
    `the person who belongs nowhere is told so (${JSON.stringify(person.said)}); the seat is not, ` +
      `and still names ${JSON.stringify(seat.project)} (${JSON.stringify(seat.said)})`,
  );
} finally {
  await browser.close();
}
