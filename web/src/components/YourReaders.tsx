import { useCallback, useEffect, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { type InboxReader, api } from "@/lib/api";

/**
 * The readers this token holds: what has your place in the log, and when each
 * of them last moved it.
 *
 * WHY THIS EXISTS, measured on 2026-08-20. Three seats compared their
 * /api/inbox/readers answers in the room and found abandoned readers in all
 * three - console:general, console:handoffs and console:incidents, declared by a
 * browser on the 18th and having acknowledged nothing in two days. Every one of
 * us then named somebody ELSE's as the culprit, twice, because the label
 * "console:general" is worn by one row per principal and none of us checked
 * whose we were reading.
 *
 * `api.inboxReaders()` has been in the client the whole time and NO ROUTE DREW
 * IT. Not filtered, not a permission we lacked - never rendered. So the reason
 * nobody had noticed their own abandoned readers is that there was nowhere to
 * see them.
 *
 * THESE ARE YOURS AND THE PANEL SAYS SO IN ITS FIRST LINE. The door answers
 * `WHERE principal = $1` (internal/store/inbox.go:76) and takes no parameter at
 * all (paramguard.go:71), so this list is this token's and cannot be anybody
 * else's. Saying that on the page is the whole fix for the mistake three seats
 * made in a row: a label is not an identity.
 *
 * WHAT IT DOES NOT SAY IS "STUCK", and that restraint is the point. A reader
 * that has acked nothing because there was nothing to ack, and one that has
 * acked nothing because whatever held it died, look identical in these columns.
 * Telling them apart needs the queue depth above the cursor, which the door does
 * not answer. So this reports the measurement - when the mark last moved, and
 * how many acks it has ever made - and leaves the verdict to the reader, rather
 * than inventing a confident answer out of one of two states. That is the same
 * defect the fleet found six times on 2026-08-19: one code path for two states.
 */

/** The window past which a reader is worth a second look, in hours. */
const QUIET_HOURS = 6;

/**
 * hoursSince is how long ago a stamp was, or null when it will not parse.
 *
 * null rather than 0, because "the node sent something this page cannot read"
 * and "it moved a moment ago" are different facts and the second one is the
 * reassuring one. A bad stamp must not read as fresh.
 */
export function hoursSince(iso: string, now = Date.now()): number | null {
  const at = new Date(iso).getTime();
  if (Number.isNaN(at)) return null;
  return (now - at) / 3_600_000;
}

/**
 * ageReads is what to say about when a mark last moved.
 *
 * It never says "stuck" - see the file comment. "no ack in 45h" is a
 * measurement; whether that is a dead reader or an idle one is a question this
 * data cannot answer.
 */
export function ageReads(iso: string, now = Date.now()): string {
  const hours = hoursSince(iso, now);
  if (hours === null) return "the node sent a time this page cannot read";
  if (hours < 1) return "moved within the hour";
  if (hours < 48) return `no ack in ${Math.floor(hours)}h`;
  return `no ack in ${Math.floor(hours / 24)}d`;
}

/** quiet is whether a reader has been still long enough to be worth reading. */
export function quiet(r: InboxReader, now = Date.now()): boolean {
  const hours = hoursSince(r.updated, now);
  return hours !== null && hours >= QUIET_HOURS;
}

export function YourReaders() {
  const [readers, setReaders] = useState<InboxReader[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState("");
  const [refused, setRefused] = useState<string | null>(null);

  const read = useCallback(async () => {
    try {
      const answer = await api.inboxReaders();
      // The slice as the node sends it, which is null when it is empty - see
      // the nil-slice class that blanked the overview on 2026-08-20.
      setReaders(answer.readers ?? []);
      setError(null);
    } catch (err) {
      setReaders(null);
      setError((err as Error).message);
    }
  }, []);

  useEffect(() => {
    void read();
  }, [read]);

  /**
   * Forgetting a reader is DELETING SOMEBODY'S PLACE IN THE LOG, so it is
   * offered only for the quiet ones and it says what it costs.
   *
   * A live waiter's row is what makes it wake where it left off; dropping that
   * row under a running waiter re-creates it at the head and silently skips
   * everything in between. The node has no way to refuse that on this reader's
   * behalf - polling is the only liveness it has - so the guard is here: the
   * button appears for a reader that has not moved its mark in hours, and the
   * confirmation names what goes.
   */
  const forget = async (name: string) => {
    setBusy(name);
    setRefused(null);
    try {
      await api.deleteInboxReader(name);
    } catch (err) {
      setRefused((err as Error).message);
    }
    setBusy("");
    // Re-read either way: the node's list is the answer, and a panel that
    // removed the row itself would look the same whether or not the delete took.
    await read();
  };

  return (
    <section className="flex flex-col gap-2" data-your-readers="">
      <div className="flex flex-col gap-1">
        <h2 className="font-semibold text-sm">your readers</h2>
        {/*
          WHOSE THESE ARE, said before the list rather than left to be assumed.
          Three seats read this same endpoint on 2026-08-20 and each described
          their own rows as somebody else's, because one label is worn by one
          row per principal.
        */}
        <p className="text-muted-foreground text-xs">
          every waiter holding a place in the log for THIS token. Another seat's readers can carry
          the same labels and are different rows - this list is only ever yours.
        </p>
      </div>

      {error ? (
        <p className="text-destructive text-xs" data-readers-error="">
          {error}
        </p>
      ) : null}

      {readers === null && !error ? (
        <p className="text-muted-foreground text-xs">reading your readers...</p>
      ) : null}

      {readers !== null && readers.length === 0 ? (
        <p className="text-muted-foreground text-xs" data-readers-empty="">
          nothing holds a place in the log for this token - which is not the same as nothing having
          been said to you
        </p>
      ) : null}

      {readers?.map((r) => (
        <div
          key={r.reader}
          data-reader={r.reader}
          data-reader-quiet={quiet(r) ? "" : undefined}
          className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-card p-3 text-xs"
        >
          <span className="font-mono">{r.reader}</span>
          {/*
            The age is the measurement and the badge is only the emphasis. A
            reader with no acks at all is drawn no differently from one with
            thousands: the count is on the row and what it MEANS is not this
            panel's to decide.
          */}
          <Badge variant={quiet(r) ? "outline" : "secondary"} data-reader-age="">
            {ageReads(r.updated)}
          </Badge>
          <span className="text-muted-foreground">
            {r.acked_delivery} delivered, {r.acked_quiet} quiet
          </span>
          <span className="text-muted-foreground">declared {r.created.slice(0, 10)}</span>
          {quiet(r) ? (
            <Button
              size="sm"
              variant="outline"
              className="ml-auto h-6"
              data-reader-forget={r.reader}
              disabled={busy === r.reader}
              onClick={() => void forget(r.reader)}
              title="delete this reader's place in the log - a waiter still using it would resume at the head and skip everything in between"
            >
              {busy === r.reader ? "forgetting..." : "forget"}
            </Button>
          ) : null}
        </div>
      ))}

      {refused ? (
        <p className="text-destructive text-xs" data-readers-refused="">
          {refused}
        </p>
      ) : null}
    </section>
  );
}
