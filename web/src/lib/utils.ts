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

/** clock renders an event's timestamp as wall time, to the second. */
export function clock(iso: string) {
  const at = new Date(iso);
  return Number.isNaN(at.getTime()) ? "" : at.toLocaleTimeString();
}
