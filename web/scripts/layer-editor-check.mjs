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

  const picker = page.locator("[data-vm-project]");
  await picker.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await picker.count()) === 0) die("the vms page drew no project picker");
  await picker.selectOption("flowy").catch(() => {});

  const box = page.locator("[data-vm-layer-text]");
  await box.waitFor({ state: "visible", timeout: 15_000 }).catch(() => {});
  if ((await box.count()) === 0) {
    die("picking a project drew no layer editor - the operator asked to manage these from the ui");
  }
  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);

  const mark = `# layer-editor-check ${before.text.length}`;
  const wanted = `${restore}\n${mark}\n`;
  await box.fill(wanted);

  const save = page.locator("[data-vm-layer-save]");
  if (await save.isDisabled()) die("save is disabled with unsaved changes in the box");
  await save.click();
  await page
    .locator('[data-vm-layer-state]:text("saved")')
    .waitFor({ state: "visible", timeout: 15_000 })
    .catch(() => {});

  // THE NODE'S COPY, asked for separately. The page holding the right text
  // proves only that the page is holding it.
  const after = await call("/api/vm/layer?project=flowy", operatorToken);
  const got = after.status === 200 ? JSON.parse(after.body) : null;
  if (!got || !got.text.includes(mark)) {
    die(`the editor said saved and the node does not have it. Asked the node again and got
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
