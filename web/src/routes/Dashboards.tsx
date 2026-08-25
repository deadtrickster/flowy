import { useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import type { Artifact } from "@/lib/api";
import { dashboards } from "@/lib/dashboards";
import { useSignedIn } from "@/lib/session";

/**
 * The dashboards: pages agents author for people to read.
 *
 * A dashboard is a declaration, not a program - the rows listed here carry
 * tiles over named metric series, and the view renders whatever the rows
 * hold. Nothing on this page writes one: authoring is an agent's work,
 * through the artifact door, and this is the shelf.
 */
export function Dashboards() {
  const signedIn = useSignedIn();
  const [items, setItems] = useState<Artifact[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!signedIn) {
      setItems([]);
      setLoaded(false);
      return;
    }
    let stopped = false;
    dashboards
      .list()
      .then((page) => {
        if (!stopped) setItems(page.artifacts ?? []);
      })
      .catch((err: Error) => {
        if (!stopped) setError(err.message);
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
        <h1 className="font-semibold text-base">dashboards</h1>
        <span className="text-muted-foreground text-xs">
          what agents are measuring, drawn from the rows they push
        </span>
      </header>

      {error ? <p className="px-4 pt-3 text-destructive text-sm">{error}</p> : null}

      {!signedIn ? (
        <p className="px-4 py-6 text-muted-foreground text-sm">
          log in, or paste a token, to see the dashboards - signed out this is a locked shelf, not
          an empty one
        </p>
      ) : !loaded ? (
        <p className="px-4 py-6 text-muted-foreground text-sm">reading the dashboards…</p>
      ) : items.length === 0 ? (
        <p className="px-4 py-6 text-muted-foreground text-sm">
          no dashboards you can read yet - an agent authors one through the artifact door, with
          fields.tiles declaring what it shows
        </p>
      ) : (
        <ul aria-label="dashboards" className="min-h-0 flex-1 overflow-y-auto">
          {items.map((d) => (
            <li
              key={d.id}
              data-dashboard-row={d.id}
              className="flex flex-col gap-1 border-border border-b px-4 py-3"
            >
              <Link
                className="font-medium text-sm hover:underline"
                to={`/dashboards/${encodeURIComponent(d.id)}`}
              >
                {d.title || d.id}
              </Link>
              <div className="flex flex-wrap items-center gap-2 text-muted-foreground text-xs">
                <Badge variant="outline">{d.visibility ?? "project"}</Badge>
                <span>{d.project ?? "no project"}</span>
                <span>updated {d.updated}</span>
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
