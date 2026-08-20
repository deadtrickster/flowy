/**
 * Related artifacts are rows a reader can read and click, on ANY artifact.
 *
 *   node scripts/related-rows-check.mjs BASE_URL TOKEN
 *
 * TWO DEFECTS, ONE CHECK, and the second was found while verifying the fix for
 * the first.
 *
 * The operator: "pathetic - artifacts are just names not lists." Related
 * artifacts rendered as eight characters of a ULID - no title, no type, no
 * link. The node answers a bare id with the row's own project, type and title,
 * so the link is read off the child rather than guessed from the parent.
 *
 * And then: the related block lived inside FindingSection, so it drew ONLY on
 * findings. A note with related ids stored them and showed nothing. Nobody
 * noticed because every check that looked at related rows looked at a finding.
 *
 * SO THIS ASKS A NOTE. Asking a finding would pass on the very tree where a
 * note shows nothing, which is the bug this half exists to catch.
 *
 * It also carries an id that cannot resolve, because vanishing is the wrong
 * answer to that: the list would disagree with the count beside it, and a
 * reader would never learn the row points somewhere they cannot reach.
 */

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/related-rows-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const die = (message) => {
  console.error(message);
  process.exit(1);
};

const GONE = "01M0000000000000000000ZZZZ";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const crashes = [];
  page.on("pageerror", (err) => crashes.push(String(err)));
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
  // Navigated first so the writes below are same-origin.
  await page.goto(`${base}/`, { timeout: 30_000 }).catch(() => {});

  const made = await page.evaluate(
    async ([t, gone]) => {
      const write = async (body) => {
        const res = await fetch("/api/artifacts", {
          method: "POST",
          headers: { Authorization: `Bearer ${t}`, "Content-Type": "application/json" },
          body: JSON.stringify(body),
        });
        if (!res.ok) return { error: `${res.status} ${await res.text()}` };
        return await res.json();
      };
      const target = await write({
        type: "note",
        title: "related-rows-check: the row that gets pointed at",
        body: "a target for the related list",
      });
      if (target.error) return target;
      // A NOTE, not a finding: the whole point of this arm.
      const pointer = await write({
        type: "note",
        title: "related-rows-check: the row that points",
        body: "carries two related ids, one of which cannot resolve",
        related: [target.id, gone],
      });
      return pointer.error ? pointer : { target, pointer };
    },
    [token, GONE],
  );
  if (made.error) die(`could not write the rows to point with: ${made.error}`);

  const { target, pointer } = made;
  await page
    .goto(`${base}/p/${encodeURIComponent(pointer.project)}/note/${pointer.id}`, {
      timeout: 30_000,
    })
    .catch(() => {});
  await page
    .locator("[data-related-block]")
    .waitFor({ state: "visible", timeout: 20_000 })
    .catch(() => {});
  if (crashes.length > 0) die(`the artifact page threw: ${crashes.join("; ")}`);

  if ((await page.locator("[data-related-block]").count()) === 0) {
    die("a NOTE carrying related ids drew no related block - related is still finding-only");
  }

  const rows = await page.evaluate(() =>
    Array.from(document.querySelectorAll("[data-related]")).map((el) => ({
      id: el.getAttribute("data-related"),
      state: el.getAttribute("data-related-state"),
      href: el.getAttribute("href"),
      text: (el.textContent || "").trim().replace(/\s+/g, " "),
    })),
  );
  if (rows.length !== 2) {
    die(`the related list drew ${rows.length} rows for 2 ids: ${JSON.stringify(rows)}`);
  }

  const resolved = rows.find((r) => r.id === target.id);
  if (!resolved) die(`the row it points at is not in the list: ${JSON.stringify(rows)}`);
  if (resolved.state !== "linked") die(`a readable row rendered as ${resolved.state}`);
  if (!resolved.href?.includes(target.id)) {
    die(`the link does not point at the row it names: ${resolved.href}`);
  }
  // Read off the CHILD. The parent here is a note; if the link were assembled
  // from the parent's own type this would still say note by luck, so the title
  // is what proves the child was actually read.
  if (!resolved.text.includes("the row that gets pointed at")) {
    die(`the row shows no title of its own: ${JSON.stringify(resolved.text)}`);
  }

  const missing = rows.find((r) => r.id === GONE);
  if (!missing) die("the unresolvable id vanished from the list instead of saying so");
  if (missing.state !== "refused") {
    die(`an id nothing can resolve rendered as ${missing.state}, not as a refusal`);
  }

  console.log(
    `a note draws its related rows: ${resolved.text.slice(0, 40)} links to ${target.id}, and one unresolvable id says so`,
  );
} finally {
  await browser.close();
}
