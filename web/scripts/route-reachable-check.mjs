// EVERY PAGE IS IN THE NAV, OR SOMEBODY DECIDED IT IS NOT.
//
// /vms shipped with a page, a panel and a shell relay, and the only way in was
// a link on the home page. Not nothing - but every other page in this console
// is in the left rail, so the rail is where a person looks, and after an
// evening of using the panel the operator asked why it was not there.
//
// THE FIRST VERSION OF THIS CHECK ASKED THE WRONG QUESTION. It asked whether
// anything in the console named the path, decided /vms was an orphan, and said
// so in its own comment. It was not: Home.tsx has linked to it all along. The
// check passed with the nav link removed, which is a check that cannot fail for
// the case it was written for. The premise was wrong, not the code.
//
// So the rule is the one that actually distinguishes: A STATIC ROUTE IS IN THE
// NAV, or it is named below with a reason. No default, for routeNeeds' reason -
// absent is a mistake and an entry in the list is a decision somebody made and
// can be argued with.

import { readFileSync } from "node:fs";

const die = (why) => {
  console.error(why);
  process.exit(1);
};

// NOT IN THE RAIL, AND WHY. Each of these is somebody's decision, not an
// oversight, and each says how a person actually gets there.
const notInTheRail = {
  "/login": "you are sent here when signed out; the credential banner and the token bar link it",
  "/todos/merge": "the second tab on /todos, which is the page that owns the queue",
};

const table = "src/App.tsx";
const routes = [...readFileSync(table, "utf8").matchAll(/path="([^"]+)"/g)].map((m) => m[1]);
if (routes.length === 0) {
  die(`${table} declares no routes, so this check is reading the wrong file and would pass
against a console with no pages at all`);
}

// Parameterised routes are not walked: /chat/:room cannot be a rail entry, and
// the pages that own them link with template strings. Named rather than
// silently dropped.
const walkable = routes.filter((r) => !r.includes(":") && r !== "*");
const skipped = routes.filter((r) => r.includes(":") || r === "*");

const rail = "src/components/Shell.tsx";
const inRail = new Set(
  [...readFileSync(rail, "utf8").matchAll(/<NavLink\s+to="([^"]+)"/g)].map((m) => m[1]),
);
if (inRail.size === 0) {
  die(`${rail} draws no NavLink at all, so this check is reading the wrong file and every
route below would look like a decision nobody made`);
}

const missing = walkable.filter((r) => !inRail.has(r) && !(r in notInTheRail));
if (missing.length > 0) {
  die(`these pages are not in the nav and nobody has said why: ${missing.join(", ")}

A page outside the rail is found by people who already know it is there. Put it
in Shell.tsx's nav, or add it to notInTheRail in this check with the way a
person actually reaches it - which is a decision somebody can then argue with.`);
}

// AND THE EXCEPTIONS ARE STILL PAGES. A stale entry here would quietly excuse a
// route that no longer exists, and the next real orphan could be added beside
// it without anybody noticing the list had stopped describing the console.
const stale = Object.keys(notInTheRail).filter((r) => !routes.includes(r));
if (stale.length > 0) {
  die(`notInTheRail names ${stale.join(", ")}, which ${table} no longer routes. An excuse list
that outlives its routes stops being read.`);
}

console.log(
  `${walkable.length} static route(s): ${walkable.length - Object.keys(notInTheRail).length} in the rail, ${Object.keys(notInTheRail).length} deliberately not (${Object.keys(notInTheRail).join(", ")}); not walked: ${skipped.join(", ")}`,
);
