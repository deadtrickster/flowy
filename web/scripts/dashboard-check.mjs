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
 *     tiles - a fixed vocabulary (number, table, grid, frame, gauge, series,
 *     report) over a named metric - and the console renders the declaration.
 *     It RUNS nothing: the data is metric rows producers push through the
 *     ordinary artifact door;
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
 *   - a gauge tile draws a value WITH ITS BOUNDS - the scale and thresholds
 *     travel beside the reading, never on the tile - and reads its direction
 *     off the threshold order: crit above warn means high is bad, crit below
 *     warn means low is bad. A reading whose shape cannot place the value -
 *     prose, half a scale, half a threshold set, a value off its own scale -
 *     says so instead of drawing a bar that lies;
 *   - a report tile draws the other dashboard style - the reading is the
 *     whole document: eyebrow, title, lede, and sections of the closed
 *     vocabulary (progress, cards, columns, squares). A tone is a WORD from
 *     the closed set the console maps to the palette, and a word outside
 *     the set draws as no tone at all; a card's spark is a metric ref with
 *     its own window, asked off the series door at that window;
 *   - a dashboard is no more readable than the artifacts it names: a reader
 *     outside the rows' scope is refused;
 *   - reopening the page shows the newest rows and the newest ages - nothing
 *     is remembered between loads, the dashboard holds no state of its own;
 *   - a dashboard left open re-fetches on its own beat: a row pushed after
 *     load wins its tile without a reload - the age ticking up while the
 *     rows are frozen would be a screenshot with a clock on it.
 *
 * NINE ARMS, of which the second is the one a component test would miss:
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
 *      a never-pushed metric says so;
 *   6. a gauge tile draws its value with the pushed bounds - the fill runs
 *      min to value, the warn and crit bands sit where the thresholds say,
 *      the direction reads off the order (crit above warn is high-bad, below
 *      is low-bad), the severity colours the fill off the palette, and a
 *      reading that cannot be placed says so;
 *   7. a report tile draws its pushed document - header and all four section
 *      kinds, segment widths over the pushed total, a tone word mapped to
 *      the palette (and a word outside the set drawn as no tone), a card's
 *      spark as a metric ref answered at its own window - and a reading that
 *      is not a document says so;
 *   8. a row pushed after load wins its tile without a reload - the page
 *      re-fetches on its own beat, so a dashboard left open follows the
 *      producers instead of freezing at page load;
 *   9. a log tile draws its stream's last lines oldest-first - each level a
 *      tag in its severity colour, a line without a level drawn as plain
 *      text - and the counts the door computed over the window; a stream
 *      never pushed says so, rather than drawing an empty list that reads
 *      as silence.
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
      // The reflow tile - one frame pushed at three widths.
      { kind: "frame", label: "reflow frame", metric: metricName("reflow") },
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
      {
        kind: "gauge",
        label: "mem used",
        metric: metricName("mem"),
        stale_after_seconds: 5,
      },
      { kind: "gauge", label: "free disk", metric: metricName("disk") },
      { kind: "gauge", label: "plain gauge", metric: metricName("plain") },
      { kind: "gauge", label: "never gauge", metric: metricName("gaugemissing") },
      // A gauge over a series of words: the honest wrong-shape state.
      { kind: "gauge", label: "wordy gauge", metric: metricName("words") },
      // Half a threshold set: one mark alone cannot say which way is worse.
      { kind: "gauge", label: "half threshold", metric: metricName("halfthr") },
      // A value its own scale cannot place.
      { kind: "gauge", label: "off scale", metric: metricName("offscl") },
      {
        kind: "report",
        label: "vl sweep report",
        metric: metricName("reportfloor"),
        stale_after_seconds: 5,
      },
      { kind: "report", label: "never report", metric: metricName("reportmissing") },
      // A report over a series of words: the honest wrong-shape state.
      { kind: "report", label: "wordy report", metric: metricName("words") },
      {
        kind: "log",
        label: "build tail",
        metric: metricName("buildlog"),
      },
      { kind: "log", label: "never log", metric: metricName("logmissing") },
    ],
  },
});

const mkMetric = (name, value, state, extra) =>
  post({
    type: "memory",
    kind: "metric",
    title: `metric ${stamp} ${name}`,
    fields: { name, value, ...(state ? { state } : {}), ...(extra ?? {}) },
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

// THE REFLOW FIXTURE, deliberately its own metric rather than variants bolted
// onto the frame above: those arms measure runs pinned to FRAME_LINES' columns,
// and a tile that picked a different rendering would fail them for a reason
// that has nothing to do with what they test.
//
// The two widths are far apart so the drawn SVG's pixel width says
// unambiguously which one the console chose.
await mkMetric(
  metricName("reflow"),
  {
    lines: ["default rendering".padEnd(60, " ")],
    cols: 60,
    variants: {
      30: { cols: 30, lines: ["narrow rendering".padEnd(30, " ")] },
      150: { cols: 150, lines: ["wide rendering".padEnd(150, " ")] },
    },
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

// THE GAUGE FIXTURE: values with their bounds riding beside them - the scale
// and the thresholds are the producer's words, the tile declares metric and
// kind only. mem is high-bad (crit above warn), disk is low-bad (crit below
// warn) - the order IS the direction, no field says which way round it is.
await mkMetric(metricName("mem"), 57, "measured", {
  min: 0,
  max: 64,
  thresholds: { warn: 40, crit: 55 },
});
await mkMetric(metricName("disk"), 4.5, undefined, {
  min: 0,
  max: 10,
  thresholds: { warn: 3, crit: 1 },
});
// A bare number: a gauge with no scale and no thresholds is just the value.
await mkMetric(metricName("plain"), 7);
// The wrong shapes: half a threshold set - one mark alone cannot say which
// way is worse - and a value its own scale cannot place.
await mkMetric(metricName("halfthr"), 5, undefined, {
  min: 0,
  max: 10,
  thresholds: { warn: 4 },
});
await mkMetric(metricName("offscl"), 12, undefined, { min: 0, max: 10 });

// THE REPORT FIXTURE: the other dashboard style - a document pushed whole as
// one reading. Four sections, one of each kind. Tones are words; "ok" below
// is deliberately OUTSIDE the closed set ("" good warn bad dim accent), so
// the arm can prove an unknown word draws as no tone, never a crash and
// never a colour the producer did not choose. The card's spark names the
// points series the series arm already pushed - the console asks that window
// once, and the series tile and the card spark read the same answer.
await mkMetric(
  metricName("reportfloor"),
  {
    eyebrow: "three boxes",
    title: "VL Sweep",
    lede: "one host, two cells, a progress bar and a tone grid - the pushed shape",
    sections: [
      {
        kind: "progress",
        total: 5850,
        value: 44.3,
        caption: "cells done",
        segments: [
          { label: "lab2x1", value: 1000 },
          { label: "lubuntu2", value: 2000 },
        ],
      },
      {
        kind: "cards",
        cards: [
          {
            title: "lab2x1",
            pill: "green",
            blurb: "the box that ran the sweep",
            stats: [
              { label: "cells", value: 44 },
              { label: "fail", value: 3 },
            ],
            spark: { metric: metricName("points"), points: 5 },
            note: "sweep of the morning",
          },
        ],
      },
      {
        kind: "columns",
        columns: [
          { label: "model", align: "left" },
          { label: "cells", align: "right" },
        ],
        rows: [
          { cells: ["a", "44"] },
          {
            cells: [
              { text: "b", tone: "good" },
              { text: "7", tone: "bad" },
            ],
          },
        ],
      },
      {
        kind: "squares",
        groups: [
          {
            label: "lab2x1",
            rows: [
              {
                label: "m",
                cells: [{ text: "x", tone: "good", title: "hover x" }, { tone: "ok" }],
              },
            ],
          },
        ],
      },
    ],
  },
  "measured",
);

// THE LOG FIXTURE: lines of one stream pushed as kind log - prose with a
// level and a type, the shape the log door filters and counts. Four lines
// oldest first: an info, a warn with a type, an error, and one with NO
// level (a crash dump straight to stderr is legal and deliberately so - the
// store's own comment). A log tile over a stream never pushed rides along
// for the honest empty state.
const mkLog = (stream, message, level, type) =>
  post({
    type: "memory",
    kind: "log",
    title: `log ${stamp} ${stream}`,
    fields: {
      stream,
      message,
      ...(level ? { level } : {}),
      ...(type ? { type } : {}),
    },
  });
await mkLog(metricName("buildlog"), "booting the sweep", "INFO");
await mkLog(metricName("buildlog"), "disk nearly full", "WARN", "disk");
await mkLog(metricName("buildlog"), "sweep died mid-run", "ERROR", "sweep");
await mkLog(metricName("buildlog"), "panic: nil map deref at 0x7f");

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
if (!Array.isArray(heldDash.fields?.tiles) || heldDash.fields.tiles.length !== 24) {
  die(
    `the dashboard row's tiles read ${JSON.stringify(heldDash.fields?.tiles)} - the seed is wrong`,
  );
}
const heldTail = await api(`/api/logs/tail?stream=${encodeURIComponent(metricName("buildlog"))}`);
const tailMessages = (heldTail.lines ?? []).map((l) => l.message);
if (
  tailMessages.length !== 4 ||
  tailMessages[0] !== "booting the sweep" ||
  tailMessages[3] !== "panic: nil map deref at 0x7f"
) {
  die(
    `the node holds log tail [${tailMessages}], wanted the four seeded lines oldest-first - the seed is wrong`,
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

  // ---- REFLOW: the widest rendering that fits the PANEL, not the window.
  //
  // A frame cannot be re-wrapped by whoever draws it - the bars and the column
  // grid were fixed when the producer chose its glyphs - so it ships several
  // widths and the tile measures its own box and takes the widest that fits. A
  // frame narrower than its panel leaves the panel half empty; one wider is the
  // horizontal scroll this exists to remove.
  //
  // Asserted on the drawn SVG's width in pixels - cols * CW + 2 * pad - because
  // that is the fact a reader cares about, and the three renderings are far
  // enough apart that the number says unambiguously which was chosen.
  {
    const reflow = tile("reflow frame");
    await reflow.scrollIntoViewIfNeeded().catch(() => {});
    const rsvg = reflow.locator("[data-frame-svg] svg");
    if ((await rsvg.count()) !== 1) {
      die("the reflow tile draws no SVG");
    }
    const widthOf = async () => Number(await rsvg.getAttribute("width"));
    const wide = 150 * 7.22 + 16;
    const narrow = 30 * 7.22 + 16;
    const atFull = await widthOf();
    if (Math.abs(atFull - wide) > 2) {
      die(
        `at a 1600px viewport the reflow frame drew ${atFull}px, wanted ~${wide.toFixed(0)} - the widest rendering that fits is the one to draw`,
      );
    }
    await page.setViewportSize({ width: 480, height: 1000 });
    let narrowed = true;
    await page
      .waitForFunction(
        (want) => {
          const el = document.querySelector(
            '[data-tile-label="reflow frame"] [data-frame-svg] svg',
          );
          return el !== null && Math.abs(Number(el.getAttribute("width")) - want) <= 2;
        },
        narrow,
        { timeout: 5000 },
      )
      .catch(() => {
        narrowed = false;
      });
    if (!narrowed) {
      die(
        `narrowed to 480px the reflow frame stayed at ${await widthOf()}px, wanted ~${narrow.toFixed(0)} - a frame wider than its panel is the horizontal scroll this removes`,
      );
    }
    await page.setViewportSize({ width: 1600, height: 1000 });
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

  // ---- ARM 6: THE GAUGE - a value drawn with its pushed bounds. The tile
  // declares metric and kind only; the scale and thresholds travel beside
  // the reading. The direction reads off the order of the thresholds - crit
  // above warn means high is bad, crit below warn means low is bad - and the
  // bands and the fill land where the pushed numbers say they land. ----
  const memGauge = tile("mem used");
  await memGauge.scrollIntoViewIfNeeded().catch(() => {});
  if ((await memGauge.getAttribute("data-gauge-value")) !== "57") {
    die(
      `the mem gauge reads ${await memGauge.getAttribute("data-gauge-value")}, wanted 57 - the pushed reading`,
    );
  }
  if (
    (await memGauge.getAttribute("data-gauge-min")) !== "0" ||
    (await memGauge.getAttribute("data-gauge-max")) !== "64"
  ) {
    die(
      `the mem gauge carries scale ${await memGauge.getAttribute("data-gauge-min")}..${await memGauge.getAttribute("data-gauge-max")}, wanted 0..64 - the pushed bounds`,
    );
  }
  if (
    (await memGauge.getAttribute("data-gauge-warn")) !== "40" ||
    (await memGauge.getAttribute("data-gauge-crit")) !== "55"
  ) {
    die("the mem gauge does not carry its pushed thresholds warn 40 crit 55");
  }
  if ((await memGauge.getAttribute("data-gauge-direction")) !== "high") {
    die(
      `the mem gauge's direction reads ${await memGauge.getAttribute("data-gauge-direction")}, wanted high - crit above warn means high is bad`,
    );
  }
  if ((await memGauge.getAttribute("data-gauge-severity")) !== "crit") {
    die(
      `the mem gauge's severity reads ${await memGauge.getAttribute("data-gauge-severity")}, wanted crit - 57 is past the crit mark of 55`,
    );
  }
  if ((await memGauge.getAttribute("data-state")) !== "measured") {
    die(
      `the mem gauge's state reads ${await memGauge.getAttribute("data-state")}, wanted measured - the row claims it`,
    );
  }
  if ((await memGauge.getAttribute("data-age")) === null) {
    die("the mem gauge carries no age - a frozen gauge reads as a live instrument");
  }
  // The bar in coordinates: the fill runs min to value, the warn band spans
  // warn..crit, the crit band crit..max. The same percents derive from the
  // pushed numbers, so the drawn boxes must land where they say they land.
  const gaugeBox = async (sel) => {
    const b = await memGauge.locator(sel).boundingBox();
    if (!b) die(`the mem gauge's ${sel} has no bounding box - it is not laid out`);
    return b;
  };
  const track = await gaugeBox("[data-gauge-track]");
  const fillBox = await gaugeBox("[data-gauge-fill]");
  if (Math.abs(fillBox.width / track.width - 57 / 64) > 0.02) {
    die(
      `the mem gauge's fill runs ${fillBox.width / track.width} of the bar, wanted 57/64 - min to value`,
    );
  }
  const warnBox = await gaugeBox('[data-gauge-band="warn"]');
  if (
    Math.abs(warnBox.x - (track.x + (40 / 64) * track.width)) > 2 ||
    Math.abs(warnBox.width / track.width - 15 / 64) > 0.02
  ) {
    die(`the mem gauge's warn band does not span 40..55:\n${JSON.stringify(warnBox)}`);
  }
  const critBox = await gaugeBox('[data-gauge-band="crit"]');
  if (
    Math.abs(critBox.x - (track.x + (55 / 64) * track.width)) > 2 ||
    Math.abs(critBox.width / track.width - 9 / 64) > 0.02
  ) {
    die(`the mem gauge's crit band does not span 55..64:\n${JSON.stringify(critBox)}`);
  }
  // The severity is styled, not just spoken: the crit fill takes the
  // palette's dim red-orange. Measured against a probe styled with the same
  // var in-page, so the comparison is of two computed colours, not of a
  // serialization a browser version chooses.
  const fillColour = await memGauge
    .locator("[data-gauge-fill]")
    .evaluate((el) => getComputedStyle(el).backgroundColor);
  const critProbe = await page.evaluate(() => {
    const probe = document.createElement("div");
    probe.style.backgroundColor = "var(--color-serenedash-3)";
    document.body.appendChild(probe);
    const colour = getComputedStyle(probe).backgroundColor;
    probe.remove();
    return colour;
  });
  if (fillColour !== critProbe) {
    die(
      `the mem gauge's crit fill is not the palette's dim red-orange: ${fillColour} vs ${critProbe}`,
    );
  }
  // A LOW-BAD gauge gets worse downwards: the danger sits at the bottom, the
  // crit band spans min..crit and the warn band crit..warn.
  const diskGauge = tile("free disk");
  if ((await diskGauge.getAttribute("data-gauge-direction")) !== "low") {
    die(
      `the free disk gauge's direction reads ${await diskGauge.getAttribute("data-gauge-direction")}, wanted low - crit below warn means low is bad`,
    );
  }
  if ((await diskGauge.getAttribute("data-gauge-severity")) !== "ok") {
    die(
      `the free disk gauge's severity reads ${await diskGauge.getAttribute("data-gauge-severity")}, wanted ok - 4.5 is above the warn mark of 3`,
    );
  }
  if ((await diskGauge.getAttribute("data-state")) !== "unknown") {
    die(
      `the free disk gauge's state reads ${await diskGauge.getAttribute("data-state")}, wanted unknown - the row claims nothing`,
    );
  }
  const diskTrack = await diskGauge.locator("[data-gauge-track]").boundingBox();
  if (!diskTrack) die("the free disk gauge's bar has no bounding box - it is not laid out");
  const diskCrit = await diskGauge.locator('[data-gauge-band="crit"]').boundingBox();
  const diskWarn = await diskGauge.locator('[data-gauge-band="warn"]').boundingBox();
  if (!diskCrit || !diskWarn) die("the free disk gauge does not draw its bands");
  if (
    Math.abs(diskCrit.x - diskTrack.x) > 2 ||
    Math.abs(diskCrit.width / diskTrack.width - 0.1) > 0.02
  ) {
    die(`the free disk gauge's crit band does not span 0..1:\n${JSON.stringify(diskCrit)}`);
  }
  if (
    Math.abs(diskWarn.x - (diskTrack.x + 0.1 * diskTrack.width)) > 2 ||
    Math.abs(diskWarn.width / diskTrack.width - 0.2) > 0.02
  ) {
    die(`the free disk gauge's warn band does not span 1..3:\n${JSON.stringify(diskWarn)}`);
  }
  // A gauge with no scale and no thresholds is just the value: no bar, no
  // bands, no severity, and it must not invent any of them.
  const plainGauge = tile("plain gauge");
  if ((await plainGauge.getAttribute("data-gauge-value")) !== "7") {
    die(
      `the plain gauge reads ${await plainGauge.getAttribute("data-gauge-value")}, wanted 7 - the pushed reading`,
    );
  }
  if ((await plainGauge.getAttribute("data-gauge-min")) !== null) {
    die("the plain gauge declares a scale it does not have");
  }
  if ((await plainGauge.getAttribute("data-gauge-severity")) !== null) {
    die("the plain gauge declares a severity it cannot have - no thresholds were pushed");
  }
  if ((await plainGauge.locator("[data-gauge-track]").count()) !== 0) {
    die("the plain gauge draws a bar over no scale");
  }
  // The honest wrong-shape states, same honesty as the grid, frame and
  // series refusals: prose, half a threshold set, and a value off its scale
  // each say so rather than drawing a bar that lies.
  if ((await tile("never gauge").getAttribute("data-empty")) === null) {
    die('the "never gauge" tile does not say its metric has no rows');
  }
  if ((await tile("wordy gauge").getAttribute("data-gauge-bad")) === null) {
    die('the "wordy gauge" tile does not say its reading is not a gauge');
  }
  if ((await tile("half threshold").getAttribute("data-gauge-bad")) === null) {
    die('the "half threshold" tile does not say its direction is undecidable');
  }
  if ((await tile("off scale").getAttribute("data-gauge-bad")) === null) {
    die('the "off scale" tile does not say its value cannot be placed on its scale');
  }

  // ---- ARM 7: THE REPORT - the other dashboard style, drawn as its
  // document. The reading carries the whole page: eyebrow, title, lede and
  // four sections of the closed vocabulary. Tones are words the console maps
  // to the palette, and a word outside the set draws as no tone at all; a
  // card's spark is a metric ref asked off the series door, the same window
  // the series tile reads. ----
  const reportTile = tile("vl sweep report");
  await reportTile.scrollIntoViewIfNeeded().catch(() => {});
  if ((await reportTile.getAttribute("data-report-bad")) !== null) {
    die("the vl sweep report draws as a wrong shape - the pushed document is a report");
  }
  // The header pieces are child elements carrying their text, not root
  // attributes - the tile root only says whether the document is bad. Count
  // first, read second: textContent auto-waits, and a missing element must
  // die in the arm's words, not in a locator timeout.
  const eyebrow = reportTile.locator("[data-report-eyebrow]");
  const eyebrowText = (await eyebrow.count()) === 1 ? await eyebrow.textContent() : null;
  if (eyebrowText !== "three boxes") {
    die(`the report's eyebrow reads ${eyebrowText}, wanted "three boxes" - the pushed document`);
  }
  const reportTitle = reportTile.locator("[data-report-title]");
  const titleText = (await reportTitle.count()) === 1 ? await reportTitle.textContent() : null;
  if (titleText !== "VL Sweep") {
    die("the report's title is not the pushed title");
  }
  if ((await reportTile.locator("[data-report-lede]").count()) !== 1) {
    die("the report's lede is not drawn");
  }
  if ((await reportTile.getAttribute("data-age")) === null) {
    die("the report carries no age - a frozen page reads as a live one");
  }
  if ((await reportTile.getAttribute("data-state")) !== "measured") {
    die(
      `the report's state reads ${await reportTile.getAttribute("data-state")}, wanted measured - the row claims it`,
    );
  }
  const sectionKinds = await reportTile
    .locator("[data-report-section]")
    .evaluateAll((els) => els.map((el) => el.getAttribute("data-report-section-kind")));
  if (
    JSON.stringify(sectionKinds) !== JSON.stringify(["progress", "cards", "columns", "squares"])
  ) {
    die(`the report draws sections [${sectionKinds}], wanted the four pushed kinds in order`);
  }
  // The progress section: figure, caption, and segments that sum into the
  // bar - the segment widths are the pushed values over the pushed total.
  // The figure and caption are text on their own elements; the total rides
  // on the track element the segments are laid out inside.
  const progressValue = reportTile.locator("[data-progress-value]");
  const valueText = (await progressValue.count()) === 1 ? await progressValue.textContent() : null;
  if (valueText !== "44.3") {
    die(`the progress figure reads ${valueText}, wanted 44.3`);
  }
  const progressTrack = reportTile.locator("[data-progress-track]");
  const totalAttr =
    (await progressTrack.count()) === 1
      ? await progressTrack.getAttribute("data-progress-total")
      : null;
  if (totalAttr !== "5850") {
    die("the progress bar does not carry its pushed total 5850");
  }
  const progressCaption = reportTile.locator("[data-progress-caption]");
  const captionText =
    (await progressCaption.count()) === 1 ? await progressCaption.textContent() : null;
  if (captionText !== "cells done") {
    die("the progress caption is not the pushed caption");
  }
  const progressSegments = reportTile.locator("[data-progress-segment]");
  if ((await progressSegments.count()) !== 2) {
    die(`the progress bar draws ${await progressSegments.count()} segments, wanted 2`);
  }
  const firstSegment = progressSegments.first();
  if (
    (await firstSegment.getAttribute("data-progress-segment-label")) !== "lab2x1" ||
    (await firstSegment.getAttribute("data-progress-segment-value")) !== "1000"
  ) {
    die("the first progress segment is not the pushed lab2x1 1000");
  }
  const trackBox = await progressTrack.boundingBox();
  const segBox = await firstSegment.boundingBox();
  if (!trackBox || !segBox) die("the progress bar has no laid-out boxes");
  if (Math.abs(segBox.width / trackBox.width - 1000 / 5850) > 0.02) {
    die(
      `the first progress segment runs ${segBox.width / trackBox.width} of the bar, wanted 1000/5850 - value over total`,
    );
  }
  // The cards section: one card carrying title, pill, blurb, stats, a spark
  // metric ref and a note - the spark draws the points window the series
  // arm seeded, asked once at that window.
  const reportCard = reportTile.locator("[data-report-card]");
  if ((await reportCard.count()) !== 1) {
    die(`the report draws ${await reportCard.count()} cards, wanted 1`);
  }
  if ((await reportCard.getAttribute("data-report-card-title")) !== "lab2x1") {
    die("the card's title is not the pushed title");
  }
  if ((await reportCard.getAttribute("data-report-card-pill")) !== "green") {
    die("the card's pill is not the pushed pill");
  }
  if ((await reportCard.getAttribute("data-report-card-stats")) !== "2") {
    die("the card does not draw its two pushed stats");
  }
  if ((await reportCard.getAttribute("data-report-spark")) !== metricName("points")) {
    die("the card's spark is not the pushed metric ref");
  }
  if ((await reportCard.getAttribute("data-report-spark-points")) !== "5") {
    die("the card's spark is not the pushed window of 5");
  }
  const cardSpark = reportCard.locator("[data-spark-svg]");
  if ((await cardSpark.count()) !== 1) {
    die("the card draws no spark over a window the door answered");
  }
  const sparkPath = await cardSpark.locator("[data-spark-path]").getAttribute("points");
  if (!sparkPath || sparkPath.split(" ").length !== 5) {
    die(
      `the card spark draws ${sparkPath?.split(" ").length ?? 0} points, wanted 5 - the pushed window`,
    );
  }
  // The columns section: a header of two aligned columns and two rows - the
  // second row's first cell carries the tone word good, which the console
  // maps to the palette's dim green. Measured against a probe styled with
  // the same var in-page, so the comparison is of two computed colours.
  const cols = reportTile.locator("[data-report-col]");
  if ((await cols.count()) !== 2) {
    die(`the columns section draws ${await cols.count()} columns, wanted 2`);
  }
  if ((await cols.nth(0).getAttribute("data-report-col-label")) !== "model") {
    die("the first column is not the pushed model column");
  }
  if ((await cols.nth(0).getAttribute("data-report-col-align")) !== "left") {
    die("the first column's align is not the pushed left");
  }
  if ((await cols.nth(1).getAttribute("data-report-col-align")) !== "right") {
    die("the second column's align is not the pushed right");
  }
  if ((await reportTile.locator("[data-report-row]").count()) !== 2) {
    die("the columns section does not draw its two rows");
  }
  const goodCell = reportTile.locator('[data-report-cell-tone="good"]');
  if ((await goodCell.count()) !== 1) die("the columns section does not carry the good-toned cell");
  if ((await goodCell.getAttribute("data-report-cell-text")) !== "b") {
    die("the good-toned cell does not carry its pushed text");
  }
  const goodColour = await goodCell.evaluate((el) => getComputedStyle(el).color);
  const greenProbe = await page.evaluate(() => {
    const probe = document.createElement("div");
    probe.style.color = "var(--color-serenedash-2)";
    document.body.appendChild(probe);
    const colour = getComputedStyle(probe).color;
    probe.remove();
    return colour;
  });
  if (goodColour !== greenProbe) {
    die(`the good-toned cell is not the palette's dim green: ${goodColour} vs ${greenProbe}`);
  }
  // The squares section: one group, one row, two cells - the good cell
  // colours off the palette and carries its hover title; the "ok" cell is a
  // word outside the tone set and draws as no tone at all, never a crash and
  // never a colour the producer did not choose.
  const squareCells = reportTile.locator("[data-square-cell]");
  if ((await squareCells.count()) !== 2) {
    die(`the squares section draws ${await squareCells.count()} cells, wanted 2`);
  }
  const groupLabel = reportTile.locator("[data-report-group-label]");
  const groupAttr =
    (await groupLabel.count()) === 1
      ? await groupLabel.getAttribute("data-report-group-label")
      : null;
  if (groupAttr !== "lab2x1") {
    die("the squares group's label is not the pushed label");
  }
  const goodSquare = reportTile.locator('[data-square-tone="good"]');
  const goodSquareColour = await goodSquare.evaluate((el) => getComputedStyle(el).backgroundColor);
  const greenBgProbe = await page.evaluate(() => {
    const probe = document.createElement("div");
    probe.style.backgroundColor = "var(--color-serenedash-2)";
    document.body.appendChild(probe);
    const colour = getComputedStyle(probe).backgroundColor;
    probe.remove();
    return colour;
  });
  if (goodSquareColour !== greenBgProbe) {
    die(`the good square is not the palette's dim green: ${goodSquareColour} vs ${greenBgProbe}`);
  }
  if ((await goodSquare.getAttribute("data-square-title")) !== "hover x") {
    die("the good square does not carry its pushed hover title");
  }
  const okSquare = reportTile.locator('[data-square-tone="ok"]');
  const okSquareColour = await okSquare.evaluate((el) => getComputedStyle(el).backgroundColor);
  const plainProbe = await page.evaluate(() => {
    const probe = document.createElement("span");
    document.body.appendChild(probe);
    const colour = getComputedStyle(probe).backgroundColor;
    probe.remove();
    return colour;
  });
  if (okSquareColour !== plainProbe) {
    die(
      `the ok square - a tone word outside the set - draws ${okSquareColour}, wanted no tone: the console must not invent a colour the producer did not choose`,
    );
  }
  // The honest states: a report tile over a never-pushed metric says so, and
  // one over a series of words says its reading is not a document.
  if ((await tile("never report").getAttribute("data-empty")) === null) {
    die('the "never report" tile does not say its metric has no rows');
  }
  if ((await tile("wordy report").getAttribute("data-report-bad")) === null) {
    die('the "wordy report" tile does not say its reading is not a report');
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
  if ((await memGauge.getAttribute("data-stale")) === null) {
    die("the mem gauge past its 5s threshold is not styled stale");
  }
  if ((await reportTile.getAttribute("data-stale")) === null) {
    die("the report tile past its 5s threshold is not styled stale");
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

  // ---- ARM 8: a pushed row shows without a reload - the page re-fetches on
  // its own beat. The age ticked up while the rows were frozen from page
  // load, a screenshot with a clock on it; now a row pushed after load wins
  // the tile within one beat, no reload asked. ----
  await mkMetric(metricName("rate"), 777);
  const refetchDeadline = Date.now() + 35_000;
  let shownValue = null;
  while (Date.now() < refetchDeadline) {
    shownValue = await rateTile.getAttribute("data-value");
    if (shownValue === "777") break;
    await new Promise((r) => setTimeout(r, 1000));
  }
  if (shownValue !== "777") {
    die(
      `without a reload the rate tile reads ${shownValue}, wanted 777 - a pushed row must reach the page on its own beat`,
    );
  }

  // ---- ARM 9: THE LOG - a stream's last lines drawn oldest first, each
  // level a tag in its severity colour, the no-level line drawn as plain
  // text, and the counts the door computed over the window. A log is prose,
  // so the tile draws lines, never a trend; a stream never pushed says so
  // rather than drawing an empty list that reads as silence. ----
  const logTile = tile("build tail");
  await logTile.scrollIntoViewIfNeeded().catch(() => {});
  if ((await logTile.getAttribute("data-tile-kind")) !== "log") {
    die('the "build tail" tile is not a log tile');
  }
  if ((await logTile.getAttribute("data-log-empty")) !== null) {
    die('the "build tail" tile draws as empty - the stream has four pushed lines');
  }
  const lineEls = logTile.locator("[data-log-line]");
  if ((await lineEls.count()) !== 4) {
    die(`the build tail draws ${await lineEls.count()} lines, wanted 4`);
  }
  const lineMessages = await lineEls.evaluateAll((els) =>
    els.map((e) => e.getAttribute("data-log-line")),
  );
  if (
    lineMessages[0] !== "booting the sweep" ||
    lineMessages[3] !== "panic: nil map deref at 0x7f"
  ) {
    die(
      `the build tail runs ${JSON.stringify(lineMessages)}, wanted the seeded lines oldest-first`,
    );
  }
  const lineLevels = await lineEls.evaluateAll((els) =>
    els.map((e) => e.querySelector("[data-log-level]")?.getAttribute("data-log-level") ?? null),
  );
  if (
    lineLevels[0] !== "INFO" ||
    lineLevels[1] !== "WARN" ||
    lineLevels[2] !== "ERROR" ||
    lineLevels[3] !== null
  ) {
    die(
      `the build tail's level tags read ${JSON.stringify(lineLevels)}, wanted INFO, WARN, ERROR and none - a line without a level must draw as no tag`,
    );
  }
  // The type rides beside its line, in the producer's own brackets - the
  // WARN and ERROR lines are both typed.
  const typeEls = logTile.locator("[data-log-type]");
  if ((await typeEls.count()) !== 2) {
    die(`the build tail draws ${await typeEls.count()} typed lines, wanted the two seeded types`);
  }
  const lineTypes = await typeEls.evaluateAll((els) =>
    els.map((e) => e.getAttribute("data-log-type")),
  );
  if (lineTypes[0] !== "disk" || lineTypes[1] !== "sweep") {
    die(`the build tail's typed lines read ${JSON.stringify(lineTypes)}, wanted disk then sweep`);
  }
  // The counts are the door's, not the page's arithmetic: one line per
  // level, and the level-less line counted under no level.
  for (const lvl of ["INFO", "WARN", "ERROR"]) {
    const chip = logTile.locator(`[data-log-count="${lvl}"]`);
    if ((await chip.count()) !== 1) die(`the build tail draws no ${lvl} count`);
    if ((await chip.getAttribute("data-log-count-value")) !== "1") {
      die(`the build tail's ${lvl} count is not 1`);
    }
  }
  // The severity is styled, not just spoken: the ERROR tag takes the
  // palette's dim red-orange, measured against an in-page probe of the same
  // var like the gauge's fill.
  const errTagColour = await logTile
    .locator('[data-log-level="ERROR"]')
    .evaluate((el) => getComputedStyle(el).color);
  const errProbe = await page.evaluate(() => {
    const probe = document.createElement("div");
    probe.style.color = "var(--color-serenedash-3)";
    document.body.appendChild(probe);
    const colour = getComputedStyle(probe).color;
    probe.remove();
    return colour;
  });
  if (errTagColour !== errProbe) {
    die(
      `the ERROR tag's colour is not the palette's dim red-orange: ${errTagColour} vs ${errProbe}`,
    );
  }
  if ((await tile("never log").getAttribute("data-log-empty")) === null) {
    die('the "never log" tile does not say its stream has no lines');
  }

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
      "truncated window, refuses prose points, draws the gauge " +
      "value with its pushed bounds - fill min to value, bands where the " +
      "thresholds say, direction off the order, severity off the palette - " +
      "refuses unplaceable readings, draws the report document - header and " +
      "all four sections, progress segments over the pushed total, tone words " +
      "mapped to the palette with an unknown word drawn as no tone, a card " +
      "spark answered at its own window - and says so when a report tile's " +
      "reading is not one, moves the " +
      "cursor row under j/k, pgup/pgdn, home/end and esc, styles a stale tile " +
      "past its threshold, says what each reading is - inferred as claimed, " +
      "unknown as unclaimed - right-aligns its numbers off the eight-colour " +
      "palette, refuses an outsider, a reload shows the newest row with a " +
      "fresh age, a row pushed after load wins its tile without one, and " +
      "draws the log tail oldest-first with level tags in severity colours, " +
      "leaves a level-less line untagged, shows the door's counts, and says " +
      "so when a stream has no lines",
  );
} finally {
  await browser.close();
}
