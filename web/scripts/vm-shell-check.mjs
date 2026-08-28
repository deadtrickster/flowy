/**
 * The shell panel: a refusal, an empty state, and a wasm that is actually there.
 *
 *   node scripts/vm-shell-check.mjs BASE_URL OPERATOR_TOKEN OTHER_TOKEN
 *
 * See checks.d/console/vm-shell.sh for what this deliberately does NOT assert
 * and why - the guest half needs a real firecracker VM, and this suite normally
 * runs inside one.
 */

import { chromium } from "playwright";

const [base, operator, other] = process.argv.slice(2);
if (!base || !operator || !other) {
  console.error("usage: node scripts/vm-shell-check.mjs BASE_URL OPERATOR_TOKEN OTHER_TOKEN");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

// 1 - THE WASM IS SERVED. ghostty-web's init() probes /ghostty-vt.wasm and vite
// emits no asset nothing imports, so this file's absence is a console that
// builds green and throws when somebody presses Run.
const wasm = await fetch(`${base}/ghostty-vt.wasm`);
if (!wasm.ok) {
  die(`GET /ghostty-vt.wasm answered ${wasm.status}. ghostty's init() has no URL argument - it
probes this path - so the terminal panel throws the moment Run is pressed, on a
console that built without an error.`);
}
const wasmBytes = new Uint8Array(await wasm.arrayBuffer());
// A REAL WASM MODULE, not an SPA fallback. This node serves index.html for
// unknown paths, so a 200 here proves nothing on its own: the four magic bytes
// are what tell "the wasm is served" from "the router answered".
const magic = [0x00, 0x61, 0x73, 0x6d];
if (wasmBytes.length < 4 || !magic.every((b, i) => wasmBytes[i] === b)) {
  die(`/ghostty-vt.wasm answered 200 with ${wasmBytes.length} bytes that are not a wasm module -
this node serves index.html for unknown paths, so that is the SPA fallback and
the file is not really there.`);
}

// 2 - THE SOCKET IS OPERATOR-ONLY, ASSERTED AS A DIFFERENCE. A door that hands
// out a shell is the one place a guard must be proven rather than read.
// A PLAIN GET, AND THAT IS ENOUGH. The first version sent Connection: Upgrade
// and the websocket headers by hand, and node's fetch REFUSED - they are
// forbidden header names, so the request never left the process and the check
// died with "fetch failed" rather than measuring anything.
//
// It does not need them. operatorOnly wraps the route, so it decides before any
// websocket logic runs: a refused caller never reaches the upgrade, and an
// allowed one is turned away by the library for not asking to upgrade. Two
// different answers, which is the whole assertion.
const handshake = async (token) => {
  const res = await fetch(`${base}/api/agent/socket`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  return res.status;
};
const asOther = await handshake(other);
const asOperator = await handshake(operator);
if (asOther === asOperator) {
  die(`the socket answered ${asOther} for an ordinary token and ${asOperator} for the operator's.
Those being equal means the guard is not deciding anything - and this door hands
out a root shell on the host's VMs.`);
}
if (asOther < 400) {
  die(`a non-operator token got ${asOther} from the shell socket, which is not a refusal`);
}

// 3 - THE PANEL SAYS WHAT IT IS BEFORE ANYTHING RUNS.
const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), operator);
  await page.goto(`${base}/vms`, { timeout: 30_000 });

  const shell = page.locator("[data-vm-shell]");
  try {
    await shell.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    die("the VMs page draws no shell panel at all");
  }
  const state = await shell.getAttribute("data-vm-shell-state");
  if (state !== "idle") {
    die(`the panel opens in state ${JSON.stringify(state)} rather than idle - it is doing
something before anybody asked it to`);
  }
  // AN EMPTY STATE THAT SAYS SO. A terminal that failed to connect and one that
  // has not been started are the same black rectangle otherwise.
  const empty = await page
    .locator("[data-vm-shell-empty]")
    .innerText()
    .catch(() => "");
  if (!empty.trim()) {
    die(`the panel draws no empty state, so "nothing has been started" and "this failed to
connect" are the same picture`);
  }
  if (!/guest/i.test(empty)) {
    die(`the empty state does not say where what you type goes: ${JSON.stringify(empty)}. That
is the one thing a person needs to know before typing into a root shell.`);
  }
  const run = page.locator("[data-vm-shell-run]");
  if ((await run.count()) !== 1 || (await run.isDisabled())) {
    die("the panel offers no enabled Run control while idle");
  }

  console.log(
    `the wasm is served (${wasmBytes.length} bytes, real module); the socket answered ${asOther} for an ordinary token and ${asOperator} for the operator's; the panel is idle and says where the typing goes`,
  );
} finally {
  await browser.close();
}
