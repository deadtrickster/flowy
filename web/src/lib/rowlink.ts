/**
 * ROW-ID LINKS: a clicked row id resolves to the row it names.
 *
 * The renderer (lib/markdown.ts) links a bare row id to the resolver route
 * /a/<ulid>, and the node answers that route with a 302 to the row's own page.
 * That answer is for a browser that carries a session cookie, where the plain
 * navigation the anchor asks for is exactly how the credential travels. A
 * token seat has no cookie: its token lives in localStorage, lib/api.ts
 * attaches it to every FETCH, and it rides no navigation - a clicked id would
 * 401 for the precise audience the ids are written for.
 *
 * So the console intercepts the click and resolves through the same door the
 * resolver resolves through - GET /api/artifact/{id}, same permission filter,
 * token attached by request() - then opens the row's page itself. The anchor's
 * href stays the resolver's, so a cookie browser without this script still
 * lands on the row, and the 302/404 honesty of /a/ stays the node's alone.
 */

import { useEffect } from "react";

import { ApiError, type Artifact, artifactPath, request } from "@/lib/api";

/** resolveRowLink turns one resolver href into the row's own page in a new tab. */
async function resolveRowLink(href: string): Promise<void> {
  const id = href.slice("/a/".length);
  let row: Artifact | null = null;
  try {
    row = await request<Artifact>(`/api/artifact/${encodeURIComponent(id)}`);
  } catch (err) {
    // A row that is gone still gets its page: ArtifactView knows how to say
    // "not found" honestly, which is the resolver's own answer. Anything else
    // - a dead credential above all - has already said its piece (request()
    // raises the sign-in banner on 401) and a page that would answer the same
    // way is no answer.
    if (!(err instanceof ApiError) || err.status !== 404) return;
  }
  const path = artifactPath(row ?? { id });
  if (path) window.open(path, "_blank", "noopener");
}

/**
 * useRowLinkClicks wires the interception above to the whole console, once.
 *
 * A listener on the document rather than on any view's container, because the
 * links render inside markdown bodies - the room, a report, a row body - and
 * a per-view handler is one more place to forget the next time a body renders.
 * Modifier clicks are left alone: cmd-click means "new tab" and the browser's
 * own handling of the anchor's target=_blank is already exactly that.
 */
export function useRowLinkClicks(): void {
  useEffect(() => {
    const onClick = (event: MouseEvent) => {
      if (event.defaultPrevented || event.button !== 0) return;
      if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      const anchor = (event.target as Element | null)?.closest?.('a[href^="/a/"]');
      if (!anchor) return;
      event.preventDefault();
      void resolveRowLink(anchor.getAttribute("href") ?? "");
    };
    document.addEventListener("click", onClick);
    return () => document.removeEventListener("click", onClick);
  }, []);
}
