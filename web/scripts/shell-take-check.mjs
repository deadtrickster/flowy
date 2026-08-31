/**
 * A SHELL THIS BROWSER DID NOT START MUST BE OFFERED, AND TAKING ONE MUST
 * ADOPT IT RATHER THAN MINT A SECOND.
 *
 *   node scripts/shell-take-check.mjs BASE_URL OPERATOR_TOKEN
 *
 * 01M1558DPM1HRGZNJGMVW24DHF item 3. The panel adopts on mount from
 * localStorage, so before this the only session a person could return to was
 * one that browser had started itself. Start a shell on the laptop, open the
 * console on the phone, and the node was still running it with no way to say
 * so. The door (GET /api/agent/sessions) landed in 3922ef0; this is the panel.
 *
 * THE DOOR IS STUBBED, DELIBERATELY, and this is the whole reason the check is
 * cheap enough to run every gate. Listing a session requires a session, and
 * agentShells excludes finished ones - so an honest end-to-end version would
 * have to START a host shell and LEAVE IT RUNNING, because VmShell's stop
 * closes the socket and explicitly does not stop the shell. Every gate run
 * would strand one. What this check exists to prove is the panel's rule - that
 * a listed session is offered, and that pressing take carries THAT id into the
 * adopt path - and that rule is independent of where the list came from.
 *
 * WHAT IS THEREFORE NOT ASSERTED, named rather than implied: that the node
 * actually lists a live session, and that adopting a real id reattaches to a
 * real PTY. The first is agentsessions_test.go, which fails on five assertions
 * if finished sessions appear. The second is the pre-existing adopt path this
 * reuses unchanged - the id is written where run(true) already reads it, so
 * there is no second mechanism here to get wrong.
 *
 * WHY THE OPERATOR'S TOKEN: /api/vm/* and /api/agent/sessions are both
 * operatorOnly. With an agent token no panel is rendered at all, so there is
 * nothing to take and nothing to measure.
 *
 * THE WRITE IS THE ASSERTION, NOT WHAT SURVIVES. Taking writes the chosen id to
 * the key run(true) reads, and that write IS "adopt this one rather than mint".
 * Reading the key back afterwards looked equivalent and is not: adopting a
 * stubbed id cannot connect - the node has no such session - and the failure
 * path calls forgetSession, which CLEARS the key. Proving this check red the
 * first time showed exactly that, reporting null rather than the wrong id, so a
 * read-back races the panel's own cleanup and would have made the passing case
 * flaky in the same way.
 *
 * So localStorage.setItem is wrapped before the page loads and every write is
 * recorded. A record cannot be undone by a later remove, and it says which id
 * the click carried - which is the whole question - rather than which id
 * happened to still be there a moment later.
 *
 * Whether the socket then connects is not asserted, and cannot be against a
 * stub: that would assert the stub, not the panel.
 */

import { chromium } from "playwright";

const [base, operator] = process.argv.slice(2);
if (!base || !operator) {
  console.error("usage: node scripts/shell-take-check.mjs BASE_URL OPERATOR_TOKEN");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

// Two, not one: a single row passes a panel that renders a hard-coded first
// session, and the take assertion below needs a row that is NOT the first to
// prove the id travels from the row that was clicked.
const OFFERED = [
  {
    id: "sess-laptop-aaa",
    project: "flowy",
    where: "host",
    started: "2026-08-31T10:00:00Z",
    watchers: 1,
    idle_seconds: 12,
  },
  {
    id: "sess-phone-bbb",
    project: "flowy",
    where: "vm",
    started: "2026-08-31T09:00:00Z",
    watchers: 0,
    idle_seconds: 900,
  },
];

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), operator);

  // Every session write recorded, before any page script runs. See the note at
  // the top: the key itself is cleared when the stubbed adopt fails to connect,
  // so what survives is not what was written.
  await page.addInitScript(() => {
    const real = Storage.prototype.setItem;
    window.__sessionWrites = [];
    Storage.prototype.setItem = function (k, v) {
      if (String(k).startsWith("flowy.vmshell.session.")) {
        window.__sessionWrites.push(String(v));
      }
      return real.call(this, k, v);
    };
  });

  // The stub goes on before navigation so the panel's first ask is served.
  await page.route("**/api/agent/sessions", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ sessions: OFFERED }),
    }),
  );

  await page.goto(`${base}/vms`, { timeout: 30_000 });

  // The same environment guard vm-rows-check carries, and for the same reason:
  // the suite runs inside a firecode guest in the ordinary case, a guest has no
  // firecode, and /vms then draws no shell panel at all. That is the
  // environment, not a defect - calling it one puts this check red on master
  // for anybody running the suite in a guest, which has happened here before.
  const panel = page.locator("[data-vm-panel]");
  await panel.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  let vmState = await panel.getAttribute("data-vm-state").catch(() => null);
  for (let i = 0; i < 40 && vmState === "reading"; i++) {
    await page.waitForTimeout(250);
    vmState = await panel.getAttribute("data-vm-state").catch(() => null);
  }
  if (vmState !== "ok") {
    console.log(
      `this host cannot run VMs (panel state ${JSON.stringify(vmState)}), so no shell panel is drawn and there is nothing to offer a session to. vm-shell-check.mjs owns this environment.`,
    );
    process.exit(0);
  }

  const shell = page.locator("[data-vm-shell]").first();
  await shell.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await page.locator("[data-vm-shell]").count()) === 0) {
    die(`the page says it CAN run VMs - panel state "ok" - and still draws no shell panel,
so a running session has nowhere to be offered.`);
  }

  const list = page.locator("[data-vm-shell-others]").first();
  await list.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await page.locator("[data-vm-shell-others]").count()) === 0) {
    die(`the node is running ${OFFERED.length} shells and the idle panel offers none of them.
A person who started a shell on another device has no way to reach it, which is
exactly what 01M1558DPM1HRGZNJGMVW24DHF item 3 is about.`);
  }

  const said = await list.getAttribute("data-vm-shell-others");
  if (said !== String(OFFERED.length)) {
    die(`the door listed ${OFFERED.length} sessions and the panel says it is offering ${said}.`);
  }

  // The SECOND row, so that a panel wired to the first would fail here.
  const wanted = OFFERED[1];
  const take = page.locator(`[data-vm-shell-take="${wanted.id}"]`);
  if ((await take.count()) === 0) {
    die(`no take control for session ${wanted.id}, so a listed shell is shown but cannot be
attached to. Offering a session that cannot be taken is worse than not listing it.`);
  }

  // Nothing written BEFORE the click, so the assertion is that this click wrote
  // it - not that it was there already.
  const before = await page.evaluate(() => window.__sessionWrites.slice());
  if (before.includes(wanted.id)) {
    die(`this browser had already claimed ${wanted.id} before anything was taken, so the
check cannot tell taking from remembering. The fixture is wrong, not the panel.`);
  }

  await take.click();

  let writes = [];
  for (let i = 0; i < 40; i++) {
    writes = await page.evaluate(() => window.__sessionWrites.slice());
    if (writes.length > before.length) break;
    await page.waitForTimeout(250);
  }
  const carried = writes.slice(before.length);
  if (carried.length === 0) {
    die(`take was pressed on ${wanted.id} and the panel claimed no session at all, so the
click reaches nothing. A listed shell that cannot be taken is worse than one
that was never listed.`);
  }
  if (carried[0] !== wanted.id) {
    die(`take was pressed on ${wanted.id} and the panel claimed ${JSON.stringify(carried[0])}.
The id from the row that was CLICKED must be the one written where run(true)
reads it - otherwise taking a session reaches a different shell, or mints a new
one beside the very shell the person was trying to get back to.`);
  }

  console.log(
    `offered ${said} shells this browser did not start; took ${wanted.id} and the panel claimed that id (${carried.length} session write(s) on the click)`,
  );
} finally {
  await browser.close();
}
