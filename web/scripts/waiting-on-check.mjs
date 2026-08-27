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

// WHY IT IS NOT VISIBLE, rather than that it is not. Playwright's actionability
// timeout says "element is not visible" and stops there, and an hour goes into
// finding out which ancestor is hiding it - the element itself is usually fine.
// 01M0K7PD9B is the cost of guessing at this: a tool's refusal was read as a
// dead button and two fixes were built for a defect that was not there.
//
// So the check walks up from the element and names the FIRST ancestor that
// answers for it - display:none, visibility:hidden, zero-sized, or clipped out
// of a scroller - and reports that instead of the symptom.
const whyInvisible = async (locator) => {
  const handle = await locator.elementHandle().catch(() => null);
  if (!handle) return "the element is not in the DOM at all";
  return handle.evaluate((el) => {
    const reasons = [];
    for (let node = el; node && node !== document.documentElement; node = node.parentElement) {
      const style = getComputedStyle(node);
      const box = node.getBoundingClientRect();
      const name =
        node.tagName.toLowerCase() +
        (node.className && typeof node.className === "string"
          ? "." + node.className.trim().split(/\s+/).slice(0, 4).join(".")
          : "");
      if (style.display === "none") reasons.push(`${name} is display:none`);
      else if (style.visibility === "hidden") reasons.push(`${name} is visibility:hidden`);
      else if (style.opacity === "0") reasons.push(`${name} is opacity:0`);
      else if (box.width === 0 || box.height === 0)
        reasons.push(`${name} is ${box.width}x${box.height}`);
      if (reasons.length) break;
    }
    const box = el.getBoundingClientRect();
    reasons.push(
      `the element itself is ${Math.round(box.width)}x${Math.round(box.height)} at ${Math.round(box.x)},${Math.round(box.y)} in a ${window.innerWidth}x${window.innerHeight} viewport`,
    );
    return reasons.join("; ");
  });
};

// AND VISIBLE IS THE ASSERTION, not present. count() and getAttribute() both
// answer for an element nobody can see, so an arm built out of them passes on a
// pane that renders nothing - which is the failure the repo rule about class
// names is about, reached by a different route.
const mustSee = async (locator, what) => {
  try {
    await locator.first().waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    await die(`${what} is in the DOM and cannot be seen: ${await whyInvisible(locator.first())}`);
  }
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

  await mustSee(page.locator(`[data-todo-open="${waiting}"]`), "the row the panel is supposed to draw");

  // 1 - DRAWN APART. Both rows are in the panel; exactly one carries the chip,
  // and it is the one with the pointer.
  await mustSee(page.locator("[data-todo-waiting-on]"), "the whose-move chip");
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
  await mustSee(page.locator("[data-todo-asked]"), "the question on the card");
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
  await mustSee(box, `the whose-move control on the card for ${plain}`);
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

  // 4 - AND TAKING IT BACK CLEARS BOTH KEYS, FROM THE SAME CONTROL. The row
  // says "from the same control", so the card staying open across a write is
  // part of what is being asserted rather than an accident of the test: a
  // person who mistypes a name has to be able to correct it where they are.
  if ((await box.count()) === 0) {
    const stillListed = await page.locator(`[data-todo-open="${plain}"]`).count();
    await die(`the card closed itself when the question was written, so the control that
set the pointer is gone and it cannot be taken back from where it was set.
The row is ${stillListed ? "still in the panel" : "no longer in the panel at all"}.`);
  }
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

  // 5 - AND THE CARD STILL CLOSES WHEN ITS ROW LEAVES. Arm 4 needed the card to
  // survive its own write, and the narrowing that allows it would also let a
  // card hang over a row that is no longer drawn - which is the finding the
  // guard was written for. So the other half is asserted here rather than
  // assumed: the row is closed on the NODE, the panel polls, and the card that
  // was open on it has to go.
  await page.locator(`[data-todo-open="${plain}"]`).click();
  await mustSee(page.locator(`[data-todo-waiting-set="${plain}"]`), "the card reopened for arm 5");
  const closed = await fetch(`${base}/api/artifact/${encodeURIComponent(plain)}/status`, {
    method: "POST",
    headers: { ...bearer, "Content-Type": "application/json" },
    body: JSON.stringify({ status: "done", note: "closed to see the card follow it" }),
  });
  if (!closed.ok) await die(`could not close ${plain} to test the guard: ${closed.status}`);
  let gone = false;
  for (let i = 0; i < 40; i++) {
    if ((await page.locator(`[data-todo-waiting-set="${plain}"]`).count()) === 0) {
      gone = true;
      break;
    }
    await page.waitForTimeout(250);
  }
  if (!gone) {
    await die(`${plain} left the panel and its card stayed open over whatever row took its
place. That is the finding the close-on-reorder guard exists for, and narrowing
it to "the open row is no longer drawn" was supposed to keep it.`);
  }

  console.log(
    `the panel drew 1 of 2 rows as blocked, showed the question, wrote ${me} onto ${plain.slice(0, 10)} without moving its carrier, took it back from the same control, and closed the card when the row left`,
  );
} finally {
  await clearRaised();
  await browser.close();
}
