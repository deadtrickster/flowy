import {
  type ReactNode,
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
} from "react";

import { api } from "@/lib/api";
import { useSession } from "@/lib/session";

/**
 * THE CONSOLE IS A READER IN ITS OWN RIGHT.
 *
 * Unread is what the node's inbox holds for this token - chat it may read and
 * did not write - since the reader mark it keeps for it. That mark is what
 * moves when a waiter acks, and it worked for every principal on the node
 * except the one that was complaining: `inbox_readers` had rows for the agents
 * and NO ROW AT ALL for the person reading in a browser. A human runs no
 * waiter, so nothing ever moved their mark, so the badge only ever grew. The
 * rule was not wrong - it had no answer for a reader that is not a process.
 *
 * So the console declares readers of its own and acks what it has actually
 * reached. A person then gets the same mechanism as an agent rather than a
 * second one beside it that can disagree with it.
 *
 * Four things follow from that, and each of them is the alternative that was
 * rejected:
 *
 *   A SEPARATE LABEL, never the principal's own waiter. Acking that one would
 *   mark messages consumed for this principal EVERYWHERE - it is the position
 *   `flowy inbox` resumes from - so reading a room in a browser would eat the
 *   digest an agent under the same identity is waiting to be handed.
 *
 *   THE NODE HOLDS IT, not localStorage. A last-seen in the tab drifts from
 *   the mark the rest of the system believes and is per device: the same room
 *   would be read on the laptop and bold on the desktop. That is the "two
 *   readers of one name see two lists" failure this project already fixed for
 *   todos, and a badge is not worth reintroducing it for.
 *
 *   ONE READER PER ROOM. seq_hlc is one sequence over the whole log, so a
 *   single mark for the console would be moved to the newest message of
 *   whichever room was read last - clearing the badge of every room whose
 *   unread messages happen to sit underneath it. wakesFor in inbox.go has the
 *   same note about the same number for the same reason.
 *
 *   NO READING EVER CROSSES INTO THIS FILE. A mark is a seq_hlc, 57 bits, and
 *   every number in a browser is a double: a reading handed to a console comes
 *   back up to eight readings out. Both halves of that were measured here. The
 *   ack landed two readings short of the message that had just been read, and
 *   left it unread for good; the count, asked for with the mark this console
 *   had been given, answered five unread in a room where nothing had been
 *   said. So the console names messages by id and asks the node for a number:
 *   GET /api/inbox/unread counts, POST /api/inbox/ack takes the id of the last
 *   message read. Nothing here does arithmetic on the log.
 */

/**
 * The rooms the sidebar offers by name, and therefore the rooms the console
 * keeps a mark for. One list rather than two, because a room with a badge and
 * no reader behind it is exactly the bug above. Any other room is still
 * reachable by URL; it has no badge, so there is nothing to clear.
 */
export const ROOMS = ["general", "handoffs", "incidents"];

/** The reader label this console keeps for one room, per principal. */
export function consoleReader(room: string): string {
  return `console:${room}`;
}

/** How often the badges are refilled from the node. */
const REFRESH_MS = 20_000;

interface Unread {
  /** How many unread messages each room holds, by room name. */
  counts: Record<string, number>;
  /**
   * markRead says the reader has REACHED this message in a room - the newest
   * one on screen with the transcript sitting at the bottom, not the fact that
   * the room was opened. A room that was opened and scrolled back through has
   * not been read to the end and does not clear.
   */
  markRead: (room: string, message: string) => void;
}

const UnreadContext = createContext<Unread>({ counts: {}, markRead: () => {} });

export function UnreadProvider({ children }: { children: ReactNode }) {
  const { token } = useSession();
  const [counts, setCounts] = useState<Record<string, number>>({});
  // The last message this console has read in each room, and the last one the
  // node has confirmed. They differ while an ack is in flight or has failed,
  // and the difference is what the refresh retries. Refs rather than state:
  // nothing renders from them, and a render per arriving message would be a
  // cost for no picture.
  const reached = useRef<Record<string, string>>({});
  const acked = useRef<Record<string, string>>({});

  useEffect(() => {
    reached.current = {};
    acked.current = {};
    if (!token) {
      setCounts({});
      return;
    }
    let stopped = false;

    const load = async () => {
      const held = await api.inboxReaders();
      if (stopped) return;
      const declared = new Set(held.readers.map((reader) => reader.reader));
      const next: Record<string, number> = {};
      for (const room of ROOMS) {
        const label = consoleReader(room);
        if (!declared.has(label)) {
          // No row: nobody has ever read this room from a console under this
          // token. Declaring starts AT THE HEAD, which is where `flowy inbox
          // --new` starts a waiter and for the same reason - everything said
          // before somebody first opened the console is history rather than
          // unread, and a first load that reported the whole log as unread
          // would be a number nobody can act on and nobody can clear.
          await api.declareInboxReader(label);
          if (stopped) return;
        }
        // Anything read while the reader did not exist yet, or while an ack
        // was failing, is acked here. Without this a room read in the same
        // breath as the first refresh would stay bold until somebody said
        // something in it again.
        const read = reached.current[room];
        if (read && read !== acked.current[room]) {
          await api.ackInbox(label, read);
          if (stopped) return;
          acked.current[room] = read;
        }
        next[room] = (await api.unreadIn(label, room)).unread;
        if (stopped) return;
      }
      setCounts(next);
    };

    // Swallowed: a badge is not worth a banner, and the next tick tries again.
    const tick = () => void load().catch(() => {});
    tick();
    const every = setInterval(tick, REFRESH_MS);

    // A CLOSED TAB TAKES ITS BOOKMARKS WITH IT.
    //
    // The console readers never poll - they hold a mark and ack what the room
    // shows - so a roster reads them as ghosts the moment their tab is gone,
    // and before this they stayed forever: nothing but a person could delete
    // them. pagehide with persisted false is the tab actually finishing, as
    // against being stashed for a return (a back-forward restore, an app
    // switch on a phone), and only that half deletes: a stashed tab that comes
    // back would find its rows gone and re-declare them at the head, and
    // everything said while it was away would arrive as read.
    //
    // TWO TABS still race, and deliberately: the closing one takes rows the
    // open one keeps using, whose next refresh re-declares them at the head.
    // The cost is one room's badge resetting in the seconds a second tab
    // closed in, against rows that never die otherwise.
    const bye = (leaving: PageTransitionEvent) => {
      if (leaving.persisted) return;
      for (const room of ROOMS) {
        void api.deleteInboxReader(consoleReader(room), true).catch(() => {});
      }
    };
    addEventListener("pagehide", bye);
    return () => {
      stopped = true;
      clearInterval(every);
      removeEventListener("pagehide", bye);
    };
  }, [token]);

  const markRead = useCallback(
    (room: string, message: string) => {
      if (!token || !message || !ROOMS.includes(room)) return;
      // Never the same message twice: in a room being read as it fills this
      // fires on every arrival. Going backwards is not a case that has to be
      // handled here - the node only ever moves a mark forward, which is what
      // stops two tabs, or two of this person's browsers, fighting over it.
      if (message === reached.current[room]) return;
      reached.current[room] = message;

      const label = consoleReader(room);
      void (async () => {
        try {
          await api.ackInbox(label, message);
          acked.current[room] = message;
        } catch {
          // Left unacknowledged on purpose, so the refresh above retries it.
          // A room that is open and at the bottom is one where the reader is
          // reading; a badge that clears a tick late is not worth a banner.
        }
      })();

      // The transcript is at the bottom of THIS room, so there is nothing in
      // it left to read. Said now rather than waited for, because a badge that
      // clears on the next refresh is a badge that looks stuck for twenty
      // seconds - and the refresh is what corrects it if the ack did not land.
      setCounts((current) => (current[room] ? { ...current, [room]: 0 } : current));
    },
    [token],
  );

  return <UnreadContext.Provider value={{ counts, markRead }}>{children}</UnreadContext.Provider>;
}

/** The unread counts and the way to clear one, for the shell and the room. */
export function useUnread(): Unread {
  return useContext(UnreadContext);
}
