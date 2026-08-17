import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { type ActivityItem, api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { clock } from "@/lib/utils";

/**
 * Search over what was SAID, from the room it was said in.
 *
 * The timeline endpoint indexes chat with the same permission filter as every
 * other read, so a hit is something this token may read and a miss is not
 * evidence nothing was said. Results say which room each hit lives in, and one
 * click goes there - searching a conversation should end in the conversation.
 */
export function RoomSearch() {
  const { token } = useSession();
  const [q, setQ] = useState("");
  const [hits, setHits] = useState<ActivityItem[]>([]);
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const navigate = useNavigate();

  useEffect(() => {
    if (!token || q.trim().length < 2) {
      setHits([]);
      return;
    }
    const timer = setTimeout(() => {
      api
        .activity({ q: q.trim(), kind: "chat" })
        .then((page) => setHits(page.items.slice(0, 8)))
        .catch(() => setHits([]));
    }, 200);
    return () => clearTimeout(timer);
  }, [q, token]);

  useEffect(() => {
    const away = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
  }, []);

  return (
    <div ref={box} className="relative ml-auto">
      <input
        value={q}
        onChange={(e) => {
          setQ(e.target.value);
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        placeholder="search what was said…"
        className="h-7 w-44 rounded-md border border-input bg-transparent px-2 text-xs outline-none focus:border-primary/60"
      />
      {open && q.trim().length >= 2 ? (
        <div className="absolute right-0 top-9 z-10 w-96 rounded-md border border-border bg-popover p-2 shadow-lg">
          {hits.length === 0 ? (
            <div className="px-2 py-1 text-muted-foreground text-xs">nothing found</div>
          ) : (
            hits.map((hit) => (
              <button
                key={hit.id}
                type="button"
                onClick={() => {
                  setOpen(false);
                  if (hit.room) navigate(`/chat/${hit.room}`);
                }}
                className="block w-full rounded px-2 py-1.5 text-left text-xs hover:bg-accent"
              >
                <span className="flex items-center gap-2 pb-0.5 text-muted-foreground">
                  <Badge variant="outline">#{hit.room}</Badge>
                  <span>{clock(hit.created)}</span>
                </span>
                <span className="line-clamp-2">{hit.body}</span>
              </button>
            ))
          )}
        </div>
      ) : null}
    </div>
  );
}
