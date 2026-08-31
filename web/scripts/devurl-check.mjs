/**
 * THE URL A DEV SERVER PRINTED, AND WHOSE LOOPBACK IT IS.
 *
 *   node scripts/devurl-check.mjs
 *
 * 01M1558DPM1HRGZNJGMVW24DHF item 6. The row calls it pleasant rather than
 * load-bearing, and finding the URL is indeed easy. What is not easy is the
 * three ways it goes wrong, which is what is asserted here.
 *
 * WHOSE MACHINE. "127.0.0.1:5173" from a shell on this host is reachable from
 * the browser. The same string from inside a microVM is the GUEST's loopback,
 * and opening it from the browser reaches the person's OWN machine on that
 * port - so at best it fails and at worst it opens something else of theirs. A
 * link that quietly goes somewhere else is worse than no link.
 *
 * SPLIT WRITES. A pty flushes when it flushes, so a URL arrives in two pieces
 * routinely - and the banner is colourised, so escape sequences sit between
 * them. A scanner that only looks at one write finds nothing on exactly the
 * servers this exists for.
 *
 * REPORTING IT ONCE. A buffer that keeps its tail wholesale rediscovers a URL
 * it already reported on every later write, so a server stopped ten minutes ago
 * keeps offering its address for as long as anything else prints.
 */

import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";

const here = dirname(fileURLToPath(import.meta.url));
const bundled = await build({
  entryPoints: [resolve(here, "..", "src", "lib", "devurl.ts")],
  bundle: true,
  format: "esm",
  write: false,
  logLevel: "silent",
});
const { devUrlScanner } = await import(
  `data:text/javascript;base64,${Buffer.from(bundled.outputFiles[0].text).toString("base64")}`
);

const bad = [];
const check = (what, ok, saw) => {
  if (!ok) bad.push(`${what}\n     saw: ${JSON.stringify(saw)}`);
};
const enc = new TextEncoder();
const feed = (scanner, ...chunks) => {
  let last = null;
  for (const c of chunks) {
    const got = scanner.push(enc.encode(c));
    if (got) last = got;
  }
  return last;
};

// The plain case.
{
  const s = devUrlScanner("host");
  const got = feed(s, "  VITE v5.0.0  ready in 312 ms\n\n  ➜  Local:   http://localhost:5173/\n");
  check("a vite banner's local URL is found", got?.url === "http://localhost:5173/", got);
  check("a shell on this host is reachable from this browser", got?.reachable === true, got);
}

// WHOSE LOOPBACK - the assertion that keeps a link from going somewhere else.
{
  const s = devUrlScanner("vm");
  const got = feed(s, "ready at http://127.0.0.1:3000\n");
  check("a URL is still reported from a guest, so the panel can say what it is", !!got, got);
  check(
    "but a guest's own loopback is NOT reachable from this browser",
    got?.reachable === false,
    got,
  );
}

// 0.0.0.0 IS NOT A DESTINATION.
{
  const s = devUrlScanner("host");
  const got = feed(s, "Listening on http://0.0.0.0:8080\n");
  check(
    "0.0.0.0 is rewritten to the address the person meant",
    got?.url === "http://127.0.0.1:8080",
    got,
  );
}

// SPLIT ACROSS WRITES, WITH COLOUR IN THE SEAM.
{
  const s = devUrlScanner("host");
  const got = feed(s, "  \x1b[32m➜\x1b[39m  Local:   http://local", "host:5173/\n");
  check("a URL split across two writes is still found", got?.url === "http://localhost:5173/", got);
}
{
  const s = devUrlScanner("host");
  const got = feed(s, "\x1b[36mhttp://\x1b[1m127.0.0.1\x1b[0m:4321/\x1b[0m\n");
  check(
    "escape sequences woven through the URL do not hide it",
    got?.url === "http://127.0.0.1:4321/",
    got,
  );
}

// REPORTED ONCE, NOT FOREVER.
{
  const s = devUrlScanner("host");
  feed(s, "ready at http://localhost:5173/\n");
  const again = feed(s, "some later output with no url in it at all\n");
  check("a URL already reported is not rediscovered by the next write", again === null, again);
}

// THE NEWEST WINS. Servers print Local and Network together, and a restart
// prints a fresh banner - the last one seen is the one that is true now.
{
  const s = devUrlScanner("host");
  const got = feed(s, "Local: http://localhost:5173/\nNetwork: http://127.0.0.1:5174/\n");
  check("the last URL in a write is the one reported", got?.url === "http://127.0.0.1:5174/", got);
}

// NOT ANY URL. Offering to open whatever host appears in the output turns a log
// line into a click on somebody else's site.
{
  const s = devUrlScanner("host");
  const got = feed(s, "fetching https://example.com/api/v1/things\n");
  check("a URL that is not loopback is not offered", got === null, got);
}

// Sentence punctuation is the sentence's, not the URL's.
{
  const s = devUrlScanner("host");
  const got = feed(s, "Server ready at http://localhost:3000/.\n");
  check(
    "a trailing full stop is not made part of the URL",
    got?.url === "http://localhost:3000/",
    got,
  );
}

if (bad.length > 0) {
  console.error(
    `the dev-server URL is read wrong in ${bad.length} way(s):\n\n  - ${bad.join("\n  - ")}`,
  );
  process.exit(1);
}
console.log(
  "a dev server's URL is found across split and colourised writes, 0.0.0.0 is made visitable, a guest's loopback is marked unreachable, and it is reported once",
);
