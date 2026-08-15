/**
 * What the console makes of an error body that is not JSON.
 *
 * There is always something in front of a node in a real deployment, and what
 * it answers with when the node is down is its own: an HTML error page, a
 * plain-text 502, a gateway's idea of a timeout. The client used to parse every
 * body it got before it looked at the status, so those came back as
 * "Unexpected token '<' ..." - a SyntaxError with no status on it, thrown past
 * every caller that handles ApiError, and shown to the person as if the console
 * were broken rather than the node unreachable.
 *
 * So this drives the real request path in web/src/lib/api.ts with fetch stood
 * in for, and asserts what comes back: an ApiError carrying resp.status, with
 * something of the body in it. The JSON paths are checked in the same run,
 * because a fallback that swallowed the node's own {"error": ...} would be a
 * worse console than the one this fixes.
 *
 * The module is TypeScript, so it goes through vite's own esbuild first - the
 * same transform the bundle is built with - and is imported as a data url.
 */

import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { transformWithEsbuild } from "vite";

const here = dirname(fileURLToPath(import.meta.url));
const source = resolve(here, "..", "src", "lib", "api.ts");

const { code } = await transformWithEsbuild(await readFile(source, "utf8"), source, {
  loader: "ts",
  format: "esm",
  target: "node20",
});
const { api, ApiError } = await import(
  `data:text/javascript;base64,${Buffer.from(code).toString("base64")}`
);

const failures = [];
const check = (name, ok, detail) => {
  if (ok) return;
  failures.push(detail ? `${name}: ${detail}` : name);
};

/** answer stands in for fetch with one response, the way a proxy would send it. */
function answer({ status, statusText, body }) {
  globalThis.fetch = async () => ({
    ok: status >= 200 && status < 300,
    status,
    statusText,
    async text() {
      return body;
    },
  });
}

/** thrown runs a call and returns whatever it threw, or null if it did not. */
async function thrown(call) {
  try {
    await call();
    return null;
  } catch (err) {
    return err;
  }
}

// A proxy's HTML 502. This is the one that used to be a SyntaxError.
answer({
  status: 502,
  statusText: "Bad Gateway",
  body: "<html><head><title>502 Bad Gateway</title></head><body>nginx</body></html>",
});
let err = await thrown(() => api.whoami());
check("an html 502 threw", err !== null);
check("an html 502 is an ApiError", err instanceof ApiError, `${err?.name}: ${err?.message}`);
check("an html 502 carries the status", err?.status === 502, `status ${err?.status}`);
check("an html 502 is not a syntax error", !(err instanceof SyntaxError), `${err?.message}`);
check(
  "an html 502 says something readable",
  typeof err?.message === "string" && err.message.includes("502 Bad Gateway"),
  `${err?.message}`,
);

// A plain-text answer from something that is not the node either.
answer({ status: 503, statusText: "Service Unavailable", body: "upstream connect error" });
err = await thrown(() => api.whoami());
check("a plain-text 503 is an ApiError", err instanceof ApiError, `${err?.name}: ${err?.message}`);
check("a plain-text 503 carries the status", err?.status === 503, `status ${err?.status}`);
check(
  "a plain-text 503 keeps the body",
  err?.message === "upstream connect error",
  `${err?.message}`,
);

// A body with nothing in it at all: the status is all there is to say.
answer({ status: 504, statusText: "Gateway Timeout", body: "   " });
err = await thrown(() => api.whoami());
check("an empty 504 carries the status", err?.status === 504, `status ${err?.status}`);
check(
  "an empty 504 falls back to the status line",
  err?.message === "504 Gateway Timeout",
  `${err?.message}`,
);

// And the node's own errors, which are JSON and say what happened. The fallback
// must not have taken these over.
answer({
  status: 404,
  statusText: "Not Found",
  body: JSON.stringify({ error: "no such artifact" }),
});
err = await thrown(() => api.whoami());
check("a json 404 is an ApiError", err instanceof ApiError, `${err?.name}: ${err?.message}`);
check("a json 404 carries the status", err?.status === 404, `status ${err?.status}`);
check(
  "a json 404 keeps the node's message",
  err?.message === "no such artifact",
  `${err?.message}`,
);

// A body that is JSON still decodes: this is the ordinary path.
answer({ status: 200, statusText: "OK", body: JSON.stringify({ user: "u_1", project: "pa" }) });
const who = await api.whoami();
check("a json 200 decodes", who?.user === "u_1" && who?.project === "pa", JSON.stringify(who));

if (failures.length > 0) {
  console.error(`the client mishandled an error body:\n${failures.join("\n")}`);
  process.exit(1);
}

console.log("a non-json error body is an ApiError with the status on it; json bodies unchanged");
process.exit(0);
