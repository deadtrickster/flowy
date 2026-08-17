/**
 * The reports page, in a real browser, asserted on ELEMENTS.
 *
 *   node scripts/reports-check.mjs BASE_URL TOKEN OLD_ID NEW_ID OLD_TITLE NEW_TITLE BODY_WORD
 *
 * Two claims, and neither can be made from the page's text alone.
 *
 * The mark. A report says which report replaced it - supersedes is written on
 * the NEWER document and points backwards, so nothing on the old one says it
 * has been overtaken and a reader who finds it acts on a stale document
 * believing it current. The node derives the reverse and puts it on the row, so
 * this reads the row's own attribute rather than looking for the words
 * somewhere on the page: the id of the replacement is also on the replacement's
 * own card, and a string that appears in two places is not evidence about
 * either. It also insists the NEWER report is listed at all - a list that
 * marked the old one and hid the new one would pass a text search for
 * "replaced by" and leave the link pointing at nothing on this page.
 *
 * The search. BODY_WORD appears only inside a report's body, and the list
 * renders titles. So a box that filtered the rows already on screen finds
 * nothing, and one that asks the node finds exactly the report that carries
 * the word. That is the whole distinction: the console's search has to be the
 * node's, over what report_search reaches - title, body, discovery and tags -
 * rather than a filter over what is already painted.
 */

import { chromium } from "playwright";

const [base, token, oldID, newID, oldTitle, newTitle, bodyWord] = process.argv.slice(2);

if (!base || !token || !oldID || !newID || !oldTitle || !newTitle || !bodyWord) {
  console.error(
    "usage: node scripts/reports-check.mjs BASE_URL TOKEN OLD_ID NEW_ID OLD_TITLE NEW_TITLE BODY_WORD",
  );
  process.exit(2);
}

/** die prints why, with what the list actually held, and stops. */
const die = async (why, list) => {
  let shown = "";
  try {
    shown = list ? await list.innerText() : "";
  } catch {
    shown = "(the list could not be read)";
  }
  console.error(`${why}\nthe list holds:\n${shown}`);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/reports`, { timeout: 20_000 }).catch(() => {});

  const list = page.locator('ol[aria-label="reports"]');
  const rows = list.locator("li[data-report]");

  try {
    await list.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    console.error(
      `/reports has no report list: no ol[aria-label="reports"].
The word "reports" is in the global nav too, so this looks for the ELEMENT.${errors}`,
    );
    process.exit(1);
  }

  // Wait for a ROW rather than for the list: the list paints from mount with
  // its empty state in it and the reports arrive one fetch later, so reading it
  // the moment it appears asserts on the empty state and fails a page that
  // works.
  try {
    await rows.first().waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    await die("/reports rendered the list and no reports at all", list);
  }

  const superseded = list.locator(`li[data-report="${oldID}"]`);
  const replacement = list.locator(`li[data-report="${newID}"]`);

  // Both documents are on the page. The replacement first, because its absence
  // is the failure that hides behind a correct mark.
  try {
    await replacement.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    await die(`the reports list never showed ${newTitle}`, list);
  }
  try {
    await superseded.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    await die(`the reports list never showed ${oldTitle}`, list);
  }

  // The mark, off the row's own attribute.
  const marked = await superseded.getAttribute("data-replaced-by");
  if (marked !== newID) {
    await die(`${oldTitle} says it was replaced by ${JSON.stringify(marked)}, want ${newID}`, list);
  }
  // And the other half: the newer one is not itself marked. Without this a page
  // that stamped every row with the same id would pass the assertion above.
  const alsoMarked = await replacement.getAttribute("data-replaced-by");
  if (alsoMarked) {
    await die(
      `${newTitle} is the replacement and says it was itself replaced by ${JSON.stringify(alsoMarked)}`,
      list,
    );
  }

  // The search, over a word that is in a body and on no card.
  const before = await list.innerText();
  if (before.includes(bodyWord)) {
    await die(
      `${JSON.stringify(bodyWord)} is already on the page, so narrowing to it proves nothing about where the search ran`,
      list,
    );
  }

  const box = page.locator('input[aria-label="search reports"]');
  try {
    await box.waitFor({ state: "visible", timeout: 10_000 });
  } catch {
    await die('/reports has no search box: no input[aria-label="search reports"]', list);
  }
  await box.fill(bodyWord);

  try {
    await page.waitForFunction(
      (want) => {
        const shown = [...document.querySelectorAll("li[data-report]")];
        return shown.length === 1 && shown[0].getAttribute("data-report") === want;
      },
      oldID,
      { timeout: 15_000 },
    );
  } catch {
    const found = await rows.evaluateAll((nodes) =>
      nodes.map((n) => n.getAttribute("data-report")),
    );
    await die(
      `searching for ${JSON.stringify(bodyWord)} - which is only in a body - left ${JSON.stringify(found)}, want just ${oldID}`,
      list,
    );
  }

  // Narrowed to it, and still marked: the hit says it has been replaced, which
  // is the case the mark exists for. Somebody who searched for a phrase they
  // remember lands on whichever document carries it, and that is as likely to
  // be the superseded one as the current one.
  const stillMarked = await rows.first().getAttribute("data-replaced-by");
  if (stillMarked !== newID) {
    await die(
      `the search hit says it was replaced by ${JSON.stringify(stillMarked)}, want ${newID}`,
      list,
    );
  }

  console.log(
    `/reports: ${newTitle} is listed, ${oldTitle} is marked replaced by ${newID}, and a body-only word narrows to it through the node`,
  );
} finally {
  await browser.close();
}
