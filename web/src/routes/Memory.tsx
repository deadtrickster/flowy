import { useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import type { Artifact } from "@/lib/api";
import { api, artifactPath } from "@/lib/api";
import { useSignedIn } from "@/lib/session";

/**
 * The memory page.
 *
 * It was a tab under todos first, which was wrong and the operator said so in
 * three words: memory is not a kind of queue. Notes, decisions and handoffs are
 * what this fabric KNOWS; todos are what it is going to DO. Neither is a view of
 * the other, so memory gets its own place in the nav rather than living inside
 * the thing it is not.
 *
 * Read only. Writing here would be a second door onto rows whose scope rules
 * live in the store, and the point of the page is to see what is there - which
 * nothing in the console could do until now, so a note could be written,
 * searched by an agent, and never seen by the person who asked for it.
 */
export function Memory() {
  const signedIn = useSignedIn();
  const [items, setItems] = useState<Artifact[]>([]);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    if (!signedIn) {
      setItems([]);
      setLoaded(false);
      return;
    }
    let stopped = false;
    api
      .notes()
      .then((n) => {
        if (!stopped) setItems(n.artifacts ?? []);
      })
      .catch(() => {
        if (!stopped) setItems([]);
      })
      .finally(() => {
        if (!stopped) setLoaded(true);
      });
    return () => {
      stopped = true;
    };
  }, [signedIn]);

  return (
    <div className="flex h-full flex-col">
      <header className="flex flex-wrap items-center gap-2 border-border border-b px-4 py-3">
        <h1 className="font-semibold text-base">memory</h1>
        <span className="text-muted-foreground text-xs">
          what this fabric knows, as far as you may read it
        </span>
        {loaded ? (
          <span className="ml-auto text-muted-foreground text-xs">{items.length} notes</span>
        ) : null}
      </header>
      {!loaded ? (
        <p className="px-4 py-6 text-muted-foreground text-sm">reading memory…</p>
      ) : items.length === 0 ? (
        <p className="px-4 py-6 text-muted-foreground text-sm">
          no notes you can read. Scope decides that, not this page - a personal note is its author's
          alone, and this list runs the same permission filter as everything else.
        </p>
      ) : (
        <ul className="min-h-0 flex-1 overflow-y-auto">
          {items.map((n) => (
            <li key={n.id} className="flex flex-col gap-1 border-border border-b px-4 py-3">
              <Link
                className="font-medium text-sm hover:underline"
                to={artifactPath({ project: n.project, type: n.type, id: n.id }) ?? "#"}
              >
                {n.title || n.id}
              </Link>
              <div className="flex flex-wrap items-center gap-2 text-muted-foreground text-xs">
                {/* Scope first: "who else can see this" is the question a person
                    has about a note, and no list has ever answered it. */}
                <Badge variant="outline">{n.visibility ?? "personal"}</Badge>
                <span>{n.project ?? "no project"}</span>
                {n.body ? <span className="truncate">{n.body.slice(0, 120)}</span> : null}
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
