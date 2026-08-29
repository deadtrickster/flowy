/**
 * A PASTED SCREENSHOT SAYS IT ARRIVED.
 *
 *   node scripts/note-paste-check.mjs BASE_URL TOKEN
 *
 * 01M17DE6CVB476710FG7K4ZWSG. The operator: "when I pasted the screenshot from
 * clipbard - 'attach a file' stayed empty and text was inserted as a markdown
 * tag - I had to wonder if I will see the screenshot attached or not". The
 * upload had worked. What failed was the confirmation: the reference went into
 * the draft, the control went back to reading "attach a file", and the only
 * evidence was a tag the writer had to parse in their head.
 *
 * THE GESTURE IS THE POINT, so this pastes rather than driving the file picker.
 * Both routes go through the same attach(), so a check on the picker would pass
 * while the reported case stayed broken if they ever diverge - and paste is the
 * first-class route here, because a screenshot is in the clipboard and was
 * never a file on disk.
 *
 * BOTH HALVES ARE ASSERTED, because the operator asked for both: "screenshots
 * should be supported in notes/comments... plus listed in attachment, JIRA
 * style". The markdown is what the note will SAY; the chip is what the box has
 * GOT. Only the first was built, and the gap between them is the doubt that was
 * reported - so a version that quietly dropped the markdown would also be wrong.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/note-paste-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const api = async (path, init) => {
  const res = await fetch(`${base}${path}`, {
    ...init,
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
  });
  if (!res.ok) throw new Error(`${path}: ${res.status} ${await res.text()}`);
  return res.json();
};

// A REAL PNG, one pixel of it. An empty file is refused by name in lib/attach,
// so a zero-byte stand-in would exercise the refusal path and report the wrong
// thing as working.
const PNG =
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==";

const filed = [];
let browser;
try {
  const who = await api("/api/whoami");
  const project = who.project || "";
  if (!project) {
    die("this token is in no project, so there is no row page to open");
  }

  const row = await api("/api/artifacts", {
    method: "POST",
    body: JSON.stringify({
      type: "memory",
      kind: "todo",
      title: `note-paste-check ${Date.now().toString(36)}`,
      body: "seeded by note-paste-check",
    }),
  });
  filed.push(row.id);

  browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/p/${encodeURIComponent(project)}/memory/${encodeURIComponent(row.id)}`, {
    timeout: 30_000,
  });

  const draft = page.locator("[data-note-draft]");
  await draft.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await draft.count()) === 0) {
    die(
      `the row page draws no note box, so there is nothing to paste into${
        crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : ""
      }`,
    );
  }

  // The paste itself, built in the page because a File and a DataTransfer only
  // exist there. React binds onPaste at the root, so this bubbles to it exactly
  // as a real paste does.
  await page.evaluate((b64) => {
    const bin = atob(b64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    const file = new File([bytes], "shot.png", { type: "image/png" });
    const dt = new DataTransfer();
    dt.items.add(file);
    const el = document.querySelector("[data-note-draft]");
    el.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true }));
  }, PNG);

  // The upload is a round trip to the node, so this waits for the answer rather
  // than for a frame.
  const chip = page.locator("[data-note-carried]");
  try {
    await chip.first().waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const said = await draft.inputValue().catch(() => "");
    const attached = /!\[[^\]]*\]\([^)]+\)/.test(said);
    die(`a screenshot was pasted and the box lists nothing it is carrying.
${
  attached
    ? `The draft DID get a markdown tag, so the upload worked and only the
confirmation is missing - which is exactly what was reported: the writer is left
parsing a tag to find out whether their image is on the note.`
    : `The draft has no markdown tag either, so the paste did not attach at all -
a different and worse failure than the one this check was written for.`
}
draft was: ${JSON.stringify(said.slice(0, 200))}${
      crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : ""
    }`);
  }

  const label = (await chip.first().innerText()).trim();
  if (!label.includes("shot.png")) {
    die(`the box says it is carrying something and does not say WHAT: ${JSON.stringify(label)}.
"1 attached" is the same sentence whether the right screenshot is on it or the
wrong one, which is why the name is the assertion.`);
  }
  if (!/\d/.test(label)) {
    die(`the chip names the file and not its size: ${JSON.stringify(label)}. A size is how a
person tells a screenshot from an empty file that uploaded fine.`);
  }

  // AND THE MARKDOWN IS STILL THERE. The reference is what the note will render;
  // listing the file must not have replaced it.
  const said = await draft.inputValue();
  if (!/!\[[^\]]*\]\([^)]+\)/.test(said)) {
    die(`the file is listed but the draft carries no markdown reference, so the note
would be written without the image in its body: ${JSON.stringify(said.slice(0, 200))}`);
  }

  if (crashes.length > 0) {
    die(`the page threw while attaching: ${crashes.join("; ")}`);
  }

  console.log(
    `a pasted screenshot is listed as ${JSON.stringify(label)} and referenced in the draft - the writer can see it arrived without reading the markup`,
  );
} finally {
  if (browser) await browser.close();
  for (const id of filed) {
    await api(`/api/artifact/${id}/status`, {
      method: "POST",
      body: JSON.stringify({ status: "done", note: "seeded by note-paste-check" }),
    }).catch(() => {});
  }
}
