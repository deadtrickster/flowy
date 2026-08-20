/**
 * WHICH PROJECT AM I IN, drawn on a page rather than deduced from a token.
 *
 *   node scripts/projects-check.mjs BASE_URL TOKEN
 *
 * The node had GET and POST /api/projects and no route drew either, so a person
 * could only learn which project they were writing into by saying something and
 * reading the line that came back. Two messages went into the wrong project's
 * #general that way and nobody saw them for ten minutes.
 *
 * WHAT IS ASSERTED, and why each one is not the obvious thing:
 *
 *   1. the page names the CURRENT project, and it is the one the door says. A
 *      page that renders a name from anywhere else - a config, a default, the
 *      product's own name in the rail - would be a page that cannot be wrong
 *      out loud, which is the failure it replaces;
 *   2. every project in the registry is drawn, so the page is not the current
 *      one with a list-shaped decoration beside it;
 *   3. a project this token cannot READ says so, in words. The registry lists
 *      names on edges in either direction and reading travels along one, so
 *      "you cannot see into it" and "there is nothing in it" are different
 *      facts that an empty row would collapse into one.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/projects-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

// What the node says, so the page is checked against the door rather than
// against this file's idea of what the door holds.
const answer = await fetch(`${base}/api/projects`, {
  headers: { Authorization: `Bearer ${token}` },
}).then((r) => {
  if (!r.ok) {
    console.error(`GET /api/projects answered ${r.status}`);
    process.exit(1);
  }
  return r.json();
});

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/projects`, { timeout: 20_000 }).catch(() => {});

  const list = page.locator("[data-projects]");
  try {
    await list.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    const failed = await page.locator("[data-projects-error]").count();
    console.error(
      failed
        ? `the projects page could not read the registry and drew its refusal instead.${errors}`
        : `/projects drew no project list: nothing matches [data-projects].${errors}`,
    );
    process.exit(1);
  }

  // 1. The current project, from the page, against the door.
  const current = await list.getAttribute("data-current-project");
  const said = (await page.locator("[data-current-panel]").innerText()).trim();
  if ((answer.current ?? "") !== (current ?? "")) {
    console.error(
      `the node says this token writes in ${JSON.stringify(answer.current ?? "")} and the page draws ${JSON.stringify(current)}`,
    );
    process.exit(1);
  }
  if (answer.current && !said.includes(answer.current)) {
    console.error(
      `the current project is in the markup and not on the screen: the panel reads ${JSON.stringify(said)}`,
    );
    process.exit(1);
  }

  // 2. Every project in the registry, not just the current one.
  const drawn = await page
    .locator("li[data-project]")
    .evaluateAll((nodes) => nodes.map((n) => n.getAttribute("data-project")));
  const missing = (answer.projects ?? []).map((p) => p.id).filter((id) => !drawn.includes(id));
  if (missing.length > 0) {
    console.error(
      `the registry has ${answer.count} projects and the page draws ${drawn.length}: missing ${missing.join(", ")}`,
    );
    process.exit(1);
  }

  // 3. An unreadable project says so rather than reading as empty.
  const reads = answer.reads ?? [];
  const unreadable = (answer.projects ?? []).find((p) => !reads.includes(p.id));
  let saidUnreadable = "not exercised - this token reads every project listed";
  if (unreadable) {
    const row = page.locator(`li[data-project="${unreadable.id}"]`);
    const flag = await row.getAttribute("data-project-readable");
    const text = (await row.innerText()).trim();
    if (flag !== "no" || !/cannot read/i.test(text)) {
      console.error(
        `${unreadable.id} is listed and unreadable by this token, and the page says ${JSON.stringify(text)}.
  "you cannot see into it" and "there is nothing in it" have to read differently.`,
      );
      process.exit(1);
    }
    saidUnreadable = `${unreadable.id} drawn as unreadable`;
  }

  console.log(
    `the console says which project this token writes in: ${JSON.stringify(answer.current ?? "")}, ${drawn.length} of ${answer.count} in the registry drawn, ${saidUnreadable}`,
  );
} finally {
  await browser.close();
}
