/**
 * WHICH THREAD A READER HAS OPEN IN A ROOM, kept across leaving and coming back.
 *
 * Its own file rather than lib/todos, where the preference it is modelled on
 * lives: hideDonePreference is about the todo pane and this is about the thread
 * pane, and a reader looking for one in the other file is how a fact ends up
 * stored twice.
 */
/**
 * WHICH THREAD THIS READER HAD OPEN IN A ROOM, so that leaving and coming back
 * puts it back.
 *
 * @deadtrickster, 01M0JPYYDZ: "visiting other panel and returned to room resets
 * the thread panel. thread panel should restore to the thread I was at when
 * leaving room panel".
 *
 * WHY STORAGE AND NOT THE ROUTE. Switching PANES already works - the pane is a
 * route and ChatRoom stays mounted, so its `opened` state survives. What does
 * not survive is leaving the room altogether: the component unmounts, `opened`
 * goes with it, and the path the sidebar brings you back to names no message.
 * A route cannot fix that, because the route is what was navigated away from.
 *
 * PER ROOM, because coming back to a DIFFERENT room and finding another room's
 * thread would be a new bug of the same kind - a pane showing something the
 * reader did not open here.
 *
 * CLEARED WHEN A THREAD IS PUT DOWN, which is the case that decides the shape.
 * Closing one is a decision, and a pane that resurrects it on the next visit
 * has ignored the reader twice. So this remembers what is OPEN, not what was
 * opened last.
 *
 * try/catch for the same reason hideDonePreference has it: storage switched off
 * is a browser that still has to work, and a thread that does not persist is a
 * smaller failure than a room that does not draw.
 */
const OPEN_THREAD_KEY = "flowy.chat.openThread";

function openThreadKey(room: string) {
  return `${OPEN_THREAD_KEY}.${room}`;
}

export function openThreadIn(room: string): string | undefined {
  try {
    return localStorage.getItem(openThreadKey(room)) || undefined;
  } catch {
    return undefined;
  }
}

export function rememberOpenThread(room: string, message: string | undefined) {
  try {
    if (message) localStorage.setItem(openThreadKey(room), message);
    else localStorage.removeItem(openThreadKey(room));
  } catch {
    // Storage switched off. The thread still holds for the length of the page,
    // which is the same trade hideDonePreference makes.
  }
}
