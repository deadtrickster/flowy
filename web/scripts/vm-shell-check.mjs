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

// 3 - WHAT THE PAGE OFFERS DEPENDS ON WHETHER THIS HOST CAN RUN VMs AT ALL,
// and both halves are asserted rather than one being assumed.
//
// api_vm.go answers 503 - not an empty list - when firecode is absent, because
// "no VMs are running" and "this node cannot run VMs" are different facts. The
// page honours that by drawing the refusal INSTEAD of the panel, and a Run
// button on a host that cannot run anything would be exactly the dead button
// this repo keeps filing. So which arm runs is decided by what the node says,
// and neither outcome is a skip.
const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), operator);
  await page.goto(`${base}/vms`, { timeout: 30_000 });

  const panel = page.locator("[data-vm-panel]");
  await panel.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await panel.count()) === 0) {
    die("the /vms page drew nothing at all");
  }
  // The page reports its own state, so this reads which world it is in rather
  // than inferring it from what happens to be on screen.
  let vmState = await panel.getAttribute("data-vm-state");
  for (let i = 0; i < 40 && vmState === "reading"; i++) {
    await page.waitForTimeout(250);
    vmState = await panel.getAttribute("data-vm-state");
  }

  let share = 0;
  if (vmState !== "ok") {
    // THIS HOST CANNOT RUN VMs - the ordinary case in the gate, which runs
    // inside a firecode guest. The assertion is that nothing offers to run a
    // shell, because there is nothing to run one on.
    if ((await page.locator("[data-vm-shell-run]").count()) !== 0) {
      die(`the page is in state ${JSON.stringify(vmState)} - this host cannot run VMs - and it
still offers a Run control. A button that cannot do anything is the failure
api_vm.go answers 503 rather than an empty list to avoid.`);
    }
    const said = (await panel.innerText()).toLowerCase();
    if (!said.includes("firecode")) {
      die(`the page cannot run VMs and does not say why: ${JSON.stringify(said.slice(0, 200))}`);
    }
    console.log(
      `the wasm is served (${wasmBytes.length} bytes, real module); the socket answered ${asOther} for an ordinary token and ${asOperator} for the operator's; this host cannot run VMs and the page says so instead of offering a Run control`,
    );
  } else {
    const shell = page.locator("[data-vm-shell]");
    try {
      await shell.waitFor({ state: "visible", timeout: 20_000 });
    } catch {
      die("this host can run VMs and the page draws no shell panel");
    }
    const shellState = await shell.getAttribute("data-vm-shell-state");
    if (shellState !== "idle") {
      die(`the panel opens in state ${JSON.stringify(shellState)} rather than idle - it is doing
something before anybody asked it to`);
    }
    // AN EMPTY STATE THAT SAYS SO. A terminal that failed to connect and one
    // that has not been started are the same black rectangle otherwise.
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

    // THE SCREEN FILLS ITS BOX, asserted as GEOMETRY rather than as a class.
    // It carried min-h-[320px], which is 320px tall in a 900px pane and 320px
    // tall in a 200px one - a number wrong in both directions. A check reading
    // the className would have passed on exactly that.
    const screen = page.locator("[data-vm-shell-screen]");
    const grew = async () => {
      const box = await screen.boundingBox();
      return box?.height ?? 0;
    };
    await page.setViewportSize({ width: 1400, height: 700 });
    const short = await grew();
    await page.setViewportSize({ width: 1400, height: 1200 });
    // The observer debounces, so the assertion waits rather than reading the
    // frame it happened to land on.
    let tall = short;
    for (let i = 0; i < 40 && tall <= short; i++) {
      await page.waitForTimeout(250);
      tall = await grew();
    }
    if (!(tall > short)) {
      die(`the terminal is ${short}px in a 700px window and ${tall}px in a 1200px one - it is
not following its container, which is what a fixed height looks like from here.`);
    }

    // AND IT FLOATS. The panel comes out of the column and goes back, and the
    // page says where it went - a floating panel with nothing in its place
    // reads as a terminal that vanished.
    const toggle = page.locator("[data-vm-shell-float-toggle]");
    if ((await toggle.count()) !== 1) die("the panel offers no way to float");
    await toggle.click();
    try {
      await page.locator("[data-vm-shell-float]").waitFor({ state: "visible", timeout: 10_000 });
    } catch {
      die("float was pressed and no floating panel appeared");
    }
    if ((await page.locator("[data-vm-shell-docked-slot]").count()) !== 1) {
      die(`the panel floated and left nothing behind in the column, so from the page it is
indistinguishable from a terminal that disappeared`);
    }
    if ((await page.locator("[data-vm-shell][data-vm-shell-floating='yes']").count()) !== 1) {
      die("the floating panel does not say it is floating");
    }
    await page.locator("[data-vm-shell-dock]").click();
    try {
      await page.locator("[data-vm-shell-float]").waitFor({ state: "detached", timeout: 10_000 });
    } catch {
      die("dock was pressed and the floating panel stayed");
    }
    // AND IT COMES BACK.
    //
    // WHERE THIS ARM RUNS, said out loud: only on a host that can run VMs, the
    // same as the float and geometry arms above it. The gate runs inside a
    // firecode guest and takes the other branch, so a green gate is NOT a
    // reading on this. It is exercised by running this suite on a host with
    // firecode, which is where the bug was reported from.
    //
    // Navigating away unmounts the panel; coming back must
    // reattach to a shell that is still running, or the shell looks lost until
    // somebody presses Run - which is the bug this arm exists for.
    //
    // MEASURED AS A DIFFERENCE, and on the SOCKET rather than on the screen:
    // with a remembered session the panel must go and ask, without one it must
    // not. A check that only looked at the screen could not tell "adopted
    // nothing" from "never asked", which are the two states this fix is
    // between.
    //
    // NOT the attach frame's contents, and the reason is worth knowing. A
    // browser cannot put an Authorization header on a WebSocket handshake, so
    // a console holding only a token - which is what this check holds - is
    // refused at /api/agent/socket and no frame is ever sent. The panel still
    // demonstrably TRIED, which is the property under test here. That the
    // request carries adopt, and that the node starts nothing when it does, is
    // asserted where it can be: TestAdoptDoesNotStartAnything, on the registry
    // count rather than on a message.
    // A PROJECT IS CHOSEN FIRST, because the page does not choose one for you:
    // the select starts on "choose one" and the panel's project is empty until
    // somebody picks. Empty here is the page's honest answer, not a race, so
    // the check does what a person does rather than waiting for something that
    // is never going to arrive.
    const picker = page.locator("[data-vm-project]");
    const names = await picker
      .locator("option")
      .evaluateAll((os) => os.map((o) => o.value).filter((v) => v));
    if (names.length === 0) {
      die("this host can run VMs and the project select offers nothing to run one over");
    }
    await picker.selectOption(names[0]);
    const project = await shell.getAttribute("data-vm-shell-project");
    if (project !== names[0]) {
      die(`the project select was set to ${JSON.stringify(names[0])} and the panel says
${JSON.stringify(project)} - a panel adopting a shell in a project nobody chose is a shell on a
machine nobody asked for`);
    }

    // WHAT THE PAGE DID, not only what it sent. A panel that never opened a
    // socket and one that opened it and stayed quiet are different failures,
    // and the message has to say which.
    const sockets = [];
    const oops = [];
    page.on("websocket", (ws) => sockets.push(ws.url()));
    page.on("pageerror", (e) => oops.push(String(e)));
    page.on("console", (m) => {
      if (m.type() === "error") oops.push(m.text());
    });

    const attachesOnMount = async (seed) => {
      const before = sockets.length;
      await page.evaluate(
        ([key, value]) => (value ? localStorage.setItem(key, value) : localStorage.removeItem(key)),
        [`flowy.vmshell.session.${project}.0`, seed],
      );
      // Away and back, which is the operator's gesture rather than a reload.
      await page.goto(`${base}/chat/general`, { timeout: 30_000 });
      await page.goto(`${base}/vms`, { timeout: 30_000 });
      // AND THE PROJECT IS CHOSEN AGAIN, because the page does not remember
      // which one you were in - the select comes back on "choose one", and a
      // panel with no project has no session to ask for. So this is part of
      // the gesture under test, not scaffolding around it.
      await page.locator("[data-vm-project]").selectOption(names[0]);
      await page.waitForTimeout(3_000);
      return sockets.length - before;
    };

    const withMemory = await attachesOnMount("01SOMEREMEMBEREDSESSION");
    if (withMemory === 0) {
      die(`the panel remembered a session and did not go and ask for it on mount - coming back to
this page leaves a running shell looking lost until somebody presses Run.
page errors: ${JSON.stringify(oops.slice(0, 3))}`);
    }
    const withoutMemory = await attachesOnMount("");
    if (withoutMemory !== 0) {
      die(`the panel opened ${withoutMemory} socket(s) on mount with nothing remembered, so
opening /vms goes looking for a shell nobody asked for`);
    }

    // THE SHELLS PANE IS THE WHOLE PANEL, which is the operator's complaint
    // stated as a number. The strip used to be the last thing on a page that
    // opens with a header, a picker, a spawn form and the list of running VMs -
    // "with three listed shells tabs already pushed to the bottom and squished".
    //
    // A RATIO, not a pixel count, because the panel's height is the window's and
    // this runs at whatever viewport the arm before it left behind. Two thirds
    // is well under what the layout gives (the tab bar and the picker are the
    // only things above it) and well over what the old page ever could.
    const tabs = page.locator("[data-vm-tabs]");
    if ((await tabs.count()) !== 1) {
      die("the vms page offers no way to switch between agents and shells");
    }
    await page.locator('[data-vm-tab="agents"]').click();
    if ((await page.locator('[data-vm-pane="shells"]').isVisible()) === true) {
      die(
        "the shells pane is still on screen with the agents tab chosen, so the tabs are decoration",
      );
    }
    await page.locator('[data-vm-tab="shells"]').click();
    const panelBox = await page.locator("[data-vm-panel]").boundingBox();
    const screenBox = await page.locator("[data-vm-shell-screen]").boundingBox();
    if (!panelBox || !screenBox) die("the shells pane draws no terminal to measure");
    share = screenBox.height / panelBox.height;
    if (share < 0.66) {
      die(`the terminal is ${Math.round(screenBox.height)}px of a ${Math.round(panelBox.height)}px panel - ${Math.round(share * 100)}% - so it is
sharing the pane with something instead of being it`);
    }

    console.log(
      `the wasm is served (${wasmBytes.length} bytes, real module); the socket answered ${asOther} for an ordinary token and ${asOperator} for the operator's; the panel is idle and says where the typing goes; it goes looking for its shell on mount only when it remembers one; and the shells pane is ${Math.round(share * 100)}% of the panel`,
    );
  }
} finally {
  await browser.close();
}
