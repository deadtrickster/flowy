import { useCallback, useEffect, useState } from "react";

import { type FlowyEvent, api } from "@/lib/api";

/**
 * The overview's inbox, kept current by the node's waiter rather than by a
 * timer - or by nothing at all, which is what it was.
 *
 * MEASURED 2026-08-20 (01M0EE7B3J): Home.tsx read /api/inbox inside a single
 * useEffect keyed on [token], with no timer anywhere in the file. So the card
 * was fetched once per sign-in and never again. Not a poll that costs - a read
 * that goes stale silently, on the surface an agent is most likely to be
 * sitting in front of.
 *
 * THE WAITER IS THE SIGNAL, NOT THE CONTENT, and that is a deliberate departure
 * from the row's wording. The row asks to "point the inbox at its waiter the
 * way the room is pointed at chatWait". The room's reader is right because
 * unread is inherently cursor-shaped: "what have I not seen". This card is a
 * SNAPSHOT - "what is waiting for me" - with no notion of read, so consuming it
 * through a cursor would invent state nobody asked for, and every open tab
 * would leave a reader row behind. The fleet spent an hour on 2026-08-20
 * tracing abandoned console readers from exactly that shape.
 *
 * So the waiter says WHEN, and GET /api/inbox says WHAT. No timer, nothing
 * repeated while the log is quiet, and the card still shows everything waiting
 * rather than only what arrived since the page opened.
 *
 * A LABEL OF ITS OWN, NEVER THE AGENT'S. /api/inbox/wait reads a durable reader
 * row keyed by name, so two consumers of one name are two writers of one mark -
 * and the console acking could carry the mark past messages the agent had not
 * finished with. That is a silent drop with nothing recording it. This declares
 * `console:inbox`, which is the same shape lib/unread.tsx already keeps per
 * room, and which the readers panel on /profile can now show and drop.
 */

/**
 * The label this console waits under. Its own, and per principal.
 *
 * NOT `console:inbox`, and the collision is worth writing down. `console:<room>`
 * is the namespace lib/unread.tsx keeps a human's unread PLACE in - bookmarks
 * that hold a position and never poll. The room roster excludes them by that
 * prefix, because "is anybody hearing me" must not be answered by a bookmark.
 *
 * This reader polls, so it belongs in that pane, and named `console:inbox` it
 * was refused by a guard whose rule it does not break - measured, as a gate red
 * on roster-check.mjs. The prefix was a proxy for "per-room bookmark" and this
 * is not one, so it lives outside the namespace rather than being let through
 * it.
 */
export const INBOX_READER = "overview:inbox";

/**
 * How long the node holds each request open. The same window the room uses, so
 * an idle console makes one request every twenty-five seconds on this channel
 * rather than one every few.
 *
 * This is a LONG POLL and not a subscription, and the honest reading of "an
 * idle console makes no repeated request" is that it makes no request driven by
 * a CLOCK. The re-arm is driven by the node answering, which is the same
 * mechanism the room has used since it was written.
 */
const WINDOW = 25;

/**
 * How many of the newest messages the card holds. Small on purpose: this is a
 * glance at what arrived, not a reader - the room is where a log is read.
 */
const INBOX_PAGE = 50;

/** How long to hold off after a failed read, so a broken node is not hammered. */
const FAIL_PAUSE = 5_000;

/**
 * useInboxFeed answers the inbox as it is now, and keeps answering.
 *
 * error is the last failure, cleared by the next success. It is reported rather
 * than swallowed because an inbox that cannot be read and an inbox with nothing
 * in it are different facts, and the card says so.
 */
export function useInboxFeed(signedIn: boolean) {
  const [events, setEvents] = useState<FlowyEvent[]>([]);
  const [error, setError] = useState<string | null>(null);

  /**
   * THE NEW END OF THE LOG, and that is not a preference.
   *
   * With no order the door answers the oldest page it has: measured on the live
   * node, 200 events whose newest was four days old, on a node that had taken
   * thousands since. The card was not stale by a page load - it was pinned to
   * the first 200 things ever said to this token. Wiring a waiter to a card
   * that re-reads the wrong end would have been a fix that changed nothing.
   */
  const readNow = useCallback(async () => {
    const page = await api.inbox({ recent: true, limit: INBOX_PAGE });
    // The slice as the node sends it, which is null when it is empty.
    setEvents(page.events ?? []);
    setError(null);
  }, []);

  useEffect(() => {
    if (!signedIn) {
      setEvents([]);
      setError(null);
      return;
    }
    let stopped = false;
    const controller = new AbortController();

    const run = async () => {
      // THE READER IS DECLARED BEFORE THE SNAPSHOT IS READ, and the order is
      // the whole of what makes this lossless. A reader is declared at the HEAD
      // of what the token can read, so anything said between the snapshot and
      // the declaration would sit above the snapshot and below the mark - seen
      // by neither, until something else happened to wake the loop.
      //
      // Measured as a gate red: the check said something a few seconds after
      // the page loaded, the declaration had not landed yet on a busy machine,
      // and the message was never shown. It is one small request before the
      // card fills, not a window.
      try {
        await api.declareInboxReader(INBOX_READER, "cursor");
      } catch {
        // A reader that cannot be declared means no waiter. The snapshot below
        // still fills the card, and the loop reports the failure once when its
        // first wait fails rather than twice here.
      }

      try {
        await readNow();
      } catch (err) {
        if (!stopped) setError((err as Error).message);
      }

      while (!stopped) {
        try {
          const page = await api.inboxWait(INBOX_READER, WINDOW, controller.signal);
          if (stopped) return;
          // ACK WHAT WAS HANDED OVER, so the next wait blocks instead of
          // returning the same page immediately. The mark does not move on
          // delivery - inbox.go:274 - so a wait that never acks turns into a
          // loop that returns at once every time, which is a flood aimed at
          // your own node wearing the shape of a waiter.
          const last = page.events?.[page.events.length - 1];
          if (last) {
            await api.ackInbox(INBOX_READER, last.id);
            // Something arrived, so the SNAPSHOT is what changed - re-read it
            // rather than appending the page. The card lists what is waiting,
            // and a message that has since been answered is no longer waiting.
            await readNow();
          } else {
            setError(null);
          }
        } catch (err) {
          if (stopped) return;
          if (controller.signal.aborted) return;
          setError((err as Error).message);
          await new Promise((resolve) => setTimeout(resolve, FAIL_PAUSE));
        }
      }
    };
    void run();

    return () => {
      stopped = true;
      // The request goes away with the view. A tab that navigated off must not
      // hold a request open on a node it is no longer showing.
      controller.abort();
    };
  }, [signedIn, readNow]);

  return { events, error };
}
