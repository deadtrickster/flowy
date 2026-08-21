import { useEffect, useState } from "react";

import { api } from "@/lib/api";

/**
 * The name a composer is part-way through typing, and the roster it can offer.
 *
 * THE OPERATOR: "no suggestions when I type @".
 *
 * This is more than convenience here, and the row (01M0GGSMBD) says why: a
 * mention only becomes a mention if the name RESOLVES AT WRITE TIME. mentions.go
 * parses the body when the message is said and records the pairs it resolved in
 * meta.mentions; a name that resolves to nobody is drawn as prose and addresses
 * nobody. So a typo is not a cosmetic miss - it is a message that looks
 * addressed to its author and reaches no one.
 *
 * OFFERED FROM /api/presence, which is the roster the NODE resolves against -
 * chat.go:429 routes both @mentions and `--to` through PrincipalsNamed for
 * exactly this reason, so one list means the composer and the door cannot
 * disagree about who a name is. A second, cleverer source here would be a second
 * answer to "who is alice", and that is the disagreement worth not inventing.
 */
export interface AtName {
  /** The roster, as the node knows it. Empty while unread or on failure. */
  names: { name: string; kind: string }[];
}

/**
 * How often the roster is refilled. Slow on purpose: a fleet's membership
 * changes when somebody mints an agent, not between keystrokes, and a composer
 * that fetched per character would ask the node a hundred times a message.
 */
const REFRESH_MS = 60_000;

/** useRoster keeps the mentionable names current for a composer. */
export function useRoster(token: string | null): AtName {
  const [names, setNames] = useState<{ name: string; kind: string }[]>([]);

  useEffect(() => {
    if (!token) {
      setNames([]);
      return;
    }
    let stopped = false;
    const load = async () => {
      // SWALLOWED, and the composer keeps working without it. A suggestion list
      // is help; typing the name yourself still resolves at the node. Failing
      // loudly here would take the message box down for a garnish.
      const roster = await api
        .presence()
        .then((p) => (p.members ?? []).map((m) => ({ name: m.name, kind: m.kind })))
        .catch(() => null);
      if (stopped || roster === null) return;
      setNames(roster);
    };
    void load();
    const every = setInterval(() => void load(), REFRESH_MS);
    return () => {
      stopped = true;
      clearInterval(every);
    };
  }, [token]);

  return { names };
}

/**
 * The @word the caret is inside, or null.
 *
 * THE SAME TWO CLAUSES mentions.go USES, and it has to be: the composer must
 * not offer to complete something the node would not treat as a mention. An @
 * that follows a name byte is an email address, so `foo@bar` offers nothing -
 * see splitBody in lib/mentions.ts, which mirrors the same rule for drawing.
 *
 * Returns the fragment typed so far (without the @) and the offsets to replace,
 * so a caller can splice the chosen name in without re-finding it - the string
 * has moved on by then if the user kept typing.
 */
export function atFragment(
  body: string,
  caret: number,
): { fragment: string; from: number; to: number } | null {
  // Walk back from the caret over name bytes to find the @.
  let i = caret;
  while (i > 0 && /[A-Za-z0-9_.-]/.test(body[i - 1] ?? "")) i--;
  if (i === 0 || body[i - 1] !== "@") return null;
  const at = i - 1;
  // An @ that follows a name byte is an address, not a mention.
  if (at > 0 && /[A-Za-z0-9_.-]/.test(body[at - 1] ?? "")) return null;
  return { fragment: body.slice(i, caret), from: at, to: caret };
}

/**
 * The names worth offering for a fragment, best first.
 *
 * PREFIX BEFORE SUBSTRING, because a person typing `cl` means claude-host far
 * more often than they mean anything that merely contains "cl". An empty
 * fragment - a bare `@` - offers everybody, which is the case that answers "who
 * is here" and is the whole reason the operator asked.
 */
export function matchNames(
  names: { name: string; kind: string }[],
  fragment: string,
): { name: string; kind: string }[] {
  const want = fragment.toLowerCase();
  if (!want) return names;
  const prefix = names.filter((n) => n.name.toLowerCase().startsWith(want));
  const rest = names.filter(
    (n) => !n.name.toLowerCase().startsWith(want) && n.name.toLowerCase().includes(want),
  );
  return [...prefix, ...rest];
}
