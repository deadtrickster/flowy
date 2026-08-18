/**
 * THE STATE COLOURS, in a real browser: can a person tell two states apart at a
 * glance, without reading a word.
 *
 *   node scripts/statecolour-check.mjs BASE_URL TOKEN PROJECT FILED_ID UNFILED_ID REFERENCED_ID REPORT_OLD REPORT_NEW
 *
 * WHY THIS IS NOT "IS IT COLOURED". That question is the one a decoration
 * passes. The operator's complaint was that everything except chat looks bland,
 * and the fix that would satisfy a presence check - tint every chip something -
 * is worse than bland, because a page where colour means nothing teaches people
 * to stop looking at it. So every assertion here is a DISCRIMINATION between two
 * states a reader can name, and the failure messages say which two.
 *
 * Three questions are asked of every pair, and all three have to hold:
 *
 *   DIFFERENT states are drawn in different colours. This is the one that
 *   catches a palette that is defined and never wired up - the exact shape of
 *   the bug colour-check.mjs was written for, where three todo states shared two
 *   colours and "done" was indistinguishable from "waiting".
 *
 *   Neither is the ORDINARY TEXT COLOUR, taken off the page rather than
 *   hard-coded, so this cannot pass by calling the default foreground a state.
 *
 *   The SAME state is drawn the same everywhere. The list and the finding page
 *   are two components, and a fact that changed colour between them would be two
 *   vocabularies for one axis - the reader would have to learn both and could
 *   trust neither.
 *
 * Nothing here seeds. The rows arrive from the gate, which writes them through
 * the node's own doors, and the ids come in on the command line - see
 * seeds_two_findings_on_three_axes in run-tests.sh. A browser check that wrote
 * its own fixtures would be asserting against rows no other reader can see.
 */

import { chromium } from "playwright";

const [base, token, project, filedID, unfiledID, referencedID, reportOld, reportNew] =
  process.argv.slice(2);

if (!base || !token || !project || !filedID || !unfiledID || !referencedID) {
  console.error(
    "usage: node scripts/statecolour-check.mjs BASE_URL TOKEN PROJECT FILED_ID UNFILED_ID REFERENCED_ID REPORT_OLD REPORT_NEW",
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

/**
 * The colour a mark is ACTUALLY drawn in, both halves of the pair.
 *
 * getComputedStyle rather than the style attribute or the class list: a tint
 * written into an inline style that a later rule overrides, and a class that
 * never made it into the bundle, both read here as what the eye would see. That
 * is the whole reason this check needs a browser rather than a grep over the
 * source - a stylesheet that defines a palette and a component that never uses
 * it are identical from every angle except a rendered page.
 */
const pairOf = (locator) =>
  locator.evaluate((n) => {
    const style = getComputedStyle(n);
    return { fg: style.color, bg: style.backgroundColor };
  });

/** The mark for one axis and one state, wherever it is on the page. */
const chip = (scope, axis, state) =>
  scope.locator(`[data-state-axis="${axis}"][data-state="${state}"]`).first();

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));

  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/findings`, { timeout: 20_000 }).catch(() => {});

  const list = page.locator('ol[aria-label="findings"]');
  const filed = list.locator(`li[data-finding="${filedID}"]`);
  const unfiled = list.locator(`li[data-finding="${unfiledID}"]`);
  const referenced = list.locator(`li[data-finding="${referencedID}"]`);
  try {
    await filed.waitFor({ state: "visible", timeout: 15_000 });
    await unfiled.waitFor({ state: "visible", timeout: 15_000 });
    await referenced.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    await die(`the findings list never showed the three seeded findings${errors}`, list);
  }

  // The ordinary text colour, taken off the page. A hard-coded rgb() here would
  // make every assertion below pass on a theme it was never looked at under.
  const plain = await page
    .locator('ol[aria-label="findings"] li')
    .first()
    .evaluate((n) => getComputedStyle(n).color);

  /** Two states, named, and the three questions asked of the pair. */
  const tellApart = async (what, a, b) => {
    if (a.fg === b.fg && a.bg === b.bg) {
      console.error(
        `${what} are drawn identically (${a.fg} on ${a.bg}), so nothing on the row tells them apart.
A colour that does not discriminate is worse than no colour: it teaches a reader that colour here means nothing.`,
      );
      process.exit(1);
    }
    for (const [name, pair] of [
      ["the first", a],
      ["the second", b],
    ]) {
      if (pair.fg === plain && pair.bg === "rgba(0, 0, 0, 0)") {
        console.error(
          `${what}: ${name} is the ordinary text colour on no background (${plain}), so it is not saying anything - ${what} needs both halves of a state pair.`,
        );
        process.exit(1);
      }
    }
  };

  // FLOW 1. Tell filed from unfiled at a glance, and referenced from both -
  // referenced is the third answer, and reading it as a filing is what turned
  // one filing into eight.
  const filedMark = await pairOf(chip(filed, "upstream", "filed"));
  const unfiledMark = await pairOf(chip(unfiled, "upstream", "unfiled"));
  const referencedMark = await pairOf(chip(referenced, "upstream", "referenced"));
  await tellApart("filed and unfiled", filedMark, unfiledMark);
  await tellApart("referenced and unfiled", referencedMark, unfiledMark);
  await tellApart("referenced and filed", referencedMark, filedMark);

  // FLOW 2. THE EVIDENCE AXIS, asserted over WHATEVER STATES THE PAGE ACTUALLY
  // RENDERS rather than over three states named here.
  //
  // It used to name them - reproduced on the filed row, source on the referenced
  // one, "not stated" on the unfiled one - and that was wrong in a way worth
  // recording. The evidence door check now runs BEFORE this one and rewrites all
  // three rows through the verb (the unfiled row becomes refuted, the referenced
  // one becomes verified), so a check that hard-coded the words was asserting
  // against a fixture that another session owns and moves. Reading the rendered
  // states instead means this measures the rule - one fact, one colour - rather
  // than one arrangement of the seed data.
  //
  // The floor is the guard against silent degradation: fewer than three distinct
  // states on the page means the seeds changed under this check and it is no
  // longer testing discrimination, which must fail loudly rather than pass.
  const evidenceChips = await page.$$eval('[data-state-axis="evidence"]', (nodes) =>
    nodes.map((n) => {
      const s = getComputedStyle(n);
      return { state: n.getAttribute("data-state") || "", fg: s.color, bg: s.backgroundColor };
    }),
  );
  const byEvidence = new Map();
  for (const c of evidenceChips) if (c.state) byEvidence.set(c.state, { fg: c.fg, bg: c.bg });
  if (byEvidence.size < 3) {
    await die(
      `the page shows ${byEvidence.size} distinct evidence state(s) (${[...byEvidence.keys()].join(", ") || "none"}) and this check needs at least three to demonstrate they are told apart`,
      list,
    );
  }
  for (const [a, pa] of byEvidence) {
    for (const [b, pb] of byEvidence) {
      if (a < b) await tellApart(`${a} and ${b} evidence`, pa, pb);
    }
  }
  // And the pair that is not a scale: verified and refuted are opposite outcomes
  // of the same act - a run against a named commit that found the bug, and one
  // that found nothing - so they must not be neighbouring shades of one colour.
  // Folding them together sends somebody upstream with a defect that is not there.
  if (byEvidence.has("verified") && byEvidence.has("refuted")) {
    await tellApart(
      "verified and refuted, which are opposite outcomes rather than two steps on a scale",
      byEvidence.get("verified"),
      byEvidence.get("refuted"),
    );
  }

  // The filed row's OWN evidence state and colour, read off the row rather than
  // named here, for the cross-surface comparison in flow 6 below. Naming a word
  // is what broke this check once already: the evidence door check runs first and
  // rewrites these rows, so the only durable question is "whatever this row says,
  // does the finding page say it the same way".
  const filedEvidenceChip = filed.locator('[data-state-axis="evidence"]').first();
  const filedEvidenceState = await filedEvidenceChip.getAttribute("data-state");
  const filedEvidence = await pairOf(filedEvidenceChip);
  if (!filedEvidenceState) {
    await die("the filed finding draws no evidence chip at all, so its axis is missing", filed);
  }

  // And a finding that ships something runnable from one that does not, which is
  // a different fact from what running it proved.
  await tellApart(
    "a finding with a repro tree and one without",
    await pairOf(chip(filed, "repro", "yes")),
    await pairOf(chip(unfiled, "repro", "no")),
  );

  // FLOW 3. A severity is legible without reading a word: the dot before the
  // title, and a high row does not share it with a low one.
  const dotOf = async (row, severity) => {
    const dot = row.locator(`[data-severity-dot="${severity}"]`);
    if ((await dot.count()) === 0) {
      await die(`the ${severity} finding has no severity dot, so its severity is only a word`, row);
    }
    return dot.first().evaluate((n) => getComputedStyle(n).backgroundColor);
  };
  const high = await dotOf(filed, "high");
  const medium = await dotOf(unfiled, "medium");
  const low = await dotOf(referenced, "low");
  const dots = new Set([high, medium, low]);
  if (dots.size < 3) {
    console.error(
      `three severities share ${dots.size} dot colour(s) - high ${high}, medium ${medium}, low ${low}.
A dot that is the same for high and low answers nothing the title did not already.`,
    );
    process.exit(1);
  }

  // The stacked bar, which is the one mark a LIST has that no amount of per-row
  // colour gives: the shape of the corpus, before anything is opened.
  const bar = page.locator("[data-severity-bar]");
  if ((await bar.count()) === 0) {
    await die("the findings list draws no severity bar, so the list itself has no shape", list);
  }
  const segments = await bar.first().locator("[data-severity-segment]").count();
  if (segments < 3) {
    await die(
      `the severity bar has ${segments} segment(s) and three severities are on the page - a bar with one segment is a rule, not a proportion`,
      bar.first(),
    );
  }

  // FLOW 7. WHOSE CODE IS THIS ABOUT. The operator asked twice and the answer
  // was nowhere on the page: "I still dont see serenedb and ragflow findings".
  // The seeded corpus has two groups - the two rows that name serenedb, and the
  // one that names nobody - so this asserts the split exists and that the filed
  // finding is filed UNDER ITS OWN TRACKER rather than into a single heap.
  //
  // Grouping is done over the rows already fetched because this is GROUPING and
  // not filtering - the page needs every finding in hand to sort them under
  // headings. The node can narrow by tag since 2b0fe67; that changed nothing
  // here, which was the point of keying the heading off the row rather than off
  // the request.
  const groups = page.locator("[data-finding-group]");
  const groupCount = await groups.count();
  if (groupCount < 2) {
    await die(
      `the findings list drew ${groupCount} upstream-project group(s) and the seeded corpus has two - a list that heaps every corpus together is the thing that was reported missing`,
      list,
    );
  }
  const named = await groups.evaluateAll((nodes) =>
    nodes.map((n) => n.getAttribute("data-finding-group")),
  );
  if (!named.includes("serenedb")) {
    console.error(
      `the findings groups are ${JSON.stringify(named)} and none of them is the tracker the seeded findings name.
Grouping by something that is not the upstream project answers a question nobody asked.`,
    );
    process.exit(1);
  }
  // THE ROW WITH NO FILING GROUPS BY ITS OWN PROJECT. This is the shape the live
  // corpus is in, and the one the seeded data nearly let through: the findings
  // were re-filed into real projects, so they carry project=ragflow or
  // project=serenedb and NO upstream_tracker, because a filing is something
  // somebody does later and most of the corpus is unfiled. A grouping that read
  // only trackers and tags finds nothing on those rows and collapses the page
  // back into one heap - while every gate check still passes, because the SEEDED
  // findings do carry a tracker. So this asserts the fallback directly: the
  // unfiled row sits under its project, not under the "nothing known" heading.
  const unfiledGroup = page.locator(
    `[data-finding-group="${project.toLowerCase()}"] li[data-finding="${unfiledID}"]`,
  );
  if ((await unfiledGroup.count()) !== 1) {
    await die(
      `the finding with no filing is not grouped under its own project ${JSON.stringify(project)} - a row naming no tracker still belongs to somebody, and dropping it into an unattributed heap is how the entire live corpus reads as ungrouped`,
      list,
    );
  }

  // And the filed finding is UNDER that heading rather than merely on the page.
  const inSerenedb = page.locator(`[data-finding-group="serenedb"] li[data-finding="${filedID}"]`);
  if ((await inSerenedb.count()) !== 1) {
    await die(
      "the finding filed with serenedb is not under the serenedb heading, so the grouping is drawn but not applied",
      list,
    );
  }

  // FLOW 4. Open the finding page and see its state without reading text - AND
  // in the same colours the row wore. Two components, one vocabulary.
  await page.goto(`${base}/p/${project}/finding/${filedID}`, { timeout: 20_000 }).catch(() => {});
  const pageFiled = chip(page, "upstream", "filed");
  try {
    await pageFiled.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    await die("the finding page draws no upstream state mark at all", page.locator("body"));
  }
  const sameFiled = await pairOf(pageFiled);
  if (sameFiled.fg !== filedMark.fg || sameFiled.bg !== filedMark.bg) {
    console.error(
      `"filed" is ${filedMark.fg} on ${filedMark.bg} in the list and ${sameFiled.fg} on ${sameFiled.bg} on the finding page.
One fact drawn two ways is two vocabularies for one axis, and a reader can trust neither.`,
    );
    process.exit(1);
  }
  const sameEvidence = await pairOf(chip(page, "evidence", filedEvidenceState));
  if (sameEvidence.fg !== filedEvidence.fg || sameEvidence.bg !== filedEvidence.bg) {
    console.error(
      `"${filedEvidenceState}" is ${filedEvidence.fg} on ${filedEvidence.bg} in the list and ${sameEvidence.fg} on ${sameEvidence.bg} on the finding page - the same axis has to read the same in both places.`,
    );
    process.exit(1);
  }
  if ((await page.locator("[data-severity-dot]").count()) === 0) {
    await die("the finding page drops the severity dot the list drew", page.locator("body"));
  }

  // FLOW 5. Tell a current report from a replaced one, before reading any text.
  if (reportOld && reportNew) {
    await page.goto(`${base}/reports`, { timeout: 20_000 }).catch(() => {});
    const oldRow = page.locator(`li[data-report="${reportOld}"]`);
    const newRow = page.locator(`li[data-report="${reportNew}"]`);
    try {
      await oldRow.waitFor({ state: "visible", timeout: 15_000 });
      await newRow.waitFor({ state: "visible", timeout: 15_000 });
    } catch {
      await die(
        "the reports list never showed both halves of the seeded pair",
        page.locator("body"),
      );
    }
    const replacedMark = chip(oldRow, "currency", "replaced");
    const currentMark = chip(newRow, "currency", "current");
    if ((await currentMark.count()) === 0) {
      await die(
        "the report nothing has replaced carries no mark, so 'current' is said with an absence - and an absence is also what a row looks like when the mark failed to render",
        newRow,
      );
    }
    await tellApart(
      "a replaced report and the current one",
      await pairOf(replacedMark),
      await pairOf(currentMark),
    );
  }

  // FLOW 6. The queue panel: the states of the work are told apart. The merge
  // verdicts have their own colours already; what was grey and said nothing was
  // "gating" - a run measuring a branch right now, drawn identically to nobody
  // having looked at it. This asserts the queue's own status scale here, which
  // is the panel this gate seeds, and leaves the verdict pairs to the unit
  // coverage they already have.
  await page.goto(`${base}/todos`, { timeout: 20_000 }).catch(() => {});
  // WAIT FOR A ROW, not for the page. The queue paints from mount with its own
  // empty state in it and the todos arrive one fetch later, so reading straight
  // after the navigation races the fetch - which is exactly what happened: this
  // check passed on one run and failed on the next against identical code,
  // because zero rows is treated as a failure below and the rows had simply not
  // landed yet. The findings half of this file already waits this way; this half
  // did not, and a flaky check is worse than a missing one because it spends
  // somebody else's slot to say nothing.
  try {
    await page.waitForSelector("[data-todo-status]", { timeout: 15_000 });
  } catch {
    await die(
      "the queue page never rendered a [data-todo-status] row, so nothing about the queue's colours was tested",
      page.locator("body"),
    );
  }
  const statuses = await page
    .$$eval("[data-todo-status]", (nodes) =>
      nodes.map((n) => ({
        state: n.getAttribute("data-todo-status") || "",
        colour: getComputedStyle(n).color,
      })),
    )
    .catch(() => []);
  // Zero is a FAILURE, not a skip. A check that quietly passes when it found
  // nothing to look at is a check that cannot go red, and it would go on
  // reporting success the day the selector stops matching.
  if (statuses.length === 0) {
    await die(
      "the queue page has no [data-todo-status] rows, so nothing about the queue's colours was tested",
      page.locator("body"),
    );
  }
  const byState = new Map(statuses.filter((s) => s.state).map((s) => [s.state, s.colour]));
  const colours = new Set(byState.values());
  if (byState.size > 1 && colours.size < byState.size) {
    const shown = [...byState].map(([s, c]) => `  ${s}: ${c}`).join("\n");
    console.error(`the queue draws ${byState.size} states in ${colours.size} colour(s), so some are not told apart:
${shown}`);
    process.exit(1);
  }
  // Said out loud rather than passed quietly, the same way colour-check.mjs
  // reports a single speaker: one state cannot demonstrate that two are told
  // apart, and hiding that would be claiming more than was tested.
  if (byState.size < 2) {
    console.log(`the queue shows ${byState.size} state(s), so distinctness there was untested`);
  }

  if (crashes.length) {
    console.error(`the pages threw:\n  ${crashes.join("\n  ")}`);
    process.exit(1);
  }
  console.log(
    "state colours: filed/referenced/unfiled apart, every evidence state on the page apart (verified never a shade of refuted), three severity dots and a stacked bar, the finding page agreeing with the list, corpora grouped, current apart from replaced",
  );
} finally {
  await browser.close();
}
