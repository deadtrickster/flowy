import { AnimatePresence, motion } from "framer-motion";
import { useEffect, useRef } from "react";

import { Badge } from "@/components/ui/badge";
import { type FlowyEvent, isAgent } from "@/lib/api";
import { clock, cn, shortId } from "@/lib/utils";

interface Props {
  events: FlowyEvent[];
  selected: FlowyEvent | null;
  onSelect: (event: FlowyEvent) => void;
}

/**
 * The room, oldest first. Selecting a message is what a reply attaches to: the
 * next thing said names it as a parent, which is the branch the thread view
 * draws.
 */
export function MessageList({ events, selected, onSelect }: Props) {
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
                selected?.id === event.id && "border-primary/70 ring-1 ring-primary/40",
              )}
            >
              <div className="flex items-center gap-2 pb-1">
                <Badge variant={agent ? "agent" : "human"}>{agent ? "agent" : "human"}</Badge>
                <span className="font-mono text-muted-foreground text-xs">
                  {shortId(event.actor, 8)}
                </span>
                <span className="ml-auto text-muted-foreground text-xs">
                  {clock(event.created)}
                </span>
              </div>
              <div className="whitespace-pre-wrap break-words text-sm">{event.body}</div>
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
