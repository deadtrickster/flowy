import type { FlowyEvent } from "@/lib/api";
import { speakerStyle } from "@/lib/speakercolour";
import { clock, speaker } from "@/lib/utils";

/**
 * A thread, top to bottom, the way a person reads a conversation.
 *
 * The pane drew the thread as its DAG and nothing else, which is why the
 * operator called it "some diagram editor, not a message thread really". The
 * DAG is the honest picture - this log really is a graph, a message can name
 * several parents, and two replies to the same message really are siblings
 * rather than a sequence. But almost every thread here is a straight line, and
 * drawing a straight line as a graph makes the reader do layout in their head
 * to recover the one thing they wanted: what was said, in order.
 *
 * So the default is the reading view and the graph is a keystroke away. Neither
 * is a simplification of the other: this shows ORDER, the DAG shows STRUCTURE,
 * and the moment a thread stops being a line the DAG is the only one that can
 * tell you so - which is why the header says how many parents are in play
 * rather than quietly flattening them.
 */
export function ThreadList({
  events,
  selected,
  onSelect,
}: {
  events: FlowyEvent[];
  selected: FlowyEvent | null;
  onSelect: (event: FlowyEvent) => void;
}) {
  if (events.length === 0) {
    return (
      <div className="px-4 py-3 text-muted-foreground text-xs">
        select a message to follow its thread
      </div>
    );
  }

  return (
    <ul className="flex h-full flex-col overflow-auto">
      {events.map((event) => {
        const name = speaker(event);
        return (
          <li key={event.id}>
            <button
              type="button"
              onClick={() => onSelect(event)}
              className={`flex w-full flex-col gap-1 border-border/60 border-b px-4 py-2 text-left text-xs hover:bg-accent/40 ${
                selected?.id === event.id ? "bg-accent/60" : ""
              }`}
            >
              <span className="flex items-center gap-2">
                <span className="rounded px-1 font-mono" style={speakerStyle(name)}>
                  {name}
                </span>
                <span className="ml-auto text-muted-foreground">{clock(event.created)}</span>
              </span>
              <span className="whitespace-pre-wrap break-words">{event.body}</span>
              {/*
                A reply that names more than one parent is the case the linear
                view cannot show, so it says so rather than pretending. That is
                the whole reason the DAG stays: this line is a hint that the
                other view has something to add, not decoration.
              */}
              {event.parents.length > 1 ? (
                <span className="font-mono text-[11px] text-muted-foreground">
                  ← {event.parents.length} parents - press d for the graph
                </span>
              ) : null}
            </button>
          </li>
        );
      })}
    </ul>
  );
}
