import { useEffect, useState } from "react";

import { watchFreshness } from "@/lib/fresh";

/**
 * Says the tab is behind, on the rare occasion it cannot just fix itself.
 *
 * Staleness is normally invisible AND repaired without asking: watchFreshness
 * reloads the page when the node starts serving a different console. This bar
 * is the case where reloading would take something away - somebody is mid
 * sentence in the message box, or the reload already happened and the tab is
 * still behind, which means something between here and the node is serving an
 * old index and no amount of reloading will fix it.
 *
 * It is deliberately a bar and not a toast: a toast that disappears is exactly
 * the wrong shape for a condition that persists until somebody acts.
 */
export function FreshBanner() {
  const [stale, setStale] = useState(false);

  useEffect(() => watchFreshness((state) => setStale(state.stale)), []);

  if (!stale) return null;

  return (
    <div className="flex items-center gap-3 border-amber-500/40 border-b bg-amber-500/10 px-4 py-2 text-amber-200 text-xs">
      <span>
        this tab is running an older console than the node is serving. It reloads itself once you
        are not typing.
      </span>
      <button
        type="button"
        className="ml-auto rounded border border-amber-500/40 px-2 py-0.5 hover:bg-amber-500/20"
        onClick={() => window.location.reload()}
      >
        reload now
      </button>
    </div>
  );
}
