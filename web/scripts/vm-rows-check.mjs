/**
 * THE BOX MUST NOT HOLD MORE TERMINAL THAN IT SHOWS.
 *
 *   node scripts/vm-rows-check.mjs BASE_URL OPERATOR_TOKEN
 *
 * The operator: "started htop - bottom truncated". A full-screen program is
 * what makes this visible: htop draws to its last row, so a terminal that
 * believes it has one row more than the box displays loses exactly that row,
 * and nothing else on screen looks wrong.
 *
 * THE INVARIANT, and it is deliberately about the BOX rather than about
 * ghostty's internals. Two candidates were on the row - ghostty's FitAddon
 * measuring an element that sizes to its own content, and our own resize path
 * telling the pty a number taken before the box had its final height - and a
 * check written against either one asserts a mechanism rather than the defect.
 * What a person sees is content the box does not show, so that is what is
 * measured here: the screen box's scrollHeight against its clientHeight, plus
 * the height of what ghostty actually put inside it. Either candidate produces
 * a violation, and neither can produce a pass while the bottom row is missing.
 *
 * WHY THE READING NEEDS THE OPERATOR'S TOKEN, recorded because it cost an
 * evening: /api/vm/* is operatorOnly, and with an ordinary token the panel is
 * not rendered at all - [data-vm-shell] resolves to 0 elements, so there is no
 * box and no terminal to measure. The reading cannot be taken from an agent
 * seat. It can be taken here, because the suite holds TOKEN_OP.
 *
 * "THIS HOST", NOT A MICROVM, AND STOP IS PRESSED. Closing the browser is
 * explicitly not a stop - VmShell's own comment: a closed socket means "this
 * browser went away" and LEAVES THE VM RUNNING. A check that pressed Run on a
 * microVM would leave a guest behind on every pass, which is the reason
 * vm-shell.sh excludes the guest arms in the first place. The geometry is
 * created and fitted before the socket is reached, so a host shell answers the
 * same question and boots nothing.
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

// The wasm first, and as a fact rather than as an assumption. Without it
// ghostty's init() throws, no element is ever created, and "the box clips" and
// "there is no terminal" would be indistinguishable below.
const wasm = await fetch(`${base}/ghostty-vt.wasm`);
if (!wasm.ok) {
  die(`GET /ghostty-vt.wasm answered ${wasm.status}, so no terminal is created and this
check cannot take its reading. vm-shell-check.mjs owns that failure.`);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), operator);
  await page.goto(`${base}/vms`, { timeout: 30_000 });

  const shell = page.locator("[data-vm-shell]").first();
  await shell.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await page.locator("[data-vm-shell]").count()) === 0) {
    die(`the shell panel is not on /vms for the operator's own token, so the terminal
geometry cannot be read. With an ordinary token this is expected and is what
operatorOnly does; with this one it is a defect.`);
  }

  // THE HOST, chosen before Run rather than after: the selector is disabled
  // once the state leaves idle, so setting it afterwards would silently do
  // nothing and the check would boot a guest it promised not to.
  await page.locator("[data-vm-shell-where]").selectOption("host");

  await page.locator("[data-vm-shell-run]").click();

  // ghostty and its 2MB wasm are fetched on demand, on the click. The wait is
  // for what it puts in the box, not for a state word - the state can reach
  // "live" from the socket while the element is still being built.
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

  // Let the fit settle. fit() runs at open and observeResize() follows the
  // container from then on, so a reading taken in the same tick as the click
  // can catch the pre-fit size and report a defect that corrects itself a
  // frame later.
  await page.waitForTimeout(1000);

  const reading = await box.evaluate((el) => {
    const kids = [...el.children].map((n) => ({
      tag: n.tagName.toLowerCase(),
      offsetHeight: n.offsetHeight,
      scrollHeight: n.scrollHeight,
    }));
    return {
      clientHeight: el.clientHeight,
      scrollHeight: el.scrollHeight,
      tallestChild: kids.reduce((m, k) => Math.max(m, k.offsetHeight), 0),
      kids,
    };
  });

  // A pixel of tolerance, and no more: sub-pixel layout rounds, a row does not.
  const overflow = reading.scrollHeight - reading.clientHeight;
  const childOver = reading.tallestChild - reading.clientHeight;

  // STOP IS PRESSED WHETHER THIS PASSES OR FAILS, and before the verdict, so a
  // red does not also leave a shell behind.
  await page
    .locator("[data-vm-shell-stop]")
    .click({ timeout: 5_000 })
    .catch(() => {});

  if (overflow > 1 || childOver > 1) {
    die(`the terminal is taller than the box that shows it, so its bottom row is drawn
where nobody can see it - which is what "started htop - bottom truncated" is.

  screen box clientHeight : ${reading.clientHeight}
  screen box scrollHeight : ${reading.scrollHeight}   (${overflow > 0 ? `+${overflow}` : overflow})
  tallest child           : ${reading.tallestChild}   (${childOver > 0 ? `+${childOver}` : childOver})
  children                : ${JSON.stringify(reading.kids)}

If the child is the taller of the two, the terminal element sized itself to its
own content and the fit is self-referential. If the box alone overflows, the
element fits and something below it does not.`);
  }

  console.log(
    `the terminal fits its box: clientHeight ${reading.clientHeight}, scrollHeight ${reading.scrollHeight}, tallest child ${reading.tallestChild} - nothing is drawn below the fold`,
  );
} finally {
  await browser.close();
}
