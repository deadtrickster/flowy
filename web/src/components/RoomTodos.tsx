import { type FormEvent, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { Artifact, FlowyEvent } from "@/lib/api";
import { speakerStyle } from "@/lib/speakercolour";
import { countTodos, sortTodos, statusStyle, todoOwner } from "@/lib/todos";
import { shortId } from "@/lib/utils";

interface Props {
  room: string;
  todos: Artifact[];
  /** raiseFrom is the message the room has selected, if any: what a todo raised
   * now would be raised out of. */
  raiseFrom: FlowyEvent | null;
  disabled: boolean;
  error: string | null;
  onRaise: (title: string) => Promise<void>;
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
 * A row is status, owner and title, because a panel beside a conversation is
 * narrow and those are the three things somebody glances at it for. The rest of
 * the item is one click away in the artifact view.
 */
export function RoomTodos({ room, todos, raiseFrom, disabled, error, onRaise }: Props) {
  const [title, setTitle] = useState("");
  const [raising, setRaising] = useState(false);
  const [failed, setFailed] = useState<string | null>(null);

  const raise = async (event: FormEvent) => {
    event.preventDefault();
    const said = title.trim();
    if (!said || raising) return;
    setRaising(true);
    setFailed(null);
    try {
      await onRaise(said);
      setTitle("");
    } catch (err) {
      setFailed(err instanceof Error ? err.message : String(err));
    } finally {
      setRaising(false);
    }
  };

  const counts = countTodos(todos);

  return (
    <section className="flex min-h-0 flex-col border-border border-b">
      <header className="flex items-center gap-2 border-border border-b px-4 py-3">
        <h2 className="font-semibold text-sm">todos</h2>
        <span className="font-mono text-muted-foreground text-xs">#{room}</span>
        <span className="ml-auto text-muted-foreground text-xs">
          {counts.active} active, {counts.open} open, {counts.done} done
        </span>
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
        <ul className="flex flex-col">
          {sortTodos(todos).map((todo) => {
            const owner = todoOwner(todo.body ?? "");
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
                  The owner in their own colour, the same one they speak in
                  just above - so "who is carrying this" and "who said that"
                  are the same glance rather than two lookups. Unowned stays
                  grey, because nobody is not a person.
                */}
                <span
                  className="shrink-0 rounded px-1 text-muted-foreground"
                  style={owner ? speakerStyle(owner) : undefined}
                >
                  {owner || "unowned"}
                </span>
                <span className="min-w-0 flex-1 break-words">{todo.title || todo.id}</span>
              </li>
            );
          })}
        </ul>
      </div>

      <form className="flex flex-col gap-1 border-border border-t p-3" onSubmit={raise}>
        <div className="flex items-center gap-2">
          <Input
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            placeholder={`raise a todo in #${room}`}
            disabled={disabled || raising}
            aria-label={`raise a todo in ${room}`}
          />
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
