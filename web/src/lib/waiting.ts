import { useEffect, useState } from "react";

import { api } from "@/lib/api";
import { useSignedIn } from "@/lib/session";

/**
 * How much is waiting for THIS principal, for the two rail rows where that
 * question has an answer.
 *
 * WHY ONLY TWO, when thirteen rows carry no number. The row this closes
 * (01M0GGEW74) reads "the rail carries one number and thirteen rows carry
 * none", and the obvious reading - put a number on all thirteen - is the wrong
 * one. A badge is a claim that something is waiting for you, and it earns its
 * place by CLEARING. Three kinds of row were sorted through and only one kind
 * qualifies:
 *
 *   inbox, todos      work handed to this principal, in a state that ends.
 *                     A number here goes down when you do the work, so it is
 *                     worth looking at. These get a badge.
 *
 *   reports, findings,
 *   projects, memory,
 *   diagrams, new     how many EXIST. Never zero on a working node and never
 *                     going down, so a badge would be decoration that trains a
 *                     reader to stop reading badges - including the two above
 *                     it that mean something. No badge. "Did it move since I
 *                     looked" is the honest question for these and it needs a
 *                     read mark per list, which the node does not keep.
 *
 *   worklog, activity,
 *   metrics, traces,
 *   profile           streams and settings. Nothing is waiting in a stream.
 *                     No badge.
 *
 *   direct            genuinely waiting-for-you, and it gets NO badge here,
 *                     which is the one uncomfortable answer. /api/dm has no
 *                     reader mark - see api.dms and api.dmWait, both of which
 *                     take a raw cursor the tab holds in memory - so there is
 *                     nothing on the node that says which DMs this person has
 *                     read. A count of all of them would be a badge that never
 *                     clears, which is the failure described two paragraphs up,
 *                     and inventing a mark here is a store change and not a
 *                     console one. Filed rather than faked.
 *
 * NULL IS NOT ZERO, and this is the whole reason the counts are typed
 * `number | null`. "Nothing is waiting for you" and "we could not ask" are
 * different facts and the second one has been rendered as the first twelve
 * separate times in this codebase's history. A failed read leaves the count
 * null and the badge absent - the same as no work - but the DISTINCTION is kept
 * here rather than collapsed at the fetch, so a caller that wants to say "could
 * not read" can, and the next one does not have to re-derive it.
 */
export interface Waiting {
  /** Open tasks handed to this principal - what /inbox lists. Null if unread. */
  tasks: number | null;
  /** Open todo rows assigned to this principal. Null if unread. */
  todos: number | null;
}

/**
 * The same twenty seconds the unread badges refill on, and deliberately the
 * same number rather than a second one: two intervals on one sidebar is two
 * moments where the rail disagrees with itself, and a reader watching one
 * number change while another does not is reading a bug that is not there.
 */
const REFRESH_MS = 20_000;

/**
 * useWaiting keeps the two counts current.
 *
 * TWO CALLS, NOT ONE, because they answer to different owners: /api/tasks is
 * the task inbox and /api/nag is the board's own count of this principal's open
 * rows. Summing them here would invent a third number that neither page shows,
 * and the point of a rail badge is that clicking it takes you to the list it
 * counted.
 *
 * They also fail independently, which is why each is caught on its own. One
 * try/catch around both would mean a broken /api/nag hiding a working task
 * count - a whole badge gone for a fault that had nothing to do with it.
 */
export function useWaiting(): Waiting {
  const signedIn = useSignedIn();
  const [waiting, setWaiting] = useState<Waiting>({ tasks: null, todos: null });

  useEffect(() => {
    if (!signedIn) {
      setWaiting({ tasks: null, todos: null });
      return;
    }
    let stopped = false;

    const load = async () => {
      // OPEN ONLY. A delegated task is somebody else's turn and a done one is
      // nobody's; counting either would give a badge that goes up when work is
      // handed on. Asked of the node by state rather than filtered here, so the
      // number is the node's answer to the same question the page asks.
      const tasks = await api
        .tasks("open")
        .then((page) => page.tasks.length)
        .catch(() => null);
      if (stopped) return;
      // mine_todo, not mine: `mine` counts every row assigned to this principal
      // including the ones already finished, and the rail is asking what is
      // left to do. This is the number the board nag reports.
      const todos = await api
        .nag()
        .then((view) => view.mine_todo)
        .catch(() => null);
      if (stopped) return;
      setWaiting({ tasks, todos });
    };

    void load();
    const every = setInterval(() => void load(), REFRESH_MS);
    return () => {
      stopped = true;
      clearInterval(every);
    };
  }, [signedIn]);

  return waiting;
}
