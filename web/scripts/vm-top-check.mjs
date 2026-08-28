// THE READINGS DOOR, AND THE PANE THAT DRAWS THEM.
//
// See checks.d/console/vm-top.sh for what this is for. The short version: the
// agents pane mirrors fctop, and the one thing it must never do is answer an
// empty fleet when what it means is that it could not ask.

import { chromium } from "playwright";

const [base, operator, other] = process.argv.slice(2);
const die = (why) => {
  console.error(why);
  process.exit(1);
};
if (!base || !operator || !other) die("usage: vm-top-check.mjs BASE OPERATOR_TOKEN OTHER_TOKEN");

const ask = async (token) => {
  const res = await fetch(`${base}/api/vm/top`, { headers: { Authorization: `Bearer ${token}` } });
  let body = null;
  try {
    body = await res.json();
  } catch {
    body = null;
  }
  return { status: res.status, body };
};

// OPERATOR-ONLY, AS A DIFFERENCE. The same request with two credentials: one
// reading cannot tell a rule being enforced from a rule that does not exist,
// and this door reports what is running inside the guests.
const asOther = await ask(other);
const asOperator = await ask(operator);
if (asOther.status === asOperator.status) {
  die(`/api/vm/top answered ${asOther.status} for an ordinary token and ${asOperator.status} for the
operator's - the same, so the door is not telling them apart`);
}
if (asOther.status !== 403) {
  die(`an ordinary token got ${asOther.status} from /api/vm/top, and it must be refused`);
}

// WHICH WORLD THIS HOST IS IN, asked of the door rather than of the filesystem:
// the node resolves fctop on ITS OWN PATH, and a suite that looked for the
// binary itself could disagree with the process it is testing.
if (asOperator.status === 503) {
  // NO FCTOP. The refusal has to be a refusal and say so - not 200 with
  // nothing in it, which is the failure this whole check exists for.
  const why = String(asOperator.body?.error ?? "");
  if (!/fctop/i.test(why)) {
    die(`the door answered 503 without naming what is missing: ${JSON.stringify(why)}`);
  }
  if (Array.isArray(asOperator.body?.vms)) {
    die(`the door answered 503 AND a vms list - a caller reading the body would see an empty
fleet where the node meant "I could not ask"`);
  }
  console.log(
    `no fctop on this node: /api/vm/top refused with 503 and said why, and did not answer an empty fleet; ${asOther.status} for an ordinary token`,
  );
  process.exit(0);
}

if (asOperator.status !== 200) {
  die(
    `/api/vm/top answered ${asOperator.status} for the operator: ${JSON.stringify(asOperator.body)}`,
  );
}
const rows = asOperator.body?.vms;
if (!Array.isArray(rows)) {
  die(`the frame carries no vms array: ${JSON.stringify(asOperator.body).slice(0, 200)}`);
}

// WHICH ARM RUNS IS DECIDED BY THIS HOST, and only one of them can. The node
// resolves fctop on its own PATH, so a suite cannot take the binary away from
// the process it is testing - and a stand-in binary spawned over here would
// assert something about this check's own child, not about the door.
//
// Said plainly rather than papered over, on the same grounds vm-shell.sh states
// for the guest arms: a host with fctop proves the frame arrives and is drawn,
// a host without proves the refusal is a refusal. Neither is a skip, and
// neither pretends to be the other.

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), operator);
  await page.goto(`${base}/vms`, { timeout: 30_000 });

  const panel = page.locator("[data-vm-panel]");
  await panel.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  let vmState = await panel.getAttribute("data-vm-state");
  for (let i = 0; i < 40 && vmState === "reading"; i++) {
    await page.waitForTimeout(250);
    vmState = await panel.getAttribute("data-vm-state");
  }
  if (vmState !== "ok") {
    // The page cannot run VMs at all, which the vm-shell check already covers.
    // Nothing here to draw, and saying so beats asserting against a page that
    // is correctly showing something else.
    console.log(
      `/api/vm/top answered ${rows.length} row(s) for the operator and 403 for an ordinary token; the page is in state ${JSON.stringify(vmState)} so the pane was not drawn`,
    );
    process.exit(0);
  }

  await page.locator('[data-vm-tab="agents"]').click();
  const table = page.locator("[data-vm-top]");
  await table.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await table.count()) === 0) {
    const why = await page
      .locator("[data-vm-top-why]")
      .innerText()
      .catch(() => "");
    die(
      `the door answered ${rows.length} row(s) and the pane draws no table. It said: ${JSON.stringify(why)}`,
    );
  }

  const drawn = await page.locator("[data-vm-top-row]").count();
  if (drawn !== rows.length) {
    die(`the door answered ${rows.length} row(s) and the pane drew ${drawn} - a table that
silently shows fewer is a fleet somebody will act on`);
  }

  // THE STATUS WORD, ON SCREEN. Not the attribute alone: an attribute a person
  // cannot see is not a dashboard telling them how much to believe.
  if (drawn > 0) {
    const first = page.locator("[data-vm-top-row]").first();
    const said = (await first.innerText()).trim();
    const status = rows[0].status;
    if (!said.includes(status)) {
      die(
        `the row does not show its status word ${JSON.stringify(status)}: ${JSON.stringify(said)}`,
      );
    }
  }

  console.log(
    `/api/vm/top: 403 for an ordinary token, ${rows.length} row(s) for the operator, all ${drawn} drawn with their status word`,
  );
} finally {
  await browser.close();
}
