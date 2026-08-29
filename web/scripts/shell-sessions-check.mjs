// THE PANEL SEES THE HOST'S SESSIONS, INCLUDING ONES IT DID NOT START.
//
// The operator: "per project byobu session i can attach to just over ssh, so
// your stuff is just byobu management." The property that carries all of that
// is whether flowy is looking at the SAME sessions their editor uses - a panel
// listing only what it started would look identical and be useless.
//
// So the check makes a session the way their editor makes one, from outside
// flowy entirely, and asserts the console shows it. Removed again afterwards,
// whatever happens: a check that leaves a session behind has changed the host
// it was measuring.

import { execFileSync } from "node:child_process";
import { chromium } from "playwright";

const [base, operator, other] = process.argv.slice(2);
const die = (why) => {
  console.error(why);
  process.exit(1);
};
if (!base || !operator || !other) die("usage: shell-sessions-check.mjs BASE OPERATOR OTHER");

const ask = async (token) => {
  const res = await fetch(`${base}/api/shell/sessions`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  let body = null;
  try {
    body = await res.json();
  } catch {
    body = null;
  }
  return { status: res.status, body };
};

// OPERATOR-ONLY AS A DIFFERENCE: this names what is running on the machine
// serving the console, so one reading cannot tell the rule from its absence.
const asOther = await ask(other);
const asOperator = await ask(operator);
if (asOther.status === asOperator.status) {
  die(`/api/shell/sessions answered ${asOther.status} for both an ordinary token and the
operator's - the door is not telling them apart`);
}
if (asOther.status !== 403) {
  die(`an ordinary token got ${asOther.status}, and it must be refused`);
}

if (asOperator.status === 503) {
  // NO MULTIPLEXER. The refusal must say so rather than answering an empty
  // list, because "cannot hold a session" and "holds none" send somebody
  // looking in different places.
  const why = String(asOperator.body?.error ?? "");
  if (!/byobu|tmux/i.test(why)) {
    die(`503 without naming what is missing: ${JSON.stringify(why)}`);
  }
  if (Array.isArray(asOperator.body?.sessions)) {
    die(
      "503 AND a sessions array - a caller would read an empty host where the node meant it could not ask",
    );
  }
  console.log(
    `no byobu or tmux on this node: the door refused with 503 and said so; ${asOther.status} for an ordinary token`,
  );
  process.exit(0);
}
if (asOperator.status !== 200) {
  die(`/api/shell/sessions answered ${asOperator.status}: ${JSON.stringify(asOperator.body)}`);
}

// A SESSION MADE FROM OUTSIDE FLOWY, exactly as their editor names one.
const mux = (() => {
  for (const bin of ["byobu", "tmux"]) {
    try {
      execFileSync(bin, ["-V"], { stdio: "ignore" });
      return bin;
    } catch {}
  }
  return null;
})();
if (!mux) {
  console.log(
    `the node answered ${asOperator.body.sessions.length} session(s); no byobu or tmux out here to make one with, so the not-ours arm did not run`,
  );
  process.exit(0);
}

// TWO SESSIONS, AND THE SECOND IS THE POINT. One named the way their editor
// names one, and one named nothing like it.
//
// The first alone is too weak, and I proved that by breaking the door to return
// only projectile/* sessions - the check still passed, because the session it
// made was a projectile/ one. A list filtered to the convention would hide
// `main`, `24`, and every session somebody opened by hand, which is exactly
// what the component says it must not do.
const name = `projectile/flowy-check-${process.pid}`;
const stranger = `flowy-check-stranger-${process.pid}`;
const gone = () => {
  for (const s of [name, stranger]) {
    try {
      execFileSync(mux, ["kill-session", "-t", s], { stdio: "ignore" });
    } catch {}
  }
};
// ON EXIT, NOT IN A finally. die() calls process.exit, which does NOT run a
// finally block - so every failing run of this check left two sessions on the
// host, and I found that by reading `tmux ls` after a red rather than by
// reasoning about it. An exit handler runs however the process ends: a pass, a
// die, or a throw.
process.on("exit", gone);

try {
  execFileSync(mux, ["new-session", "-d", "-s", name], { stdio: "ignore" });
  execFileSync(mux, ["new-session", "-d", "-s", stranger], { stdio: "ignore" });
} catch (err) {
  gone();
  console.log(`cannot make a session here (${err}); the not-ours arm did not run`);
  process.exit(0);
}

const browser = await chromium.launch();
try {
  const after = await ask(operator);
  // THE STATUS FIRST. `body?.sessions ?? []` turns a refusal into an empty
  // host, which is the exact confusion this door exists to prevent - and it
  // made this check report "the door named []" when the door may have said it
  // could not look. Read what it answered before reading what it listed.
  if (after.status !== 200) {
    die(`after making two sessions the door answered ${after.status}: ${JSON.stringify(after.body)}

That is not an empty host. It is the node saying something else, and the arm
below would have reported it as sessions that are missing.`);
  }
  if (!Array.isArray(after.body?.sessions)) {
    die(
      `the door answered 200 with no sessions array: ${JSON.stringify(after.body).slice(0, 200)}`,
    );
  }
  const names = after.body.sessions.map((s) => s.name);
  if (!names.includes(name)) {
    die(`a session made outside flowy is not in the door's answer. It named: ${JSON.stringify(names)}

That is the whole property: the panel must see the sessions the operator's own
editor and ssh use, not only the ones it started.`);
  }
  const row = after.body.sessions.find((s) => s.name === name);
  if (row.ours !== true) {
    die(`${name} follows the projectile/ convention and the door says ours=${row.ours}`);
  }
  // AND THE ONE THAT IS NOT OURS. A door that answered only the convention
  // would pass everything above and still hide every session the operator
  // opened by hand.
  if (!names.includes(stranger)) {
    die(`${stranger} does not follow the projectile/ convention and the door left it out.
It named: ${JSON.stringify(names)}

The list is the HOST'S sessions. Filtering it to ours hides main, the numbered
ones, and anything somebody started by hand - which is the list they most want.`);
  }
  const oddOne = after.body.sessions.find((s) => s.name === stranger);
  if (oddOne.ours !== false) {
    die(`${stranger} is not a projectile/ session and the door says ours=${oddOne.ours}`);
  }

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
    console.log(
      `the door listed ${names.length} session(s) including one made outside flowy; the page is in state ${JSON.stringify(vmState)} so the pane was not drawn`,
    );
    process.exit(0);
  }

  await page.locator('[data-vm-tab="shells"]').click();
  await page.locator("[data-shell-sessions-fold] summary").click();
  const shown = page.locator(`[data-shell-session="${name}"]`);
  await shown.waitFor({ state: "visible", timeout: 15_000 }).catch(() => {});
  if ((await shown.count()) === 0) {
    const drew = await page
      .locator("[data-shell-sessions]")
      .innerText()
      .catch(() => "");
    die(
      `the door lists ${name} and the pane does not draw it: ${JSON.stringify(drew.slice(0, 300))}`,
    );
  }
  // THE LINE SOMEBODY TYPES ELSEWHERE, which is the answer to the question that
  // started this - reaching the session from a terminal that is not this one.
  const said = await shown.innerText();
  if (!said.includes(`byobu attach -t ${name}`)) {
    die(`the row does not say how to attach to it from a shell: ${JSON.stringify(said)}`);
  }

  // AND PICKING ONE OPENS IT HERE. The list existing is half of it; the other
  // half is that a session flowy never started can be joined from the panel
  // rather than only from an ssh somewhere else.
  //
  // ASSERTED ON THE WIRE, because opening it needs the socket and this browser
  // holds a token rather than a session, so the handshake is refused - see the
  // note in vm-shell-check. What is measured is the request the panel makes:
  // the name it asks to join.
  // THE TWO ENDS SPELL THE SESSION NAME THE SAME WAY.
  //
  // The rule lives in three places on purpose - internal/flowy/byobusession.go,
  // web/src/lib/api.ts, and their own init.el - because the panel needs a name
  // before any socket has answered and the node needs one with no browser at
  // all. Three copies is fine; three copies that DISAGREE is a shell somebody
  // cannot find.
  //
  // ASKED WITH A DOTTED NAME, and that is the whole point of doing it here. The
  // first version of this arm compared the rule against whatever project the
  // gate happened to offer - all of which are plain words - so removing the dot
  // from the sanitising changed nothing and the arm passed. The dot is the case
  // that matters: tmux silently turns it into an underscore, so a console that
  // stopped replacing it would address a session the node never makes.
  //
  // /shell takes the project from the query, so any name can be asked for
  // without needing a firecode project named that way.
  const shellOf = () => page.locator("[data-vm-shell]").first();
  for (const asked of ["a.b", "with space", "co:lon", "plain-name"]) {
    await page.goto(`${base}/shell?project=${encodeURIComponent(asked)}&slot=0`, {
      timeout: 30_000,
    });
    await shellOf().waitFor({ state: "visible", timeout: 15_000 });
    const spelled = await shellOf().getAttribute("data-vm-shell-session-name");
    const want = `projectile/${asked.replace(/[.: ]/g, "_")}`;
    if (spelled !== want) {
      die(`for project ${JSON.stringify(asked)} the console would join ${JSON.stringify(spelled)},
and the rule says ${JSON.stringify(want)}. The two ends have drifted, and the panel
would open a session the node never makes.`);
    }
  }
  await page.goto(`${base}/vms`, { timeout: 30_000 });
  await page.locator('[data-vm-tab="shells"]').click();
  await page.locator("[data-shell-sessions-fold] summary").click();

  const onFirst = await shellOf().getAttribute("data-vm-shell-mux");
  if (onFirst) {
    die(
      `the pane opens already pointed at ${JSON.stringify(onFirst)}; it should start on the project's own session`,
    );
  }
  await page.locator(`[data-shell-session-open="${stranger}"]`).click();
  await page.waitForTimeout(1000);
  const onPicked = await shellOf().getAttribute("data-vm-shell-mux");
  if (onPicked !== stranger) {
    die(`picking ${stranger} out of the list left the pane on ${JSON.stringify(onPicked)}.

Picking a session has to change which one the panel joins, or the list is
decoration and the panel always opens the project's own.`);
  }

  console.log(
    `the panel lists ${names.length} session(s) on this host including ${name}, which flowy did not start, says how to attach to it, and switches to ${stranger} when that one is picked`,
  );
} finally {
  await browser.close();
}
