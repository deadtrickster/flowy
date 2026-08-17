// Prove the vendored draw.io is complete and works offline.
//
// It serves web/public exactly as the node serves it - so the editor is at
// /drawio/index.html, a subpath, which is where drawio's own relative paths
// have to resolve - drives it in a real browser with the shape palettes and
// format panels open, and then asserts three things:
//
//   1. nothing 404s. A file the editor asks for that is not vendored is a
//      broken editor in front of a user, and it is silent until then. This is
//      what web/scripts/drawio.manifest is checked against.
//   2. nothing goes to the network. The console must build and run offline;
//      a request that leaves the machine means a CDN crept back in.
//   3. the embed round-trip works AND a flowy entity reference survives it.
//      A shape referencing a room is a <UserObject> carrying link/flowyType/
//      flowyId. If drawio dropped those on load->save the whole feature is
//      gone, so the probe reads them back out of the xml drawio returns.
//
// It fails loudly rather than reporting. Usage: node scripts/drawio-probe.mjs
// [--keep-open] - run from web/.

import { createReadStream, statSync } from "node:fs";
import { createServer } from "node:http";
import { dirname, extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const here = dirname(fileURLToPath(import.meta.url));
const ROOT = join(dirname(here), "public");

const TYPES = {
  ".html": "text/html",
  ".js": "text/javascript",
  ".css": "text/css",
  ".png": "image/png",
  ".gif": "image/gif",
  ".svg": "image/svg+xml",
  ".txt": "text/plain",
  ".json": "application/json",
  ".xml": "application/xml",
  ".ico": "image/x-icon",
  ".woff2": "font/woff2",
  ".woff": "font/woff",
};

// A diagram with one shape that points at a real flowy room. link is drawio's
// own attribute, so it becomes an anchor in the read-only viewer; flowyType
// and flowyId are ours and drawio carries unknown attributes verbatim.
const DIAGRAM = [
  '<mxfile><diagram id="d" name="Page-1"><mxGraphModel dx="800" dy="600" grid="1" gridSize="10" ',
  'guides="1" tooltips="1" connect="1" arrows="1" fold="1" page="1" pageScale="1" pageWidth="850" ',
  'pageHeight="1100" math="0" shadow="0"><root><mxCell id="0"/><mxCell id="1" parent="0"/>',
  '<UserObject label="general" link="/chat/general" flowyType="room" flowyId="general" id="n1">',
  '<mxCell style="rounded=1;whiteSpace=wrap;html=1;" vertex="1" parent="1">',
  '<mxGeometry x="200" y="160" width="160" height="60" as="geometry"/></mxCell></UserObject>',
  "</root></mxGraphModel></diagram></mxfile>",
].join("");

const HOST = `<!doctype html><html><body style="margin:0">
<iframe id="f" style="width:100vw;height:100vh;border:0"
  src="/drawio/index.html?embed=1&proto=json&offline=1&spin=0&libraries=1&noExitBtn=1&saveAndExit=0"></iframe>
<script>
const f = document.getElementById("f");
window.addEventListener("message", (evt) => {
  if (typeof evt.data !== "string" || !evt.data.length) return;
  let m; try { m = JSON.parse(evt.data); } catch { return; }
  if (m.event === "init") f.contentWindow.postMessage(JSON.stringify(
    { action: "load", autosave: 1, xml: ${JSON.stringify(DIAGRAM)} }), "*");
  if (m.event === "load" || m.event === "autosave") window.probeXml(m.event, m.xml || "");
});
</script></body></html>`;

const misses = [];
const server = createServer((req, res) => {
  const path = decodeURIComponent(req.url.split("?")[0]);
  if (path === "/host.html") {
    res.writeHead(200, { "Content-Type": "text/html" }).end(HOST);
    return;
  }
  const file = join(ROOT, normalize(path).replace(/^(\.\.[/\\])+/, ""));
  try {
    const st = statSync(file);
    if (st.isDirectory()) throw new Error("dir");
    res.writeHead(200, { "Content-Type": TYPES[extname(file)] ?? "application/octet-stream" });
    createReadStream(file).pipe(res);
  } catch {
    misses.push(path);
    res.writeHead(404).end("not vendored");
  }
});

await new Promise((r) => server.listen(0, "127.0.0.1", r));
const base = `http://127.0.0.1:${server.address().port}`;

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 960 } });

const external = [];
page.on("request", (r) => {
  const u = r.url();
  if (!u.startsWith(base) && !u.startsWith("data:") && !u.startsWith("blob:")) external.push(u);
});

const seen = new Map();
await page.exposeFunction("probeXml", (event, xml) => seen.set(event, xml));

await page.goto(`${base}/host.html`, { waitUntil: "load" });
await page.waitForTimeout(7000);

const editor = page.frames().find((f) => f.url().includes("/drawio/index.html"));
if (!editor) {
  console.error("FAIL: the editor iframe never loaded");
  await browser.close();
  server.close();
  process.exit(1);
}

// Exercise the chrome, which is what reaches for files the bare load does not.
const poke = async (fn) => {
  try {
    await fn();
    await page.waitForTimeout(700);
  } catch {
    /* a gesture that does not land is not a failure of the vendoring */
  }
};
await poke(() => editor.click("text=general", { timeout: 5000 }));
for (const pal of ["Misc", "Advanced", "General"]) {
  await poke(() => editor.click(`.geTitle >> text=${pal}`, { timeout: 2500 }));
}
await poke(() =>
  editor.evaluate(() => {
    const s = document.querySelector(".geSidebarContainer");
    if (s) s.scrollTop = s.scrollHeight;
  }),
);
// An actual edit, so autosave fires and hands the xml back.
await poke(() =>
  editor.dblclick(".geDiagramContainer", { position: { x: 700, y: 520 }, timeout: 5000 }),
);
await page.keyboard.press("Escape");
await page.waitForTimeout(2500);

await browser.close();
server.close();

const problems = [];
if (misses.length)
  problems.push(`${misses.length} file(s) 404: ${[...new Set(misses)].join(", ")}`);
if (external.length) {
  problems.push(
    `${external.length} request(s) left the machine: ${[...new Set(external)].join(", ")}`,
  );
}

const back = seen.get("autosave") ?? seen.get("load") ?? "";
if (!back) {
  problems.push("the editor never handed a diagram back (no load/autosave event)");
} else {
  for (const want of ['link="/chat/general"', 'flowyType="room"', 'flowyId="general"']) {
    if (!back.includes(want)) problems.push(`the round-trip dropped ${want}`);
  }
}

if (problems.length) {
  console.error("drawio-probe FAILED:");
  for (const p of problems) console.error(`  - ${p}`);
  process.exit(1);
}
console.log(
  `drawio-probe ok: no 404s, no network, entity reference survived (${back.length} chars back)`,
);
