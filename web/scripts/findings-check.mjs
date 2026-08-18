/**
 * The findings console, in a real browser, asserted on ELEMENTS.
 *
 *   node scripts/findings-check.mjs BASE_URL TOKEN PROJECT FILED_ID UNFILED_ID ISSUE DRAFT_WORD REPRO_PATH
 *
 * FOUR CLAIMS, and each one of them is a defect this console has already shipped
 * in some other shape.
 *
 * BOTH AXES ON THE ROW. A finding carries our lifecycle (open/triaged/done) AND
 * a filing state on somebody else's tracker (unfiled/filed as #123/accepted),
 * and neither answers the other: done-and-unfiled - written up, nobody sent it -
 * is the state most of the corpus is in, and a list drawing only `status` calls
 * that finished work. So this reads the two off the row's own attributes rather
 * than searching the page for words: "done" and "filed" are one string apart on
 * screen, and a page that drew our status twice would pass any text search.
 *
 * THE ISSUE NUMBER IS ON SCREEN. It is the only part of a filing a reader can
 * act on. A state word without it means "somebody sent this somewhere" and
 * nothing else.
 *
 * THE MARK IS A FILTER. "show me everything written up and not yet filed" is the
 * question this list exists to answer - it is the list somebody works from
 * before filing anything - so unfiled is selectable, and selecting it must
 * remove the filed row rather than merely reordering or dimming it.
 *
 * THE REPORT DRAFT IS ITS OWN DOCUMENT, on the finding page, and its ABSENCE is
 * drawn. The text that goes upstream is not the body (written for us) and not
 * the discovery (how it was found, and never packaged), and a page that omitted
 * the pane when there is no draft would read as a finding ready to send - the
 * missing upstream write-up being the commonest reason a finding sits unfiled.
 *
 * Nothing here seeds: the two findings arrive from the gate, which writes them
 * through the node's own door. A browser check that wrote its own fixtures
 * would be asserting against rows no other reader could see.
 */

import { chromium } from "playwright";

const [base, token, project, filedID, unfiledID, referencedID, issue, draftWord, reproPath] =
  process.argv.slice(2);

if (
  !base ||
  !token ||
  !project ||
  !filedID ||
  !unfiledID ||
  !referencedID ||
  !issue ||
  !draftWord ||
  !reproPath
) {
  console.error(
    "usage: node scripts/findings-check.mjs BASE_URL TOKEN PROJECT FILED_ID UNFILED_ID REFERENCED_ID ISSUE DRAFT_WORD REPRO_PATH",
  );
  process.exit(2);
}

/** die prints why, with what the page actually held, and stops. */
const die = async (why, where) => {
  let shown = "";
  try {
    shown = where ? await where.innerText() : "";
  } catch {
    shown = "(the page could not be read)";
  }
  console.error(`${why}\nthe page holds:\n${shown}`);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/findings`, { timeout: 20_000 }).catch(() => {});

  const list = page.locator('ol[aria-label="findings"]');
  try {
    await list.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    console.error(
      `/findings has no findings list: no ol[aria-label="findings"].
The word "findings" is in the global nav too, so this looks for the ELEMENT.${errors}`,
    );
    process.exit(1);
  }

  const filed = list.locator(`li[data-finding="${filedID}"]`);
  const unfiled = list.locator(`li[data-finding="${unfiledID}"]`);

  // Wait for a ROW rather than for the list: the list paints from mount with
  // its empty state in it and the findings arrive one fetch later.
  try {
    await filed.waitFor({ state: "visible", timeout: 15_000 });
    await unfiled.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    await die("the findings list never showed both seeded findings", list);
  }

  // Both axes, off the row's own attributes. The filed one is DONE for us and
  // FILED for them, and reading the same word twice would mean one axis is
  // standing in for the other.
  const lifecycle = await filed.getAttribute("data-lifecycle");
  const upstream = await filed.getAttribute("data-upstream");
  if (lifecycle !== "done") {
    await die(
      `the filed finding says our lifecycle is ${JSON.stringify(lifecycle)}, want done`,
      list,
    );
  }
  if (upstream !== "filed") {
    await die(
      `the filed finding says its upstream filing is ${JSON.stringify(upstream)}, want filed - our status and their tracker are two axes and this row is only showing one`,
      list,
    );
  }
  const upstreamID = await filed.getAttribute("data-upstream-id");
  if (upstreamID !== issue) {
    await die(`the filed finding carries issue ${JSON.stringify(upstreamID)}, want ${issue}`, list);
  }
  // And the number is legible, not only in an attribute: a reader acts on #123.
  const filedText = await filed.innerText();
  if (!filedText.includes(`#${issue}`)) {
    await die(`the filed row never shows #${issue} - the number is the actionable half`, filed);
  }

  // The other row is unfiled, and that is a fact rather than a blank: a finding
  // carrying none of the filing keys reads as unfiled, the same answer the store
  // gives for one.
  const alsoFiled = await unfiled.getAttribute("data-upstream");
  if (alsoFiled !== "unfiled") {
    await die(
      `the unfiled finding says ${JSON.stringify(alsoFiled)} - a row with no filing keys is unfiled`,
      list,
    );
  }

  // REFERENCED IS NOT FILED. This row names two things over there and nobody
  // claims to have sent it, and it carries no state word at all - so the page
  // has to reach the store's own answer for that shape (FindingUpstreamOf's
  // fallback) rather than calling it unfiled, and must not fold it into the
  // filed count. Reading these as filings is what reported one filing as eight.
  const referenced = list.locator(`li[data-finding="${referencedID}"]`);
  try {
    await referenced.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    await die("the findings list never showed the referenced finding", list);
  }
  const cites = await referenced.getAttribute("data-upstream");
  if (cites !== "referenced") {
    await die(
      `the finding that cites two issues and was sent to nobody says ${JSON.stringify(cites)}, want referenced`,
      list,
    );
  }

  // The counts, which is what makes the page report its own state without being
  // filtered first.
  const counts = page.locator('[aria-label="findings counts"]');
  if ((await counts.count()) === 0) {
    await die("the findings page shows no counts at all", list);
  }
  const unfiledCount = Number(await counts.getAttribute("data-unfiled"));
  const filedCount = Number(await counts.getAttribute("data-filed"));
  const referencedCount = Number(await counts.getAttribute("data-referenced"));
  if (!(unfiledCount >= 1 && filedCount >= 1 && referencedCount >= 1)) {
    await die(
      `the counts say ${unfiledCount} unfiled, ${referencedCount} referenced and ${filedCount} filed, and all three seeded findings are on the page`,
      counts,
    );
  }
  // The three are separate counts and the referenced one is NOT inside filed:
  // one seeded finding is filed and one is referenced, so a page folding them
  // together reports two.
  if (filedCount !== 1) {
    await die(
      `the page counts ${filedCount} findings as filed upstream, and exactly one was sent anywhere - a referenced finding is not a filed one`,
      counts,
    );
  }

  // The mark as a filter: unfiled selected, the filed row goes.
  await page.selectOption("#finding-filter-upstream", "unfiled");
  try {
    await filed.waitFor({ state: "detached", timeout: 10_000 });
  } catch {
    await die(
      "narrowing to unfiled left the filed finding on the page - the mark is a filter, not a badge",
      list,
    );
  }
  if ((await unfiled.count()) !== 1) {
    await die("narrowing to unfiled also removed the unfiled finding", list);
  }
  // And the referenced one goes with the filed one, because it is not unfiled
  // either - it is the third answer, and a filter that kept it here would be
  // saying nobody has touched it upstream.
  if ((await referenced.count()) !== 0) {
    await die(
      "narrowing to unfiled kept the finding that cites two of their issues - referenced is not unfiled",
      list,
    );
  }

  // The finding page: three faces, and the repro tree.
  await page.goto(`${base}/p/${project}/finding/${filedID}`, { timeout: 20_000 }).catch(() => {});
  const report = page.locator("[data-finding-report]");
  try {
    await report.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    await die(
      `the finding page for ${filedID} drew no report-draft pane at all`,
      page.locator("body"),
    );
  }
  if ((await report.getAttribute("data-finding-report")) !== "yes") {
    await die("the finding with an upstream draft says it has none", report);
  }
  const reportText = await report.innerText();
  if (!reportText.includes(draftWord)) {
    await die(
      `the report pane never shows the draft - ${JSON.stringify(draftWord)} is not in it`,
      report,
    );
  }
  // The body and the draft are different documents, and the draft must not be
  // the body echoed: the seeded body does not carry this word.
  const body = page.locator("[data-artifact-body]");
  if ((await body.innerText()).includes(draftWord)) {
    await die("the draft's words are in the body too, so this proves nothing about either", body);
  }

  // The repro tree: what would actually run, not only that something would.
  const tree = page.locator("[data-finding-repro]");
  if ((await tree.getAttribute("data-finding-repro")) !== "yes") {
    await die("the finding carries a repro tree and the page says it does not", tree);
  }
  if (!(await tree.innerText()).includes(reproPath)) {
    await die(`the repro tree pane never names ${reproPath}`, tree);
  }

  // And the absence, on the other one, said out loud.
  await page.goto(`${base}/p/${project}/finding/${unfiledID}`, { timeout: 20_000 }).catch(() => {});
  const noReport = page.locator("[data-finding-report]");
  try {
    await noReport.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    await die(
      "the finding without a draft drew no report pane, so its absence says nothing",
      page.locator("body"),
    );
  }
  if ((await noReport.getAttribute("data-finding-report")) !== "no") {
    await die("the finding without a draft claims to have one", noReport);
  }
  if (!(await noReport.innerText()).includes("finding_write")) {
    await die(
      "the empty report pane does not name the verb that fills it, so a reader is told there is nothing rather than what to do",
      noReport,
    );
  }

  if (crashes.length) {
    console.error(`the findings pages threw:\n  ${crashes.join("\n  ")}`);
    process.exit(1);
  }
  console.log(
    "findings: both axes on the row, the number on screen, the mark filters, the draft and the tree are their own panes",
  );
} finally {
  await browser.close();
}
