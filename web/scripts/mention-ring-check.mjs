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

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/mention-ring-check.mjs BASE_URL TOKEN");
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

// SOMEBODY ELSE, chosen from the node rather than named here. A check that
// invents a handle seeds a mention the node cannot resolve, and an unresolved
// mention is not ringed for a reason that has nothing to do with the reader.
const mine = await api("/api/me");
const handle = mine.user?.handle;
if (!handle) die("this token's user has no handle, so it cannot be mentioned");
const roster = (await api("/api/presence")).members ?? [];
const other = roster.map((m) => m.name).find((h) => h && h !== handle);
if (!other) die("no second principal on this node, so the negative arm cannot be seeded");

const stamp = Date.now().toString(36);
const say = (body) =>
  api(`/api/chat/${room}/say`, { method: "POST", body: JSON.stringify({ body }) });

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
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 20_000 });

  const chip = page.locator(`[data-mention="${who.user}"]`).last();
  await chip.waitFor({ state: "visible", timeout: 20_000 });
  const ringed = await chip.evaluate((el) => el.className);
  if (!/ring-1/.test(ringed)) {
    failure = `a mention of the reader (${handle}, ${who.user}) is drawn without a ring: class=${ringed}`;
  }

  // The other arm, and it must be a DIFFERENT element: if both chips carried
  // the same id the two assertions would be about one span and the negative
  // control would be vacuous.
  const theirs = page.locator(`[data-mention]:not([data-mention="${who.user}"])`).last();
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
