/**
 * A thread's replies fold into its head row in the room stream, per reader.
 *
 *   node scripts/thread-collapse-check.mjs BASE_URL TOKEN SPEAKER_TOKEN
 *
 * The operator, 01M0WF2T2: hiding followup messages in threads from the main
 * room stream. The contract, answered on the row:
 *
 *   - the root stays in the stream for everyone, and its replies collapse into
 *     it with a count and enough of the latest one to know whether to open it;
 *   - a reply ADDRESSED TO THE READER still surfaces in the stream - it does
 *     not disappear into the collapse, because this fleet's recurring failure
 *     is silence reading as absence;
 *   - a RAISE stays in the stream too - a raise message joins the thread it
 *     came out of, and a folded raise reads as work never filed. That arm
 *     lives in rowcard-check, whose raise is exactly such a reply.
 *   - collapsing is PER-READER state, stored on the node, not a property of
 *     the room;
 *   - opening the thread shows everything, which already works.
 *
 * THREE ARMS, of which the last is the one a component test would miss:
 *
 *   1. a room holding a threaded conversation shows one row rather than eight;
 *   2. opening the thread shows all of them;
 *   3. as the addressee of one reply, that reply is in the stream while the
 *      rest stay collapsed.
 *
 * Plus the stored half of per-reader: unfolding a thread in the stream
 * survives a reload, and folding it back hides them again - without that, a
 * client-side toggle would pass everything this check measures.
 *
 * TWO THREADS IN ONE ROOM, so the arms hold at once: A has eight messages and
 * nothing addressed, B has eight of which one is addressed to the reader. The
 * addressed reply is the NEWEST in B on purpose: the fold block's snippet must
 * skip a visible reply and summarise the latest HIDDEN one, and seeding the
 * addressed one last is what catches a snippet that just takes the thread's
 * newest message.
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, token, speaker] = process.argv.slice(2);
if (!base || !token || !speaker) {
  console.error("usage: node scripts/thread-collapse-check.mjs BASE_URL TOKEN SPEAKER_TOKEN");
  process.exit(2);
}

refuseRemote(base, "thread-collapse-check");

const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

const room = "threadcollapse";
const api = async (path, init = {}, as = token) => {
  const res = await fetch(`${base}${path}`, {
    ...init,
    headers: {
      Authorization: `Bearer ${as}`,
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
  });
  if (!res.ok) die(`${path}: ${res.status} ${await res.text()}`);
  return res.json();
};

// WHO THIS TOKEN IS, asked rather than assumed: the addressed arm turns on the
// node's own addressee resolution matching the reader's user or agent id, and
// a check that hardcoded a handle would pass against the wrong person's row.
const who = await api("/api/whoami");
const mine = await api("/api/me");
const handle = mine.user?.handle;
if (!handle) die("this token's user has no handle, so nothing can be addressed to them");

// THE FIXTURE, one thread at a time and sequential, not parallel: the node
// stamps an ordering and a burst of concurrent writes is a fixture whose order
// depends on the scheduler.
const stamp = `${Date.now().toString(36)}`;
const word = (n) => `collapse ${stamp} ${n}`;

const say = async (body, extra = {}, as = token) =>
  api(
    `/api/chat/${encodeURIComponent(room)}/say`,
    {
      method: "POST",
      body: JSON.stringify({ body, ...extra }),
    },
    as,
  );

// Thread A: a root and seven replies, none addressed to anybody.
const rootA = await say(`thread-collapse-check: root A ${word("a0")}`);
const aReplies = [];
for (let i = 1; i <= 7; i++) {
  aReplies.push(
    await say(`thread-collapse-check: reply A${i} ${word(`a${i}`)}`, { thread: rootA.thread }),
  );
}

// Thread B: a root, six plain replies spoken by the other principal, and one
// reply ADDRESSED TO THE READER, seeded last so it is the thread's newest.
const rootB = await say(`thread-collapse-check: root B ${word("b0")}`);
const bReplies = [];
for (let i = 1; i <= 6; i++) {
  bReplies.push(
    await say(
      `thread-collapse-check: reply B${i} ${word(`b${i}`)}`,
      { thread: rootB.thread },
      speaker,
    ),
  );
}
const bAddr = await say(
  `thread-collapse-check: reply B7 for the reader ${word("b7")}`,
  { thread: rootB.thread, to: handle },
  speaker,
);

// WHAT THE NODE HOLDS, read back before the browser opens. If the fixture
// seeded differently than intended, this fails as a fixture problem instead
// of the screen answering a question about the wrong room.
for (const [name, thread, wanted] of [
  ["A", rootA.thread, 8],
  ["B", rootB.thread, 8],
]) {
  const back = await api(
    `/api/chat/${encodeURIComponent(room)}?thread=${encodeURIComponent(thread)}`,
  );
  const got = (back.events ?? []).length;
  if (got !== wanted) die(`thread ${name} holds ${got} messages on the node, wanted ${wanted}`);
}
const bAddrBack = (
  await api(`/api/chat/${encodeURIComponent(room)}?thread=${encodeURIComponent(rootB.thread)}`)
).events.find((e) => e.id === bAddr.id);
if (!bAddrBack?.addressee || ![who.user, who.agent].includes(bAddrBack.addressee)) {
  die(
    `the addressed reply's addressee is ${bAddrBack?.addressee ?? "unset"}, not this token's user ${who.user} or agent ${who.agent} - the node resolved "to: ${handle}" to nobody this reader is`,
  );
}

// The sixteen ids this check cares about, with what the room must show.
const all = [rootA, ...aReplies, rootB, ...bReplies, bAddr];
const visibleInStream = new Set([rootA.id, rootB.id, bAddr.id]);
const hiddenA = new Set(aReplies.map((e) => e.id));
const hiddenB = new Set(bReplies.map((e) => e.id));

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  const openRoom = async () => {
    await page
      .goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 })
      .catch(() => {});
    await page
      .locator(`[data-message="${rootA.id}"]`)
      .waitFor({ state: "visible", timeout: 20_000 })
      .catch(() => {});
  };

  // ---- ARM 1: one row rather than eight, per thread, for this reader ----
  await openRoom();

  // The whole fixture at once: exactly the heads and the addressed reply are
  // in the stream. Counted per id rather than as a bare total, so the message
  // that says which row is missing rather than just how many.
  const missing = [];
  const extra = [];
  for (const e of all) {
    const n = await page.locator(`[data-message="${e.id}"]`).count();
    if (visibleInStream.has(e.id) && n !== 1) missing.push(`${e.id} (${n})`);
    if (!visibleInStream.has(e.id) && n !== 0) extra.push(`${e.id} (${n})`);
  }
  if (missing.length || extra.length) {
    die(
      `the stream is not one row per thread plus the addressed reply:
missing: ${missing.join(", ") || "none"}
extra: ${extra.join(", ") || "none"}`,
    );
  }
  if (crashes.length > 0) die(`the room threw: ${crashes.join("; ")}`);

  // The fold control on A: a count of what is hidden, and the LATEST hidden
  // reply's words - now in the title rather than in the row.
  //
  // 2026-08-28, the operator: "we dont need this '3 hidden - flowy-claude:...'
  // we already see '3 replies'. So keep only '3 replies' and 'show replies' and
  // pin them to the hard-right". The snippet said the same thing as the count
  // beside it and was the longest item in a row with no room to spare.
  //
  // THE ASSERTION IS NOT RELAXED, IT IS RE-POINTED. What had to stay true is
  // that a reader can find out what is hidden without opening the thread, and
  // that is still true - one hover instead of always on screen. Checking
  // innerText after the words moved to the title would be checking where they
  // are drawn rather than whether they are reachable, which is how a check ends
  // up defending a layout nobody asked for.
  const foldA = page.locator(`[data-message="${rootA.id}"] [data-fold="${rootA.thread}"]`);
  await foldA.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
  if ((await foldA.count()) === 0) die(`thread A's head row carries no fold block`);
  const countA = await foldA.first().getAttribute("data-fold-count");
  if (countA !== "7") die(`thread A's fold block says ${countA} hidden, the node holds 7`);
  const foldATitle = (await foldA.first().getAttribute("title")) ?? "";
  if (!foldATitle.includes(word("a7"))) {
    die(`thread A's fold control does not offer the latest reply's words:\n${foldATitle}`);
  }
  if ((await page.locator(`[data-fold-show="${rootA.thread}"]`).count()) === 0) {
    die("thread A's fold block offers no way to show the replies in the stream");
  }

  // ---- ARM 3: the addressed reply is IN the stream while its thread stays
  // collapsed, and the block summarises the latest HIDDEN reply, not the
  // addressed one that is visible as its own row. ----
  const foldB = page.locator(`[data-message="${rootB.id}"] [data-fold="${rootB.thread}"]`);
  await foldB.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
  if ((await foldB.count()) === 0) die(`thread B's head row carries no fold block`);
  const countB = await foldB.first().getAttribute("data-fold-count");
  if (countB !== "6") {
    die(
      `thread B's fold block says ${countB} hidden - the addressed reply is visible as a row, so the count is 6`,
    );
  }
  // Same move as A: the words are in the title now. The SECOND assertion here
  // is the one that matters most and is unchanged in substance - the offered
  // reply must be the latest HIDDEN one, not the addressed reply that is drawn
  // as its own row. That is a fact about which message the console picked, and
  // moving the words to a title does not soften it.
  const foldBTitle = (await foldB.first().getAttribute("title")) ?? "";
  if (!foldBTitle.includes(word("b6"))) {
    die(`thread B's fold control does not offer the latest hidden reply's words:\n${foldBTitle}`);
  }
  if (foldBTitle.includes(word("b7"))) {
    die(
      `thread B's fold control offers the addressed reply (b7), which is drawn as its own row - it must offer the latest HIDDEN reply`,
    );
  }

  // ---- ARM 2: opening the thread shows all of them ----
  await page.locator(`[data-message="${rootB.id}"] [data-thread-open="${rootB.id}"]`).click();
  const pane = page.locator('[data-room-pane-body="thread"]');
  await pane.waitFor({ state: "visible", timeout: 15_000 }).catch(() => {});
  const paneId = await page
    .locator("[data-thread-pane-id]")
    .first()
    .getAttribute("data-thread-pane-id")
    .catch(() => null);
  if (paneId !== rootB.thread) {
    die(`opening thread B's head left the pane on ${paneId ?? "nothing"}, not ${rootB.thread}`);
  }
  for (const e of [rootB, ...bReplies, bAddr]) {
    const n = await pane.locator(`[data-body="${e.id}"]`).count();
    if (n !== 1)
      die(
        `the thread pane shows ${e.id} ${n} times - the collapsed stream must not leak into the pane`,
      );
  }
  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);

  // ---- THE STORED HALF: unfolding is per-reader state on the node ----
  await openRoom();
  await page.locator(`[data-fold-show="${rootA.thread}"]`).click();
  for (const e of aReplies) {
    await page
      .locator(`[data-message="${e.id}"]`)
      .waitFor({ state: "visible", timeout: 10_000 })
      .catch(() => {});
    if ((await page.locator(`[data-message="${e.id}"]`).count()) !== 1) {
      die(`show replies did not put ${e.id} in the stream`);
    }
  }
  if ((await page.locator(`[data-fold-hide="${rootA.thread}"]`).count()) === 0) {
    die("an unfolded thread offers no way to hide its replies again");
  }

  // A fresh load must still show them - the state is the node's, not the tab's.
  await openRoom();
  for (const e of aReplies) {
    await page
      .locator(`[data-message="${e.id}"]`)
      .waitFor({ state: "visible", timeout: 10_000 })
      .catch(() => {});
    if ((await page.locator(`[data-message="${e.id}"]`).count()) !== 1) {
      die(
        `after a reload, thread A's replies are folded again - the unfold did not stick on the node`,
      );
    }
  }

  // And hide puts them back, so a reader is not stuck with a thread they
  // unfolded once.
  await page.locator(`[data-fold-hide="${rootA.thread}"]`).click();
  for (const e of aReplies) {
    await page
      .locator(`[data-message="${e.id}"]`)
      .waitFor({ state: "hidden", timeout: 10_000 })
      .catch(() => {});
    if ((await page.locator(`[data-message="${e.id}"]`).count()) !== 0) {
      die(`hide replies left ${e.id} in the stream`);
    }
  }
  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);

  console.log(
    `the room stream draws ${rootA.thread.slice(-6)} as one row of eight, ${rootB.thread.slice(-6)} as its head plus the addressed reply with the block counting six, the pane shows all eight, and an unfold sticks on the node until hidden`,
  );
} finally {
  await browser.close();
}
