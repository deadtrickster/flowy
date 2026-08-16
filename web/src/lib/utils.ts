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

/** clock renders an event's timestamp as wall time, to the second. */
export function clock(iso: string) {
  const at = new Date(iso);
  return Number.isNaN(at.getTime()) ? "" : at.toLocaleTimeString();
}
