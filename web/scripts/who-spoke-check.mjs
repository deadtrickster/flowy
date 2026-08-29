/**
 * WHO SPOKE, WITHOUT READING.
 *
 *   node scripts/who-spoke-check.mjs BASE_URL USER_TOKEN AGENT_TOKEN [ROOM]
 *
 * From the UI row 01M173AT9V2PYK7XHG4GZDD1MJ, item 4: "the two speakers look
 * identical... ours are the same rectangle with a different word in a pill."
 * Telling a person's turn from an agent's cost a read of a five-character
 * word, in a room where four agents and one person talk past each other.
 *
 * IT ASSERTS A DIFFERENCE BETWEEN TWO ROWS ON ONE PAGE, which is the only
 * shape that can fail honestly: "a person's message has a background" passes
 * on a page where every message has the same background, and that page is the
 * defect. So it posts one message as a person and one as an agent into the
 * same room, finds both, and requires their resolved styles to disagree.
 *
 * AND THE WORDS STAY. The badge saying "agent" or "human" is asserted too.
 * Shape is the fast channel; the pill is the exact one, and a reader who
 * cannot see the shape difference is the reason the pill was there first. A
 * change that swapped one for the other would be a regression wearing a
 * redesign's clothes.
 */

import { chromium } from "playwright";

const [base, userToken, agentToken, room = "general"] = process.argv.slice(2);
if (!base || !userToken || !agentToken) {
  console.error("usage: node scripts/who-spoke-check.mjs BASE_URL USER_TOKEN AGENT_TOKEN [ROOM]");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const say = async (token, body) => {
  const res = await fetch(new URL(`/api/chat/${encodeURIComponent(room)}/say`, base), {
    method: "POST",
    headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
    body: JSON.stringify({ body }),
  });
  if (!res.ok) die(`say answered ${res.status}: ${(await res.text()).slice(0, 200)}`);
  const said = await res.json();
  const id = said.body?.id ?? said.id;
  if (!id)
    die(`the node took a message and did not say its id: ${JSON.stringify(said).slice(0, 200)}`);
  return id;
};

// One of each, into the same room, so the comparison is between two rows a
// reader sees at once rather than between two runs of the check.
const byPerson = await say(userToken, "who-spoke-check: a person said this");
const byAgent = await say(agentToken, "who-spoke-check: an agent said this");

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 950 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), userToken);
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 }).catch(() => {});

  const row = (id) => page.locator(`[data-message="${id}"]`);
  await row(byAgent)
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  for (const [id, what] of [
    [byPerson, "the person's message"],
    [byAgent, "the agent's message"],
  ]) {
    if ((await row(id).count()) === 0) die(`${what} (${id}) never drew`);
  }
  if (crashes.length > 0) die(`the room threw: ${crashes.join("; ")}`);

  const look = (id) =>
    row(id).evaluate((el) => {
      const style = getComputedStyle(el);
      return {
        voice: el.getAttribute("data-msg-voice"),
        background: style.backgroundColor,
        leftBorder: `${style.borderLeftWidth} ${style.borderLeftColor}`,
        text: (el.textContent ?? "").toLowerCase(),
      };
    });

  const person = await look(byPerson);
  const agent = await look(byAgent);

  // The page has to agree about which is which before its pixels mean anything.
  if (person.voice !== "person" || agent.voice !== "agent") {
    die(
      `the rows name their voices as ${JSON.stringify(person.voice)} and ${JSON.stringify(
        agent.voice,
      )} - expected "person" and "agent", so the styles below would be compared against the wrong rows`,
    );
  }

  const same = person.background === agent.background && person.leftBorder === agent.leftBorder;
  if (same) {
    die(`a person's turn and an agent's turn are drawn identically - background ${person.background}, left edge ${person.leftBorder}.
The pill still says which, and reading a five-character word is exactly the cost this is meant to remove.`);
  }

  // AND THE WORDS STAY.
  if (!person.text.includes("human")) die("the person's row no longer says human in words");
  if (!agent.text.includes("agent")) die("the agent's row no longer says agent in words");

  console.log(
    `a person's turn is ${person.background} with a ${person.leftBorder} edge; ` +
      `an agent's is ${agent.background} with ${agent.leftBorder} - and both still say which in words`,
  );
} finally {
  await browser.close();
}
