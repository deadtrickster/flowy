import { useEffect, useState } from "react";

import { type Health, isStale, onHealth } from "@/lib/stream";

/**
 * WHEN THE PANEL LAST HEARD FROM THE NODE.
 *
 * A board that silently stopped listening looks exactly like a board where
 * nothing changed. That is the same shape as a listener reporting "listening"
 * while unable to act, and as a filter that returns everything while filtering
 * nothing: the failure is invisible because success and failure render
 * identically. A visible age is what makes it detectable, and it is the reason
 * this line exists rather than a nicety on top of the stream.
 *
 * IT READS THE HEARTBEAT, NOT THE LAST CHANGE. The node writes a beat every
 * five seconds whether or not anything happened, and this clock moves on that.
 * A version of this that read the last EVENT would freeze the moment the queue
 * went quiet - which is indistinguishable from the node having gone away, and
 * is the defect the whole row was filed about, one layer down.
 *
 * It ticks in a component of its own so that a clock updating every second
 * re-renders eleven words rather than a queue of two hundred rows.
 */
export function StreamAsOf() {
  const [health, setHealth] = useState<Health>({ lastHeard: null, error: null, open: false });
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => onHealth(setHealth), []);
  useEffect(() => {
    const every = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(every);
  }, []);

  // Before the first message there is nothing to be as of. "connecting" is a
  // true statement and "as of never" is not.
  if (health.lastHeard === null) {
    return (
      <span
        data-stream-asof=""
        data-stream-state="connecting"
        className="text-muted-foreground text-xs"
      >
        connecting to the node…
      </span>
    );
  }

  const stale = isStale(health.lastHeard, now) || Boolean(health.error);
  return (
    <span
      // The reading itself, so a check can assert that it ADVANCES rather than
      // that a word is present. A frozen clock and a missing one are different
      // failures and both have to be visible from the outside.
      data-stream-asof={health.lastHeard}
      data-stream-state={stale ? "stale" : "live"}
      className={`text-xs ${stale ? "text-destructive" : "text-muted-foreground"}`}
      title={`last heard from the node at ${new Date(health.lastHeard).toLocaleTimeString()}${
        health.error ? ` - ${health.error}` : ""
      }`}
    >
      as of {ago(now - health.lastHeard)}
      {stale ? ` - not answering${health.error ? `: ${health.error}` : ""}` : null}
    </span>
  );
}

/** The age in words. Seconds while it matters, minutes once it plainly does
 * not - a board that is four minutes behind does not need the seconds, and one
 * that is four seconds behind needs nothing else. */
function ago(ms: number): string {
  const seconds = Math.max(0, Math.round(ms / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  return `${Math.floor(minutes / 60)}h ago`;
}
