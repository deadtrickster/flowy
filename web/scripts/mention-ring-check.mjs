/**
 * A mention of YOU wears a ring, and a mention of somebody else does not.
 *
 *   node scripts/mention-ring-check.mjs BASE_URL TOKEN
 *
 * The operator, 2026-08-20: "@operator is not highlighted in the chat". The
 * highlight was already built - markdown.ts rings a chip whose resolved id is
 * the reader's - so 01M0GGSM99 listed three candidates and measured none of
 * them: the reader is unknown in that render path, the node never resolved the
 * name, or it rings and is too faint to see.
 *
 * The second was ruled out on the wire: meta.mentions carries
 * "deadtrickster:01M03E6M1G6R1XM38K67PS6EZ9" on messages that say the name.
 * The first is what this measures, and it is measured for the CHAT room
 * specifically - checks.d/console/mention-me.sh proves every MessageList is
 * PASSED a reader, which is a different claim from the ring reaching the
 * screen.
 *
 * BOTH ARMS, ON ONE PAGE. A check that only asserts the ring is present passes
 * on a console that rings every chip - which is the same defect wearing the
 * opposite sign, and worse, because then the ring means nothing and a reader
 * who trusts it misses the message that was for them. So a mention of somebody
 * else is seeded beside it and must come out plain.
 *
 * Asserted on the CLASS rather than on colour: the ring is a class the renderer
 * adds, a computed style would also answer for a border somebody else drew, and
 * the chip already carries data-mention with the resolved id so the two chips
 * can be told apart without matching on their words.
 */
import { chromium } from "playwright";

const [base, token, other] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/mention-ring-check.mjs BASE_URL TOKEN OTHER_HANDLE");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const room = "mentionring";
const api = async (path, init) => {
  const res = await fetch(`${base}${path}`, {
    ...init,
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
  });
  if (!res.ok) die(`${path}: ${res.status} ${await res.text()}`);
  return res.json();
};

// WHO THIS TOKEN IS, asked rather than assumed: the ring turns on an id
// comparison and a check that hardcoded a handle would pass against the wrong
// person's row.
const who = await api("/api/whoami");
if (!who.user) die("whoami answered no user id, so there is nothing to ring");

// SOMEBODY ELSE, STATED BY THE CALLER rather than discovered from the roster.
//
// 01M0KDXZHQ. This used to be `roster.find(h => h !== handle)` over
// /api/presence.members, and presence is who is AROUND - it changes between
// runs on an unchanged tree. Two consequences, and this check has produced
// both: the negative arm asserted about a different principal from one run to
// the next, and on a quiet node the scan found nobody and the whole check died
// with "no second principal", which is a fixture refusal wearing a red.
//
// Measured on this branch: an ONLY= run failed exactly there while the tree it
// was testing was fine.
//
// The suite already knows a second user - the seed writes HANDLE_B beside
// TOKEN_A - so the caller passes it and nothing has to be found. Required
// rather than optional with a fallback: a fallback would put the roster scan
// back for anybody running this by hand, and the whole point is that two
// people running the same check on the same tree must be asking the same
// question.
const mine = await api("/api/me");
const handle = mine.user?.handle;
if (!handle) die("this token's user has no handle, so it cannot be mentioned");
if (!other) {
  die(`name the other principal: mention-ring-check.mjs BASE_URL TOKEN OTHER_HANDLE
The negative arm needs somebody who is NOT the reader, and reading one off
/api/presence makes this check depend on who happens to be around.`);
}
if (other === handle) {
  die(`the other principal (${other}) is the reader, so the negative arm would
assert about the same person as the positive one and prove nothing.`);
}

const stamp = Date.now().toString(36);
const say = (body) =>
  api(`/api/chat/${room}/say`, {
    method: "POST",
    body: JSON.stringify({ body }),
  });

const forMe = await say(`ring check ${stamp} - @${handle} this one is yours`);
const forThem = await say(`ring check ${stamp} - @${other} this one is theirs`);

// THE NODE'S OWN RESOLUTION, read back before the browser opens. If the name
// did not resolve, the screen is answering a question about mention parsing and
// not about the reader, so this fails as a fixture problem rather than as a
// missing ring.
const back = await api(`/api/chat/${room}?since=0`);
const seen = (back.events ?? []).filter((e) => e.id === forMe.id || e.id === forThem.id);
if (seen.length !== 2) die(`seeded 2 messages, read back ${seen.length}`);
for (const e of seen) {
  if (!(e.meta?.mentions ?? "").includes(":")) {
    die(`the node resolved no mention on ${e.id}: ${JSON.stringify(e.meta ?? {})}`);
  }
}

// AND THE NAME THE ROOM ACTUALLY USES. @operator resolved to nobody until the
// role arm in PrincipalsNamed, which is why the operator saw no ring on the one
// surface they read: four seats wrote the word at them daily and the node
// treated every one of them as prose. Asserted on the node's own resolution
// rather than on the screen, because that is where it was missing - and skipped
// rather than failed on a node with no operator, since a name nobody holds
// correctly resolves to nobody.
const roles = await api("/api/presence");
const operators = (roles.members ?? []).filter((m) => m.role === "operator");
if (operators.length === 1) {
  const probe = await say(`ring check ${stamp} - @operator this one is for the role`);
  const resolved = probe.meta?.mentions ?? "";
  if (!resolved.includes(`operator:${operators[0].actor}`)) {
    die(
      `@operator did not resolve to the one operator (${operators[0].name}, ${operators[0].actor}): ` +
        `mentions=${JSON.stringify(resolved)}`,
    );
  }
} else {
  console.log(
    `skipping the role arm: this node reports ${operators.length} operators in the roster`,
  );
}

const browser = await chromium.launch();
let failure = "";
try {
  const page = await browser.newPage({
    viewport: { width: 1280, height: 900 },
  });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 });

  // ANCHORED TO THE MESSAGE THIS CHECK WROTE, by id.
  //
  // 01M0KDXZHQ. This was `[data-mention="..."]` .last() - the last matching
  // chip ON THE PAGE - and the page is the room "mentionring", which is a fixed
  // name every run has ever seeded into. So the element under test could be:
  //
  //   a chip from a PREVIOUS RUN, still in the room, in either arm
  //   the @operator probe this check seeds below, when the node happens to
  //     report exactly one operator - a conditional fixture
  //   a mention by whoever /api/presence returned as `other` that run
  //
  // Three inputs that vary without the tree changing, which is why it flipped
  // on an unchanged tree and why re-running it settled nothing. @dead-claude
  // lost a pass to it on a branch with no console files in it.
  //
  // data-message carries the event id (MessageList.tsx:482), so scoping to it
  // makes the assertion about the message this run seeded and nothing else. It
  // neutralises all three at once rather than removing them one at a time.
  const chip = page.locator(`[data-message="${forMe.id}"] [data-mention="${who.user}"]`).last();
  await chip.waitFor({ state: "visible", timeout: 20_000 });
  const ringed = await chip.evaluate((el) => el.className);
  if (!/ring-1/.test(ringed)) {
    failure = `a mention of the reader (${handle}, ${who.user}) is drawn without a ring: class=${ringed}`;
  }

  // The other arm, and it must be a DIFFERENT element: if both chips carried
  // the same id the two assertions would be about one span and the negative
  // control would be vacuous.
  // Anchored the same way, and to the OTHER seeded message. The :not() stays -
  // it is what keeps the two arms about two different spans, which the comment
  // above is right that they must be - but it is no longer doing the work of
  // finding the right message as well.
  const theirs = page
    .locator(`[data-message="${forThem.id}"] [data-mention]:not([data-mention="${who.user}"])`)
    .last();
  await theirs.waitFor({ state: "visible", timeout: 20_000 });
  const plain = await theirs.evaluate((el) => el.className);
  if (/ring-1/.test(plain) && !failure) {
    failure = `a mention of ${other} is ringed for a reader who is not them: class=${plain}`;
  }
} finally {
  await browser.close();
}

if (failure) die(failure);
console.log(`a mention of ${handle} rings and a mention of ${other} does not`);
