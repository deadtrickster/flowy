/**
 * THE BOX MUST NOT HOLD MORE TERMINAL THAN IT SHOWS - ONCE IT HAS SETTLED.
 *
 *   node scripts/vm-rows-check.mjs BASE_URL OPERATOR_TOKEN
 *
 * The operator: "started htop - bottom truncated". htop draws to its last row,
 * so a terminal that believes it has more rows than the box displays loses
 * exactly that row and nothing else on screen looks wrong.
 *
 * WHY THIS POLLS INSTEAD OF READING ONCE, and it is the correction of my own
 * mistake rather than a nicety. The first version read the geometry 1000ms
 * after the terminal's children appeared and asserted on that one frame. But
 * the box shrinks when the panel's refusal text renders, and that is driven by
 * the websocket failing - asynchronous, and not ordered against any wait. So a
 * read could land after the shrink and before the addon's refit, which is
 * debounced 100ms. It went red twice with identical numbers, 764 against 784,
 * and a repeatable race produces exactly that as readily as a real defect does.
 *
 * A ONE-FRAME ASSERTION CANNOT TELL THOSE APART. This one waits for the
 * invariant to HOLD, up to a budget, and fails only if it never does. A
 * transient then passes - correctly, because a terminal that corrects itself
 * within a frame is not what loses htop's bottom row - and a stale size still
 * fails, because it never settles.
 *
 * IT REPORTS THE FIRST READING TOO, on pass and on fail. A pass that took eight
 * samples to settle is a different fact from one that was right immediately,
 * and the difference is worth seeing before it becomes the next bug.
 *
 * WHAT IS NOT ASSERTED, named rather than implied: nothing here reaches the
 * PTY. The socket is refused for a token-only console, so no shell exists and
 * no resize is ever sent to a guest. Whether the node is told a stale row count
 * is a separate question and needs a live shell.
 *
 * WHY THE READING NEEDS THE OPERATOR'S TOKEN: /api/vm/* is operatorOnly, and
 * with an ordinary token the panel is not rendered at all - [data-vm-shell]
 * resolves to 0 elements, measured. There is no box and no terminal. The
 * reading cannot be taken from an agent seat; it can be taken here.
 *
 * "THIS HOST", NOT A MICROVM, AND STOP IS PRESSED. VmShell says plainly that
 * closing the socket is not a stop and LEAVES THE VM RUNNING, so a check that
 * pressed Run on a microVM would strand a guest on every pass.
 */

import { chromium } from "playwright";

const [base, operator] = process.argv.slice(2);
if (!base || !operator) {
  console.error("usage: node scripts/vm-rows-check.mjs BASE_URL OPERATOR_TOKEN");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

// The wasm first, as a fact. Without it ghostty's init() throws, no element is
// created, and "the box clips" and "there is no terminal" are indistinguishable.
const wasm = await fetch(`${base}/ghostty-vt.wasm`);
if (!wasm.ok) {
  die(`GET /ghostty-vt.wasm answered ${wasm.status}, so no terminal is created and this
check cannot take its reading. vm-shell-check.mjs owns that failure.`);
}

const SETTLE_MS = 8_000;
const SAMPLE_MS = 250;

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), operator);
  await page.goto(`${base}/vms`, { timeout: 30_000 });

  // WHICH WORLD THIS HOST IS IN, ASKED OF THE PAGE. The suite runs inside a
  // firecode guest in the ordinary case, and a guest has no firecode: /vms
  // answers that it cannot run VMs and draws no shell panel. That is the
  // environment, not a defect, and the first version of this check called it a
  // defect and went red on master for anybody running the suite in a guest.
  // vm-shell-check.mjs already owns the assertion that matters there - that
  // nothing offers a Run control when there is nothing to run it on.
  const panel = page.locator("[data-vm-panel]");
  await panel.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  let vmState = await panel.getAttribute("data-vm-state").catch(() => null);
  for (let i = 0; i < 40 && vmState === "reading"; i++) {
    await page.waitForTimeout(250);
    vmState = await panel.getAttribute("data-vm-state").catch(() => null);
  }
  if (vmState !== "ok") {
    // SAID, NOT SILENT. A check that prints nothing here is indistinguishable
    // from one that ran and found nothing wrong.
    console.log(
      `this host cannot run VMs (panel state ${JSON.stringify(vmState)}), so there is no terminal to measure - the geometry this check exists for does not exist here. vm-shell-check.mjs owns this environment.`,
    );
    process.exit(0);
  }

  const shell = page.locator("[data-vm-shell]").first();
  await shell.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await page.locator("[data-vm-shell]").count()) === 0) {
    die(`the page says it CAN run VMs - panel state "ok" - and still draws no shell panel,
so the terminal geometry cannot be read. Those two cannot both be right.`);
  }

  // Chosen BEFORE Run: the selector is disabled once the state leaves idle, so
  // setting it afterwards would silently do nothing and boot a guest.
  await page.locator("[data-vm-shell-where]").selectOption("host");
  await page.locator("[data-vm-shell-run]").click();

  const box = page.locator("[data-vm-shell-screen]").first();
  await box.waitFor({ state: "visible", timeout: 20_000 });
  let children = 0;
  for (let i = 0; i < 120; i++) {
    children = await box.evaluate((el) => el.children.length).catch(() => 0);
    if (children > 0) break;
    await page.waitForTimeout(500);
  }
  if (children === 0) {
    const why = await page
      .locator("[data-vm-shell-why]")
      .innerText()
      .catch(() => "");
    die(`Run was pressed and 60s later the screen box is still empty - ghostty created no
element, on a node that serves the wasm. The panel said: ${JSON.stringify(why)}`);
  }

  const read = () =>
    box.evaluate((el) => {
      const kids = [...el.children].map((n) => ({
        tag: n.tagName.toLowerCase(),
        offsetHeight: n.offsetHeight,
      }));
      return {
        clientHeight: el.clientHeight,
        scrollHeight: el.scrollHeight,
        tallestChild: kids.reduce((m, k) => Math.max(m, k.offsetHeight), 0),
        kids,
      };
    });

  // A pixel of tolerance and no more: sub-pixel layout rounds, a row does not.
  const fits = (r) => r.scrollHeight - r.clientHeight <= 1 && r.tallestChild - r.clientHeight <= 1;

  const samples = [];
  let settled = null;
  const started = Date.now();
  for (;;) {
    const r = await read();
    const state = await shell.getAttribute("data-vm-shell-state").catch(() => null);
    samples.push({ ms: Date.now() - started, state, ...r, kids: undefined });
    if (fits(r)) {
      settled = { ...r, ms: Date.now() - started, state };
      break;
    }
    if (Date.now() - started > SETTLE_MS) break;
    await page.waitForTimeout(SAMPLE_MS);
  }

  // Stop before the verdict, so a red does not also leave a shell behind.
  await page
    .locator("[data-vm-shell-stop]")
    .click({ timeout: 5_000 })
    .catch(() => {});

  const first = samples[0];
  const last = samples[samples.length - 1];

  if (!settled) {
    die(`the terminal is taller than the box that shows it and stayed that way for ${SETTLE_MS}ms,
so its bottom row is drawn where nobody can see it - which is what "started htop
- bottom truncated" is. This is not a frame caught mid-refit: the addon debounces
its own resize by 100ms, and this waited ${Math.round((last.ms || 0) / 100) / 10}s.

  first  ${first.ms}ms  state ${JSON.stringify(first.state)}  box ${first.clientHeight}  scroll ${first.scrollHeight}  tallest child ${first.tallestChild}
  last   ${last.ms}ms  state ${JSON.stringify(last.state)}  box ${last.clientHeight}  scroll ${last.scrollHeight}  tallest child ${last.tallestChild}
  samples ${samples.length}: ${JSON.stringify(samples.map((s) => `${s.ms}:${s.clientHeight}/${s.tallestChild}`))}

The addon measures the CONTAINER - Terminal.open(A) sets element = A, which is
this box - so this is not a fit reading the wrong element. The remaining
mechanism worth looking at is the _isResizing guard: fit() holds it for 50ms and
the resize observer drops any container change that arrives inside that window,
with nothing re-checking afterwards.`);
  }

  const late = settled.ms > 0;
  const lateNote = late
    ? ` - it did NOT fit on the first read (box ${first.clientHeight}, child ${first.tallestChild}, state ${JSON.stringify(first.state)}), so the refit is real but not instant`
    : "";
  const when = late ? ` (${samples.length} sample(s))` : " (first read)";
  console.log(
    `the terminal fits its box after ${settled.ms}ms${when}: clientHeight ${settled.clientHeight}, scrollHeight ${settled.scrollHeight}, tallest child ${settled.tallestChild}${lateNote}`,
  );
} finally {
  await browser.close();
}
