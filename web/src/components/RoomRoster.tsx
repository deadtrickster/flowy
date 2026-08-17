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

  // THE STORE DECIDES WHAT IS A GHOST, AND THIS RENDERS ITS ANSWER.
  //
  // This pane once filtered its own ghosts - every row that had never polled
  // was dropped - which fixed a roster of dead console cursors and broke the
  // other thing the same field carries: a waiter that EXISTS and has not
  // polled yet is STARTING, seconds old, kind unknown, and the roster is where
  // somebody watches it start. Same empty last_poll_at as a cursor a closed
  // page left behind; two different facts.
  //
  // Presence now windows rows at the store - attached, polled, or updated
  // within ten minutes - so what arrives here is already the answer, and this
  // second-guessing filter would hide exactly the rows that change made a
  // point of keeping. The view asks the narrower question; it no longer
  // re-answers the one the store already did.
  const live = presence.listeners;

  // ONE LINE PER PRINCIPAL, NOT PER NAME. The name is a label somebody chose
  // and the principal is who they are, and on this node the two disagree in
  // BOTH directions at once:
  //
  //   claude-glm and flowy-glm   DIFFERENT names, SAME principal - one seat
  //                              listening twice, which by name reads as two
  //                              healthy agents
  //   claude-host and claude-host  SAME name, DIFFERENT users - one is the
  //                              agent, one is the operator carrying that agent
  //                              id, which by name reads as one
  //
  // So deduping by name would merge the two that are genuinely different and
  // keep the two that are genuinely one - exactly inverted. The principal is
  // user + agent + project and it is already on every row.
  //
  // THE COUNT STAYS VISIBLE because a principal polling twice is a DOUBLED
  // WAITER, and that is a bug rather than clutter: two waiters under one
  // identity share one server-side cursor, so wake-ups split between them and
  // both look healthy while messages go to whichever answered first. That is
  // what silenced a watcher here earlier today. A pane that tidied them into
  // one line would hide the failure it exists to show.
  //
  // AND THE KIND IS PART OF THE KEY, which grouping on the principal alone got
  // wrong and turned master red: one principal can hold a TRACKED reader and a
  // FORKED one at the same time, and those are not one listener seen twice -
  // they are the difference between something that can wake you and something
  // that hears the room with nothing to wake. Telling those apart is the whole
  // point of this pane, so collapsing them destroyed the feature to tidy the
  // display. Two rows of the same kind under one identity is the doubled waiter;
  // two rows of different kinds is two answers to what this seat can do.
  const byPrincipal = new Map<
    string,
    { row: (typeof live)[number]; count: number; names: string[] }
  >();
  for (const l of live) {
    const key = `${l.principal}\u001f${l.waiter_kind ?? ""}`;
    const seen = byPrincipal.get(key);
    if (!seen) {
      byPrincipal.set(key, { row: l, count: 1, names: [l.reader] });
      continue;
    }
    seen.count++;
    if (!seen.names.includes(l.reader)) seen.names.push(l.reader);
    // Freshest wins: the live process is the one still polling, and the stale
    // row is the one somebody restarted away from.
    if ((l.last_poll_at ?? "") > (seen.row.last_poll_at ?? "")) seen.row = l;
  }
  const ears = [...byPrincipal.values()];

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
      {ears.length === 0 ? (
        <div className="text-muted-foreground text-xs">no reader is polling</div>
      ) : (
        <ul className="flex flex-col gap-0.5">
          {ears.map(({ row: l, count, names }) => {
            const kind = listenerKind(l.waiter_kind);
            return (
              <li
                key={l.principal}
                data-listener={l.reader}
                data-listener-rows={count}
                className="flex items-center gap-2 text-xs"
              >
                <span className="min-w-0 truncate" style={{ color: speakerStyle(l.reader).color }}>
                  {names.join(" + ")}
                  {l.reader !== l.user_name ? (
                    <span className="text-muted-foreground"> · {l.user_name}</span>
                  ) : null}
                  {/*
                    One identity, more than one row polling. Said out loud
                    rather than tidied away: they share one cursor, so wake-ups
                    split between them and each looks healthy while messages
                    reach only whichever answered first.
                  */}
                  {count > 1 ? (
                    <span
                      className="ml-1 text-amber-600 dark:text-amber-500"
                      title={`${count} rows are polling as this one principal - they share a cursor, so each hears only part of the room`}
                    >
                      ×{count} doubled
                    </span>
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
