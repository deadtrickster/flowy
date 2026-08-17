import { AnimatePresence, motion } from "framer-motion";
import { useEffect, useRef } from "react";

import { Badge } from "@/components/ui/badge";
import { type FlowyEvent, isAgent } from "@/lib/api";
import { splitBody } from "@/lib/mentions";
import { speakerStyle } from "@/lib/speakercolour";
import { clock, cn, shortId, speaker } from "@/lib/utils";

interface Props {
  events: FlowyEvent[];
  selected: FlowyEvent | null;
  onSelect: (event: FlowyEvent) => void;
  /** me is the principal reading, so a message for them can say so. */
  me?: { user?: string; agent?: string };
}

/**
 * The room, oldest first. Selecting a message is what a reply attaches to: the
 * next thing said names it as a parent, which is the branch the thread view
 * draws.
 */
export function MessageList({ events, selected, onSelect, me }: Props) {
  // Whether an id is the person reading or the agent working for them, which
  // is the pair the node treats as one reader everywhere else.
  const isMe = (id?: string) => !!id && (id === me?.user || id === me?.agent);

  const bottom = useRef<HTMLDivElement>(null);
  const count = events.length;

  // Follow the room as it grows, which is what a message arriving mid-poll
  // should do to the view.
  useEffect(() => {
    if (count === 0) return;
    bottom.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [count]);

  return (
    <div className="flex flex-1 flex-col gap-2 overflow-y-auto p-4">
      <AnimatePresence initial={false}>
        {events.map((event) => {
          const agent = isAgent(event);
          // For you, and not from you. An addressed message is still an
          // ordinary message in the room - the same people read it, it sits in
          // the same place - so this is a ring around it and never a filter.
          const forMe = isMe(event.addressee) && !isMe(event.actor);
          return (
            <motion.button
              type="button"
              key={event.id}
              layout
              initial={{ opacity: 0, y: 8 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0 }}
              transition={{ duration: 0.18, ease: "easeOut" }}
              onClick={() => onSelect(event)}
              className={cn(
                "rounded-lg border border-border bg-card p-3 text-left transition-colors hover:border-primary/40",
                forMe && "border-primary/50 bg-primary/5",
                selected?.id === event.id && "border-primary/70 ring-1 ring-primary/40",
              )}
            >
              <div className="flex items-center gap-2 pb-1">
                <Badge variant={agent ? "agent" : "human"}>{agent ? "agent" : "human"}</Badge>
                {/*
                 * Who said it. The name the node recorded when it was said,
                 * and the tail of the actor id when the message has none - a
                 * room where every line was an id is what this replaces, and
                 * the id stays on the title so it is still there to copy.
                 */}
                {/*
                  In the speaker's own colour, derived from the name so the
                  same person is the same colour here, in the roster and on a
                  todo they own. The name stays: colour is an accelerator for
                  people who see it, never the only thing carrying who spoke.
                */}
                <span
                  className="rounded px-1.5 py-0.5 font-mono text-xs"
                  style={speakerStyle(speaker(event))}
                  title={event.actor}
                >
                  {speaker(event)}
                </span>
                {event.addressee ? (
                  <Badge variant="outline">to {forMe ? "you" : shortId(event.addressee, 8)}</Badge>
                ) : null}
                <span className="ml-auto text-muted-foreground text-xs">
                  {clock(event.created)}
                </span>
              </div>
              {/*
                The body, with the @names the node resolved drawn in the
                colour of whoever they name - the same colour that person
                speaks in above, because both come from the name. A mention of
                YOU is ringed as well, which is the treatment the row already
                gives a message addressed at you: the ring says "this one is
                yours" whether it is around the message or around your name
                inside it. An @word the node resolved to nobody is left as the
                text it is.
              */}
              <div className="whitespace-pre-wrap break-words text-sm">
                {splitBody(event.body, event.meta?.mentions).map((run) =>
                  run.name ? (
                    <span
                      key={run.key}
                      data-mention={run.id}
                      className={cn(
                        "rounded px-0.5 font-medium",
                        isMe(run.id) && "ring-1 ring-primary/70",
                      )}
                      style={speakerStyle(run.name)}
                    >
                      {run.text}
                    </span>
                  ) : (
                    run.text
                  ),
                )}
              </div>
              <div className="flex gap-2 pt-1 font-mono text-[11px] text-muted-foreground">
                <span>#{shortId(event.id)}</span>
                <span>thread {shortId(event.thread)}</span>
                {event.parents.length > 0 ? (
                  <span>← {event.parents.map((id) => shortId(id)).join(" ")}</span>
                ) : (
                  <span>opens a thread</span>
                )}
              </div>
            </motion.button>
          );
        })}
      </AnimatePresence>
      <div ref={bottom} />
    </div>
  );
}
