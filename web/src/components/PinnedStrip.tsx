import { useMemo } from "react";

import type { FlowyEvent } from "@/lib/api";
import { speakerStyle } from "@/lib/speakercolour";
import { clock, speaker } from "@/lib/utils";

interface Props {
  /** The ids that are up, in the order they were first pinned. */
  pinned: string[];
  /** The room's messages, as the view already has them. */
  events: FlowyEvent[];
  /** Selecting a pin selects the message it points at. */
  onSelect: (event: FlowyEvent) => void;
  /** Taking one down. Undefined while a room is read-only. */
  onUnpin?: (id: string) => void;
}

/**
 * What a room decided, kept where a new reader lands.
 *
 * The strip renders the MESSAGES the room already has rather than a copy stored
 * with the pin - the pin carries an id and nothing else, so this is the same
 * text, through the same filter, as the transcript below it. A pinned message
 * that this reader cannot see does not appear: they are not told a decision
 * exists and withheld, because the pin was never visible to them either.
 *
 * A pin whose message has not been loaded yet is skipped rather than drawn as a
 * placeholder. The room pages in from a cursor, so an old decision is often not
 * in memory on first paint, and a strip of grey boxes that fill in later is
 * worse than a strip that grows.
 */
export function PinnedStrip({ pinned, events, onSelect, onUnpin }: Props) {
  // Both of these come off the wire, and Go marshals an empty slice as null.
  // A strip that throws takes the WHOLE ROOM down with it - measured: the room
  // never rendered at all and the check reported "0 requests measures nothing",
  // which is a transcript nobody can read because a decoration could not draw.
  // Nothing here is worth that.
  const byId = useMemo(() => {
    const index = new Map<string, FlowyEvent>();
    for (const event of events ?? []) index.set(event.id, event);
    return index;
  }, [events]);

  const shown = (pinned ?? []).map((id) => byId.get(id)).filter((e): e is FlowyEvent => !!e);
  if (shown.length === 0) return null;

  return (
    <div className="border-border border-b bg-muted/30 px-4 py-2">
      <div className="flex items-center gap-2 pb-1 text-muted-foreground text-xs">
        <span aria-hidden="true">📌</span>
        <span>
          {shown.length} pinned message{shown.length === 1 ? "" : "s"}
        </span>
      </div>
      <ul className="flex flex-col gap-1">
        {shown.map((event) => (
          <li key={event.id} className="flex items-start gap-2 text-xs">
            <button
              type="button"
              onClick={() => onSelect(event)}
              className="flex-1 truncate text-left hover:underline"
              title={event.body}
            >
              <span
                className="mr-2 rounded px-1 py-0.5 font-mono"
                style={speakerStyle(speaker(event))}
              >
                {speaker(event)}
              </span>
              <span className="text-muted-foreground">{clock(event.created)}</span>{" "}
              <span>{event.body}</span>
            </button>
            {onUnpin ? (
              <button
                type="button"
                onClick={() => onUnpin(event.id)}
                className="shrink-0 text-muted-foreground hover:text-foreground"
                aria-label={`unpin ${event.id}`}
                title="unpin"
              >
                ×
              </button>
            ) : null}
          </li>
        ))}
      </ul>
    </div>
  );
}
