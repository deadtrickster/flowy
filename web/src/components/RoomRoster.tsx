import { Badge } from "@/components/ui/badge";
import type { Presence } from "@/lib/api";

/**
 * Who is in the room, and what the node can honestly say about who has an ear
 * on. Members are who has spoken. Listener lines never claim "online" - the
 * node sees the polling, not the process, so the line says when a poll last
 * started and whether one is in flight, which is checkable.
 */
export function RoomRoster({ presence }: { presence: Presence | null }) {
  if (!presence) {
    return (
      <div className="border-border border-b px-4 py-3">
        <div className="font-medium text-muted-foreground text-xs uppercase tracking-wide">
          in the room
        </div>
        <div className="text-muted-foreground text-xs">…</div>
      </div>
    );
  }

  const named = (m: { name: string; actor: string }) => m.name || m.actor.slice(-8);

  return (
    <div className="border-border border-b px-4 py-3">
      <div className="pb-1 font-medium text-muted-foreground text-xs uppercase tracking-wide">
        in the room
      </div>
      <div className="flex flex-wrap gap-1 pb-2">
        {presence.members.length === 0 ? (
          <span className="text-muted-foreground text-xs">nobody has spoken yet</span>
        ) : (
          presence.members.map((m) => (
            <Badge key={m.actor} variant="secondary">
              {named(m)}
            </Badge>
          ))
        )}
      </div>

      <div className="pb-1 font-medium text-muted-foreground text-xs uppercase tracking-wide">
        listening
      </div>
      {presence.listeners.length === 0 ? (
        <div className="text-muted-foreground text-xs">no reader is polling</div>
      ) : (
        <ul className="flex flex-col gap-0.5">
          {presence.listeners.map((l) => (
            <li key={l.principal + l.reader} className="flex items-center gap-2 text-xs">
              <span className="min-w-0 truncate">
                {l.reader}
                {l.reader !== l.user_name ? (
                  <span className="text-muted-foreground"> · {l.reader}</span>
                ) : null}
              </span>
              <span className="ml-auto shrink-0 text-muted-foreground">
                {l.attached ? "polling" : ""} {ago(l.last_poll_at)}
              </span>
            </li>
          ))}
        </ul>
      )}
      <div className="pt-1 text-muted-foreground text-[10px]">
        the node sees polling, not processes - a dead listener looks attached until its window
        lapses
      </div>
    </div>
  );
}

/** ago renders how long ago a poll last started, honestly and briefly. */
function ago(at?: string | null): string {
  if (!at) return "never polled";
  const s = Math.max(0, Math.floor((Date.now() - new Date(at).getTime()) / 1000));
  if (s < 5) return "just now";
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m ago`;
}
