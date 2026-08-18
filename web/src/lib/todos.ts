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

/**
 * The kinds of memory the node treats as WORK: the ones with a status, a
 * dependency edge and a note door. It mirrors store.WorkKinds, which is the
 * list the node refuses everything else against.
 *
 * Mirrored rather than asked for, on the same terms as TODO_KINDS below: the
 * set is closed and changes about once a year, and the alternative is a fetch
 * before the page can decide whether to draw a box. What the mirror must never
 * do is decide anything the node then contradicts, which is why it is used only
 * to choose whether to OFFER the note box - a row this list gets wrong shows a
 * box whose write the node refuses in words, rather than a refusal invented
 * over here.
 */
const WORK_KINDS = ["todo", "feature", "handoff", "merge", "work"];

/**
 * isQueueItem says a row is one of those: a memory of a work kind. It is what
 * the notes section is drawn for, because a report or a proposal has no note
 * door and offering one on it would be a control the node answers with a 404.
 */
export function isQueueItem(artifact: Artifact): boolean {
  return artifact.type === "memory" && WORK_KINDS.includes((artifact.kind ?? "").trim());
}

/** isTodoDone says a piece of work is finished, by the same ranking the list sorts by. */
export function isTodoDone(todo: Artifact): boolean {
  return todoRank(todo.status) === RANK.done;
}

/**
 * Whether the panel draws the finished ones, remembered across reloads.
 *
 * #general holds 26 todos and 16 of them are done, in a panel beside a
 * conversation with about fifteen visible rows - so the finished work pushes the
 * live work off the bottom, and a panel that exists to answer "what is this room
 * doing" mostly answers "what has this room finished".
 *
 * DEFAULT HIDDEN. The other choice keeps today's behaviour and fixes nothing for
 * anybody who never finds the checkbox, which is most people; a default nobody
 * discovers is a feature nobody has. What keeps hiding honest rather than lossy
 * is that the number hidden is on screen the whole time it is hiding anything,
 * beside the box that did it - a panel showing four rows and no hint that
 * sixteen are behind it is how somebody concludes a todo does not exist.
 *
 * ONE setting for every room rather than one key per room. This is a habit about
 * reading a panel, not a fact about a room: somebody who wants finished work out
 * of the way in #general wants it out of the way in #build too, and a per-room
 * key means ticking the same box once per room and finding the next one buried
 * again. It also keeps the panel honest across a room switch - the component
 * stays mounted when the room changes, so a per-room value would have to be
 * re-read on that change or the new room would be drawn under the old room's
 * setting.
 */
const HIDE_DONE_KEY = "flowy.todos.hideDone";

export function hideDonePreference(): boolean {
  try {
    // Anything that is not an explicit "false" is hidden, so a browser with
    // nothing stored yet gets the default.
    return localStorage.getItem(HIDE_DONE_KEY) !== "false";
  } catch {
    return true;
  }
}

export function setHideDonePreference(hide: boolean) {
  try {
    localStorage.setItem(HIDE_DONE_KEY, hide ? "true" : "false");
  } catch {
    // Storage switched off. The setting still holds for the length of the page,
    // the same way the token does.
  }
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
  if (!name || NOBODY.has(name.toLowerCase())) return "";
  return name;
}

/**
 * The words that mean nobody is carrying this. They all collapse to the empty
 * owner, so the panel says ONE word for one state.
 *
 * Raised as a todo through the panel itself: 'todo list has "unowned" and
 * "unassigned" - looks identical. triage and fix'. Both were there because the
 * render falls back to "unowned" while several bodies had been written with
 * "OWNER: unassigned" - two words for one state, which reads as two states and
 * makes a reader look for a distinction that does not exist.
 *
 * Normalising here rather than rewriting those bodies is the durable half: the
 * next person to write "OWNER: none" or "OWNER: TBD" gets the same single word,
 * without anybody having to know the convention.
 */
const NOBODY = new Set(["?", "-", "none", "nobody", "tbd", "unassigned", "unowned", "n/a"]);

/**
 * Who is carrying a todo: the `assignee` field if the item has one, and the
 * body's OWNER line if it does not.
 *
 * The order is the compatibility, and it is the same order the node and the
 * terminal client read these in. The whole queue predates the field, and those
 * items still read the way they always did. But a field that is THERE wins even
 * when it is empty - somebody who unassigned a todo through this panel said so
 * out loud, and falling through to the OWNER line still sitting in the body
 * would quietly put the old name back on the next render.
 *
 * So the presence of the key is the question, not its truthiness: `""` is a
 * value here and `undefined` is a silence, which is why this cannot go through
 * fieldOf below.
 */
export function todoAssignee(artifact: Artifact): string {
  const fields = artifact.fields;
  if (fields && typeof fields === "object") {
    const named = (fields as Record<string, unknown>).assignee;
    if (typeof named === "string") return named.trim();
  }
  return todoOwner(artifact.body ?? "");
}

/**
 * WHO RAISED IT: who the work came from, beside who is carrying it.
 *
 * These are two facts and the row draws both, because a row that draws one of
 * them is ambiguous in exactly the way this field exists to fix. Four agents
 * share this board and one of them files rows out of conversations the operator
 * had - `owner_user` is then the agent, which is true and is not where the work
 * came from, and nothing on the row said so.
 *
 * It is NOT `owner_user` and it is not a fallback for it. owner_user is the seat
 * whose token wrote the row - the signing author, and the answer to a different
 * question - so a todo with no raiser is drawn with none rather than with the
 * author's id standing in for one. Most of this queue was written before the
 * field and says nothing here, which is the truth about it.
 *
 * Read off `fields`, like the room and the message beside it: the node also
 * derives it onto the row at read time, and one place to dig is what keeps the
 * two clients agreeing about what an absent one means.
 */
export function todoRaiser(artifact: Artifact): string {
  return fieldOf(artifact, "raiser").trim();
}

/**
 * THE KIND OF WORK, which the node calls `category`.
 *
 * Two different things are labelled on a todo and they are not variants of each
 * other. TAGS are free labels: any number of them, any word, nobody's schema,
 * and they are searched with the title and the body. THIS is one value out of a
 * closed set the node REFUSES anything outside of - which is the only reason it
 * can be counted or routed on. A filter over tags answers "whatever people
 * typed"; a filter over this answers "the bugs".
 *
 * It is called "Kind" here and `category` on the wire, and the difference is not
 * an accident. A todo already IS kind=todo one level up, so the node cannot use
 * that word twice without the two meanings being told apart by context - which
 * is exactly the confusion this console should not pass on to the person reading
 * it. They see one word, in the place a person expects it; the wire keeps a word
 * that can only mean one thing.
 *
 * Empty is a todo nobody has classified. That is legal, it is most of this
 * queue, and it is drawn as such rather than being guessed at from the title.
 */
export const TODO_KINDS = ["bug", "feature", "chore", "question"] as const;

export type TodoKind = (typeof TODO_KINDS)[number];

/** What the node calls it, off fields. Unknown words read as unclassified: the
 * node refuses them on the way in, so one here came from somewhere this console
 * cannot fix and is not a word to paint as though it were in the set. */
export function todoKind(artifact: Artifact): string {
  const named = fieldOf(artifact, "category").trim().toLowerCase();
  return (TODO_KINDS as readonly string[]).includes(named) ? named : "";
}

/**
 * The colour a kind is drawn in, on the same terms as statusStyle: the word
 * stays inside the badge, so colour is the second signal and never the only
 * one, and the colours are picked for what each one means.
 *
 * RED for a bug, because it is the one an operator scans for. BLUE for a
 * feature, GREY-GREEN for a chore - work that has to happen and is nobody's
 * news - and VIOLET for a question, which is the one that is not yet a piece of
 * work at all.
 */
export function kindStyle(kind: string): { color: string; backgroundColor: string } {
  const colour = KIND_COLOUR[kind] ?? "#8b93a7";
  return { color: colour, backgroundColor: `color-mix(in srgb, ${colour} 18%, transparent)` };
}

const KIND_COLOUR: Record<string, string> = {
  bug: "#d1626b", // red - something is broken
  feature: "#5b8dd6", // blue - something new
  chore: "#7d9c8a", // quiet green - has to happen, is not news
  question: "#a481d4", // violet - not yet a piece of work
};

/**
 * The free labels on an item: the author's tags and the node's own, as one
 * list, deduplicated and in a stable order.
 *
 * Both columns are shown because both are labels a person put there - user_tags
 * is what somebody typed on their own item and tags is what the surfaces that
 * write items fill in - and a console that drew one of them would be hiding half
 * the labels behind a distinction nobody reading the queue is making.
 */
export function todoTags(artifact: Artifact): string[] {
  const all = [...(artifact.tags ?? []), ...(artifact.user_tags ?? [])];
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of all) {
    const tag = raw.trim();
    if (!tag || seen.has(tag)) continue;
    seen.add(tag);
    out.push(tag);
  }
  return out.sort((a, b) => a.localeCompare(b));
}

/** Every tag in a queue, for the filter to offer. It is built from the rows on
 * the page rather than from a list somewhere: tags have no schema, so what
 * exists is whatever has been written. */
export function tagsIn(list: Artifact[]): string[] {
  const seen = new Set<string>();
  for (const todo of list) {
    for (const tag of todoTags(todo)) seen.add(tag);
  }
  return [...seen].sort((a, b) => a.localeCompare(b));
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
