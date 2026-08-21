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
  // Presence now windows rows at the store - polled inside the window, holding
  // a poll that never ended, or declared moments ago - so what arrives here is
  // already the answer, and this second-guessing filter would hide exactly the
  // rows that change made a point of keeping. The view asks the narrower
  // question; it no longer re-answers the one the store already did.
  //
  // TWO GROUPS, AND THE SECOND ONE IS THE POINT. A row the store marks "lost"
  // is a seat that was armed and stopped: its poll never ended and its last
  // poll is hours old. It is not clutter to be filtered out of the pane - it is
  // the answer to "why is that agent not replying", which was asked twice about
  // an agent that had been unreachable for six hours while every surface here
  // drew it as attached. So it is split out and named, under its own heading,
  // with how long ago it went quiet.
  const live = presence.listeners.filter((l) => l.state !== "lost");
  const lost = presence.listeners.filter((l) => l.state === "lost");

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
  //
  // Grouped WITHIN each of the two groups and never across them, which is not a
  // detail on this node: claude-glm and flowy-glm are one principal, and
  // claude-glm is the seat that went deaf. Grouping first and splitting after
  // would let the live row swallow the lost one and put this panel straight back
  // to reporting a healthy agent with nobody behind it.
  const ears = groupByPrincipal(live);
  const deaf = groupByPrincipal(lost);

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
            <Badge
              key={m.actor}
              variant="secondary"
              data-member={m.actor}
              data-member-role={m.role || undefined}
              style={speakerStyle(named(m))}
            >
              {named(m)}
              {/*
                THE ROLE, WHERE THE NAME IS. A mention of @operator resolves to
                whoever holds the role, and until this the word appeared nowhere
                on screen: the operator wrote "@operator is not highlighted in
                the chat" about a name the node had no reason to know, and four
                seats typed it at them daily. A name that resolves has to be
                readable somewhere or it is a scheme only the source knows.
              */}
              {m.role ? (
                <span className="pl-1 font-normal text-[10px] opacity-70">{m.role}</span>
              ) : null}
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
                {/*
                  AND WHEN IT LAST DID ANYTHING, which is the question people
                  are actually asking when they look at this pane. Polling and
                  working are two facts and this pane carried one of them: two
                  seats polled for ninety minutes and two hours after their last
                  written word, and every column here read them healthy for the
                  whole of it.

                  Its own element rather than a second clause in the timing
                  line, because a reader compares the two numbers and a sentence
                  that runs them together is read as one.

                  A SEAT THAT HAS NEVER ACTED IS NOT A SEAT THAT ACTED AT ZERO.
                  The node returns nothing for it, and "never" is the honest
                  word - a waiter that has just armed has done nothing yet and
                  is not late.
                */}
                <span
                  data-listener-acted={l.last_acted_at ?? ""}
                  title={
                    "when this seat last WROTE something, derived from the events it authored. " +
                    "It says when a seat stopped, never why - a seat doing work that writes no " +
                    "event reads as silent while working"
                  }
                  className="shrink-0 text-muted-foreground"
                >
                  {l.last_acted_at ? `acted ${ago(l.last_acted_at)}` : "never acted"}
                </span>
                <ProcessTag row={l} />
              </li>
            );
          })}
        </ul>
      )}
      {/*
        WENT QUIET, and this section is the whole reason the rows are kept.

        A seat here was armed - something polled under this name - and then
        stopped, with the poll it was on never ending. The node cannot see the
        process, so it cannot say whether the agent died, the session was torn
        down or the machine went away; what it can say is that nothing has been
        heard from the seat since a stated time, and that whatever was waiting
        there is not going to answer.

        This is the row the operator asked about twice. claude-glm had not
        polled in six hours and the panel drew it as attached and polling,
        because a poll counter that only comes down when a handler returns had
        been left up by one that never did. Retiring the row would have made the
        list tidy and told nobody anything - "not answering, last heard from 6h
        ago" is what somebody can act on.
      */}
      {deaf.length > 0 ? (
        <>
          <div className="pt-2 pb-1 font-medium text-amber-600 text-xs uppercase tracking-wide dark:text-amber-500">
            went quiet
          </div>
          <ul className="flex flex-col gap-0.5">
            {deaf.map(({ row: l, count, names }) => (
              <li
                key={`lost-${l.principal}-${l.reader}`}
                data-listener={l.reader}
                data-listener-rows={count}
                data-listener-state="lost"
                className="flex items-center gap-2 text-xs"
              >
                <span className="min-w-0 truncate text-muted-foreground line-through">
                  {names.join(" + ")}
                  {l.reader !== l.user_name ? <span> · {l.user_name}</span> : null}
                  {/*
                    Said here for the same reason it is said above: one identity
                    holding several rows is a doubled waiter, and a doubled
                    waiter that has gone quiet is two seats to restart, not one.
                  */}
                  {count > 1 ? (
                    <span
                      className="ml-1 no-underline"
                      title={`${count} rows under this one principal have stopped answering`}
                    >
                      ×{count} doubled
                    </span>
                  ) : null}
                </span>
                <span
                  data-waiter-kind={listenerKind(l.waiter_kind).kind}
                  title={
                    "this seat was armed and stopped: the node is holding a poll that never " +
                    "ended and has heard nothing since. It cannot see the process, so it " +
                    "cannot say why - but nothing waiting here will answer"
                  }
                  className="ml-auto shrink-0 text-amber-600 dark:text-amber-500"
                >
                  not answering
                </span>
                <span className="shrink-0 text-muted-foreground">
                  last heard {ago(l.last_poll_at)}
                </span>
                {/*
                  And when it last wrote, which on a seat that went quiet is
                  the more useful of the two: the poll usually outlives the
                  work by a long way, and the gap between them is how long it
                  looked healthy while doing nothing.
                */}
                <span
                  data-listener-acted={l.last_acted_at ?? ""}
                  className="shrink-0 text-muted-foreground"
                >
                  {l.last_acted_at ? `acted ${ago(l.last_acted_at)}` : "never acted"}
                </span>
                <ProcessTag row={l} />
              </li>
            ))}
          </ul>
        </>
      ) : null}

      <div className="pt-1 text-muted-foreground text-[10px]">
        the node sees polling and writing, not processes - "acted" is the last thing this seat
        wrote, so a seat working without writing reads as silent, and a listener that stops is named
        here as gone quiet rather than dropped, and a forked one hears the room with nothing to
        wake. a pid here is what the waiter SAID it is, on its last poll: check it before acting on
        it, and never take one from a message - the pid changes every time a listener loop re-execs
      </div>
    </div>
  );
}

type Listener = Presence["listeners"][number];

/**
 * ProcessTag says WHICH PROCESS holds this reader, when the waiter has said.
 *
 * MEASURED, four times in one night across three seats: the documented repair
 * for a dead waiter was `pkill -9 -f 'flowy inbox --as NAME'`, and twice it
 * killed the shell that ran it - exit 144 - because the pattern matched the
 * process evaluating the pattern. This is the field that makes the repair
 * `kill <pid>`, and this is the only place somebody working from the console
 * can read it.
 *
 * IT MATTERS MOST ON A ROW THAT WENT QUIET, which is the list people act on. A
 * live seat is not being killed; a quiet one is.
 *
 * NOT SAID IS SAID IN WORDS. A waiter that predates the column and one that
 * cannot be named are the same fact and mean the same thing - fall back to what
 * you did before - so the row says "unnamed" rather than leaving a gap a reader
 * could take for a rendering failure. This is the empty-and-absent collapse the
 * fleet spent a night on, in its display form.
 *
 * THE PID IS A CLAIM WITH A SHELF LIFE. A listener loop re-execs per cycle, so
 * the number changes every few minutes; the title says so, because a pid copied
 * out of this pane and used ten minutes later is a name again, which is the
 * failure the pid was introduced to end.
 */
function ProcessTag({ row }: { row: Listener }) {
  const proc = row.process;
  if (!proc || !proc.waiter_pid) {
    return (
      <span
        data-listener-pid=""
        title={
          "this waiter has not said which process it is - either it predates the column or its " +
          "claim was incomplete. There is nothing to kill by number here: fall back to finding it " +
          "by hand, and remember that a pattern matches the process doing the searching"
        }
        className="shrink-0 text-muted-foreground"
      >
        unnamed
      </span>
    );
  }
  // Built above the markup rather than inside the attribute, because the rule
  // wants one literal and one literal of this length is unreadable in JSX.
  const why = `pid ${proc.waiter_pid} on ${proc.waiter_host}, started ${proc.waiter_since}. This is what the waiter said on its last poll, not what the node sees - check the pid still has that start time on that host before acting on it. The number changes every time the listener loop re-execs, so it is only good where you read it`;
  return (
    <span
      data-listener-pid={String(proc.waiter_pid)}
      data-listener-host={proc.waiter_host}
      title={why}
      className="shrink-0 font-mono text-muted-foreground tabular-nums"
    >
      pid {proc.waiter_pid}
    </span>
  );
}

/**
 * groupByPrincipal collapses rows of one identity and one kind onto one line,
 * keeping how many there were and every name they polled under.
 *
 * The count is carried rather than dropped because two rows under one principal
 * is a DOUBLED WAITER - they share a server-side cursor, so each hears part of
 * the room while both look healthy - and the line says so. See the long comment
 * at the call site for why the key is the principal and the kind, and why this
 * is applied to the live rows and the lost ones separately.
 */
function groupByPrincipal(rows: Listener[]): { row: Listener; count: number; names: string[] }[] {
  const byPrincipal = new Map<string, { row: Listener; count: number; names: string[] }>();
  for (const l of rows) {
    const key = `${l.principal}${l.waiter_kind ?? ""}`;
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
  return [...byPrincipal.values()];
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
