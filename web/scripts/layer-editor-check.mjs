/**
 * THE LAYER A PROJECT DECLARES IS EDITABLE HERE, AND THE NODE KEEPS IT.
 *
 *   node scripts/layer-editor-check.mjs BASE_URL OPERATOR_TOKEN OTHER_TOKEN
 *
 * 01M0G8AM6R2BGPCWZQMV6321DR, the operator: "i should be able to manage them
 * from the flowy ui... also must be available for agent to edit - say we figure
 * things out in the chat together and then one of you goes ahead and adds a
 * dependency here."
 *
 * THE ASSERTION IS THE ROUND TRIP, not the textarea. A box that renders, takes
 * typing and posts nothing looks identical to one that works until somebody
 * reboots a VM and finds their dependency missing - and the page cannot tell
 * the difference either, because it is holding the text it just typed. So the
 * text is read back FROM THE NODE with a separate request after the save, and
 * the value asserted is the one the node returned.
 *
 * IT ALSO ASSERTS THE REFUSAL, because these doors are operator-only and a
 * check that only ever drives them as the operator cannot tell a guard that
 * works from one that was never there. Same door, other token, must differ.
 */

import { chromium } from "playwright";

const [base, operatorToken, otherToken] = process.argv.slice(2);
if (!base || !operatorToken || !otherToken) {
  console.error("usage: node scripts/layer-editor-check.mjs BASE_URL OPERATOR_TOKEN OTHER_TOKEN");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const call = async (path, token, init = {}) => {
  const res = await fetch(new URL(path, base), {
    ...init,
    headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
  });
  return { status: res.status, body: await res.text() };
};

// THE GUARD, BEFORE ANYTHING ELSE. Both tokens against the same door.
const asOther = await call("/api/vm/layer?project=flowy", otherToken);
const asOperator = await call("/api/vm/layer?project=flowy", operatorToken);
if (asOther.status === asOperator.status) {
  die(`the layer door answered ${asOther.status} to a non-operator and ${asOperator.status} to the
operator - the same answer to both is a guard that is not there. Body: ${asOther.body.slice(0, 200)}`);
}
if (asOther.status !== 403) {
  die(`a non-operator got ${asOther.status} from the layer door, expected 403`);
}

// FROM HERE THE ARMS DEPEND ON WHAT THIS HOST IS, and both hosts are correct.
// The suite runs on machines with firecode and on machines without; api_vm.go
// answers 503 on the second precisely so the two are not confused, and a check
// that demanded the first would fail every guest for a fault that is not in the
// branch. What is asserted on a node with no firecode is that it SAYS SO -
// because the failure that matters here is a layer door answering an empty
// editable file on a host that could never apply it.
if (asOperator.status === 503) {
  if (!/cannot run VMs|no firecode/i.test(asOperator.body)) {
    die(`the node answered 503 without saying it cannot run VMs: ${asOperator.body.slice(0, 200)}`);
  }
  let parsed = null;
  try {
    parsed = JSON.parse(asOperator.body);
  } catch {
    /* a 503 that is not json is still a refusal, and the text was matched above */
  }
  if (parsed && typeof parsed.text === "string") {
    die(`a node that cannot run VMs answered the layer door with a text field
(${JSON.stringify(parsed.text.slice(0, 80))}). That is an editable file on a host that could
never apply it - the collapse api_vm.go returns 503 to prevent.`);
  }
  console.log(
    `this node has no firecode: the layer door says so (503) and refuses a non-operator (${asOther.status}). THE ROUND TRIP WAS NOT EXERCISED - that arm needs a host with firecode.`,
  );
  process.exit(0);
}

// A project this host does not have must be refused with the list that would
// have worked, not with an empty file somebody could then save over.
const bogus = await call("/api/vm/layer?project=no-such-project-here", operatorToken);
if (bogus.status !== 400 || !/Registered:/.test(bogus.body)) {
  die(
    `an unknown project answered ${bogus.status} without naming what is registered: ${bogus.body.slice(0, 200)}`,
  );
}

// A parameter this door does not take must be refused rather than ignored.
const typo = await call("/api/vm/layer?project=flowy&porject=flowy", operatorToken);
if (typo.status !== 400) {
  die(`the layer door accepted an unknown query parameter (${typo.status}) - an argument the
callee drops is a lie, and a misspelt filter that returns a plausible answer is how it reads`);
}

if (asOperator.status !== 200) {
  die(
    `the operator could not read the layer: ${asOperator.status} ${asOperator.body.slice(0, 200)}`,
  );
}
const before = JSON.parse(asOperator.body);
// Kept so the finally can put the file back: this check edits something the
// next VM boot applies, and leaving its marker behind would have every later
// guest run a line a test wrote.
const restore = before.text;

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), operatorToken);
  await page.goto(`${base}/vms`, { timeout: 30_000 }).catch(() => {});

  // THE AGENTS PANE FIRST. The page opens on `shells` and the editor lives in
  // `agents`, so on load the textarea is IN THE DOM AND NOT VISIBLE - which is
  // how the first version of this check got to `fill` at all: it waited for
  // visible, swallowed the timeout in a .catch, then counted the element and
  // found one. A wait whose failure is discarded is not a wait.
  const agents = page.locator('[data-vm-tab="agents"]');
  await agents.waitFor({ state: "visible", timeout: 20_000 });
  await agents.click();

  const picker = page.locator("[data-vm-project]");
  await picker.waitFor({ state: "visible", timeout: 20_000 });
  await picker.selectOption("flowy").catch(() => {});

  const box = page.locator("[data-vm-layer-text]");
  // NOT SWALLOWED. "Present in the DOM" is what the old version proved, and an
  // editor a person cannot see is not an editor they can use.
  await box.waitFor({ state: "visible", timeout: 15_000 });
  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);

  const mark = `# layer-editor-check ${before.text.length}`;
  const wanted = `${restore}\n${mark}\n`;
  await box.fill(wanted);

  const saved = page.locator('[data-vm-layer-state]:text-is("saved")');

  // THE LOCATOR MUST EXCLUDE THE STATE IT IS MEANT TO EXCLUDE, asserted here on
  // the live page at the one moment it is knowably false: the box has just been
  // filled and not saved, so this span reads "unsaved changes" and the saved
  // locator must match NOTHING.
  //
  // Without this the wait below can pass for the wrong reason and no run will
  // ever say so - which is precisely what happened. `:text("saved")` is a
  // substring match and "unsaved changes" contains "saved", so the old wait
  // matched the opposite state instantly and the check raced the POST for as
  // long as it has existed.
  if ((await saved.count()) !== 0) {
    die(
      "the box holds unsaved changes and the saved-state locator already matches - " +
        "it is matching the state it exists to exclude, so the wait after the save proves nothing",
    );
  }

  const save = page.locator("[data-vm-layer-save]");
  if (await save.isDisabled()) die("save is disabled with unsaved changes in the box");
  await save.click();

  // TWO FAILURES, TWO SENTENCES. This wait used to end in `.catch(() => {})`,
  // so an editor that never reported the save was indistinguishable from one
  // that did: the check carried on, read the node, found the old text, and said
  // "the editor said saved and the node does not have it" - which it could not
  // know, having just discarded the only evidence that would say so.
  //
  // It cost two drainer cycles five days apart - 01M1AH5Z5H on 2026-08-31 and
  // 01M1SD4HYAE9BAA1BYJW821X2Y on 2026-09-05, unrelated trees both times - and
  // sent both readers after a node dropping a write, which is the alarming
  // reading and the wrong one.
  //
  // AND `:text-is`, NOT `:text`, WHICH IS WHY IT FLAKED AT ALL. Playwright's
  // `:text("saved")` is a SUBSTRING match, and the other state this span renders
  // is "unsaved changes" - which contains "saved". Measured rather than read off
  // the docs: a span holding "unsaved changes" matches `:text("saved")` and does
  // not match `:text-is("saved")`.
  //
  // So the wait matched the UNSAVED state instantly and never waited for
  // anything. The check has been racing the POST since it was written, and the
  // swallow above meant nothing could ever notice. That is the whole
  // intermittency: when the write was slower than the round trip, the node had
  // not got the text yet and the check blamed the store.
  //
  // The two changes are one fix and neither is sufficient. text-is makes the
  // wait real; not swallowing makes a wait that times out say so instead of
  // carrying on. See 01M1SHA4C23AXVY1FETTXE23Y2.
  const reported = await saved
    .waitFor({ state: "visible", timeout: 15_000 })
    .then(() => true)
    .catch(() => false);
  if (!reported) {
    die(
      "the editor never reported the save - no 'saved' state appeared within 15s of the click, " +
        "so the node was not asked and nothing here says whether it kept the text. " +
        "This is the page half, not the store half.",
    );
  }

  // THE NODE'S COPY, asked for separately. The page holding the right text
  // proves only that the page is holding it.
  const after = await call("/api/vm/layer?project=flowy", operatorToken);
  const got = after.status === 200 ? JSON.parse(after.body) : null;
  if (!got || !got.text.includes(mark)) {
    die(`the editor reported the save and the node does not have it - the page half
worked and the store half did not. Asked the node again and got
${after.status}: ${(got?.text ?? after.body).slice(0, 200)}`);
  }
  if (!got.exists) die("the node kept the text and still reports exists:false");

  console.log(
    `the editor wrote ${got.path} and the node has it back (${got.text.length} bytes), ` +
      `and the same door answers ${asOther.status} to a non-operator`,
  );
} finally {
  // PUT BACK, always. This check edits a file the next VM boot will apply, so
  // leaving its marker behind would make every later guest run a line written
  // by a test.
  await call("/api/vm/layer", operatorToken, {
    method: "POST",
    body: JSON.stringify({ project: "flowy", text: restore }),
  });
  await browser.close();
}
