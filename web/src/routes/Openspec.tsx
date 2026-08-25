import { useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { type Artifact, api } from "@/lib/api";
import { openspecStateOf } from "@/lib/openspec";
import { useSignedIn } from "@/lib/session";

/**
 * The openspec board: the spec and change rows, newest first.
 *
 * The rows are ordinary artifacts and the list is the same two kinds POST
 * /api/openspec writes - the fields that make a row openspec are dug in
 * lib/openspec, which is the one place this console reads them. A spec is a
 * capability and a change is a proposal to change one; both are discussed in
 * their own document room like every other artifact, and this board is where
 * they are found without knowing an id first.
 */
export function Openspec() {
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
    api
      .openspec()
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
        <h1 className="font-semibold text-base">openspec</h1>
        <span className="text-muted-foreground text-xs">
          capabilities and the changes proposed to them
        </span>
      </header>

      {error ? <p className="px-4 pt-3 text-destructive text-sm">{error}</p> : null}

      {!signedIn ? (
        <p className="px-4 py-6 text-muted-foreground text-sm">
          log in, or paste a token, to see the openspec board - signed out this is a locked shelf,
          not an empty one
        </p>
      ) : !loaded ? (
        <p className="px-4 py-6 text-muted-foreground text-sm">reading the board…</p>
      ) : items.length === 0 ? (
        <p className="px-4 py-6 text-muted-foreground text-sm">
          no specs or changes filed yet - POST /api/openspec writes them, and the FUSE mount edits
          them
        </p>
      ) : (
        <ul aria-label="openspec" className="min-h-0 flex-1 overflow-y-auto">
          {items.map((row) => {
            const state = openspecStateOf(row);
            return (
              <li
                key={row.id}
                data-openspec={row.id}
                data-openspec-kind={row.kind ?? ""}
                className="flex flex-col gap-1 border-border border-b px-4 py-3"
              >
                <Link
                  className="font-medium text-sm hover:underline"
                  to={`/openspec/${encodeURIComponent(row.id)}`}
                >
                  {row.title || row.id}
                </Link>
                <div className="flex flex-wrap items-center gap-2 text-muted-foreground text-xs">
                  <Badge variant="outline">{row.kind}</Badge>
                  {state ? (
                    <Badge
                      variant={state === "archived" ? "outline" : "default"}
                      data-openspec-state={state}
                    >
                      {state}
                    </Badge>
                  ) : null}
                  <span>{row.project ?? "no project"}</span>
                  <span>updated {row.updated}</span>
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
