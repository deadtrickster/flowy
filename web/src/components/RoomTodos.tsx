import { Paperclip, X } from "lucide-react";
import { type ClipboardEvent, type FormEvent, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";

import { AttachmentCards } from "@/components/AttachmentCards";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { type Artifact, type FlowyEvent, ROOM_TODO_LIMIT, artifactPath } from "@/lib/api";
import { type Attached, writeFile } from "@/lib/attach";
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
  todoAssigneeClaimed,
  todoAttachments,
  todoRaiser,
} from "@/lib/todos";
import { shortId } from "@/lib/utils";

// WHAT TO DO FIRST, as the row shows it.
//
// The vocabulary is the node's - see store/todopriority.go - and this file draws
// what it is given rather than keeping a list of its own: a console that carried
// the words would draw a control that is wrong the day a fourth is added, and
// the refusal comes from the node either way.
function priorityOf(todo: Artifact): string {
  const fields = todo.fields as Record<string, unknown> | undefined;
  const value = fields?.priority;
  return typeof value === "string" ? value.trim().toLowerCase() : "";
}

// now is loud, next is plain, later is quiet - which is the ORDER the field
// sorts in, said in colour so a scan down the panel matches a scan down the
// queue. An unknown word gets the plain treatment rather than no treatment: a
// value this console has not heard of is still somebody's decision.
function priorityClass(priority: string): string {
  switch (priority) {
    case "now":
      return "border-primary/60 text-primary";
    case "later":
      return "border-border/50 text-muted-foreground";
    default:
      return "border-border text-foreground";
  }
}

interface Props {
  room: string;
  todos: Artifact[];
  /** raiseFrom is the message the room has selected, if any: what a todo raised
   * now would be raised out of. */
  raiseFrom: FlowyEvent | null;
  disabled: boolean;
  error: string | null;
  /** onRaise files one. attachments are ids already written to the node - see
   * the paperclip below, and roomTodoRequest.Attachments on the other side. */
  onRaise: (title: string, category?: string, attachments?: string[]) => Promise<void>;
  /** onAssign says who is carrying one. An empty name says nobody is. It has to
   * land on the node and come back from it - see the assignee cell below. */
  onAssign: (id: string, assignee: string, expect: string) => Promise<void>;
  /**
   * onPriority says what to do first, or takes the ranking away with "". Like
   * onAssign it goes to the node and the panel is refilled from the node's
   * answer - there is no second idea here of what a row is ranked.
   */
  onPriority: (id: string, priority: string) => Promise<void>;
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
export function RoomTodos({
  room,
  todos,
  raiseFrom,
  disabled,
  error,
  onRaise,
  onAssign,
  onPriority,
}: Props) {
  // WHICH ROW IS OPEN BENEATH ITS TITLE. An id rather than the row, so a reread
  // of the panel keeps the summary pointed at the same work instead of at a
  // stale copy of it.
  const [open, setOpen] = useState("");
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
  /**
   * The files this todo will carry, written to the node ALREADY.
   *
   * The bytes go up when they are picked, not when the row is raised, because
   * that is where the refusals are - a file over the ceiling, an empty one, a
   * node that is not answering - and a person finds out while they are still
   * looking at the file rather than after typing a title. What the raise sends
   * is a list of ids.
   *
   * Taking one off this list does NOT delete it: the log is append-only and
   * the bytes are written, so the X below is honestly "not this row".
   */
  const [carrying, setCarrying] = useState<Attached[]>([]);
  const [uploading, setUploading] = useState(0);
  const picker = useRef<HTMLInputElement>(null);

  /**
   * One file, up to the node, in the same three sentences the message box uses
   * - literally the same, out of lib/attach, because a second ceiling and a
   * second refusal wording drift apart on the day one of them is corrected.
   */
  const attach = async (file: File, pasted: boolean) => {
    setUploading((n) => n + 1);
    setFailed(null);
    try {
      const got = await writeFile(
        file,
        room,
        pasted ? `pasted in #${room}` : file.name || undefined,
      );
      setCarrying((current) => [...current, got]);
    } catch (err) {
      setFailed(err instanceof Error ? err.message : String(err));
    } finally {
      setUploading((n) => n - 1);
    }
  };

  /** A screenshot is pasted, not picked. The title field takes it because that
   * is where the cursor is when somebody has just copied an image and is about
   * to describe it. */
  const onPaste = (event: ClipboardEvent<HTMLInputElement>) => {
    const files = Array.from(event.clipboardData?.files ?? []);
    if (files.length === 0) return;
    event.preventDefault();
    for (const file of files) void attach(file, true);
  };
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

  /**
   * WHAT IS BEING LOOKED FOR, and it is one box rather than a search field and
   * an author menu.
   *
   * The operator, 06:47: "might be duplicates because of your stulls and the
   * fact that it is impossible to filter by author or seaech on the todo pane
   * of room" - and they were right about the consequence within the hour. Two
   * of us filed the same row SECONDS APART that morning (01M0HPQASA and
   * 01M0HPPY7G, one closed as the other's duplicate), each having decided
   * independently that nothing covered it. Neither could check: 32 open rows,
   * no search, no way to ask whether a thing had been raised.
   *
   * ONE CONTROL, because this panel is 26rem wide beside a live conversation
   * and the header comment two lines down already says what a second menu costs.
   * A bare word matches the title or the id; a word starting with "@" matches
   * the people - raiser or assignee. So "seen mark" and "@deadtrickster" are
   * both typed into the same box, which is the whole of the ask.
   *
   * NOT PERSISTED, unlike hide-done. Hiding finished work is how somebody reads
   * this queue every day; a search is what they are doing for the next twenty
   * seconds, and a box that still held "@orchestrator" tomorrow would look like
   * a panel with rows missing.
   */
  const [find, setFind] = useState("");

  const raise = async (event: FormEvent) => {
    event.preventDefault();
    const said = title.trim();
    if (!said || raising) return;
    setRaising(true);
    setFailed(null);
    try {
      await onRaise(
        said,
        category,
        carrying.map((c) => c.id),
      );
      setTitle("");
      // Cleared only once the node has the row. A raise that was refused still
      // has its files in front of the person who picked them, so retrying is
      // retyping a title rather than finding the file again.
      setCarrying([]);
    } catch (err) {
      setFailed(err instanceof Error ? err.message : String(err));
    } finally {
      setRaising(false);
    }
  };

  // expected holds what the cell CLAIMED when it was clicked - the field, not
  // the OWNER-line the display may fall back to. The commit claims against it,
  // so the two names a handover needs - who this had, who has it now - are
  // captured from the same reading the person acted on, and a row whose holder
  // is only the body's OWNER line claims against nothing, which is what the
  // node's compare-and-set will judge.
  const expected = useRef("");
  const edit = (todo: Artifact) => {
    setEditing(todo.id);
    setNamed(todoAssignee(todo));
    expected.current = todoAssigneeClaimed(todo);
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
      await onAssign(editing, named.trim(), expected.current);
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
  /**
   * MATCHING IS CASE-FOLDED AND SUBSTRING, because the alternative is a person
   * typing a name correctly to find out that a row exists. "@dead" finds
   * deadtrickster.
   *
   * The people arm reads BOTH raiser and assignee on purpose. "@deadtrickster"
   * asked of this queue means "rows that are anything to do with them", and
   * splitting it into two controls to keep the distinction would cost the space
   * the single box exists to save. The row itself says which it was.
   */
  const wanted = find.trim().toLowerCase();
  const matches = (todo: Artifact) => {
    if (!wanted) return true;
    if (wanted.startsWith("@")) {
      const who = wanted.slice(1);
      if (!who) return true;
      return (
        todoRaiser(todo).toLowerCase().includes(who) ||
        todoAssignee(todo).toLowerCase().includes(who)
      );
    }
    return (
      (todo.title || "").toLowerCase().includes(wanted) || todo.id.toLowerCase().includes(wanted)
    );
  };
  const drawn = todos.filter((todo) => (hideDone ? !isTodoDone(todo) : true) && matches(todo));
  /**
   * TWO REASONS A ROW IS NOT ON SCREEN, counted apart, because one line saying
   * "16 done hidden" while a search is on would be a false statement about a
   * queue somebody is trying to read. That is the failure this row is about
   * pointing at itself.
   */
  const hiddenDone = hideDone ? todos.filter((todo) => isTodoDone(todo)).length : 0;
  const unmatched = todos.length - hiddenDone - drawn.length;
  // A CARD ANCHORED TO A ROW THAT HAS MOVED IS ANCHORED TO THE WRONG PLACE.
  //
  // The summary is positioned against its own row - absolute, top-full - so it
  // hangs over whatever is drawn below it. That is correct while the list holds
  // still and wrong the moment it does not: setting a priority REORDERS the
  // panel, sortTodos runs again, and the card stays open at a position that now
  // belongs to somebody else's row, covering the control on it.
  //
  // Measured 2026-08-21: ranking one row and then reaching for another found
  // the first row's card over the second one's opener, and a click that a
  // person would have aimed at a title went into a card about a different todo.
  // It is invisible in a short list and appears as the panel fills up, which is
  // the shape of a defect that reaches a person and not a check.
  //
  // So the card closes when the ORDER changes rather than when the contents do:
  // a row arriving, leaving or moving all invalidate where it is pointing.
  const order = sortTodos(drawn)
    .map((t) => t.id)
    .join(" ");
  // IN AN EFFECT AND NOT DURING RENDER, which is measured rather than
  // stylistic. The render-time version closed the card in the same commit that
  // opened it whenever a poll landed in between, and the panel check red on it.
  // After the paint the panel has settled, so the close is about the list the
  // reader is actually looking at.
  useEffect(() => {
    // `order` is READ here rather than only named as a dependency. An effect
    // that depends on something it never touches is the shape the
    // exhaustive-deps rule exists to catch, and a biome-ignore whose reasoning
    // ran onto a second line stopped being the last comment above the code and
    // suppressed nothing - twice.
    if (order !== "") setOpen("");
  }, [order]);

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
              concludes a todo does not exist - which is exactly how two of us
              filed the same row seconds apart. The two reasons are named
              separately because they answer different questions: one is a
              setting somebody chose once, the other is what they are typing. */}
          {hiddenDone > 0 || unmatched > 0 ? (
            <span data-hidden-count="" className="ml-auto text-muted-foreground text-xs">
              {[
                hiddenDone > 0 ? `${hiddenDone} done hidden` : "",
                unmatched > 0 ? `${unmatched} not matching` : "",
              ]
                .filter(Boolean)
                .join(", ")}
            </span>
          ) : null}
        </div>
        {/* THE BOX, on its own line. It is under the counts rather than beside
            them because at 26rem an input sharing a line with two numbers and a
            checkbox is four things competing, and the input is the one that has
            to be big enough to read what was typed into it. */}
        <Input
          data-todo-find=""
          value={find}
          onChange={(event) => setFind(event.target.value)}
          placeholder="find a row, or @somebody"
          className="h-7 text-xs"
          aria-label="find a todo by title, id, or @person"
        />
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
        {!error && todos.length > 0 && drawn.length === 0 && wanted ? (
          // THE THIRD EMPTY, and it is the one this row exists for. A search
          // that matches nothing must not read like a room that raised nothing
          // or a room that finished everything - the whole point of typing into
          // the box is to find out whether a thing has been raised, and all
          // three empties would otherwise give the same answer to three
          // different questions.
          <div className="px-4 py-2 text-muted-foreground text-xs">
            {/* WHAT WAS SEARCHED, and whether that is the whole room.
                A page that comes back FULL is the signal that there may be more
                behind it - the door answers with a page and no total, so this is
                the only truncation evidence a client has. Saying "nothing
                matches" about a window while sounding like the room is the exact
                failure this box was added to prevent, one boundary further out.
                Measured 2026-08-21: the door's default is 200 and #general had
                324. */}
            nothing in #{room} matches {find.trim()} -{" "}
            {todos.length >= ROOM_TODO_LIMIT
              ? `searched the ${todos.length} row(s) this pane has loaded, and there may be more`
              : `${todos.length} row(s) here`}
          </div>
        ) : null}
        {!error && todos.length > 0 && drawn.length === 0 && !wanted ? (
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
              // RELATIVE, because the card is drawn OVER the list from here -
              // see TodoSummary. The row is the anchor, so the popup cannot
              // appear anywhere but beside the thing it is about.
              <li
                key={todo.id}
                className="relative flex items-baseline gap-2 border-border/60 border-b px-4 py-2 text-xs"
              >
                {/*
                  Amber in flight, grey waiting, green finished - the three
                  states this panel exists to show, told apart at a glance
                  instead of by reading every row. The word stays inside the
                  badge: colour alone would leave a queue with no states in it
                  for anybody who cannot separate amber from green.
                */}
                {/*
                  WHAT TO DO FIRST, when somebody has said. The operator asked
                  for priorities with sixteen unowned rows on the board and
                  nothing on any of them saying which they wanted.

                  DRAWN ONLY WHEN SET. A chip on every row saying "unjudged"
                  would be a column of the same word, and the field's whole
                  point is that unjudged and unimportant are different facts -
                  see store/todopriority.go, where the unjudged sort ABOVE the
                  shelved for that reason. The control to set it is in the
                  summary, one click away, because this column is already four
                  things wide beside a conversation.
                */}
                {priorityOf(todo) ? (
                  <Badge
                    variant="outline"
                    data-todo-priority={todo.id}
                    data-todo-priority-value={priorityOf(todo)}
                    className={priorityClass(priorityOf(todo))}
                  >
                    {priorityOf(todo)}
                  </Badge>
                ) : null}
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
                {/*
                  THE TITLE OPENS THE ROW. The operator: "no way to go from chat
                  todo to full todo card", and then "i want a quick view for
                  chat todo when I click on it - quick summary card + link to
                  the full todo card".

                  The panel had the id, the title, the status and the assignee
                  and no way to reach any of the rest - the body, the notes, the
                  history - so a row raised out of a conversation was readable
                  here and nowhere else without copying an id into a URL by
                  hand.

                  Expanding in place rather than navigating, because the panel
                  sits BESIDE the conversation the row came out of: leaving the
                  room to read a todo is what the finding pane fixed on the
                  other surface tonight, and the same argument applies here. The
                  link to the full card is drawn inside the summary, which is
                  what the operator asked for in the same sentence.
                */}
                <button
                  type="button"
                  data-todo-open={todo.id}
                  aria-expanded={open === todo.id}
                  className="min-w-0 flex-1 break-words text-left hover:underline"
                  onClick={() => setOpen(open === todo.id ? "" : todo.id)}
                >
                  {todo.title || todo.id}
                </button>
                {/* THE CARD, OVER THE LIST AND BESIDE ITS OWN ROW.
                    It used to be rendered after the whole map, so it opened at
                    the BOTTOM of the list: click the third row of thirty and
                    the card appears twenty-seven rows down, outside a narrow
                    scrolling pane. The operator, 01M0HGX1S3: "some clumsy panel
                    at the bottom that i know about only because scroll bar
                    shrinks" - a feature that was live and unreachable, and the
                    comment above it had claimed "under the row it belongs to"
                    since the day it was written. So it is drawn by the row, and
                    it floats rather than pushing thirty rows down the pane. */}
                {open === todo.id ? (
                  <TodoSummary
                    todo={todo}
                    onClose={() => setOpen("")}
                    disabled={disabled}
                    onPriority={onPriority}
                  />
                ) : null}
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
            onPaste={onPaste}
            placeholder={`raise a todo in #${room}`}
            disabled={disabled || raising}
            aria-label={`raise a todo in ${room}`}
          />
          {/* The file the row is about. Hidden input, clicked by the button,
              exactly as the message box does it - a bare file input styles
              differently in every browser and this panel is narrow. */}
          <input
            ref={picker}
            type="file"
            multiple
            className="hidden"
            onChange={(event) => {
              for (const file of Array.from(event.target.files ?? [])) void attach(file, false);
              // So picking the same file twice in a row fires onChange the
              // second time: the input reports a CHANGE of value.
              event.target.value = "";
            }}
            aria-label="attach a file to this todo"
          />
          <Button
            type="button"
            size="icon"
            variant="ghost"
            className="h-7 w-7 shrink-0"
            disabled={disabled || raising}
            onClick={() => picker.current?.click()}
            aria-label="attach a file to this todo"
            data-todo-attach
          >
            <Paperclip className="h-3.5 w-3.5" />
          </Button>
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
        {carrying.length > 0 ? (
          <div className="flex flex-wrap gap-2" data-todo-carrying>
            {carrying.map((c) => (
              <span
                key={c.id}
                className="flex items-center gap-1 rounded bg-muted px-2 py-1 text-xs"
                data-todo-carried={c.id}
              >
                <Paperclip className="h-3 w-3 shrink-0" />
                <span className="max-w-40 truncate">{c.name}</span>
                <span className="text-muted-foreground">{Math.ceil(c.bytes / 1024)}k</span>
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  className="h-4 w-4 shrink-0"
                  onClick={() => setCarrying((current) => current.filter((x) => x.id !== c.id))}
                  aria-label={`do not attach ${c.name}`}
                >
                  <X className="h-3 w-3" />
                </Button>
              </span>
            ))}
          </div>
        ) : null}
        {uploading > 0 ? (
          <span className="text-muted-foreground text-xs">attaching {uploading}…</span>
        ) : null}
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

/**
 * A chat todo, opened where it sits.
 *
 * THE OPERATOR ASKED FOR EXACTLY THIS: "i want a quick view for chat todo when
 * I click on it - quick summary card + link to the full todo card", after
 * "no way to go from chat todo to full todo card".
 *
 * The panel had the id, the title, the status and the assignee, and no way to
 * reach the body or the row itself - so work raised out of a conversation was
 * readable beside that conversation and nowhere else, unless somebody typed an
 * id into a URL.
 *
 * The body is drawn as PLAIN TEXT, not markdown. Every other body in this
 * console goes through the sanitizer, and this one is a summary in a narrow
 * column beside a live transcript - rendering headings and lists here would
 * make a three-line panel out of a row that is meant to be glanceable. The full
 * card is one click away and renders it properly.
 */
function TodoSummary({
  todo,
  onClose,
  disabled,
  onPriority,
}: {
  todo: Artifact;
  onClose: () => void;
  disabled: boolean;
  onPriority: (id: string, priority: string) => Promise<void>;
}) {
  // ESCAPE CLOSES IT, because a thing that floats over what you were reading
  // has to be dismissable without hunting for the control that opened it. The
  // listener is on the document rather than on the card: the card does not hold
  // focus (the row's button still does), so a keydown here would never arrive.
  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.addEventListener("keydown", key);
    return () => document.removeEventListener("keydown", key);
  }, [onClose]);

  // NOT OPTIONAL ANY MORE, and the "that row is no longer in this panel" branch
  // is gone with the reason for it. This used to be looked up by id out of the
  // whole list, so it could be asked about a row a poll or a filter had dropped;
  // it is now rendered BY that row, so a row that leaves takes its card with it
  // and there is no state where one is drawn without the other.
  const to = artifactPath(todo);
  const body = (todo.body ?? "").trim();
  // What the row carries. The same cards the room draws under a message, so a
  // screenshot raised into the queue is looked at where the queue is read
  // instead of being an id somebody has to go and resolve.
  const files = todoAttachments(todo);
  return (
    // ABSOLUTE, over the rows below it, anchored on the row (which is relative).
    // left-2/right-2 rather than a width: the pane is 26rem and a popup that
    // guessed its own width would either overflow the column or waste half of
    // it. top-full puts its top edge on the row's bottom edge, so the row it is
    // about stays visible above it.
    //
    // z-20 clears the rows; the shadow and the solid background are what make it
    // read as ON TOP rather than as another row, which is the whole complaint.
    <div
      data-todo-summary={todo.id}
      className="absolute top-full right-2 left-2 z-20 rounded-md border border-border bg-background px-3 py-2 text-xs shadow-lg"
    >
      <div className="flex items-baseline gap-2">
        <span className="font-mono text-muted-foreground">{shortId(todo.id)}</span>
        {/*
          WHERE THE RANKING IS SET, one click from the row and not on it: the
          panel is a narrow column beside a conversation and a control per row
          would crowd out the titles, which are what somebody is reading.

          A select rather than a cycling chip. Four states - unjudged, now,
          next, later - and a chip that cycled would make "take it back" three
          clicks and an accident on the way.
        */}
        <label className="ml-auto flex items-center gap-1 text-muted-foreground">
          do first
          <select
            data-todo-priority-set={todo.id}
            aria-label={`what to do first about ${todo.title || todo.id}`}
            className="rounded border border-border bg-background px-1 py-0.5 text-foreground text-xs"
            disabled={disabled}
            value={priorityOf(todo)}
            onChange={(event) => void onPriority(todo.id, event.target.value)}
          >
            {/*
              The empty option is FIRST and is named. "unjudged" rather than
              "none" or a blank line, because the whole distinction this field
              keeps is that nobody having looked is a different fact from
              somebody deciding it can wait - and a blank option reads as the
              latter.
            */}
            <option value="">unjudged</option>
            <option value="now">now</option>
            <option value="next">next</option>
            <option value="later">later</option>
          </select>
        </label>
        {to ? (
          <Link to={to} data-todo-full-card={todo.id} className="text-primary underline">
            open the full card
          </Link>
        ) : null}
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className="-my-1 h-5 w-5 shrink-0"
          onClick={onClose}
          aria-label={`close the card for ${todo.title || todo.id}`}
        >
          <X className="h-3 w-3" />
        </Button>
      </div>
      {body ? (
        <p className="whitespace-pre-wrap break-words pt-1">{body.slice(0, 400)}</p>
      ) : (
        <p className="pt-1 text-muted-foreground">no body on this row</p>
      )}
      {body.length > 400 ? (
        <p className="pt-1 text-muted-foreground">
          … {body.length - 400} more characters on the full card
        </p>
      ) : null}
      {files.length > 0 ? (
        <div className="pt-1" data-todo-files>
          <AttachmentCards ids={files} />
        </div>
      ) : null}
    </div>
  );
}
