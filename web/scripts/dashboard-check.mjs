/**
 * A dashboard row renders declared tiles over pushed metric rows, and nothing
 * else: no code runs, no state is remembered, and scope decides who reads it.
 *
 *   node scripts/dashboard-check.mjs BASE_URL AUTHOR_TOKEN OUTSIDER_TOKEN
 *
 * The operator, 01M0WY7F5: agents author dashboards for their activity, "start
 * stop pause and monitor", asap. The contract, answered on the row:
 *
 *   - a dashboard is a memory row of kind `dashboard` whose fields declare
 *     tiles - a fixed vocabulary (number, table, grid, frame, series) over a
 *     named metric - and the console renders the declaration. It RUNS nothing:
 *     the data is metric rows producers push through the ordinary artifact door;
 *   - every number shows its age from the row it reads, and a datum older than
 *     its tile's threshold is visibly stale, not silently live - the operator
 *     reading prose today is exactly the failure this exists to fix;
 *   - an age under a minute reads <1m, not a ticking second count - coarse
 *     formatting is stable formatting, and a page that recomputes its age
 *     every second must not redraw it differently every second;
 *   - a reading says what it is - measured, inferred, unknown - in the
 *     producer's own words from fields.state, and absent is unknown: a number
 *     that does not say what it is must read as unknown, not as measured;
 *   - numbers right-align - digits line up, so a column of readings reads as
 *     a column (the serenedash finding, 01M0XCCQK19G4T03NBJDDDWFW1);
 *   - a frame reading is ANSI lines a producer pushed; the console draws
 *     them exactly - every run pinned to its column, fill bars as rects,
 *     angle-bracket text as text - and answers the pointer from the frame's
 *     own legend prose, while j/k, pgup/pgdn, home/end and esc drive a
 *     visible cursor row;
 *   - a series tile draws the newest N readings of its metric as a
 *     sparkline - oldest first, left to right, the newest point pinned -
 *     off the series door, which is the shape a trend needs; a window that
 *     was truncated says so, and a series whose points are not numbers
 *     says so instead of connecting prose into a trend;
 *   - a dashboard is no more readable than the artifacts it names: a reader
 *     outside the rows' scope is refused;
 *   - reopening the page shows the newest rows and the newest ages - nothing
 *     is remembered between loads, the dashboard holds no state of its own.
 *
 * FIVE ARMS, of which the second is the one a component test would miss:
 *
 *   1. an agent authors a dashboard and metric rows through the API; the page
 *      lists the dashboard and renders each declared number with its age;
 *   2. a principal from another project opens it and is refused - and their
 *      metrics read comes back empty;
 *   3. a newer metric row shows on reload with a fresh age, and a stale tile
 *      is styled stale, not silently current;
 *   4. a frame tile draws a pushed frame reading exactly - every run pinned
 *      to its column, fill bars as rects, angle-bracket text as text - and
 *      answers the pointer from the frame's own legend while j/k, pgup/pgdn,
 *      home/end and esc drive a visible cursor row;
 *   5. a series tile draws its window oldest-first - rising values read as
 *      a rising line in coordinates - pins the newest point, flags a
 *      truncated window, refuses non-numeric points, and a series tile over
 *      a never-pushed metric says so.
 *
 * TWO TOKENS. The author writes in their project; the outsider proves the
 * scope arm, because a check with one token could not prove "readable by me,
 * refused for everybody else". The missing-metric tile is the honest third
 * state: a tile whose data was never pushed says so, rather than rendering a
 * plausible zero.
 */

import { chromium } from "playwright";

import { refuseRemote } from "./localonly.mjs";

const [base, author, outsider] = process.argv.slice(2);
if (!base || !author || !outsider) {
  console.error("usage: node scripts/dashboard-check.mjs BASE_URL AUTHOR_TOKEN OUTSIDER_TOKEN");
  process.exit(2);
}

refuseRemote(base, "dashboard-check");

const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

const api = async (path, init = {}, as = author) => {
  const res = await fetch(`${base}${path}`, {
    ...init,
    headers: {
      Authorization: `Bearer ${as}`,
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
  });
  if (!res.ok) die(`${path}: ${res.status} ${await res.text()}`);
  return res.json();
};

// THE FIXTURE, written through the same door an agent uses, sequential on
// purpose: the node stamps the order and the newest row decides what renders.
const stamp = `${Date.now().toString(36)}`;
const title = `dashboard-check ${stamp}`;
const metricName = (n) => `dashcheck.${stamp}.${n}`;

const post = async (body, as = author) =>
  api("/api/artifacts", { method: "POST", body: JSON.stringify(body) }, as);

const dash = await post({
  type: "memory",
  kind: "dashboard",
  title,
  fields: {
    tiles: [
      {
        kind: "number",
        label: "cells done",
        metric: metricName("cells"),
        stale_after_seconds: 5,
      },
      {
        kind: "number",
        label: "rate",
        metric: metricName("rate"),
        stale_after_seconds: 86400,
      },
      {
        kind: "number",
        label: "never pushed",
        metric: metricName("missing"),
        stale_after_seconds: 5,
      },
      {
        kind: "table",
        label: "cells, latest rows",
        metric: metricName("cells"),
      },
      {
        kind: "grid",
        label: "coverage",
        metric: metricName("grid"),
        stale_after_seconds: 5,
      },
      // A grid over a series that holds plain numbers: the honest wrong-shape
      // state, drawn as a refusal rather than a matrix that lies.
      { kind: "grid", label: "wrong shape", metric: metricName("cells") },
      {
        kind: "frame",
        label: "lab frame",
        metric: metricName("frame"),
        stale_after_seconds: 5,
      },
      // A frame over a numeric series: the same wrong-shape refusal.
      { kind: "frame", label: "wrong frame", metric: metricName("cells") },
      {
        kind: "series",
        label: "sweep points",
        metric: metricName("points"),
        points: 5,
        stale_after_seconds: 5,
      },
      { kind: "series", label: "never sparkline", metric: metricName("sparkmissing") },
      // A series of words: the honest wrong-shape state for a line chart.
      { kind: "series", label: "wordy sparkline", metric: metricName("words") },
    ],
  },
});

const mkMetric = (name, value, state) =>
  post({
    type: "memory",
    kind: "metric",
    title: `metric ${stamp} ${name}`,
    fields: { name, value, ...(state ? { state } : {}) },
  });

await mkMetric(metricName("cells"), 1200);
await mkMetric(metricName("rate"), 4.2);
// The newest cells row claims inferred; the older one and the rate row claim
// nothing, so they read unknown - the serenedash states arm needs both.
await mkMetric(metricName("cells"), 1350, "inferred");
// The matrix ask: the store holds a nested value as-is, and a grid tile
// draws it. The fixture grid is the coverage shape claude-host pushes.
await mkMetric(metricName("grid"), {
  cols: ["node", "pass", "fail"],
  rows: [
    { label: "lubuntu2", cells: [12, 0] },
    { label: "lab2x1", cells: [55, 3] },
  ],
});

// THE FRAME FIXTURE: ANSI lines a producer rendered, pushed with the grid
// the console reads them by - serenedash's own (label 22, value 10, bar 18),
// so the columns below are built to it. The bars are SGR-coloured runs, so
// the renderer draws them as rects rather than text (a fill glyph inside a
// mixed run is text, same as the export); the last line is a free tail whose
// angle brackets must reach the page as text, never as markup.
const ESC = String.fromCharCode(27);
const sgr = (codes, text) => `${ESC}[${codes}m${text}${ESC}[0m`;
const FRAME_BOX_W = 63;
const frameCell = (label, value, bar, spark) =>
  `│${label.padEnd(22)}${String(value).padStart(10)}  ${sgr(32, bar.padEnd(18))}${spark}`;
const FRAME_LINES = [
  // The space after the title is the producer's own layout (views.py:1001
  // draws `┌─{t} ` then the dashes) - the cursor's title scan reads a word
  // there, and a title glued to its dashes would read as the whole border.
  `${sgr(1, "┌─lab ")}${"─".repeat(FRAME_BOX_W - 7)}┐`,
  frameCell("pass cells", 12, "████████░░░░░░░░░░", "▁▂▃▄▅▆▇█▂▁"),
  frameCell("lab2x1", 55, "███████░░░░░░░░░░░", "▂▄▆█▄▂▁▁▂▁"),
  `└${"─".repeat(FRAME_BOX_W - 2)}┘`,
  `  <sweep> & "cells" 12/13`,
];
await mkMetric(
  metricName("frame"),
  {
    lines: FRAME_LINES,
    grid: { label: 22, value: 10, bar: 18 },
    legend: { lab: [["pass cells", "cells that ran green under the latest sweep"]] },
    panels: { lab: "the lab sweep - one box per host, newest row first" },
  },
  "measured",
);

// THE SERIES FIXTURE: seven readings rising one per push - the window a
// series tile draws is the last five, and oldest-first is the contract: the
// first drawn point must be the oldest of the window (value 3), the pinned
// dot the newest (7). One series of words rides along: a sparkline over
// prose would draw a trend that is not there, and the tile must say so.
for (let v = 1; v <= 7; v++) {
  await mkMetric(metricName("points"), v, v === 7 ? "measured" : undefined);
}
await mkMetric(metricName("words"), "high");
await mkMetric(metricName("words"), "low");

// WHAT THE NODE HOLDS, read back before the browser opens: if the fixture
// seeded differently than intended, this fails as a fixture problem instead
// of the page answering a question about the wrong data.
const held = await api(
  `/api/metrics/rows?name=${encodeURIComponent(metricName("cells"))}&limit=20`,
);
const cells = (held.metrics ?? []).map((m) => m.fields?.value);
if (cells.length !== 2 || cells[0] !== 1350 || cells[1] !== 1200) {
  die(`the node holds cells [${cells}], wanted [1350, 1200] newest-first - the seed is wrong`);
}
const heldSeries = await api(
  `/api/metrics/series?name=${encodeURIComponent(metricName("points"))}&points=5`,
);
const seriesHeld = (heldSeries.series?.[0]?.points ?? []).map((p) => p.value);
if (
  seriesHeld.length !== 5 ||
  seriesHeld[0] !== 3 ||
  seriesHeld[4] !== 7 ||
  !heldSeries.series?.[0]?.truncated
) {
  die(
    `the node holds series [${seriesHeld}] truncated=${heldSeries.series?.[0]?.truncated}, wanted [3,4,5,6,7] truncated=true - the seed is wrong`,
  );
}
const heldDash = await api(`/api/artifact/${dash.id}`);
if (!Array.isArray(heldDash.fields?.tiles) || heldDash.fields.tiles.length !== 11) {
  die(
    `the dashboard row's tiles read ${JSON.stringify(heldDash.fields?.tiles)} - the seed is wrong`,
  );
}

// ---- ARM 2, API half first: the outsider is refused the row and reads no
// metrics. An absence assertion against a door that answers the author is the
// point - out of scope and missing look the same from the outside. ----
const outsiderRow = await fetch(`${base}/api/artifact/${dash.id}`, {
  headers: { Authorization: `Bearer ${outsider}` },
});
if (outsiderRow.status !== 404) {
  die(
    `the outsider reads the dashboard row and gets ${outsiderRow.status}, wanted 404 - a dashboard is no more readable than the artifacts it names`,
  );
}
const outsiderMetrics = await fetch(
  `${base}/api/metrics/rows?name=${encodeURIComponent(metricName("cells"))}`,
  { headers: { Authorization: `Bearer ${outsider}` } },
);
if (!outsiderMetrics.ok) {
  die(`the outsider's metrics read failed ${outsiderMetrics.status}, wanted an empty 200`);
}
const outsiderList = await outsiderMetrics.json();
if ((outsiderList.metrics ?? []).length !== 0) {
  die("the outsider reads the author's metric rows - the scope gate is open");
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({
    viewport: { width: 1600, height: 1000 },
  });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), author);

  const openDashboard = async () => {
    await page.goto(`${base}/dashboards/${dash.id}`, { timeout: 30_000 }).catch(() => {});
    await page
      .locator(`[data-dashboard="${dash.id}"]`)
      .waitFor({ state: "visible", timeout: 20_000 })
      .catch(() => {});
  };

  const tile = (label) => page.locator(`[data-tile-label="${label}"]`);

  // ---- ARM 1: the list names the dashboard, and the page renders every
  // declared tile from the pushed rows, each number carrying its age. ----
  await page.goto(`${base}/dashboards`, { timeout: 30_000 }).catch(() => {});
  await page
    .locator(`[data-dashboard-row="${dash.id}"]`)
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if ((await page.locator(`[data-dashboard-row="${dash.id}"]`).count()) !== 1) {
    die("/dashboards does not list the authored dashboard");
  }

  await openDashboard();
  const cellsTile = tile("cells done");
  await cellsTile.waitFor({ state: "visible", timeout: 15_000 }).catch(() => {});
  if ((await cellsTile.count()) !== 1) die(`the "cells done" tile did not render`);

  const value = await cellsTile.getAttribute("data-value");
  if (value !== "1350") die(`the cells tile reads ${value}, wanted 1350 - the newest pushed row`);
  const age = Number(await cellsTile.getAttribute("data-age"));
  if (!Number.isFinite(age) || age < 0) {
    die(`the cells tile's age reads ${age} - every number must say how old it is`);
  }
  const ageText = (await cellsTile.innerText()).trim();
  if (!/<?\d+\s*(s|m|h|d)/.test(ageText)) {
    die(`the cells tile's age is not in words a person reads:\n${ageText}`);
  }
  if (!ageText.includes("<1m")) {
    die(
      `the cells tile's fresh age does not read <1m - under a minute is coarse, not a ticking second count:\n${ageText}`,
    );
  }

  const rateTile = tile("rate");
  if ((await rateTile.getAttribute("data-value")) !== "4.2") {
    die(`the rate tile reads ${await rateTile.getAttribute("data-value")}, wanted 4.2`);
  }
  if ((await rateTile.getAttribute("data-stale")) !== null) {
    die("the rate tile is styled stale with a day-wide threshold and a fresh row");
  }

  // ---- THE SERENEDASH ARM: a reading says what it is, and numbers
  // right-align. The newest cells row claims inferred; the rate row claims
  // nothing, so it reads unknown. ----
  if ((await cellsTile.getAttribute("data-state")) !== "inferred") {
    die(
      `the cells tile's state reads ${await cellsTile.getAttribute("data-state")}, wanted inferred - the newest row's claim`,
    );
  }
  const cellsState = cellsTile.locator("[data-tile-state]");
  if ((await cellsState.count()) !== 1 || (await cellsState.innerText()).trim() !== "inferred") {
    die(`the cells tile does not say inferred in words:\n${await cellsTile.innerText()}`);
  }
  if ((await rateTile.getAttribute("data-state")) !== "unknown") {
    die(
      `the rate tile's state reads ${await rateTile.getAttribute("data-state")}, wanted unknown - an unclaimed reading must read as unknown, not as measured`,
    );
  }
  const rateState = rateTile.locator("[data-tile-state]");
  if ((await rateState.count()) !== 1 || (await rateState.innerText()).trim() !== "unknown") {
    die(`the rate tile does not say unknown in words:\n${await rateTile.innerText()}`);
  }
  // The state is styled, not just spoken: its colour comes off the serenedash
  // palette, not the tile's muted text.
  const stateColour = await cellsState.evaluate((el) => getComputedStyle(el).color);
  const mutedColour = await cellsTile
    .locator("[data-tile-age] span")
    .first()
    .evaluate((el) => getComputedStyle(el).color);
  if (stateColour === mutedColour) {
    die(`the cells tile's state is not styled - its colour (${stateColour}) is the age line's`);
  }
  // Numbers right-align: the number tile's value and the grid's cells.
  const valueAlign = await cellsTile
    .locator("[data-tile-value]")
    .evaluate((el) => getComputedStyle(el).textAlign);
  if (valueAlign !== "right") {
    die(`the cells tile's value text-aligns ${valueAlign}, wanted right - numbers right-align`);
  }
  // The palette exists as the framework's colour vocabulary: eight dim
  // workhorse colours, none of them a verdict.
  const palette = await page.evaluate(() => {
    const cs = getComputedStyle(document.documentElement);
    const slots = [];
    for (let i = 1; i <= 8; i++) {
      slots.push(cs.getPropertyValue(`--color-serenedash-${i}`).trim());
    }
    return slots;
  });
  if (palette.some((s) => s === "")) {
    die(`the serenedash palette is not eight colours: ${JSON.stringify(palette)}`);
  }

  // The honest third state: a declared tile whose metric was never pushed says
  // so, instead of drawing a plausible zero.
  const missingTile = tile("never pushed");
  if ((await missingTile.getAttribute("data-empty")) === null) {
    die('the "never pushed" tile does not say its metric has no rows');
  }

  // The table tile lists the pushed rows with their values and ages.
  const tableTile = tile("cells, latest rows");
  const rows = tableTile.locator("[data-metric-row]");
  if ((await rows.count()) !== 2) {
    die(`the table tile lists ${await rows.count()} rows, wanted 2 - the two pushed cells rows`);
  }
  const firstRowValue = await rows.first().getAttribute("data-value");
  if (firstRowValue !== "1350") {
    die(`the table's newest row reads ${firstRowValue}, wanted 1350`);
  }
  if ((await rows.first().getAttribute("data-state")) !== "inferred") {
    die(
      `the table's newest cells row reads state ${await rows.first().getAttribute("data-state")}, wanted inferred`,
    );
  }
  if ((await rows.nth(1).getAttribute("data-state")) !== "unknown") {
    die(
      `the table's older cells row reads state ${await rows.nth(1).getAttribute("data-state")}, wanted unknown`,
    );
  }

  // ---- THE GRID: the matrix ask - one vocabulary word plus a renderer over
  // {cols, rows}, carrying its age and staleness like every other tile. ----
  const gridTile = tile("coverage");
  const headerCells = gridTile.locator("[data-grid-col]");
  if ((await headerCells.count()) !== 3) {
    die(`the coverage grid draws ${await headerCells.count()} column headers, wanted 3`);
  }
  const modelRows = gridTile.locator("[data-grid-label]");
  if ((await modelRows.count()) !== 2) {
    die(`the coverage grid draws ${await modelRows.count()} rows, wanted 2`);
  }
  if ((await modelRows.first().getAttribute("data-grid-label")) !== "lubuntu2") {
    die(
      `the coverage grid's first row is ${await modelRows.first().getAttribute("data-grid-label")}, wanted lubuntu2`,
    );
  }
  const cell = (rowIdx, colIdx) => gridTile.locator(`[data-grid-cell="${rowIdx * 3 + colIdx}"]`);
  if ((await cell(0, 0).getAttribute("data-grid-value")) !== "12") {
    die(
      `the coverage grid's lubuntu2 pass cell reads ${await cell(0, 0).getAttribute("data-grid-value")}, wanted 12`,
    );
  }
  if ((await cell(1, 1).getAttribute("data-grid-value")) !== "3") {
    die(
      `the coverage grid's lab2x1 fail cell reads ${await cell(1, 1).getAttribute("data-grid-value")}, wanted 3`,
    );
  }
  if ((await gridTile.getAttribute("data-age")) === null) {
    die(
      "the coverage grid carries no age - a frozen grid reads as a sweep that covered everything",
    );
  }
  if ((await gridTile.getAttribute("data-state")) !== "unknown") {
    die(
      `the coverage grid's state reads ${await gridTile.getAttribute("data-state")}, wanted unknown - the grid row claims nothing`,
    );
  }
  const cellAlign = await cell(0, 0).evaluate((el) => getComputedStyle(el).textAlign);
  if (cellAlign !== "right") {
    die(`the grid's cells text-align ${cellAlign}, wanted right - numbers right-align`);
  }

  // The honest wrong-shape state: a grid tile over a numeric series says so
  // instead of drawing a matrix that lies.
  const wrongTile = tile("wrong shape");
  if ((await wrongTile.getAttribute("data-grid-bad")) === null) {
    die('the "wrong shape" tile does not say its reading is not a grid');
  }

  // ---- THE FRAME: a pushed ANSI rendering, drawn exactly. The geometry
  // numbers below are the export's own - CW 7.22, LH 15, pad 8 - so a run
  // that lands off its column fails here, not in someone's reading. ----
  const frameTile = tile("lab frame");
  await frameTile.scrollIntoViewIfNeeded().catch(() => {});
  const svg = frameTile.locator("[data-frame-svg] svg");
  if ((await svg.count()) !== 1) {
    die(`the lab frame tile does not draw one SVG:\n${await frameTile.innerText()}`);
  }
  if ((await frameTile.getAttribute("data-state")) !== "measured") {
    die(
      `the lab frame's state reads ${await frameTile.getAttribute("data-state")}, wanted measured - the row claims it`,
    );
  }
  if ((await frameTile.getAttribute("data-age")) === null) {
    die("the lab frame carries no age - a frozen frame reads as a live screen");
  }

  // Every styled run is pinned to its column. The line starts with the │ at
  // column zero (x = pad + 0*CW), and the bar - an all-fill SGR run - draws
  // as rects at the bar column of the fixture grid (1 + label 22 + value 10
  // + gap 2): the █ group first, then the ░ group at reduced opacity.
  const textEls = await svg.locator("text").evaluateAll((els) =>
    els.map((el) => ({
      x: Number(el.getAttribute("x")),
      len: Number(el.getAttribute("textLength")),
      bold: el.getAttribute("font-weight"),
      text: el.textContent,
    })),
  );
  const pinned = textEls.some(
    (t) =>
      Math.abs(t.x - 8) < 0.01 &&
      Math.abs(t.len - 35 * 7.22) < 0.01 &&
      (t.text ?? "").includes("pass cells"),
  );
  if (!pinned) {
    die(
      `no run is pinned to column zero at the export's geometry:\n${JSON.stringify(textEls.slice(0, 3))}`,
    );
  }
  const title = textEls.find((t) => (t.text ?? "").includes("┌─lab"));
  if (!title || title.bold !== "700") {
    die(`the bold title run is not bold:\n${JSON.stringify(title)}`);
  }
  const rects = await svg.locator("rect").evaluateAll((els) =>
    els
      .map((el) => ({
        x: Number(el.getAttribute("x")),
        w: Number(el.getAttribute("width")),
        h: Number(el.getAttribute("height")),
        o: el.getAttribute("opacity"),
        fill: el.getAttribute("fill"),
      }))
      .filter((r) => r.x > 0),
  );
  const bar = rects.find((r) => Math.abs(r.x - (8 + 35 * 7.22)) < 0.01);
  if (
    !bar ||
    Math.abs(bar.w - 8 * 7.22) > 0.01 ||
    Math.abs(bar.h - 15) > 0.01 ||
    bar.fill !== "#b5e08d"
  ) {
    die(`the bar is not one green rectangle at its grid column:\n${JSON.stringify(rects)}`);
  }
  // The ░ group follows it, same row, at the glyph's reduced opacity - the
  // FILL table's own alpha, not a guess.
  const dim = rects.find(
    (r) => Math.abs(r.x - (8 + 43 * 7.22)) < 0.01 && Math.abs(r.w - 10 * 7.22) < 0.01,
  );
  if (!dim || dim.o !== "0.22") {
    die(
      `the dim half of the bar is not one rectangle at the fill's opacity:\n${JSON.stringify(rects)}`,
    );
  }

  // The tail's angle brackets reach the page as text: the SVG holds a text
  // node spelling them, and no element named by them.
  if ((await svg.locator("sweep").count()) !== 0) {
    die("the frame's angle-bracket content became markup - the escape is broken");
  }
  if ((await svg.locator("text").filter({ hasText: "<sweep>" }).count()) === 0) {
    die("the tail text does not spell <sweep> - the escape is broken");
  }

  // The wrong-shape refusal for frames, same honesty as the grid's.
  const wrongFrame = tile("wrong frame");
  if ((await wrongFrame.getAttribute("data-frame-bad")) === null) {
    die('the "wrong frame" tile does not say its reading is not a frame');
  }

  // ---- THE CURSOR: the pointer answers from the frame's own legend, and
  // the keys drive a visible cursor row. Pointer first, over the "pass
  // cells" label (row 1, cell column 4). ----
  const frameBox = frameTile.locator("[data-frame-svg]");
  const frameBoxRect = await frameBox.boundingBox();
  if (!frameBoxRect) die("the frame's SVG has no bounding box - it is not laid out");
  await page.mouse.move(frameBoxRect.x + 8 + 4 * 7.22, frameBoxRect.y + 8 + 1 * 15 + 7.5);
  await frameTile
    .locator("[data-frame-tip]")
    .waitFor({ state: "visible", timeout: 5000 })
    .catch(() => {});
  if ((await frameTile.locator("[data-frame-tip]").count()) !== 1) {
    die("no tooltip under the pointer over the frame");
  }
  const tipTitle = await frameTile.locator("[data-frame-tip]").getAttribute("data-frame-tip-title");
  const tipBody = await frameTile.locator("[data-frame-tip]").getAttribute("data-frame-tip-body");
  if (tipTitle !== "lab · pass cells") {
    die(
      `the tooltip's title reads ${tipTitle}, wanted "lab · pass cells" - the legend's own words`,
    );
  }
  if (tipBody !== "cells that ran green under the latest sweep") {
    die(`the tooltip's body is not the legend's meaning:\n${tipBody}`);
  }

  // Keyboard: the pointer stands down and the keys take over. The cursor
  // highlight is a real element on the row, and the tooltip answers the row
  // the cursor is on - the same legend entry the pointer answered.
  await page.mouse.move(0, 0);
  await frameTile
    .locator("[data-frame-tip]")
    .waitFor({ state: "detached", timeout: 5000 })
    .catch(() => {});
  const keys = frameTile.locator('[role="application"]');
  await keys.focus();
  await page.keyboard.press("Home");
  if ((await keys.getAttribute("data-frame-cursor-row")) !== "0") {
    die(
      `Home does not put the cursor on row zero: ${await keys.getAttribute("data-frame-cursor-row")}`,
    );
  }
  await page.keyboard.press("j");
  if ((await keys.getAttribute("data-frame-cursor-row")) !== "1") {
    die(
      `j does not move the cursor down one row: ${await keys.getAttribute("data-frame-cursor-row")}`,
    );
  }
  const cursorHl = frameTile.locator("[data-frame-cursor]");
  if ((await cursorHl.count()) !== 1) {
    die("the cursor highlight does not render on the row");
  }
  if ((await cursorHl.evaluate((el) => el.style.top)) !== `${8 + 15}px`) {
    die(
      `the cursor highlight sits at top ${await cursorHl.evaluate((el) => el.style.top)}, wanted ${8 + 15}px - row one's line`,
    );
  }
  await frameTile
    .locator("[data-frame-tip]")
    .waitFor({ state: "visible", timeout: 5000 })
    .catch(() => {});
  const keyTipTitle = await frameTile
    .locator("[data-frame-tip]")
    .getAttribute("data-frame-tip-title");
  if (keyTipTitle !== "lab · pass cells") {
    die(
      `the cursor's tooltip title reads ${keyTipTitle}, wanted "lab · pass cells" - the cursor answers the row's label`,
    );
  }
  await page.keyboard.press("k");
  if ((await keys.getAttribute("data-frame-cursor-row")) !== "0") {
    die(
      `k does not move the cursor back up one row: ${await keys.getAttribute("data-frame-cursor-row")}`,
    );
  }
  await page.keyboard.press("PageDown");
  if ((await keys.getAttribute("data-frame-cursor-row")) !== "4") {
    die(
      `PageDown does not move the cursor ten rows, clamped to the last: ${await keys.getAttribute("data-frame-cursor-row")}`,
    );
  }
  await page.keyboard.press("Home");
  await page.keyboard.press("End");
  if ((await keys.getAttribute("data-frame-cursor-row")) !== "4") {
    die(
      `End does not put the cursor on the last row: ${await keys.getAttribute("data-frame-cursor-row")}`,
    );
  }
  await page.keyboard.press("Escape");
  if ((await keys.getAttribute("data-frame-cursor-row")) !== null) {
    die("Escape does not clear the cursor");
  }
  if ((await cursorHl.count()) !== 0) {
    die("the cursor highlight lingers after Escape clears it");
  }

  // ---- ARM 5: THE SERIES - a sparkline drawn oldest-first from the series
  // door. The window is the last five of seven pushed readings, so the first
  // drawn point is 3 and the pinned dot is 7 - and the door's window was
  // truncated, which the tile says rather than reading as the whole series. ----
  const seriesTile = tile("sweep points");
  await seriesTile.scrollIntoViewIfNeeded().catch(() => {});
  if ((await seriesTile.getAttribute("data-series-points")) !== "5") {
    die(
      `the series tile draws ${await seriesTile.getAttribute("data-series-points")} points, wanted 5 - its declared window`,
    );
  }
  if ((await seriesTile.getAttribute("data-series-first")) !== "3") {
    die(
      `the series tile's first point is ${await seriesTile.getAttribute("data-series-first")}, wanted 3 - the oldest of the window`,
    );
  }
  if ((await seriesTile.getAttribute("data-series-latest")) !== "7") {
    die(
      `the series tile's latest point is ${await seriesTile.getAttribute("data-series-latest")}, wanted 7 - the newest pushed reading`,
    );
  }
  if ((await seriesTile.getAttribute("data-series-truncated")) === null) {
    die("the series tile does not say its window was truncated - seven pushed, five drawn");
  }
  if ((await seriesTile.getAttribute("data-state")) !== "measured") {
    die(
      `the series tile's state reads ${await seriesTile.getAttribute("data-state")}, wanted measured - the newest row claims it`,
    );
  }
  if ((await seriesTile.getAttribute("data-age")) === null) {
    die(
      "the series tile carries no age - a trend without its newest reading's age is a frozen chart",
    );
  }
  const seriesSvg = seriesTile.locator("[data-series-svg]");
  if ((await seriesSvg.count()) !== 1) {
    die(`the series tile does not draw one SVG:\n${await seriesTile.innerText()}`);
  }
  // Oldest-first in coordinates: rising values must read as a rising line -
  // x grows left to right and SVG y grows downward, so the points attribute
  // must be five pairs with x strictly up and y strictly down, ending at the
  // pinned dot (x 240, y 4 - the value 7 at the top of the window 3..7).
  const seriesPath = await seriesSvg.locator("[data-series-path]").getAttribute("points");
  const pairs = (seriesPath ?? "")
    .trim()
    .split(/\s+/)
    .map((p) => p.split(",").map(Number));
  if (pairs.length !== 5 || pairs.some((p) => p.length !== 2 || !p.every(Number.isFinite))) {
    die(`the series polyline is not five coordinate pairs:\n${seriesPath}`);
  }
  for (let i = 1; i < pairs.length; i++) {
    if (!(pairs[i][0] > pairs[i - 1][0]) || !(pairs[i][1] < pairs[i - 1][1])) {
      die(`the series polyline does not rise oldest-first:\n${JSON.stringify(pairs)}`);
    }
  }
  const dot = seriesSvg.locator("[data-series-dot]");
  if (Number(await dot.getAttribute("cx")) !== 240 || Number(await dot.getAttribute("cy")) !== 4) {
    die(
      `the pinned dot is not at the newest point (240, 4): cx=${await dot.getAttribute("cx")} cy=${await dot.getAttribute("cy")}`,
    );
  }
  if ((await seriesTile.locator("[data-tile-value]").innerText()).trim() !== "7") {
    die(
      `the series tile's number reads ${await seriesTile.locator("[data-tile-value]").innerText()}, wanted 7 - the newest reading`,
    );
  }
  if ((await tile("never sparkline").getAttribute("data-empty")) === null) {
    die('the "never sparkline" tile does not say its metric has no rows');
  }
  if ((await tile("wordy sparkline").getAttribute("data-series-bad")) === null) {
    die('the "wordy sparkline" tile does not say its points are not numbers');
  }

  // ---- THE STALENESS HALF: past its threshold, the tile is styled stale,
  // not silently live. ----
  await page.waitForTimeout(6000);
  if ((await cellsTile.getAttribute("data-stale")) === null) {
    die("the cells tile past its 5s threshold is not styled stale");
  }
  if ((await gridTile.getAttribute("data-stale")) === null) {
    die("the coverage grid past its 5s threshold is not styled stale");
  }
  if ((await frameTile.getAttribute("data-stale")) === null) {
    die("the lab frame past its 5s threshold is not styled stale");
  }
  if ((await seriesTile.getAttribute("data-stale")) === null) {
    die("the series tile past its 5s threshold is not styled stale");
  }
  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);

  // ---- ARM 3: a newer pushed row shows on reload, with a fresh age, and
  // nothing is remembered between loads. ----
  await mkMetric(metricName("cells"), 1400);
  await openDashboard();
  if ((await cellsTile.getAttribute("data-value")) !== "1400") {
    die(
      `after a reload the cells tile reads ${await cellsTile.getAttribute("data-value")}, wanted 1400 - the newest pushed row must win`,
    );
  }
  if ((await cellsTile.getAttribute("data-stale")) !== null) {
    die("the cells tile is styled stale against a row pushed seconds ago");
  }
  if ((await tableTile.locator("[data-metric-row]").count()) !== 3) {
    die("the table tile does not list the newly pushed row after reload");
  }
  if (crashes.length > 0) die(`the page threw: ${crashes.join("; ")}`);

  // ---- ARM 2, browser half: the outsider's page is a refusal, not a blank. ----
  // The outsider's session mounts the console shell like any visit, and the
  // shell declares its own console:<room> readers under the outsider's
  // principal - the check wrapper deletes those rows when this script is
  // done, so the shared database keeps one reader row per token (see
  // checks.d/console/dashboard.sh).
  const outsiderPage = await browser.newPage({
    viewport: { width: 1600, height: 1000 },
  });
  const outsiderCrashes = [];
  outsiderPage.on("pageerror", (err) => outsiderCrashes.push(String(err)));
  await outsiderPage.addInitScript((t) => localStorage.setItem("flowy.token", t), outsider);
  await outsiderPage.goto(`${base}/dashboards/${dash.id}`, { timeout: 30_000 }).catch(() => {});
  await outsiderPage
    .locator("[data-dashboard-refused]")
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if ((await outsiderPage.locator("[data-dashboard-refused]").count()) !== 1) {
    die("the outsider's page does not say it is refused - out of scope must not read as absent");
  }
  if ((await outsiderPage.locator(`[data-tile-label="cells done"]`).count()) !== 0) {
    die("the outsider's page renders tiles - the scope gate is open");
  }
  if (outsiderCrashes.length > 0) die(`the outsider's page threw: ${outsiderCrashes.join("; ")}`);

  console.log(
    "the dashboard lists, renders its declared tiles from pushed rows with ages, " +
      "draws the coverage grid from its nested reading and says so when a grid " +
      "tile's reading is not one, draws the pushed frame exactly - pinned runs, " +
      "a green fill bar as one rect at its grid column, angle-bracket text as " +
      "text - answers the pointer and the cursor from its own legend, draws the " +
      "series window oldest-first with the newest point pinned, flags a " +
      "truncated window, refuses prose points, moves the " +
      "cursor row under j/k, pgup/pgdn, home/end and esc, styles a stale tile " +
      "past its threshold, says what each reading is - inferred as claimed, " +
      "unknown as unclaimed - right-aligns its numbers off the eight-colour " +
      "palette, refuses an outsider, and a reload shows the newest row with a " +
      "fresh age",
  );
} finally {
  await browser.close();
}
