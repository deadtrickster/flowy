import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

/** cn merges tailwind classes, last one winning on a conflict. */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

/** shortId is the tail of a ULID, which is the part that differs on a screen. */
export function shortId(id: string, tail = 6) {
  return id.length <= tail ? id : id.slice(-tail);
}

/**
 * speaker is who to draw beside a message: the name the node recorded when it
 * was said, and the tail of the actor id when there is none.
 *
 * The fallback carries the whole of the log written before names were stamped -
 * a room of ids is what this exists to fix, but a blank where a name would go
 * is worse than the id it replaced.
 *
 * It takes the shape it reads rather than importing FlowyEvent, and it lives
 * here rather than beside that type, because api.ts is imported on its own as a
 * data url by scripts/api-error-check.mjs - a module with an "@/" import in it
 * cannot be resolved from there, and the gate says so.
 */
export function speaker(event: { actor: string; meta?: { actor_name?: string } }) {
  return event.meta?.actor_name || shortId(event.actor, 8);
}

/**
 * clock renders a timestamp as wall time - AND ITS DATE WHEN IT IS NOT TODAY.
 *
 * 01M10Y3JBD, the operator: "all time labels must show the date 'if not today'
 * chats - memoryies -todos". A bare "23:14:02" is unambiguous for about a day
 * and then silently starts lying by omission: a row from Tuesday and a row from
 * ten minutes ago render identically, and on a board that keeps months of
 * history most of them are not from today. This console had exactly one reader
 * of that stamp - the sidebar clock - and every list inherited it.
 *
 * WHAT IS ADDED IS ONLY WHAT DISAMBIGUATES. Today is the time alone, because
 * stamping today's date on every line of a live room is noise that pushes the
 * message off a phone. A different day gains the day, a different year gains
 * the year - each one appears at the point where leaving it out would make two
 * different instants read the same.
 *
 * The comparison is on the LOCAL CALENDAR DAY, not on elapsed hours: 00:05 and
 * 23:55 are twenty minutes apart and are different days, and a reader glancing
 * at a timestamp is asking which day it says, not how long ago it was.
 */
export function clock(iso: string) {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return "";
  const time = at.toLocaleTimeString();
  const now = new Date();
  if (
    at.getFullYear() === now.getFullYear() &&
    at.getMonth() === now.getMonth() &&
    at.getDate() === now.getDate()
  ) {
    return time;
  }
  const sameYear = at.getFullYear() === now.getFullYear();
  const date = at.toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    ...(sameYear ? {} : { year: "numeric" }),
  });
  return `${date} ${time}`;
}
