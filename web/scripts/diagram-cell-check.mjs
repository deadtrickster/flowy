/**
 * A shape inside a diagram is a place you can send somebody.
 *
 *   node scripts/diagram-cell-check.mjs BASE_URL TOKEN PROJECT
 *
 * The reference in this store is (project, type, id) everywhere; a cell inside
 * a diagram is that plus the mxCell id, which is the id that survives a
 * re-layout - measured in the vendored editor, where moving a shape and adding
 * another left the existing id untouched. A coordinate has no such property.
 *
 * Two answers, and the second is the one worth having:
 *
 *   the shape is here      the page says which shape the link means
 *   the shape is gone      the page SAYS SO
 *
 * The second is the whole reason this is a check rather than a link. A shape
 * can be deleted after somebody points at it, and the failure that costs a
 * reader is the silent one: a diagram opens looking perfectly ordinary while
 * the thing they were sent to look at is not in it. A citation that no longer
 * lands has to be distinguishable from one that does, from the page, without
 * anybody knowing what the drawing used to contain.
 *
 * The diagram is written through the node's own door rather than drawn in the
 * editor: this check is about the reference, and driving drawio to place a
 * shape would be measuring the editor instead - which drawio-probe already
 * does, offline and without a node.
 */

import { chromium } from "playwright";

const [base, token, project] = process.argv.slice(2);
if (!base || !token || !project) {
  console.error("usage: node scripts/diagram-cell-check.mjs BASE_URL TOKEN PROJECT");
  process.exit(2);
}

const die = (why) => {
  console.error(why);
  process.exit(1);
};

const CELL = "shape-under-test";
const XML = [
  '<mxfile><diagram id="d" name="Page-1"><mxGraphModel><root>',
  '<mxCell id="0"/><mxCell id="1" parent="0"/>',
  `<UserObject label="the room this points at" link="/chat/general" flowyType="room" `,
  `flowyId="general" id="${CELL}">`,
  '<mxCell style="rounded=1;" vertex="1" parent="1">',
  '<mxGeometry x="80" y="80" width="160" height="60" as="geometry"/></mxCell></UserObject>',
  "</root></mxGraphModel></diagram></mxfile>",
].join("");

const ask = async (path, init = {}) => {
  const res = await fetch(`${base}${path}`, {
    ...init,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${init.method ?? "GET"} ${path} -> ${res.status} ${text}`);
  return text ? JSON.parse(text) : null;
};

const made = await ask("/api/artifacts", {
  method: "POST",
  body: JSON.stringify({
    type: "memory",
    kind: "diagram",
    project,
    title: `a diagram with one shape ${Date.now()}`,
    body: XML,
    visibility: "project",
  }),
});

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  // 1. THE LINK IS SOMETHING SOMEBODY CAN PRODUCE BY CLICKING. A reference
  // that can only be composed by hand is a reference nobody makes.
  await page.goto(`${base}/diagrams/${made.id}`, { timeout: 30_000 }).catch(() => {});
  const shape = page.locator(`[data-shape-link="${CELL}"]`);
  try {
    await shape.waitFor({ state: "visible", timeout: 20_000 });
  } catch {
    const errors = crashes.length ? `\npage errors:\n  ${crashes.join("\n  ")}` : "";
    die(`the entity panel offers no link to the shape ${CELL} that carries the entity.${errors}`);
  }
  await shape.click();

  const named = page.locator(`[data-diagram-cell="${CELL}"]`);
  try {
    await named.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    die(`following the shape link landed at ${page.url()} and the page does not say which shape
it means - a reference the reader cannot see is not one they can check`);
  }
  if (!new URL(page.url()).pathname.endsWith(`/${CELL}`)) {
    die(`the shape link left the address at ${page.url()}, so the shape is not addressable`);
  }
  const says = (await named.innerText()).trim();
  if (!says.includes("the room this points at")) {
    die(`the page names the shape as ${JSON.stringify(says)} and does not carry its label, so a
reference reads as an id somebody has to go and look up`);
  }

  // 2. AND A REFERENCE THAT NO LONGER LANDS SAYS SO. The control, and the
  // reason for the check: without it "the page rendered" is true of both.
  await page.goto(`${base}/diagrams/${made.id}/no-such-shape`, { timeout: 30_000 }).catch(() => {});
  const gone = page.locator('[data-diagram-cell-missing="no-such-shape"]');
  try {
    await gone.waitFor({ state: "visible", timeout: 15_000 });
  } catch {
    const drawn = (await page.locator("body").innerText()).replace(/\s+/g, " ").slice(0, 200);
    die(`a link to a shape this diagram does not contain opened quietly: nothing on the page says
the reference is dead. It showed: ${JSON.stringify(drawn)}`);
  }
  if ((await page.locator(`[data-diagram-cell="no-such-shape"]`).count()) !== 0) {
    die("the page claims a shape that is not in the diagram is in it");
  }

  if (crashes.length) die(`the console threw while following a shape link: ${crashes.join("; ")}`);
  console.log(
    `a shape is addressable: /diagrams/${made.id}/${CELL} names it and carries its label, and a link to a shape that is not there says so`,
  );
} finally {
  await browser.close();
}
