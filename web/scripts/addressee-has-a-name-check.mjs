/**
 * THE ROOM SAYS WHO A MESSAGE IS FOR BY NAME.
 *
 *   node scripts/addressee-has-a-name-check.mjs BASE_URL TOKEN HANDLE
 *
 * Measured on the dogfood console before this: a message addressed to
 * 01M05YCEFY6BQAR2WPMMXTYVG2 rendered as "to MMXTYVG2" - eight characters of a
 * ULID - two inches from a speaker chip that said "claude-host". The same
 * person, named twice on one row, one of those times as an identifier.
 *
 * The page could not fix it: meta.mentions carries a name only for a message
 * that named somebody with an @, and `flowy say --to NAME` addresses without
 * writing one. So the door resolves it (store.FillAddresseeNames) and this
 * check is about what a reader is then shown.
 *
 * ASSERTED AS A DIFFERENCE, and the difference is between the badge and the id.
 * "The badge says claude-host" would pass on a console that drew the handle by
 * luck of a mention; what makes this a measurement is that the badge must NOT
 * be a slice of the addressee's id - the exact fallback it used to draw. So the
 * check reads the id off the node's own answer and asserts the rendered text is
 * not a prefix of it.
 *
 * AND THE ID IS STILL REACHABLE. A name is what you read; the id is what you
 * copy into a command, and dropping it would trade one missing fact for
 * another. It is on the badge's title.
 */

import { chromium } from "playwright";

const [base, token, handle] = process.argv.slice(2);
if (!base || !token || !handle) {
  console.error("usage: node scripts/addressee-has-a-name-check.mjs BASE_URL TOKEN HANDLE");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const room = "naming";
const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 30_000 });
  await page.waitForTimeout(2500);
  if (crashes.length > 0) die(`the console threw: ${crashes.join("; ")}`);

  // WHAT THE NODE SAYS, so the check compares the page against the door rather
  // than against a string this file made up.
  const answer = await page.evaluate(async (r) => {
    const t = localStorage.getItem("flowy.token");
    const res = await fetch(`/api/chat/${r}`, { headers: { Authorization: `Bearer ${t}` } });
    const body = await res.json();
    const addressed = (body.events ?? []).filter((e) => e.addressee);
    return addressed.map((e) => ({ id: e.addressee, name: e.addressee_name ?? "" }));
  }, room);

  if (answer.length === 0) {
    die(`the node reports no addressed message in #${room}, so there is nothing on this page to
judge - that is a fixture that did not arrive, not a console that draws names`);
  }
  const unnamed = answer.filter((a) => !a.name);
  if (unnamed.length > 0) {
    die(`the door answered no addressee_name for ${unnamed.length} of ${answer.length} addressed
message(s) in #${room} (${JSON.stringify(unnamed.map((u) => u.id))}). The page cannot draw a name
the node did not resolve, so this is the door's arm failing, not the rendering's.`);
  }

  const badges = await page.evaluate(() => {
    const out = [];
    for (const el of document.querySelectorAll("*")) {
      const t = (el.textContent ?? "").trim();
      if (!/^to \S+$/.test(t) || el.children.length > 1) continue;
      out.push({ text: t.slice(3), title: el.getAttribute("title") ?? "" });
    }
    return out;
  });
  if (badges.length === 0) {
    die(`no "to <somebody>" badge is rendered in #${room}, though the node answers
${answer.length} addressed message(s). The page is not drawing the addressee at all.`);
  }

  for (const badge of badges) {
    if (badge.text === "you") continue; // the reader's own row says so instead
    const asId = answer.find((a) => a.id.startsWith(badge.text));
    if (asId) {
      die(`the badge reads ${JSON.stringify(badge.text)}, which is a slice of the addressee's id
${asId.id}. That is the fallback this fix replaced: a person is not their identifier's first
characters, and the node answered the name ${JSON.stringify(asId.name)} for that very row.`);
    }
    if (!answer.some((a) => a.name === badge.text)) {
      die(`the badge reads ${JSON.stringify(badge.text)}, which is neither "you" nor any name the
node resolved (${JSON.stringify(answer.map((a) => a.name))}) - so the page is drawing something
of its own rather than the door's answer`);
    }
    // The id has to stay reachable: a name is for reading, the id is for
    // pasting into a command, and this trades neither for the other.
    if (!answer.some((a) => badge.title.includes(a.id))) {
      die(`the badge for ${JSON.stringify(badge.text)} does not carry the addressee's id anywhere
(title: ${JSON.stringify(badge.title)}). The name replaced the id on screen; it must not have
removed it from the page.`);
    }
  }

  console.log(
    `#${room} draws ${badges.length} addressee badge(s) by name (${JSON.stringify(
      badges.map((b) => b.text),
    )}), none of them a slice of an id, and each carrying the id in its title; ` +
      `the door named ${answer.length} addressed message(s), including one addressed with --to ` +
      `and no @${handle} in its body`,
  );
} finally {
  await browser.close();
}
