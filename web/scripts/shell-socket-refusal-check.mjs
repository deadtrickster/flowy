/**
 * A REFUSED HANDSHAKE IS NOT A SHELL THAT ENDED.
 *
 *   node scripts/shell-socket-refusal-check.mjs BASE_URL OPERATOR_TOKEN
 *
 * 01M154DRDCD3P2M8FKNG0RCC30. A browser cannot put an Authorization header on a
 * websocket handshake - there is no API for it - so /api/agent/socket can only
 * authenticate a browser by its session cookie. A console whose credential is a
 * token in localStorage, which every other panel on the page accepts, is turned
 * away. The panel used to render that as state "ended" with the words "the
 * connection to this node closed - the VM may still be running", so "log in"
 * and "your shell died" were the same screen, about a socket that never opened
 * and a VM that was never asked for.
 *
 * THIS CHECK IS THAT CONSOLE. It authenticates the page exactly the way the
 * suite's other console checks do - the operator's token in localStorage, no
 * cookie - which is precisely the credential the row is about. So the refusal
 * is reached deterministically here rather than needing a second kind of user.
 *
 * IT ASSERTS THE DISTINCTION, NOT THE WORDING. Three things, each of which was
 * wrong before: the state is "refused" and not "ended"; the reason names how
 * the handshake carries a credential, so a person knows to log in rather than
 * to go looking for a dead guest; and the reason does NOT claim a VM may still
 * be running, which is the generic close message that used to overwrite the
 * accurate one a tick later because onerror and onclose both fired.
 *
 * IT STARTS NOTHING. "this host" is selected before Run, and the socket is
 * refused before any shell exists, so there is nothing to leave behind.
 */

import { chromium } from "playwright";

const [base, operator] = process.argv.slice(2);
if (!base || !operator) {
  console.error("usage: node scripts/shell-socket-refusal-check.mjs BASE_URL OPERATOR_TOKEN");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  // A TOKEN AND NOTHING ELSE. No cookie is set, deliberately - that is the
  // console this row is about, and adding a session here would test the one
  // case that already worked.
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), operator);
  await page.goto(`${base}/vms`, { timeout: 30_000 });

  const shell = page.locator("[data-vm-shell]").first();
  await shell.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await page.locator("[data-vm-shell]").count()) === 0) {
    // vm-shell-check owns "this host cannot run VMs and says so". Saying it
    // here too would be a second check reporting the same environment.
    console.log(
      "no shell panel on /vms for the operator's token, so there is no handshake to refuse - vm-shell-check.mjs owns that case",
    );
    process.exit(0);
  }

  await page.locator("[data-vm-shell-where]").selectOption("host");
  await page.locator("[data-vm-shell-run]").click();

  // Wait for it to settle out of idle/starting. The refusal arrives on the
  // socket's own events, which is a network round trip and not a render.
  let state = "";
  for (let i = 0; i < 120; i++) {
    state = (await shell.getAttribute("data-vm-shell-state")) ?? "";
    if (state !== "idle" && state !== "starting") break;
    await page.waitForTimeout(500);
  }

  const why = await page
    .locator("[data-vm-shell-why]")
    .innerText()
    .catch(() => "");

  if (state === "live") {
    die(`the shell socket OPENED for a console holding only a token, with no session cookie.
That is not what this node is supposed to allow, and if it is now allowed on
purpose then this check is the thing that is out of date - but a websocket door
that takes a bearer token is a change nobody should learn about from a check
that silently kept passing.`);
  }

  if (state !== "refused") {
    die(`the handshake was refused and the panel calls it ${JSON.stringify(state)}.
"This browser has no session, log in" and "your shell died" are then the same
screen, and only one of them is the shell's fault. It said: ${JSON.stringify(why)}`);
  }

  // THE REASON HAS TO BE ACTIONABLE. A distinct state that says nothing useful
  // moves the problem rather than fixing it.
  if (!/websocket/i.test(why) || !/cookie/i.test(why)) {
    die(`the panel refuses without naming the mechanism, so a person cannot tell what to do
about it: ${JSON.stringify(why)}. It should say that a browser cannot put an
Authorization header on a websocket handshake and that this door reads a
session cookie.`);
  }

  // THE OVERWRITE, GUARDED DIRECTLY. onerror fires and then onclose fires; if
  // the second one is allowed through, this generic sentence replaces the
  // accurate one and invents a VM.
  if (/may still be running/i.test(why)) {
    die(`the refusal was overwritten by the generic close message: ${JSON.stringify(why)}.
onerror and onclose both fire for a refused handshake, so the accurate reason
has to win - and there is no VM here that could still be running.`);
  }

  console.log(
    `a console holding only a token is refused the shell socket, and the panel says so: state ${JSON.stringify(state)}, and the reason names the handshake rather than reporting a dead shell`,
  );
} finally {
  await browser.close();
}
