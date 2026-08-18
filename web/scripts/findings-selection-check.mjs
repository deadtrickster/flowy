/**
 * Selecting findings, and what a bulk action says before and after it runs.
 *
 *   node scripts/findings-selection-check.mjs BASE_URL TOKEN PROJECT
 *
 * The python console this is parity with had select-all, an n-selected counter
 * and "run a selection"; twenty-seven findings carry repro trees now, which is
 * what makes this a batch rather than a list.
 *
 * The claims, in the order they would break:
 *
 *   - a tick is counted, and the count is on an element with the number in it
 *     rather than in prose somebody has to parse;
 *   - a selection that is only half runnable SAYS which half, before the button
 *     is pressed. A bulk action that silently skips three of five is one nobody
 *     can audit;
 *   - a filter that hides a selected row does not silently drop it - the count
 *     holds and the page says how many are off-screen;
 *   - and pressing it reports what came back. There is no runner in the gate,
 *     so what comes back is a refusal, and the assertion is that the page SAYS
 *     so rather than looking like nothing happened. A panel that swallowed the
 *     answer would be indistinguishable from one that worked.
 */

import { chromium } from "playwright";

const [base, token, project] = process.argv.slice(2);
if (!base || !token || !project) {
  console.error("usage: node scripts/findings-selection-check.mjs BASE_URL TOKEN PROJECT");
  process.exit(2);
}

const die = (why, shown) => {
  console.error(shown ? `${why}\nThe screen shows:\n${shown}` : why);
  process.exit(1);
};

const write = async (title, fields) => {
  const res = await fetch(`${base}/api/artifacts`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify({
      type: "finding",
      project,
      title,
      body: "seeded by the selection check",
      visibility: "project",
      fields,
    }),
  });
  if (!res.ok) die(`seeding ${title} answered ${res.status}`);
  return (await res.json()).id;
};

// One of each, because the run control's whole job is telling them apart. The
// runnable one carries a manifest shaped the way the store writes one; nothing
// here runs it, so the tree's contents are not the point.
const runnable = await write("selection: this one has a repro tree", {
  repro_files: JSON.stringify([{ path: "repro.sh", attachment_id: "01SEEDED" }]),
  repro_entrypoint: "repro.sh",
  repro_interp: "bash",
  severity: "high",
});
const bare = await write("selection: this one has no repro tree", { severity: "low" });

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/findings`, { timeout: 30_000 }).catch(() => {});

  const tick = (id) => page.locator(`[data-finding-select="${id}"]`);
  try {
    await tick(runnable).waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(`the findings list has no tick for ${runnable}, so nothing can be selected.${errors}`);
  }

  // 1. TICKED IS COUNTED.
  await tick(runnable).check();
  await tick(bare).check();
  const bar = page.locator("[data-finding-selection]");
  await bar.waitFor({ state: "visible", timeout: 10_000 });
  const count = await page
    .locator("[data-finding-selected-count]")
    .first()
    .getAttribute("data-finding-selected-count");
  if (count !== "2") {
    die(`two rows are ticked and the page says ${JSON.stringify(count)}`, await bar.innerText());
  }

  // 2. AND THE RUN CONTROL SAYS WHICH HALF IT WOULD ACTUALLY RUN.
  const run = page.locator("[data-finding-run-selected]");
  const label = (await run.innerText()).trim();
  if (!/1 of 2/.test(label)) {
    die(
      `the run control says ${JSON.stringify(label)}. One of the two selected findings has no
repro tree, so it has to say "run 1 of 2" - a control that offered to run both would
skip one silently, and one that said "run 2" would be lying about what it did`,
      await bar.innerText(),
    );
  }

  // 3. A FILTER DOES NOT SILENTLY DROP A SELECTED ROW.
  await page.selectOption('select[aria-label="severity"]', "high").catch(() => {});
  await page.waitForTimeout(300);
  const afterFilter = await page
    .locator("[data-finding-selected-count]")
    .first()
    .getAttribute("data-finding-selected-count");
  if (afterFilter !== "2") {
    die(`filtering to high severity changed the selection count to ${JSON.stringify(afterFilter)} -
a selection that moves with a filter is one nobody can trust`);
  }
  const off = page.locator("[data-finding-selected-offscreen]");
  if ((await off.count()) === 0) {
    die("one selected finding is filtered off the page and nothing says so", await bar.innerText());
  }

  // 4. PRESSING IT REPORTS WHAT CAME BACK.
  await run.click();
  const result = page.locator("[data-finding-bulk-result]");
  try {
    await result.waitFor({ state: "visible", timeout: 30_000 });
  } catch {
    die(`running the selection drew no result line - with no runner configured the node
answers 503, and a page that showed nothing would look exactly like one that worked`);
  }
  const said = (await result.innerText()).trim();
  if (!/queued|refused/.test(said)) {
    die(`the result line says ${JSON.stringify(said)}, which reports neither what was queued
nor what was refused`);
  }

  console.log(
    `the selection bar counted 2, offered "run 1 of 2", kept the count under a filter, and reported: ${said}`,
  );
} finally {
  await browser.close();
}
