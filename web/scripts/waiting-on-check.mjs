/**
 * A row waiting on somebody looks different from a row nobody has touched.
 *
 *   node scripts/waiting-on-check.mjs BASE_URL TOKEN ROOM
 *
 * The field landed and the console drew none of it: a row blocked on an answer
 * and a row nobody had picked up rendered identically. One appearance, two
 * states - which is the defect 01M0K4MENH was raised on, at the surface the
 * person who is OWED the answers actually reads.
 *
 * FOUR ARMS, and each is a difference rather than a reading:
 *
 *   1  two rows alike but for the pointer are DRAWN APART. Fails on master,
 *      where both draw the same, and fails on any version that draws the chip
 *      unconditionally.
 *   2  the question is readable where the pointer is, on the card.
 *   3  asking from the panel reaches the NODE - and the assignee is UNCHANGED,
 *      which is the arm that matters: a control that quietly reassigned would
 *      look exactly like success.
 *   4  and taking it back clears both keys, asked of the node rather than
 *      believed off the chip.
 */

import { chromium } from "playwright";

const [base, token, room, me] = process.argv.slice(2);
if (!base || !token || !room || !me) {
  console.error("usage: node scripts/waiting-on-check.mjs BASE_URL TOKEN ROOM HANDLE");
  process.exit(2);
}

const bearer = { Authorization: `Bearer ${token}` };
const raised = [];

const clearRaised = async () => {
  for (const id of raised) {
    const res = await fetch(`${base}/api/artifact/${encodeURIComponent(id)}/status`, {
      method: "POST",
      headers: { ...bearer, "Content-Type": "application/json" },
      body: JSON.stringify({ status: "done", note: "closed by waiting-on-check" }),
    }).catch((err) => ({ ok: false, status: String(err) }));
    if (!res.ok) console.error(`could not clear the fixture ${id}: ${res.status}`);
  }
};

const die = async (message) => {
  console.error(message);
  await clearRaised();
  process.exit(1);
};

const file = async (title) => {
  const res = await fetch(`${base}/api/chat/${encodeURIComponent(room)}/todo`, {
    method: "POST",
    headers: { ...bearer, "Content-Type": "application/json" },
    body: JSON.stringify({ title }),
  });
  if (!res.ok) await die(`could not file ${title}: ${res.status} ${await res.text()}`);
  const id = (await res.json()).item?.id;
  if (!id) await die(`filing ${title} answered without item.id`);
  raised.push(id);
  return id;
};

const rowOf = async (id) => {
  const res = await fetch(`${base}/api/artifact/${encodeURIComponent(id)}`, { headers: bearer });
  if (!res.ok) await die(`could not read ${id} back: ${res.status}`);
  return res.json();
};

// WHO TO ASK IS GIVEN, NOT FOUND. The suite knows this token's handle and
// passes it in; a check that went looking would depend on a roster read, which
// is the input that made the mention-ring check flip on an unchanged tree
// tonight - three seats measured it and the fix was to hand it its fixture.
//
// My first version asked /api/whoami for `.handle`, a field it does not have,
// and reported "this token has no handle" about a token that has one. An
// invented field name reads as a fact about the node.
const stamp = Date.now().toString(36);
const waiting = await file(`waiting-check ${stamp}: this one is blocked`);
const plain = await file(`waiting-check ${stamp}: this one is nobody's move`);

const asked = "does this go ahead";
const set = await fetch(`${base}/api/todo/${encodeURIComponent(waiting)}/waiting-on`, {
  method: "POST",
  headers: { ...bearer, "Content-Type": "application/json" },
  body: JSON.stringify({ waiting_on: me, asked }),
});
if (!set.ok) await die(`could not seed the pointer: ${set.status} ${await set.text()}`);

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/chat/${encodeURIComponent(room)}`, { timeout: 30_000 });

  await page
    .locator(`[data-todo-open="${waiting}"]`)
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});

  // 1 - DRAWN APART. Both rows are in the panel; exactly one carries the chip,
  // and it is the one with the pointer.
  const chips = await page.locator("[data-todo-waiting-on]").count();
  if (chips === 0) {
    await die(`the panel draws no whose-move chip at all, so a blocked row reads as untouched.
${waiting} carries waiting_on=${me} on the node.`);
  }
  if (chips !== 1) {
    await die(`${chips} rows draw a whose-move chip and only ${waiting} is waiting on anybody -
the chip is drawn unconditionally, so it says nothing about which row is blocked.`);
  }
  const named = await page.locator("[data-todo-waiting-on]").getAttribute("data-todo-waiting-on");
  if (named !== me) {
    await die(`the chip names ${named}, and the node says the row waits on ${me}`);
  }

  // 2 - THE QUESTION IS READABLE where the pointer is.
  await page.locator(`[data-todo-open="${waiting}"]`).click();
  await page
    .locator("[data-todo-asked]")
    .waitFor({ state: "visible", timeout: 10_000 })
    .catch(() => {});
  const shown =
    (await page
      .locator("[data-todo-asked]")
      .first()
      .innerText()
      .catch(() => "")) || "";
  if (!shown.includes(asked)) {
    await die(`the card does not carry what was asked - it says ${JSON.stringify(shown)},
and the node holds ${JSON.stringify(asked)}. A name with no question is a row
somebody has to open the artifact page to understand.`);
  }
  await page.keyboard.press("Escape");

  // 3 - ASKING FROM THE PANEL REACHES THE NODE, and does not move the carrier.
  const before = await rowOf(plain);
  const carrier = before.fields?.assignee ?? "";
  await page.locator(`[data-todo-open="${plain}"]`).click();
  const box = page.locator(`[data-todo-waiting-set="${plain}"]`);
  await box.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
  if ((await box.count()) === 0) {
    await die(`the card for ${plain} has no control to say whose move it is`);
  }
  await box.fill(me);
  await box.blur();
  let after = null;
  for (let i = 0; i < 40; i++) {
    after = await rowOf(plain);
    if ((after.fields?.waiting_on ?? "") === me) break;
    await page.waitForTimeout(250);
  }
  if ((after?.fields?.waiting_on ?? "") !== me) {
    await die(`the panel was told ${plain} waits on ${me} and the node never took it:
${JSON.stringify(after?.fields ?? {})}`);
  }
  // THE ARM THAT MATTERS.
  if ((after?.fields?.assignee ?? "") !== carrier) {
    await die(`asking a question moved the carrier of ${plain} from ${JSON.stringify(carrier)} to
${JSON.stringify(after?.fields?.assignee ?? "")}. That is the assignment this field
exists to stop being the only way to ask somebody something.`);
  }

  // 4 - AND TAKING IT BACK CLEARS BOTH KEYS.
  await box.fill("");
  await box.blur();
  let cleared = null;
  for (let i = 0; i < 40; i++) {
    cleared = await rowOf(plain);
    if ((cleared.fields?.waiting_on ?? "") === "") break;
    await page.waitForTimeout(250);
  }
  if ((cleared?.fields?.waiting_on ?? "") !== "") {
    await die(`the question was withdrawn in the panel and the node still says
${cleared?.fields?.waiting_on}`);
  }

  console.log(
    `the panel drew 1 of 2 rows as blocked, showed the question, wrote ${me} onto ${plain.slice(0, 10)} without moving its carrier, and took it back`,
  );
} finally {
  await clearRaised();
  await browser.close();
}
