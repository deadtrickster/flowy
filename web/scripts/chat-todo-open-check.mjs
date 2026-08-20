/**
 * A todo raised in a room can be read there, and reached from there.
 *
 *   node scripts/chat-todo-open-check.mjs BASE_URL TOKEN ROOM
 *
 * THE OPERATOR, TWICE: "no way to go from chat todo to full todo card", then
 * "i want a quick view for chat todo when I click on it - quick summary card +
 * link to the full todo card".
 *
 * The panel beside a conversation had the id, the title, the status and the
 * assignee, and nothing else - so work raised out of a conversation was
 * readable there and nowhere else short of typing an id into a URL.
 *
 * The last arm is the one that matters and the easy one to leave out: the link
 * has to POINT AT THE ROW IT IS UNDER. A summary that opens somebody else's
 * todo is worse than no link, and it is exactly what a list-index bug produces.
 */

import { chromium } from "playwright";

const [base, token, room = "general"] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/chat-todo-open-check.mjs BASE_URL TOKEN [ROOM]");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const BODY = "chat-todo-open-check: the body only the summary can show";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${room}`, { timeout: 30_000 }).catch(() => {});

  // TWO rows, not one. With a single row a link that always points at the first
  // todo in the list passes, and that is the bug this check exists for.
  const made = await page.evaluate(
    async ([t, r, body]) => {
      const raise = async (title, withBody) => {
        const res = await fetch("/api/artifacts", {
          method: "POST",
          headers: { Authorization: `Bearer ${t}`, "Content-Type": "application/json" },
          body: JSON.stringify({
            type: "memory",
            kind: "todo",
            title,
            body: withBody,
            fields: { room: r },
          }),
        });
        if (!res.ok) return { error: `${res.status} ${await res.text()}` };
        return await res.json();
      };
      const first = await raise("chat-todo-open-check: the decoy", "not this one");
      if (first.error) return first;
      const second = await raise("chat-todo-open-check: the one to open", body);
      return second.error ? second : { first, second };
    },
    [token, room, BODY],
  );
  if (made.error) die(`could not raise the rows: ${made.error}`);
  const { second } = made;

  await page.goto(`${base}/chat/${room}`, { timeout: 30_000 }).catch(() => {});
  const opener = page.locator(`[data-todo-open="${second.id}"]`);
  await opener.waitFor({ state: "visible", timeout: 30_000 }).catch(() => {});
  if (crashes.length > 0) die(`the room threw: ${crashes.join("; ")}`);
  if ((await opener.count()) === 0) {
    die("the row just raised is not openable in the room's todo panel");
  }

  if ((await page.locator("[data-todo-summary]").count()) !== 0) {
    die("a summary was open before anything was clicked");
  }
  await opener.click();

  const summary = page.locator(`[data-todo-summary="${second.id}"]`);
  await summary.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
  if ((await summary.count()) === 0) die("clicking the title opened no summary");

  const shown = await summary.textContent();
  if (!shown?.includes(BODY)) {
    die(`the summary does not show the row's body: ${JSON.stringify(shown?.slice(0, 120))}`);
  }

  // AND THE LINK POINTS AT THIS ROW. Scoped to the open summary, because the
  // panel may hold many and an unscoped selector would pass on the wrong one.
  const href = await summary.locator("[data-todo-full-card]").getAttribute("href");
  if (!href?.includes(second.id)) {
    die(`the full-card link points at ${href}, not at ${second.id}`);
  }

  console.log(`a chat todo opens where it sits, shows its body, and links to ${second.id}`);
} finally {
  await browser.close();
}
