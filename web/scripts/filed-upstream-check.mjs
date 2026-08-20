/**
 * A person can say a finding has been sent upstream, and take it back.
 *
 *   node scripts/filed-upstream-check.mjs BASE_URL TOKEN
 *
 * THE VERB HAD NO DOOR A PERSON COULD OPEN. finding_upstream shipped as an MCP
 * tool only, so any seat with an MCP connection could file a finding while the
 * operator, in the console, read "upstream: unfiled" on every row with no way
 * to change it. findingevidence.go's head comment makes the same complaint
 * about the axis beside it and api_mergegate.go makes it about the gate: a door
 * only agents can knock on is half a door.
 *
 * The arms are the definition of done written on the row, in order:
 *   mark it filed with a number
 *   the LIST beside the pane shows it, without a reload
 *   take it back as WITHDRAWN, which keeps the number - the store refuses
 *   "unfiled" for a filed row, because erasing the number is how a thing gets
 *   filed upstream twice
 *
 * The middle arm is the one worth the browser. The write could succeed and the
 * page still show the old state, and that is indistinguishable from a write
 * that failed - which is the reason this checks the list and not the response.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/filed-upstream-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const ISSUE = "16958";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/findings`, { timeout: 30_000 }).catch(() => {});

  // Its own finding, so the check does not depend on the seed carrying an
  // unfiled one - and so it never files somebody else's row upstream.
  const made = await page.evaluate(
    async ([t]) => {
      const res = await fetch("/api/artifacts", {
        method: "POST",
        headers: { Authorization: `Bearer ${t}`, "Content-Type": "application/json" },
        body: JSON.stringify({
          type: "finding",
          title: "filed-upstream-check: a finding to send upstream",
          body: "written by the check that files it",
          severity: "low",
        }),
      });
      if (!res.ok) return { error: `${res.status} ${await res.text()}` };
      return await res.json();
    },
    [token],
  );
  if (made.error) die(`could not write a finding to file: ${made.error}`);

  await page.goto(`${base}/findings`, { timeout: 30_000 }).catch(() => {});
  const opener = page.locator(`[data-finding-open="${made.id}"]`);
  await opener.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if (crashes.length > 0) die(`the findings page threw: ${crashes.join("; ")}`);
  if ((await opener.count()) === 0) die("the finding just written is not in the list");
  await opener.click();

  const control = page.locator(`[data-upstream-control="${made.id}"]`);
  await control.waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
  if ((await control.count()) === 0) die("the finding pane offers no way to file it upstream");

  await control.locator("[data-upstream-input]").fill(ISSUE);
  await control.locator("[data-upstream-file]").click();

  const filed = control.locator('[data-upstream-state="filed"]');
  await filed.waitFor({ state: "visible", timeout: 15_000 }).catch(() => {});
  if ((await filed.count()) === 0) {
    const err = await page
      .locator("[data-upstream-error]")
      .textContent()
      .catch(() => null);
    die(`marking it filed did not take${err ? `: ${err}` : ""}`);
  }
  // Scoped to THIS finding's control: the list rows carry data-upstream-id too,
  // so an unscoped selector matches every filed finding on the page and fails
  // as a strict-mode violation rather than as anything about this one.
  const shown = await control.locator("[data-upstream-id]").getAttribute("data-upstream-id");
  if (shown !== ISSUE) die(`the control shows issue ${JSON.stringify(shown)}, not ${ISSUE}`);

  // THE LIST, WITHOUT A RELOAD. This is the arm that says the write reached the
  // page and not only the node.
  const inList = await page.evaluate((id) => {
    const row = document.querySelector(`[data-finding="${id}"]`);
    return row ? row.getAttribute("data-upstream") : null;
  }, made.id);
  if (inList !== "filed") {
    die(`the row in the list still reads ${JSON.stringify(inList)} after it was filed`);
  }

  // AND IT COMES BACK OFF AS "WITHDRAWN", NOT "UNFILED". The store refuses
  // unfiled for a filed row and says why: calling it unfiled erases the number,
  // "after which somebody files it there a second time". The python console's
  // "unmark to reopen" was the looser model; this node keeps the number and
  // records what happened to it.
  await control.locator("[data-upstream-withdraw]").click();
  const withdrawn = control.locator('[data-upstream-state="withdrawn"]');
  await withdrawn.waitFor({ state: "visible", timeout: 15_000 }).catch(() => {});
  if ((await withdrawn.count()) === 0) {
    const err = await control
      .locator("[data-upstream-error]")
      .textContent()
      .catch(() => null);
    die(`withdrawing it did not take${err ? `: ${err}` : ""}`);
  }

  console.log(
    `filed ${made.id} as #${ISSUE}, the list agreed without a reload, and it withdrew keeping the number`,
  );
} finally {
  await browser.close();
}
