/**
 * Load the built console the way a browser does, and check that it renders.
 *
 * A bundle that exists and is served is not a console: it can still throw on
 * mount and leave an empty <div id="root"> behind, and every check that only
 * looks at the HTML the server returns would still pass. So this one runs the
 * real bundle out of web/dist against a jsdom document, at a deep route, with
 * no token - which is the state a browser is in when somebody opens the link
 * for the first time - and asserts the app painted something.
 *
 * jsdom does not execute <script type="module"> itself, so the bundle is
 * imported here and the DOM globals it expects are put on globalThis first.
 * That is the same trick a jsdom test environment plays, and it runs the file
 * that was shipped rather than a second build made for the occasion.
 *
 * There is no network here on purpose: signed out, the console makes no API
 * call, so what this exercises is the router, the shell and the room view.
 */

import { readFileSync, readdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import jsdom from "jsdom";

// jsdom is CommonJS, so its classes come off the default export.
const { JSDOM, VirtualConsole } = jsdom;

const here = dirname(fileURLToPath(import.meta.url));
const dist = resolve(here, "..", "dist");

const index = readFileSync(join(dist, "index.html"), "utf8");
const bundle = readdirSync(join(dist, "assets")).find((name) => name.endsWith(".js"));
if (!bundle) {
  console.error("web/dist/assets holds no javascript bundle");
  process.exit(1);
}

const problems = [];
const virtualConsole = new VirtualConsole();
virtualConsole.on("jsdomError", (err) => problems.push(err.message));
virtualConsole.on("error", (...args) => problems.push(args.join(" ")));

//   node scripts/render-check.mjs [BASE_URL TOKEN EXPECTED_TEXT [PATH [ABSENT_TEXT]]]
//
// PATH is the route to mount at, and defaults to the room view. Phase 4 passes
// /inbox, which is the same app at another deep link: the check is that the
// route the server falls back to is a route the bundle can actually paint.
//
// ABSENT_TEXT is the other half of a banner check: text that must NOT be on the
// page. It is only meaningful together with EXPECTED_TEXT - "this string is not
// here" is trivially true of a page that never loaded - so the wait for
// EXPECTED_TEXT happens first and the absence is asserted against the page that
// resulted. Phase 9 uses it to check that a resolved announcement is gone,
// which nothing else here can express: every other assertion is a presence.
const [base, token, expected, path = "/chat/general", absent] = process.argv.slice(2);

const dom = new JSDOM(index, {
  // A deep link, because that is the path the server's SPA fallback exists for.
  url: `http://127.0.0.1:8787${path}`,
  pretendToBeVisual: true,
  virtualConsole,
});

// The globals a browser gives the bundle. React reads several of them at import
// time, so they go on before the import rather than after.
const { window } = dom;

/**
 * define puts a global on even when node already declares it read-only, and
 * leaves the handful that cannot be redefined at all (Infinity, NaN, undefined)
 * as node has them - they are the same in a browser anyway.
 */
const define = (name, value) => {
  try {
    Object.defineProperty(globalThis, name, { value, writable: true, configurable: true });
  } catch {
    // A non-configurable global of the language itself. Nothing to do.
  }
};

// Everything the document object model brings, rather than a list of the names
// that happened to be missing the last time this was run: the bundle reaches
// for MutationObserver, getComputedStyle and half a dozen constructors, and a
// hand-kept list of them is a check that breaks whenever a dependency changes.
for (const name of Object.getOwnPropertyNames(window)) {
  // Only what node does not already have. Node's own queueMicrotask and timers
  // are what jsdom's delegate to, so copying those back over the top of them
  // is an infinite recursion rather than a polyfill.
  if (name in globalThis || name.startsWith("_")) continue;
  const value = window[name];
  define(name, typeof value === "function" && !value.prototype ? value.bind(window) : value);
}

// The handful node does have, where the document object model's version is the
// one the bundle means.
define("window", window);
define("document", window.document);
define("navigator", window.navigator);
define("location", window.location);
define("history", window.history);
define("localStorage", window.localStorage);

// jsdom implements neither of these, and the console's layout asks for both.
define("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
define(
  "ResizeObserver",
  class {
    observe() {}
    unobserve() {}
    disconnect() {}
  },
);
window.matchMedia = globalThis.matchMedia;
window.ResizeObserver = globalThis.ResizeObserver;
// jsdom has no layout, so it has no scrollIntoView either. The message list
// follows the room with it, and a missing method is a crash on mount.
window.Element.prototype.scrollIntoView = () => {};

// With a node to talk to, the check goes further: sign in with a real token and
// wait for the view to fill from the API. Without one it stays offline and only
// asserts the app mounts.
if (base && token) {
  const upstream = globalThis.fetch;
  // The bundle asks for /api/... , which is relative to the page in a browser
  // and meaningless to node's fetch, so it is resolved against the node here.
  define("fetch", (input, init) => upstream(new URL(input, base), init));
  window.localStorage.setItem("flowy.token", token);
}

await import(pathToFileURL(join(dist, "assets", bundle)).href);
// React mounts in an effect; give it a couple of frames.
await new Promise((done) => setTimeout(done, 500));

const root = window.document.getElementById("root");
const rendered = () => root?.textContent ?? "";

if (expected) {
  // The view is filled by a fetch and then kept up to date by a long poll, so
  // the text arrives a beat after the mount does.
  const deadline = Date.now() + 15000;
  while (!rendered().includes(expected) && Date.now() < deadline) {
    await new Promise((done) => setTimeout(done, 250));
  }
}

// The banner polls on a timer of its own, so a string that is on its way off
// the page needs a beat after the room has filled. Waiting for it to go is the
// same loop as waiting for it to arrive, with the test the other way round.
if (absent) {
  const deadline = Date.now() + 15000;
  while (rendered().includes(absent) && Date.now() < deadline) {
    await new Promise((done) => setTimeout(done, 250));
  }
}

const text = rendered();
// The frame is on every route; the rest of the list is what this one route has
// to have painted for the check to mean anything.
const want = ["flowy", "inbox"];
if (path === "/chat/general") {
  want.push("#general", "bearer token", "thread");
  if (expected) want.push("watching");
}
if (expected) want.push(expected);
const missing = want.filter((phrase) => !text.includes(phrase));

if (problems.length > 0) {
  console.error(`the console logged errors while mounting:\n${problems.join("\n")}`);
  process.exit(1);
}
if (!root || root.children.length === 0) {
  console.error("the app mounted nothing into #root");
  process.exit(1);
}
if (missing.length > 0) {
  console.error(`${path} is missing ${missing.join(", ")} - it rendered:\n${text}`);
  process.exit(1);
}
if (absent && text.includes(absent)) {
  console.error(`${path} still shows ${absent} - it rendered:\n${text}`);
  process.exit(1);
}

console.log(`mounted ${bundle} at ${path}: ${text.slice(0, 140).replace(/\s+/g, " ")}`);
process.exit(0);
