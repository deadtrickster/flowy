import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { Artifact } from "@/lib/api";
import { EMPTY_DIAGRAM, diagrams } from "@/lib/diagrams";
import { useSession } from "@/lib/session";

/**
 * What a diagram nobody named is called. It is a real title rather than an
 * empty one because every list in this console falls back to the id when a
 * title is missing, and a shelf of ULIDs is not a shelf anybody can read.
 */
const UNTITLED = "untitled diagram";

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
  //
  // The name is not a precondition for the drawing. This button used to be
  // disabled until the box beside it had something in it, which is how it was
  // reported as broken: an operator who does not read the empty box as a
  // required field clicks new, gets no navigation, no error and no cursor
  // change, and has been told nothing at all - a dead button and a dead page
  // look exactly the same from the outside. So new always makes a diagram. One
  // made without a name gets UNTITLED, and the editor is where it is renamed.
  async function create() {
    if (making) return;
    const named = title.trim() || UNTITLED;
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
            placeholder="name it, or leave it blank…"
            value={title}
            autoComplete="off"
            onChange={(event) => setTitle(event.target.value)}
          />
          {/*
            Signed out is the only state that disables this: there is no token
            to write with, and the page says so under the header. An empty name
            is not a reason to refuse - see create().
          */}
          <Button type="submit" size="sm" disabled={!token || making}>
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
