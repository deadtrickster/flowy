import { Badge } from "@/components/ui/badge";
import type { Presence } from "@/lib/api";
import { speakerStyle } from "@/lib/speakercolour";

/**
 * Who is in the room, and what the node can honestly say about who has an ear
 * on. Members are who has spoken. Listener lines never claim "online" - the
 * node sees the polling, not the process, so the line says when a poll last
 * started and whether one is in flight, which is checkable.
 *
 * And what KIND of listener it is, which is the half the other two cannot
 * carry. A forked successor - the one a waiter leaves behind so the room stays
 * heard while its agent reads - polls, is attached, is seconds fresh, and can
 * wake nobody, because only a harness-tracked waiter exiting produces a
 * notification. Drawn from attachment alone this panel called such a listener
 * healthy for 28 minutes while the person who wrote into the room got silence.
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
            <Badge key={m.actor} variant="secondary" style={speakerStyle(named(m))}>
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
          {presence.listeners.map((l) => {
            const kind = listenerKind(l.waiter_kind);
            return (
              <li
                key={l.principal + l.reader}
                data-listener={l.reader}
                className="flex items-center gap-2 text-xs"
              >
                <span className="min-w-0 truncate" style={{ color: speakerStyle(l.reader).color }}>
                  {l.reader}
                  {l.reader !== l.user_name ? (
                    <span className="text-muted-foreground"> · {l.user_name}</span>
                  ) : null}
                </span>
                {/*
                  The kind is its own element rather than a word inside the
                  timing line, so that what a listener can DO is what the eye
                  lands on and so a check can assert on it. "polling 4s ago" is
                  true of all three of these and answers none of them.
                */}
                <span
                  data-waiter-kind={kind.kind}
                  title={kind.why}
                  className={`ml-auto shrink-0 ${kind.className}`}
                >
                  {kind.label}
                </span>
                <span className="shrink-0 text-muted-foreground">
                  {l.attached ? "polling · " : ""}
                  {ago(l.last_poll_at)}
                </span>
              </li>
            );
          })}
        </ul>
      )}
      <div className="pt-1 text-muted-foreground text-[10px]">
        the node sees polling, not processes - a dead listener looks attached until its window
        lapses, and a forked one hears the room with nothing to wake
      </div>
    </div>
  );
}

/**
 * listenerKind is the word for each kind and the reason behind it.
 *
 * Three states get three renderings, and unknown is not quietly folded into
 * tracked: a row written before the node reported kinds, or by a client that
 * does not say, is evidence of nothing - and reading absence as the good case
 * is exactly what made a deaf listener report healthy. The three labels are
 * deliberately different words rather than one word and a colour, because a
 * roster is read at a glance and a colour is not a sentence.
 */
function listenerKind(kind: string | undefined): {
  kind: string;
  label: string;
  why: string;
  className: string;
} {
  switch (kind) {
    case "tracked":
      return {
        kind: "tracked",
        label: "can wake",
        why: "a harness-tracked waiter: when it hears something it exits, and that exit wakes its session",
        className: "text-muted-foreground",
      };
    case "forked":
      return {
        kind: "forked",
        label: "heard, cannot wake",
        why: "a forked successor: it keeps the room heard while its agent reads, but it is nobody's task, so hearing something wakes no one",
        className: "text-amber-400",
      };
    default:
      return {
        kind: "unknown",
        label: "kind unknown",
        why: "this reader has not said what it is - a poll from before the node reported kinds, or a client that does not send one. It may or may not be able to wake anybody",
        className: "text-muted-foreground italic",
      };
  }
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
