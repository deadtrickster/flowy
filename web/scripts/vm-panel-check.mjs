/**
 * The VMs panel tells four answers apart, and never draws three of them blank.
 *
 *   node scripts/vm-panel-check.mjs BASE_URL OPERATOR_TOKEN OTHER_TOKEN
 *
 * 01M0G0KT52, the operator's ask (3): "I want to be able to spawn agent right
 * from flow - inside fc VM". The node has had every door since api_vm.go
 * landed; the console had no caller for any of them.
 *
 * WHAT THIS ASSERTS, and it is deliberately the error half:
 *
 *   403  a non-operator is TOLD it is not theirs
 *   503  a node with no firecode says it cannot run VMs
 *   200  a node that can says so, and an empty list reads as "none running"
 *
 * api_vm.go goes out of its way to keep these apart - it returns 503 rather
 * than an empty list, in as many words, because "no VMs are running" and "this
 * node cannot run VMs" are different facts. All of that care is undone by a
 * console that catches everything into a blank page, and a blank page is what a
 * first cut of this panel does by default. So the check reads the SAME page
 * with two different tokens and requires the answers to differ.
 *
 * WHAT THIS DOES NOT ASSERT, said plainly rather than left to be assumed:
 * spawn, say and down are not driven end to end. Driving them means booting a
 * real firecracker VM on the machine running the suite, on every pass, and
 * leaving one behind whenever the run dies between spawn and down. That is a
 * worse thing to own than the coverage is worth. The buttons are wired to the
 * typed client in lib/api.ts and are covered by tsc; their behaviour against a
 * live host is not covered here, and 01M0G0KT52 carries that as an open note.
 */

import { chromium } from "playwright";

const [base, operatorToken, otherToken] = process.argv.slice(2);
if (!base || !operatorToken || !otherToken) {
  console.error("usage: node scripts/vm-panel-check.mjs BASE_URL OPERATOR_TOKEN OTHER_TOKEN");
  process.exit(2);
}

const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

/** What the NODE answers, which is the ground truth the pane is judged against. */
const ask = async (token) => {
  const r = await fetch(`${base}/api/vm/list`, { headers: { Authorization: `Bearer ${token}` } });
  const text = await r.text();
  let body = text;
  try {
    body = JSON.parse(text);
  } catch {
    /* a refusal is a sentence */
  }
  return { status: r.status, body };
};

const panelWith = async (browser, token) => {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/vms`, { timeout: 30_000 }).catch(() => {});
  const panel = page.locator("[data-vm-panel]");
  // WAIT FOR A RESOLVED STATE, not for the panel element. The panel is on
  // screen from the first paint carrying data-vm-state="reading", so waiting
  // for it returns instantly and the attribute is sampled mid-read - which is
  // what this check did on its first two runs, reporting "drew no panel at all
  // for the operator" about a page that was working and merely not finished.
  //
  // Longer than the node's own ceiling on purpose: api_vm.go gives `firecode
  // ps` twenty seconds and `projects` fifteen, in parallel, so a host that is
  // slow but fine can legitimately take most of that before either resolves.
  await page
    .locator('[data-vm-state]:not([data-vm-state="reading"])')
    .first()
    .waitFor({ state: "visible", timeout: 45_000 })
    .catch(() => {});
  if ((await panel.count()) === 0) {
    // WHAT THE BROWSER ACTUALLY HAS, because "drew no panel" is a symptom with
    // several causes - a route that did not match, a redirect to the login
    // page, a shell that never finished its first read - and they are not
    // distinguishable from the absence alone. The first run of this check said
    // exactly that about the operator's arm and left nothing to reason from.
    const url = page.url();
    const seen = ((await page.locator("body").textContent()) ?? "").replace(/\s+/g, " ").trim();
    await page.close();
    return { state: null, words: "", empty: false, crashes, url, seen: seen.slice(0, 400) };
  }
  const got = {
    state: await panel.first().getAttribute("data-vm-state"),
    words: ((await panel.first().textContent()) ?? "").replace(/\s+/g, " ").trim(),
    empty: (await page.locator("[data-vm-empty]").count()) > 0,
    crashes,
    url: page.url(),
  };
  await page.close();
  // "reading" is not an answer, it is the absence of one arriving. Reported as
  // its own sentence so a slow host reads as a slow host rather than as a pane
  // drawing the wrong state.
  if (got.state === "reading") {
    return { ...got, state: null, seen: "the panel was still reading the host when time ran out" };
  }
  return got;
};

const asOperator = await ask(operatorToken);
const asOther = await ask(otherToken);

// The fixture only works if the two tokens actually differ in authority. A node
// where both are the operator cannot show the distinction, and this says so
// rather than passing on a comparison it never made.
if (asOther.status !== 403) {
  die(`the second token is not a non-operator: /api/vm/list answered ${asOther.status} for it.
This check compares an operator's view with somebody else's, so it needs one of
each. That is a fixture problem, not a console one.`);
}

const browser = await chromium.launch();
try {
  const other = await panelWith(browser, otherToken);
  if (other.crashes.length > 0)
    die(`the page threw for a non-operator: ${other.crashes.join("; ")}`);
  if (!other.state) {
    die(`the VMs page drew no panel at all for a non-operator.
  browser was at ${other.url}
  page said      ${JSON.stringify(other.seen)}`);
  }
  if (other.state !== "forbidden") {
    die(`a non-operator sees state=${other.state}: ${JSON.stringify(other.words)}
The node answered 403. A console that renders that as anything other than "not
yours" is telling somebody the feature is broken, or absent, when it is neither.`);
  }
  if (other.empty) {
    die(
      "a non-operator is shown the empty-list message, which claims the host answered and had none",
    );
  }

  const operator = await panelWith(browser, operatorToken);
  if (operator.crashes.length > 0) {
    die(`the page threw for the operator: ${operator.crashes.join("; ")}`);
  }
  if (!operator.state) {
    die(`the VMs page drew no panel at all for the operator.
  node answered  ${asOperator.status} to this token
  browser was at ${operator.url}
  page said      ${JSON.stringify(operator.seen)}`);
  }

  // THE OPERATOR'S ARM IS JUDGED AGAINST WHAT THE NODE SAID, not against a
  // fixed expectation: this suite runs on machines with firecode and on
  // machines without, and both are correct. What is not correct is drawing
  // either one as the other.
  if (asOperator.status === 503) {
    if (operator.state !== "unavailable") {
      die(`the node answered 503 - it has no firecode and cannot run VMs - and the panel
drew state=${operator.state}: ${JSON.stringify(operator.words)}
This is the collapse api_vm.go returns 503 specifically to prevent.`);
    }
    if (operator.empty) {
      die(`the panel told the operator no VMs are running on a host that cannot run any.
The node distinguished these; the console put them back together.`);
    }
  } else if (asOperator.status === 200) {
    if (operator.state !== "ok") {
      die(
        `the node answered 200 and the panel drew state=${operator.state}: ${JSON.stringify(operator.words)}`,
      );
    }
    const running = (asOperator.body?.vms ?? []).length;
    if (running === 0 && !operator.empty) {
      die(`nothing is running and the panel does not say so: ${JSON.stringify(operator.words)}
An empty page is the one rendering that means all four answers at once.`);
    }
    if (running > 0 && operator.empty) {
      die(`${running} VM(s) are running and the panel says none are`);
    }
  } else {
    die(`/api/vm/list answered ${asOperator.status} to the operator, which this check has no
expectation for: ${JSON.stringify(asOperator.body)}`);
  }

  // THE DIFFERENCE, which is the assertion that a blank panel cannot satisfy.
  if (operator.state === other.state) {
    die(`the panel draws a refusal and an answer the same way (${operator.state})`);
  }

  console.log(
    `the panel tells them apart: non-operator=${other.state}, operator=${operator.state} (node said ${asOperator.status})`,
  );
} finally {
  await browser.close();
}
