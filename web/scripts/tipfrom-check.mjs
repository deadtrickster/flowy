/**
 * Both copies of the merges pane say where the tip came from, and agree with
 * the node.
 *
 *   node scripts/tipfrom-check.mjs BASE_URL TOKEN
 *
 * 01M0JZ5VM8. routes/ChatRoom.tsx passed tipFrom="deployed" as a LITERAL while
 * the response it already had carried tip_from. routes/Todos.tsx read the real
 * value. So the same component, drawn in two places from one endpoint, made two
 * different claims about the same fact.
 *
 * WHY IT MATTERS MORE THAN A LABEL. MergeQueue draws "the commit this node was
 * built from, not a live read of the target" when tipFrom is "deployed". That
 * caveat is a real one and it is worth saying WHEN IT IS TRUE. Said over
 * verdicts that were measured against the last sha to land through the merge
 * door - which is what the node reports as "landed", and what it was reporting
 * the whole time this literal was here - it teaches a reader to discount a
 * verdict that was measured correctly. A hedge in the wrong place is not
 * caution, it is noise that costs trust in the hedges that mean something.
 *
 * AND THE UNION WAS SHORT. The console declared "stated" | "deployed" | "none"
 * in five places. api_mergequeue.go sets four values and the missing one was
 * "landed" - the value this node answers with today. Nothing crashed, because
 * the only test anywhere is `=== "deployed"`; the type simply was not true, and
 * a type that is not true is a check that has been switched off.
 *
 * THE ASSERTION IS AGREEMENT, in two directions: the room's pane against the
 * board's, and both against what the node said. None of it is a constant this
 * file chose, so it keeps working when the node's answer changes - which it
 * does, whenever something lands.
 */

import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";

import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
if (!base || !token) {
  console.error("usage: node scripts/tipfrom-check.mjs BASE_URL TOKEN");
  process.exit(2);
}

const die = (message, shown = "") => {
  console.error(shown ? `${message}\n${shown}` : message);
  process.exit(1);
};

const call = async (path, init = {}) => {
  const r = await fetch(`${base}${path}`, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
      ...init.headers,
    },
  });
  const text = await r.text();
  let body = text;
  try {
    body = JSON.parse(text);
  } catch {
    /* a refusal is a sentence */
  }
  return { ok: r.ok, status: r.status, body };
};

// ─────────────────────────────────────────────────────────────────────────────
// FIRST, THE ARM THAT CANNOT BE HIDDEN BY THE FIXTURE.
//
// The browser arms below compare each pane against what the node told it, which
// is the right assertion and is NOT DISCRIMINATING ON A FRESH NODE. A suite's
// node has had nothing landed through the merge door, so LandedTipOf returns
// nothing and api_mergequeue.go falls through to tip_from "deployed" - which is
// exactly the literal this row is about. Both panes then agree, with and
// without the fix, and the check passes either way.
//
// That is the same failure this fleet hit twice tonight: a check whose two arms
// fail identically, which looks like coverage and is not. Making the fixture
// discriminate would mean landing a row - declare, verdict, land - and that
// writes node-wide state every later check would inherit, to test a console
// literal. Not worth it.
//
// So the defect is asserted where it lives: nobody passes a CONSTANT for a
// value the node answers. It reads the source rather than the screen, which is
// weaker evidence about behaviour and stronger evidence about this bug - it
// cannot pass because the fixture happened to match the constant.
const routes = join(process.cwd(), "src", "routes");
const literals = [];
for (const name of readdirSync(routes)) {
  if (!name.endsWith(".tsx")) continue;
  const src = readFileSync(join(routes, name), "utf8");
  // tipFrom={...} is a value from somewhere; tipFrom="..." is a decision this
  // file made about a fact it does not own.
  for (const m of src.matchAll(/tipFrom=("[^"]*")/g)) {
    literals.push(`${name}: tipFrom=${m[1]}`);
  }
}
if (literals.length > 0) {
  die(`a route states the tip's provenance instead of reading it:
  ${literals.join("\n  ")}
/api/merge-queue answers tip_from on every read - "stated", "landed", "deployed"
or "none" - and a route that hardcodes one is right only by luck. ChatRoom.tsx
said "deployed" while this node answered "landed", so the room's pane drew "the
commit this node was built from" over verdicts measured against something newer.`);
}

// ITS OWN ROOM. The room pane is narrowed by room, and a check that seeded into
// a shared one would depend on what everybody else had filed there - the
// failure class that made four console checks red in a suite tonight while
// passing alone.
const room = `tipfrom-${Date.now().toString(36)}`;

let row = null;
try {
  const made = await call("/api/artifacts", {
    method: "POST",
    body: JSON.stringify({
      type: "memory",
      kind: "merge",
      title: "tipfrom-check seeded row",
      body: "seeded by tipfrom-check",
      visibility: "project",
      // THE ROOM RIDES fields, and only there. /api/artifacts is a strict
      // decoder - a key it does not know is a 400 naming it, not a value
      // dropped - and a top-level "room" is one of those. The store reads
      // fields->>'room' (internal/store/artifacts.go:1020), which is the same
      // place the room filter looks, so this is where it has to go.
      fields: { branch: "tipfrom-check/branch", target: "master", room },
    }),
  });
  if (!made.ok)
    die(`could not file the seed row: HTTP ${made.status} ${JSON.stringify(made.body)}`);
  row = made.body.id;

  // WHAT THE NODE SAYS, for each of the two reads the two panes actually make.
  // Asked separately rather than assumed equal: they are different endpoints,
  // and this check has no business asserting they agree with each other - only
  // that each pane agrees with the answer it was given.
  const board = await call("/api/merge-queue?limit=200");
  if (!board.ok) die(`/api/merge-queue answered ${board.status}`);
  const roomQ = await call(`/api/merge-queue?room=${encodeURIComponent(room)}`);
  if (!roomQ.ok) die(`/api/merge-queue?room= answered ${roomQ.status}`);

  if (!(roomQ.body.items ?? []).some((i) => i.id === row)) {
    die(`the seeded row is not in the room's queue, so the room pane will draw the
empty state and there is nothing to compare. That is a fixture problem.`);
  }

  const browser = await chromium.launch();
  try {
    const drawn = async (path) => {
      const page = await browser.newPage({ viewport: { width: 1500, height: 950 } });
      const crashes = [];
      page.on("pageerror", (err) => crashes.push(String(err)));
      await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
      await page.goto(`${base}${path}`, { timeout: 30_000 }).catch(() => {});
      const line = page.locator("[data-merge-tipfrom]");
      await line
        .first()
        .waitFor({ state: "visible", timeout: 20_000 })
        .catch(() => {});
      const got =
        (await line.count()) === 0
          ? { value: null, words: "" }
          : {
              value: await line.first().getAttribute("data-merge-tipfrom"),
              words: ((await line.first().textContent()) ?? "").replace(/\s+/g, " ").trim(),
            };
      await page.close();
      return { ...got, crashes, path };
    };

    // The room's copy is behind the merges pane of the room the row was filed
    // in - /chat/<room>/merges, which is a path since the pane became one.
    const inRoom = await drawn(`/chat/${room}/merges`);
    const onBoard = await drawn("/todos/merge");

    for (const got of [inRoom, onBoard]) {
      if (got.crashes.length > 0) die(`${got.path} threw: ${got.crashes.join("; ")}`);
      if (!got.value) {
        die(`${got.path} draws no provenance at all.
The pane says what the verdicts were judged against; where that tip came from is
half of what makes the answer readable.`);
      }
    }

    const wantRoom = roomQ.body.tip_from;
    const wantBoard = board.body.tip_from;
    if (inRoom.value !== wantRoom) {
      die(`the room's pane says the tip is "${inRoom.value}" and the node told it "${wantRoom}".
  drawn: ${JSON.stringify(inRoom.words)}
This pane passed a literal for as long as it has existed. A hedge printed where
it is not true is worse than no hedge.`);
    }
    if (onBoard.value !== wantBoard) {
      die(`the board's pane says "${onBoard.value}" and the node told it "${wantBoard}".
  drawn: ${JSON.stringify(onBoard.words)}`);
    }

    // AND THE SENTENCE THAT HANGS OFF IT, because the value being right while
    // the prose ignores it would be the same defect one layer along.
    const hedge = "the commit this node was built from";
    for (const got of [inRoom, onBoard]) {
      const said = got.words.includes(hedge);
      const should = got.value === "deployed";
      if (said !== should) {
        die(`${got.path} draws tipfrom=${got.value} and ${said ? "says" : "omits"} the built-from caveat.
It belongs to "deployed" alone - a tip that LANDED is fresher than the build and
answers a different question.
  drawn: ${JSON.stringify(got.words)}`);
      }
    }

    console.log(`both panes agree with the node: room=${inRoom.value}, board=${onBoard.value}`);
  } finally {
    await browser.close();
  }
} finally {
  if (row) {
    await call(`/api/artifact/${row}/status`, {
      method: "POST",
      body: JSON.stringify({ status: "abandoned" }),
    });
  }
}
