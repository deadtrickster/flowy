/**
 * The diagram editor is dark, like everything around it.
 *
 *   node scripts/diagram-theme-check.mjs BASE_URL TOKEN PROJECT
 *
 * Raised by the operator: a white editor opening inside a dark console. The
 * console has one theme on purpose - index.css, "the console is dark, once" -
 * so this is not a switch to follow, it is a mismatch to end.
 *
 * TWO ARMS, because one reading cannot tell a theme being applied from a build
 * whose default happens to be dark. The same editor is loaded twice, differing
 * only in the parameter under test, and the check fails unless the two answers
 * DIFFER - which is also what fails if a later drawio drops `dark` and leaves
 * the console asking for something nobody reads.
 *
 * The measurement is the editor's own background colour, taken from inside the
 * frame. Not a screenshot, not a class name: the pixel a person would see.
 */

import { chromium } from "playwright";

const [base, token, project] = process.argv.slice(2);
if (!base || !token || !project) {
  console.error("usage: node scripts/diagram-theme-check.mjs BASE_URL TOKEN PROJECT");
  process.exit(2);
}

const die = (why) => {
  console.error(why);
  process.exit(1);
};

const XML =
  '<mxfile><diagram id="d" name="Page-1"><mxGraphModel><root>' +
  '<mxCell id="0"/><mxCell id="1" parent="0"/>' +
  "</root></mxGraphModel></diagram></mxfile>";

const made = await fetch(`${base}/api/artifacts`, {
  method: "POST",
  headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
  body: JSON.stringify({
    type: "memory",
    kind: "diagram",
    project,
    title: "a diagram to look at in the dark",
    body: XML,
    visibility: "project",
  }),
}).then((r) => (r.ok ? r.json() : die(`seeding a diagram answered ${r.status}`)));

/** luminance is 0 for black and 1 for white, from an rgb() string. */
function luminance(colour) {
  const parts = /rgba?\(([^)]+)\)/.exec(colour ?? "");
  if (!parts) return null;
  const [r, g, b] = parts[1].split(",").map((n) => Number(n.trim()));
  return (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255;
}

/** background reads the editor's own body colour, once it has drawn itself. */
async function background(page, url) {
  await page.goto(url, { timeout: 30_000 }).catch(() => {});
  await page.waitForSelector(".geDiagramContainer, .geEditor, body", { timeout: 30_000 });
  await page.waitForTimeout(1500);
  return page.evaluate(() => {
    const container = document.querySelector(".geDiagramContainer") ?? document.body;
    return getComputedStyle(container).backgroundColor;
  });
}

const browser = await chromium.launch();
try {
  // 1. THE CONSOLE ASKS FOR IT. The parameter has to be on the frame the app
  // actually mounts, not merely supported by the editor.
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  await page.goto(`${base}/diagrams/${made.id}`, { timeout: 30_000 }).catch(() => {});
  const frame = page.locator("iframe");
  try {
    await frame.first().waitFor({ state: "attached", timeout: 30_000 });
  } catch {
    die("the diagram page mounted no editor frame, so there is no theme to check");
  }
  const src = (await frame.first().getAttribute("src")) ?? "";
  if (!/[?&]dark=1(&|$)/.test(src)) {
    die(`the editor frame is loaded as ${src} - without dark=1 the editor draws white
inside a console that is dark everywhere else`);
  }

  // 2. AND IT DOES SOMETHING. Same editor, same page, one parameter apart.
  const dark = await background(page, `${base}${src}`);
  const light = await background(page, `${base}${src.replace(/&dark=1/, "")}`);
  const darkL = luminance(dark);
  const lightL = luminance(light);
  if (darkL === null || lightL === null) {
    die(`could not read a background colour: dark=${dark} light=${light}`);
  }
  if (!(darkL < lightL)) {
    die(`the editor's background is ${dark} with dark=1 and ${light} without it, so the
parameter changed nothing this reader can see - the check that would catch a
drawio that stopped honouring it is this comparison, and it just failed`);
  }
  console.log(`the editor is dark: background ${dark} with dark=1 against ${light} without it`);
} finally {
  await browser.close();
}
