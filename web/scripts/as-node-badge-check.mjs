/**
 * AN OPERATOR CAN OPEN A ROW OUTSIDE THE PROJECT THEY ARE STANDING IN, AND THE
 * PAGE SAYS THAT IS WHY.
 *
 *   node scripts/as-node-badge-check.mjs BASE_URL TOKEN OUTSIDE_ID OUTSIDE_PROJECT INSIDE_ID INSIDE_PROJECT
 *
 * The operator, 2026-09-05, on a link to a join request in Oracle: "interesting
 * what you have raised linked as http://.../p/_/_/<id> which is 404". The row
 * was real and had a page. Their console could not READ it - the single-row
 * fetch never asked for scope=all, though the door has honoured it for an
 * operator all along (api.go:899, auth.go:263) and the same api.ts sends it for
 * metrics and traces. Having failed to read the row, the console drew the link
 * with `_` for every field it could not see.
 *
 * ASSERTS A DIFFERENCE, NOT A PRESENCE. Two rows are opened by the same
 * operator in the same browser: one outside their project and one inside it.
 * The first must render AND carry the as-node mark; the second must render
 * WITHOUT it. A console that always retried, or that always drew the badge,
 * passes either arm alone and fails this pair.
 *
 * AND IT MUST NOT MATCH ON THE SENTINEL. Asserting "the path is not /p/_/_/id"
 * would pass the day somebody respells `_` as `-` with the defect standing, so
 * nothing here looks at the link text - it opens the row and asks whether the
 * page came back.
 *
 * IN A BROWSER, AND THAT IS NOT A PREFERENCE. This server answers 200 for every
 * path because it serves the SPA shell, so a curl probe reports success on a
 * page that does not render. A shell check here would be one that cannot fail.
 */
import { chromium } from "playwright";

const [base, token, outsideId, outsideProject, insideId, insideProject] = process.argv.slice(2);
if (!base || !token || !outsideId || !outsideProject || !insideId || !insideProject) {
  console.error(
    "usage: node scripts/as-node-badge-check.mjs BASE_URL TOKEN OUTSIDE_ID OUTSIDE_PROJECT INSIDE_ID INSIDE_PROJECT",
  );
  process.exit(2);
}
const die = (why) => {
  console.error(why);
  process.exit(1);
};

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  const open = async (project, id) => {
    await page.goto(`${base}/p/${project}/note/${id}`, { timeout: 30_000 });
    const title = page.locator(`[data-artifact-title="${id}"]`);
    const shown = await title
      .waitFor({ state: "visible", timeout: 15_000 })
      .then(() => true)
      .catch(() => false);
    const badge = await page.locator("[data-as-node]").count();
    return { shown, badge };
  };

  // OUTSIDE: the row this whole fix is about. Under the old code the fetch
  // 404s, nothing retries, and the page never draws a title at all.
  const outside = await open(outsideProject, outsideId);
  if (!outside.shown) {
    die(
      `the operator opened a row in ${outsideProject}, outside the project they are in, and the page did not draw it - the console is still not asking the door for it`,
    );
  }
  if (outside.badge !== 1) {
    die(
      `the row from ${outsideProject} rendered but the page does not say why the operator can see it (${outside.badge} marks) - a reader who does not know will quote it to somebody whose credential cannot open it`,
    );
  }

  // INSIDE: the control, and the reason this is a difference rather than a
  // presence. An ordinary row in the operator's own project needs no widening,
  // so saying "as node" over it would be false.
  const inside = await open(insideProject, insideId);
  if (!inside.shown) {
    die(
      `the operator could not open a row in their OWN project ${insideProject} - the retry broke the ordinary path`,
    );
  }
  if (inside.badge !== 0) {
    die(
      `a row in the operator's own project ${insideProject} was marked as read at node scope (${inside.badge} marks), so the mark says nothing`,
    );
  }

  console.log(
    `operator opened ${outsideId} in ${outsideProject} with the as-node mark, and ${insideId} in ${insideProject} without it`,
  );
} finally {
  await browser.close();
}
