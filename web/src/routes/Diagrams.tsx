import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { Artifact } from "@/lib/api";
import { EMPTY_DIAGRAM, diagrams } from "@/lib/diagrams";
import { useSession } from "@/lib/session";

/**
 * The diagrams: draw.io documents that belong to the fabric rather than to
 * somebody's desktop.
 *
 * The reason they are here and not attachments on a message is that a diagram
 * is worked on. An architecture drawing that lands in a room as a png is out of
 * date by the next decision and nobody can correct it; one that is an artifact
 * has a place to be edited, a permission filter like every other list, and
 * shapes that point at the entities it draws.
 */
export function Diagrams() {
  const { token } = useSession();
  const nav = useNavigate();
  const [items, setItems] = useState<Artifact[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [title, setTitle] = useState("");
  const [making, setMaking] = useState(false);

  useEffect(() => {
    if (!token) {
      setItems([]);
      setLoaded(false);
      return;
    }
    let stopped = false;
    diagrams
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
  }, [token]);

  // A new diagram is written before it is opened, rather than opened and
  // written on first edit. An editor holding a document with no id is a
  // document that a closed tab loses, and the id is what the route needs.
  async function create() {
    const named = title.trim();
    if (!named || making) return;
    setMaking(true);
    try {
      const made = await diagrams.write({ title: named, xml: EMPTY_DIAGRAM });
      nav(`/diagrams/${encodeURIComponent(made.id)}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setMaking(false);
    }
  }

  return (
    <div className="flex h-full flex-col">
      <header className="flex flex-wrap items-center gap-2 border-border border-b px-4 py-3">
        <h1 className="font-semibold text-base">diagrams</h1>
        <span className="text-muted-foreground text-xs">
          drawings whose shapes are links to the things they draw
        </span>
        <form
          className="ml-auto flex items-center gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            void create();
          }}
        >
          <Input
            className="h-8 w-56"
            aria-label="new diagram title"
            placeholder="name a new diagram…"
            value={title}
            autoComplete="off"
            onChange={(event) => setTitle(event.target.value)}
          />
          <Button type="submit" size="sm" disabled={!token || !title.trim() || making}>
            {making ? "creating…" : "new"}
          </Button>
        </form>
      </header>

      {error ? <p className="px-4 pt-3 text-destructive text-sm">{error}</p> : null}

      {!token ? (
        <p className="px-4 py-6 text-muted-foreground text-sm">
          paste a token to see the diagrams - signed out this is a locked shelf, not an empty one
        </p>
      ) : !loaded ? (
        <p className="px-4 py-6 text-muted-foreground text-sm">reading the diagrams…</p>
      ) : items.length === 0 ? (
        <p className="px-4 py-6 text-muted-foreground text-sm">
          no diagrams yet - name one above and draw it
        </p>
      ) : (
        <ul aria-label="diagrams" className="min-h-0 flex-1 overflow-y-auto">
          {items.map((d) => (
            <li
              key={d.id}
              data-diagram={d.id}
              className="flex flex-col gap-1 border-border border-b px-4 py-3"
            >
              <Link
                className="font-medium text-sm hover:underline"
                to={`/diagrams/${encodeURIComponent(d.id)}`}
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
