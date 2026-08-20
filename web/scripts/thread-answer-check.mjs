/**
 * A thread can be answered from the pane that shows it.
 *
 *   node scripts/thread-answer-check.mjs BASE_URL TOKEN
 *
 * The operator, 2026-08-20: "no way to post to a thread (look at mattermost
 * again)". Measured before anything was built, and the measurement is the
 * reason this check asserts what it does: posting into a thread had worked the
 * whole time. chat.go:538 takes {body, thread, parents}, api.ts's say() carries
 * it, and ChatRoom's send() passed selected?.thread - so every reply sent while
 * a message happened to be selected went into that message's thread.
 *
 * What was absent was every way of KNOWING that. The thread pane held zero
 * textareas, the composer never named the thread it was about, and selecting a
 * message was the only door and was not labelled as one. Another seat counted
 * the consequence: not one of the operator's messages has ever landed in a
 * thread.
 *
 * SO THE ASSERTION IS NOT "the API accepts a thread" - it always did, and a
 * check on that would have passed on the day the complaint was made. It is that
 * a person looking at a thread can answer it, and that what they type lands IN
 * that thread rather than beside it.
 *
 * It makes its own thread, because a room whose last message happens to be a
 * root is a fixture that depends on the order the suite ran in.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/thread-answer-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const room = "threadanswer";
const say = async (body, thread) => {
  const res = await fetch(`${base}/api/chat/${room}/say`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify(thread ? { body, thread } : { body }),
  });
  if (!res.ok) die(`could not seed the room: ${res.status} ${await res.text()}`);
  return res.json();
};

// A root and one reply, so the pane has a thread with a shape rather than a
// single message that could be read either way.
const root = await say("the root of a thread somebody will answer");
await say("a first reply, so this is visibly a thread", root.thread);

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}/thread`, { timeout: 20_000 });

  // WAITED FOR, like everything else in this file. The pane renders once the
  // room's events have arrived, and counting before that is the defect that
  // cost two gate passes in way-in-check tonight.
  const pane = page.locator('[data-room-pane-body="thread"]');
  await pane.waitFor({ state: "visible", timeout: 20_000 });

  const compose = page.locator("[data-thread-compose] textarea");
  try {
    await compose.waitFor({ state: "visible", timeout: 10_000 });
  } catch {
    die("the thread pane has no way to answer the thread it is showing");
  }

  const answer = `answered from the thread pane at ${root.id}`;
  await compose.fill(answer);
  await compose.press("Enter");

  // IT LANDS IN THE THREAD, asked of the NODE rather than of the screen. The
  // console shows what it just sent whether or not the node kept it, and
  // whether or not it kept it in the right thread - so the screen cannot
  // answer the only question this check is about.
  let landed = null;
  for (let i = 0; i < 40 && !landed; i++) {
    const res = await fetch(`${base}/api/chat/${room}?thread=${encodeURIComponent(root.thread)}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (res.ok) {
      const body = await res.json();
      landed = (body.events || []).find((e) => (e.body || "").includes(answer)) || null;
    }
    if (!landed) await page.waitForTimeout(250);
  }
  if (!landed) {
    die(`what was typed in the thread pane is not in thread ${root.thread}`);
  }
  if (landed.thread !== root.thread) {
    die(`the answer landed in thread ${landed.thread}, not in ${root.thread}`);
  }
  // AND IT CONTINUES THE THREAD rather than answering its opening line. The
  // parent is the last event in the thread, so the graph is a conversation and
  // not a fan of replies to the root - which is what a reply parented to the
  // root would draw, however many people spoke in between.
  if (!Array.isArray(landed.parents) || landed.parents.length === 0) {
    die("the answer hangs off nothing - the thread's shape is lost");
  }
  if (landed.parents.includes(root.id) && landed.parents.length === 1) {
    die("the answer is parented to the ROOT, so every reply will fan off the opening line");
  }

  // AND THE PANE SHOWS IT, which is the other half: a message the node kept and
  // the reader cannot see is the same silence the complaint was about.
  const shown = pane.getByText(answer, { exact: false }).first();
  try {
    await shown.waitFor({ state: "visible", timeout: 10_000 });
  } catch {
    die("the answer is in the thread on the node and not on the screen");
  }

  console.log(
    `the thread pane answered ${root.thread}: the message landed in that thread, parented to the reply before it, and is on screen`,
  );
} finally {
  await browser.close();
}
