/**
 * A tab that has been open across a deploy is running code nobody is looking at
 * any more, and nothing on the screen says so.
 *
 * This is not a convenience. The console flooded the node at 567 requests a
 * second for days after the fix had shipped, because the tab holding the bug
 * never reloaded - the deploy was real, the fix was real, and the running page
 * was neither. A stale tab is the one place a repaired bug keeps happening.
 *
 * The node reports the hashed bundle its index.html loads (GET /api/node), the
 * page reads the hashed bundle it was itself loaded from, and a mismatch means
 * the tab is behind. The fingerprint is Vite's content hash, so it changes
 * exactly when the bytes change: nothing to remember to bump, and no way for it
 * to claim one thing while serving another.
 */

import { api } from "@/lib/api";

/** How often to ask. The answer changes on a deploy, so this is a slow fact. */
const EVERY_MS = 30_000;

/**
 * The bundle this page is running, taken from its own script tag.
 *
 * In dev there is no hashed asset and this is empty, which reads as "nothing to
 * compare" rather than "stale" - the wrong way round would reload the dev
 * server's page in a loop forever.
 */
function runningBundle(): string {
  const scripts = [...document.querySelectorAll<HTMLScriptElement>('script[src*="/assets/"]')];
  const src = scripts.map((s) => s.src).find((s) => s.endsWith(".js"));
  return src ? (src.split("/assets/").pop() ?? "") : "";
}

/**
 * Whether somebody is mid-sentence. Reloading out from under a half-typed
 * message would lose it, and losing what somebody wrote to save them a click is
 * a bad trade - so when the box has text in it the page stays put and says so
 * instead.
 */
function isTyping(): boolean {
  const el = document.activeElement as HTMLInputElement | HTMLTextAreaElement | null;
  if (!el) return false;
  const tag = el.tagName;
  if (tag !== "TEXTAREA" && tag !== "INPUT") return false;
  return Boolean(el.value);
}

/**
 * Reloading once for a given bundle, and never twice.
 *
 * If the reload does not actually change what is running - a cached index, a
 * proxy serving yesterday's html - the mismatch is still there afterwards, and
 * a page that reloads on mismatch would reload forever. A RELOAD LOOP IS WORSE
 * THAN THE STALENESS IT IS FIXING: it is unusable rather than merely old. So
 * the target is recorded before reloading and a second attempt at the same
 * target is refused, which leaves the banner as the fallback.
 */
const RELOADED_FOR = "flowy.reloadedFor";

export type Freshness = { stale: boolean; bundle: string };

/**
 * watchFreshness polls the node and calls back when this tab is behind.
 *
 * It reloads by itself when that is safe, and reports staleness when it is not,
 * so the caller can say so on screen. Returns its own stop function.
 */
export function watchFreshness(onStale: (state: Freshness) => void): () => void {
  const mine = runningBundle();
  let stopped = false;

  const look = async () => {
    if (stopped || !mine) return;
    let theirs = "";
    try {
      const node = await api.node();
      theirs = node.bundle ?? "";
    } catch {
      // A node that cannot be reached says nothing about freshness. The room's
      // own poll already surfaces "not watching", so this stays quiet rather
      // than adding a second alarm for one problem.
      return;
    }
    if (stopped || !theirs || theirs === mine) {
      if (theirs && theirs === mine) sessionStorage.removeItem(RELOADED_FOR);
      return;
    }

    onStale({ stale: true, bundle: theirs });

    if (sessionStorage.getItem(RELOADED_FOR) === theirs) return;
    if (isTyping()) return;
    sessionStorage.setItem(RELOADED_FOR, theirs);
    window.location.reload();
  };

  void look();
  const every = setInterval(look, EVERY_MS);
  return () => {
    stopped = true;
    clearInterval(every);
  };
}
