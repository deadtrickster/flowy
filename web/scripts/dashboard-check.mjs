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
 *     tiles - a fixed vocabulary (number, table) over a named metric - and the
 *     console renders the declaration. It RUNS nothing: the data is metric
 *     rows producers push through the ordinary artifact door;
 *   - every number shows its age from the row it reads, and a datum older than
 *     its tile's threshold is visibly stale, not silently live - the operator
 *     reading prose today is exactly the failure this exists to fix;
 *   - a dashboard is no more readable than the artifacts it names: a reader
 *     outside the rows' scope is refused;
 *   - reopening the page shows the newest rows and the newest ages - nothing
 *     is remembered between loads, the dashboard holds no state of its own.
 *
 * THREE ARMS, of which the second is the one a component test would miss:
 *
 *   1. an agent authors a dashboard and metric rows through the API; the page
 *      lists the dashboard and renders each declared number with its age;
 *   2. a principal from another project opens it and is refused - and their
 *      metrics read comes back empty;
 *   3. a newer metric row shows on reload with a fresh age, and a stale tile
 *      is styled stale, not silently current.
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
      { kind: "number", label: "cells done", metric: metricName("cells"), stale_after_seconds: 5 },
      { kind: "number", label: "rate", metric: metricName("rate"), stale_after_seconds: 86400 },
      {
        kind: "number",
        label: "never pushed",
        metric: metricName("missing"),
        stale_after_seconds: 5,
      },
      { kind: "table", label: "cells, latest rows", metric: metricName("cells") },
    ],
  },
});

const mkMetric = (name, value) =>
  post({
    type: "memory",
    kind: "metric",
    title: `metric ${stamp} ${name}`,
    fields: { name, value },
  });

await mkMetric(metricName("cells"), 1200);
await mkMetric(metricName("rate"), 4.2);
await mkMetric(metricName("cells"), 1350);

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
const heldDash = await api(`/api/artifact/${dash.id}`);
if (!Array.isArray(heldDash.fields?.tiles) || heldDash.fields.tiles.length !== 4) {
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
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
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
  if (!/\d+\s*(s|m|h|d)/.test(ageText)) {
    die(`the cells tile's age is not in words a person reads:\n${ageText}`);
  }

  const rateTile = tile("rate");
  if ((await rateTile.getAttribute("data-value")) !== "4.2") {
    die(`the rate tile reads ${await rateTile.getAttribute("data-value")}, wanted 4.2`);
  }
  if ((await rateTile.getAttribute("data-stale")) !== null) {
    die("the rate tile is styled stale with a day-wide threshold and a fresh row");
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

  // ---- THE STALENESS HALF: past its threshold, the tile is styled stale,
  // not silently live. ----
  await page.waitForTimeout(6000);
  if ((await cellsTile.getAttribute("data-stale")) === null) {
    die("the cells tile past its 5s threshold is not styled stale");
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
  const outsiderPage = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
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
    "the dashboard lists, renders four declared tiles from pushed rows with ages, " +
      "styles a stale tile past its threshold, refuses an outsider, and a reload " +
      "shows the newest row with a fresh age",
  );
} finally {
  await browser.close();
}
