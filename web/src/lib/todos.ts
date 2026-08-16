/**
 * What a todo is, as the two surfaces that draw one both need it.
 *
 * A todo is an artifact of type memory and kind todo, and everything else about
 * it is convention the queue has carried since before there was a view: the
 * status words, the reading order, and the OWNER line at the top of the body.
 * The global page and the room panel have to agree about all three - two ideas
 * of what "active" sorts above, or two ways of finding the owner, is a queue
 * that reads differently depending on where you look at it from.
 */

import type { Artifact } from "@/lib/api";

/**
 * The order statuses are shown in. Active first, then open, then done: a list
 * that buries what is in flight under what is finished answers no question
 * anybody opened it to ask. Everything unrecognised sorts with the open ones -
 * an unknown status is work nobody has said is done.
 */
const RANK: Record<string, number> = { active: 0, todo: 1, done: 2 };

export function todoRank(status: string): number {
  return RANK[status.trim().toLowerCase()] ?? RANK.todo;
}

/** sortTodos puts a queue in reading order, keeping the node's order within a status. */
export function sortTodos(list: Artifact[]): Artifact[] {
  return [...list].sort((a, b) => todoRank(a.status) - todoRank(b.status));
}

/** countTodos is the header line: how many are in flight, waiting, and finished. */
export function countTodos(list: Artifact[]): { active: number; open: number; done: number } {
  const counts = { active: 0, open: 0, done: 0 };
  for (const t of list) {
    if (todoRank(t.status) === RANK.active) counts.active += 1;
    else if (todoRank(t.status) === RANK.done) counts.done += 1;
    else counts.open += 1;
  }
  return counts;
}

/**
 * The owner is the first line of the body, not the artifact's owner_user.
 *
 * owner_user is whoever wrote the row, which for most of this queue is the one
 * operator principal that filed all of it - it says "operator" and answers
 * nothing. The body carries `OWNER: <name>` as its first line, which is the
 * claim somebody actually made about who is doing the work, and a literal `?`
 * there means nobody has taken it.
 */
export function todoOwner(body: string): string {
  const line = body.split("\n").find((l) => l.startsWith("OWNER:"));
  const name = line?.slice("OWNER:".length).trim();
  return name && name !== "?" ? name : "";
}

/** The body without the OWNER line, which is rendered as its own column. */
export function todoDetail(body: string): string {
  return body
    .split("\n")
    .filter((l) => !l.startsWith("OWNER:"))
    .join("\n")
    .trim();
}

/**
 * Where a todo was raised, off fields. The node keeps `room` and `message`
 * there - fields is jsonb of whatever the type puts in it, so it is narrowed
 * here at the use site rather than typed on Artifact for every type at once.
 */
function fieldOf(artifact: Artifact, key: string): string {
  const fields = artifact.fields;
  if (!fields || typeof fields !== "object") return "";
  const value = (fields as Record<string, unknown>)[key];
  return typeof value === "string" ? value : "";
}

export function todoRoom(artifact: Artifact): string {
  return fieldOf(artifact, "room");
}

/** The message the todo was raised out of, when it was raised in a room. */
export function todoMessage(artifact: Artifact): string {
  return fieldOf(artifact, "message");
}

/**
 * The colour a status is drawn in. Asked for directly: "I wanted colors for
 * Active Done and Todo".
 *
 * Three states, three jobs, and the colours are picked for what each one means
 * rather than for variety. AMBER for active, because in-flight work is the
 * thing you want your eye to land on when you open the panel. GREEN for done,
 * which is the one convention here strong enough to be worth obeying. GREY for
 * todo, deliberately quiet: a queue is mostly waiting, and if waiting shouts
 * then nothing does.
 *
 * The status word stays inside the badge. Colour on its own would leave anybody
 * who cannot separate amber from green reading a queue with no states in it,
 * and this is a panel about what is happening rather than a decoration.
 *
 * Explicit colours rather than theme tokens, for the same reason speaker
 * colours are: a status is a fact about the work, not a role in the interface,
 * and "primary" and "destructive" already mean other things here.
 */
export function statusStyle(status: string): { color: string; backgroundColor: string } {
  const colour = STATUS_COLOUR[todoRank(status)] ?? STATUS_COLOUR[RANK.todo];
  return { color: colour, backgroundColor: `color-mix(in srgb, ${colour} 18%, transparent)` };
}

const STATUS_COLOUR: Record<number, string> = {
  [RANK.active]: "#e0a03f", // amber - in flight, and the first thing to see
  [RANK.todo]: "#8b93a7", // grey - waiting, and quiet on purpose
  [RANK.done]: "#4fae7a", // green - finished
};
