/**
 * A message, or the part of it somebody selected, becomes a row.
 *
 *   node scripts/span-todo-check.mjs BASE_URL TOKEN OTHER_TOKEN ROOM
 *
 * THE OPERATOR, 01M0HGVPFN: "I should be able to quickly turn a message or a
 * selected part into a rodo". Half of it existed - the todo panel raises out of
 * the SELECTED message - and the half that did not is the half the ask is
 * about: the words. A row whose title is "raised out of message 01M0H..." sends
 * its reader back to the conversation, which is the errand the queue exists to
 * end.
 *
 * IT SELECTS TEXT AND PRESSES THE BUTTON, because the selection is the whole
 * feature and it cannot be reached any other way: the control reads the
 * browser's selection at the moment it is pressed, deliberately holding no
 * state (storing it re-renders the message and destroys the reader's highlight
 * under their pointer - see MessageList, where cite pays for the same rule).
 *
 * AND IT ASKS THE NODE what the row says. A panel that drew the right title
 * from a row that never carried it would pass a DOM assertion; two things are
 * checked on the row itself, and they are the two halves of the ask: the row
 * carries the WORDS, and it carries the MESSAGE they came out of.
 */

import { chromium } from "playwright";

const [base, token, other, room] = process.argv.slice(2);
if (!base || !token || !other || !room) {
  console.error("usage: node scripts/span-todo-check.mjs BASE_URL TOKEN OTHER_TOKEN ROOM");
  process.exit(2);
}

const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

const call = async (path, init = {}, as = token) => {
  const r = await fetch(`${base}${path}`, {
    ...init,
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${as}`, ...init.headers },
  });
  return { ok: r.ok, status: r.status, body: r.ok ? await r.json() : await r.text() };
};

// The words to pick out, and words either side of them so that "it took the
// selection" and "it took the whole message" cannot look the same.
const PICKED = "pay the invoice before friday";
const said = await call(
  `/api/chat/${encodeURIComponent(room)}/say`,
  {
    method: "POST",
    body: JSON.stringify({
      body: `span-todo-check: ${PICKED} - and some trailing words nobody selected`,
    }),
  },
  other,
);
if (!said.ok) die(`the probe was not accepted: HTTP ${said.status} ${said.body}`);
const message = said.body.id;

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 950 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 }).catch(() => {});

  const control = page.locator(`[data-todo-from="${message}"]`);
  await control.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await control.count()) === 0) {
    die(`no way to raise a todo out of a message - no [data-todo-from] beside ${message}`);
  }
  if (crashes.length > 0) die(`the room threw: ${crashes.join("; ")}`);

  // THE SELECTION, made the way a reader makes one: a range over the text node
  // that holds those words, inside that message's body and no other.
  const picked = await page.evaluate(
    ({ id, want }) => {
      const container = document.querySelector(`[data-body="${CSS.escape(id)}"]`);
      if (!container) return "no body element for that message";
      const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT);
      for (let node = walker.nextNode(); node; node = walker.nextNode()) {
        const at = node.textContent?.indexOf(want) ?? -1;
        if (at < 0) continue;
        const range = document.createRange();
        range.setStart(node, at);
        range.setEnd(node, at + want.length);
        const selection = window.getSelection();
        selection?.removeAllRanges();
        selection?.addRange(range);
        return "";
      }
      return `the words are not in the rendered message: ${container.textContent?.slice(0, 200)}`;
    },
    { id: message, want: PICKED },
  );
  if (picked) die(`could not select inside the message: ${picked}`);

  await control.click();

  // THE ROW, off the node, through the same door the panel is filled from.
  let row = null;
  for (let i = 0; i < 40 && !row; i++) {
    const list = await call(
      `/api/artifacts?type=memory&kind=todo&room=${encodeURIComponent(room)}`,
    );
    if (list.ok) row = (list.body.artifacts ?? []).find((item) => item.title === PICKED) ?? null;
    if (!row) await page.waitForTimeout(500);
  }
  if (!row) {
    const list = await call(
      `/api/artifacts?type=memory&kind=todo&room=${encodeURIComponent(room)}`,
    );
    const titles = (list.body.artifacts ?? []).slice(0, 5).map((a) => a.title);
    die(`no todo titled ${JSON.stringify(PICKED)} reached the node after 20s.
The newest rows in #${room} are ${JSON.stringify(titles)}. A title carrying the
trailing words means the whole message was raised and the selection was ignored,
which is the half of the ask that did not exist before.`);
  }

  // AND IT KNOWS WHERE IT CAME FROM. Provenance is the reason to raise a row
  // from a message rather than typing it into the panel.
  if (row.fields?.message !== message) {
    die(`the row was raised without the message it came out of: fields.message is
${JSON.stringify(row.fields?.message ?? null)}, wanted ${message}.`);
  }

  // AND THE READER IS SHOWN THE QUEUE IT LANDED IN. A row raised into a pane
  // nobody is looking at is indistinguishable from a button that did nothing.
  const pane = page.locator('[data-room-pane-body="todos"]');
  await pane.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
  if ((await pane.count()) === 0) {
    die("the row was raised and the todos pane was not shown, so the reader saw nothing happen");
  }

  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);
  console.log(`a selected span became ${row.id}, titled with the words and carrying ${message}`);
} finally {
  await browser.close();
}
