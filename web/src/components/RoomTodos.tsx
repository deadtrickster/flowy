import { type FormEvent, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { Artifact, FlowyEvent } from "@/lib/api";
import { speakerStyle } from "@/lib/speakercolour";
import {
  TODO_KINDS,
  countTodos,
  hideDonePreference,
  isTodoDone,
  setHideDonePreference,
  sortTodos,
  statusStyle,
  todoAssignee,
  todoRaiser,
} from "@/lib/todos";
import { shortId } from "@/lib/utils";

interface Props {
  room: string;
  todos: Artifact[];
  /** raiseFrom is the message the room has selected, if any: what a todo raised
   * now would be raised out of. */
  raiseFrom: FlowyEvent | null;
  disabled: boolean;
  error: string | null;
  onRaise: (title: string, category?: string) => Promise<void>;
  /** onAssign says who is carrying one. An empty name says nobody is. It has to
   * land on the node and come back from it - see the assignee cell below. */
  onAssign: (id: string, assignee: string) => Promise<void>;
}

/**
 * The room's todos, beside the room's messages.
 *
 * The queue was readable from everywhere except the place it is agreed. Two
 * agents and a person settle in #build what has to happen, and until this panel
 * the settling lived in the messages - to find out what the room had decided
 * you read the room back. So the plan sits next to the conversation that
 * produced it, and it is filled by the same long poll the messages are: a todo
 * somebody else raises appears here without a reload, because that is the case
 * the panel exists for.
 *
 * A row is status, assignee and title, because a panel beside a conversation is
 * narrow and those are the three things somebody glances at it for. The rest of
 * the item is one click away in the artifact view.
 *
 * The assignee cell is also the control. Somebody in the room takes a line of
 * the plan while the conversation that produced it is still on screen, which is
 * the whole reason the panel is here rather than a page away - and a queue you
 * can read and not answer is the surface this replaced.
 */
export function RoomTodos({ room, todos, raiseFrom, disabled, error, onRaise, onAssign }: Props) {
  const [title, setTitle] = useState("");
  /**
   * What KIND of work this is, chosen when it is raised.
   *
   * The node has taken a category on this door since the ontology landed and
   * the panel never sent one, so everything raised from a room arrived
   * unclassified and somebody had to go back and file it afterwards - which
   * nobody does. Empty is a real choice and stays the default: most of the
   * queue is unclassified and that is legal.
   */
  const [category, setCategory] = useState("");
  const [raising, setRaising] = useState(false);
  const [failed, setFailed] = useState<string | null>(null);
  // Which row is being edited, and what has been typed into it. The name being
  // typed is NOT the row's assignee: what a person has half-written is theirs
  // until they commit it, and the cell keeps showing the node's answer until
  // the node has a new one.
  const [editing, setEditing] = useState<string | null>(null);
  const [named, setNamed] = useState("");
  const [assigning, setAssigning] = useState(false);
  /**
   * Whether the finished ones are drawn. Read from storage ONCE, at mount, and
   * owned by this component from then on.
   *
   * Deliberately not derived from `todos`, which is the whole of why the room's
   * poll cannot wipe it. The panel is refilled from the node every time the poll
   * comes back - that is what silently reverted the assignee an hour ago, where
   * an older read landed after a newer one and repainted the cell - but this is
   * not the node's fact about the queue, it is this tab's fact about how the
   * queue is being read. No answer from the node can be a stale copy of
   * something the node was never asked. The list arriving again just gets
   * filtered through it again.
   */
  const [hideDone, setHideDone] = useState(hideDonePreference);

  const raise = async (event: FormEvent) => {
    event.preventDefault();
    const said = title.trim();
    if (!said || raising) return;
    setRaising(true);
    setFailed(null);
    try {
      await onRaise(said, category);
      setTitle("");
    } catch (err) {
      setFailed(err instanceof Error ? err.message : String(err));
    } finally {
      setRaising(false);
    }
  };

  const edit = (todo: Artifact) => {
    setEditing(todo.id);
    setNamed(todoAssignee(todo));
    setFailed(null);
  };

  /**
   * Committing writes to the NODE and nothing else: onAssign posts and reloads
   * the list, so what the cell says next is what the store holds.
   *
   * Nothing optimistic here on purpose. An assignee kept in this component
   * looks perfect until the room's long poll comes back with the node's list
   * and replaces it, which is the bug this panel would have shipped with -
   * somebody takes a piece of work, somebody else says something in the room,
   * and the name silently reverts.
   */
  const commit = async (event: FormEvent) => {
    event.preventDefault();
    if (!editing || assigning) return;
    setAssigning(true);
    setFailed(null);
    try {
      await onAssign(editing, named.trim());
      setEditing(null);
    } catch (err) {
      setFailed(err instanceof Error ? err.message : String(err));
    } finally {
      setAssigning(false);
    }
  };

  /** Ticking it is both the render and the preference: the next reload agrees. */
  const setHiding = (hide: boolean) => {
    setHideDone(hide);
    setHideDonePreference(hide);
  };

  const counts = countTodos(todos);
  const drawn = hideDone ? todos.filter((todo) => !isTodoDone(todo)) : todos;
  const withheld = todos.length - drawn.length;

  return (
    // flex-1 because this is a whole pane now rather than the top half of one:
    // it used to sit above the thread in the room's column and take its content
    // height, and under a tab bar that leaves the rest of the column blank.
    <section className="flex min-h-0 flex-1 flex-col border-border border-b">
      {/* Two lines rather than one: the panel is 26rem wide, and the counts plus
          the control plus what is being withheld do not fit beside the heading
          at a readable size. The line the numbers are already on is where the
          control belongs, so it goes directly under them. */}
      <header className="flex flex-col gap-1 border-border border-b px-4 py-3">
        <div className="flex items-center gap-2">
          <h2 className="font-semibold text-sm">todos</h2>
          <span className="font-mono text-muted-foreground text-xs">#{room}</span>
          {/* The counts are of the WHOLE queue, hiding or not. They are the one
              line that never moves when the box is ticked, which is what makes
              the filter a view rather than a deletion. */}
          <span className="ml-auto text-muted-foreground text-xs">
            {counts.active} active, {counts.open} open, {counts.done} done
          </span>
        </div>
        <div className="flex items-center gap-2">
          {/* A checkbox and two words, not a form: this sits beside a
              conversation, and the moment it needs a heading or a menu it is
              competing with the room for the space. */}
          <label className="flex cursor-pointer items-center gap-1.5 text-muted-foreground text-xs">
            <input
              type="checkbox"
              data-hide-done=""
              className="size-3.5 cursor-pointer accent-current"
              checked={hideDone}
              onChange={(event) => setHiding(event.target.checked)}
            />
            hide done
          </label>
          {/* Said whenever anything is being withheld, and never rounded or
              dropped. A panel showing four rows with no sign that sixteen are
              behind it lies about the size of the queue, and somebody reading it
              concludes a todo does not exist. */}
          {withheld > 0 ? (
            <span data-hidden-count="" className="ml-auto text-muted-foreground text-xs">
              {withheld} done hidden
            </span>
          ) : null}
        </div>
      </header>

      <div className="min-h-0 flex-1 overflow-auto">
        {error ? <div className="px-4 py-2 text-destructive text-xs">{error}</div> : null}
        {!error && todos.length === 0 ? (
          // Said out loud, because an empty panel beside a busy room reads as a
          // broken view rather than as a room that has not written anything down.
          <div className="px-4 py-2 text-muted-foreground text-xs">
            nothing raised in #{room} yet
          </div>
        ) : null}
        {!error && todos.length > 0 && drawn.length === 0 ? (
          // The other empty panel, and a different fact: this room has written
          // things down and finished all of them. Without this line the filter
          // produces a view indistinguishable from a room that raised nothing,
          // which is the reading the count beside the box exists to prevent.
          <div className="px-4 py-2 text-muted-foreground text-xs">
            everything raised in #{room} is done
          </div>
        ) : null}
        <ul className="flex flex-col">
          {sortTodos(drawn).map((todo) => {
            const owner = todoAssignee(todo);
            // Where the work came from, when the row says. A todo raised out of
            // a message carries the speaker of that message without anybody
            // typing it, which is the case this panel produces most of: the ask
            // is four messages up the column beside it, and the row is what is
            // still here tomorrow.
            const raiser = todoRaiser(todo);
            return (
              <li
                key={todo.id}
                className="flex items-baseline gap-2 border-border/60 border-b px-4 py-2 text-xs"
              >
                {/*
                  Amber in flight, grey waiting, green finished - the three
                  states this panel exists to show, told apart at a glance
                  instead of by reading every row. The word stays inside the
                  badge: colour alone would leave a queue with no states in it
                  for anybody who cannot separate amber from green.
                */}
                <Badge variant="secondary" style={statusStyle(todo.status)}>
                  {todo.status || "todo"}
                </Badge>
                {/*
                  The assignee in their own colour, the same one they speak in
                  just above - so "who is carrying this" and "who said that"
                  are the same glance rather than two lookups. Unowned stays
                  grey, because nobody is not a person.

                  And it is the control, in place rather than behind a menu: the
                  cell that answers "who has this" is the one you click to say.
                  Empty means nobody, which is how the work is put back down.
                */}
                {editing === todo.id ? (
                  <form autoComplete="off" onSubmit={commit}>
                    <Input
                      name="todo-assignee"
                      className="h-6 w-24 px-1 text-xs"
                      value={named}
                      // The input replaced the cell that was just clicked, so
                      // the caret belongs in it: the click was the request to
                      // type here, and it is never on screen unmounted.
                      autoFocus
                      disabled={assigning}
                      onChange={(event) => setNamed(event.target.value)}
                      onKeyDown={(event) => {
                        if (event.key === "Escape") setEditing(null);
                      }}
                      placeholder="nobody"
                      aria-label={`who is carrying ${todo.title || todo.id}`}
                    />
                  </form>
                ) : (
                  <button
                    type="button"
                    data-assignee=""
                    disabled={disabled}
                    onClick={() => edit(todo)}
                    className="shrink-0 rounded px-1 text-muted-foreground hover:underline disabled:no-underline"
                    style={owner ? speakerStyle(owner) : undefined}
                    title="say who is carrying this"
                    aria-label={`assignee of ${todo.title || todo.id}`}
                  >
                    {owner || "unowned"}
                  </button>
                )}
                {/* Raised by X, carried by Y. The cell to the left is who is
                    carrying it and is the control; this is who it came from and
                    is not - nobody hands the origin of a piece of work to
                    somebody else. Drawn only when the row says one, because
                    every todo raised before this field says nothing here. */}
                {raiser ? (
                  <span
                    data-todo-raiser={raiser}
                    className="shrink-0 text-muted-foreground"
                    title="who this work came from"
                  >
                    from <span style={speakerStyle(raiser)}>{raiser}</span>
                  </span>
                ) : null}
                <span className="min-w-0 flex-1 break-words">{todo.title || todo.id}</span>
              </li>
            );
          })}
        </ul>
      </div>

      {/* autoComplete="off" on the form as well as on the field, because the
          browser reads the form to decide what the whole group is for and the
          field to decide what one box is for, and it is the group that looked
          like a sign-in here: one unnamed text box and a submit button. The
          message box beside this panel never had the problem because what you
          type into it is a textarea, which browsers do not offer a password or
          a card over. */}
      <form
        className="flex flex-col gap-1 border-border border-t p-3"
        autoComplete="off"
        onSubmit={raise}
      >
        <div className="flex items-center gap-2">
          <Input
            name="todo-title"
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            placeholder={`raise a todo in #${room}`}
            disabled={disabled || raising}
            aria-label={`raise a todo in ${room}`}
          />
          {/* The kind, beside the title rather than after the fact. The closed
              set is the node's - anything outside it is refused there - so this
              offers exactly those four and "unclassified", which is legal and
              is what most of the queue is. */}
          <select
            data-raise-category=""
            aria-label="kind of work"
            className="rounded border border-border bg-background px-2 py-1 text-xs"
            value={category}
            onChange={(event) => setCategory(event.target.value)}
            disabled={disabled || raising}
          >
            <option value="">unclassified</option>
            {TODO_KINDS.map((k) => (
              <option key={k} value={k}>
                {k}
              </option>
            ))}
          </select>
          <Button type="submit" size="sm" disabled={disabled || raising || !title.trim()}>
            raise
          </Button>
        </div>
        {/* Which message it will be raised out of. The link is the point of
            raising it here rather than filing it somewhere else, so the panel
            says which message it is about to keep. */}
        <span className="text-muted-foreground text-xs">
          {raiseFrom
            ? `out of message #${shortId(raiseFrom.id)}`
            : "select a message to raise it out of that message"}
        </span>
        {failed ? <span className="text-destructive text-xs">{failed}</span> : null}
      </form>
    </section>
  );
}
