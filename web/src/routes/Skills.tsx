import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { type Artifact, artifactPath } from "@/lib/api";
import { useSignedIn } from "@/lib/session";
import { skills } from "@/lib/skills";

/**
 * The skills shelf: procedures the fabric keeps, so a thing that was hard once
 * is hard only once.
 *
 * A skill is a memory row of kind `skill` whose body is the procedure in GFM -
 * see lib/skills.ts for the ruling. This page is the collection: the list, and
 * a way to write one. The row itself opens on the ordinary artifact page,
 * where the body renders as markdown and the owner can edit it, so a skill is
 * a shelf entry here and a document there, not a second document system.
 */
export function Skills() {
  const signedIn = useSignedIn();
  const nav = useNavigate();
  const [items, setItems] = useState<Artifact[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [making, setMaking] = useState(false);

  useEffect(() => {
    if (!signedIn) {
      setItems([]);
      setLoaded(false);
      return;
    }
    let stopped = false;
    skills
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

  // The button is never disabled - the diagrams page learned that a disabled
  // button and a dead page look the same from outside. A skill without a name
  // or a body is refused HERE, with a sentence, instead of silently: the row
  // is written before it is opened, and an empty procedure is not a skill.
  async function create() {
    if (making) return;
    if (!title.trim() || !body.trim()) {
      setError("a skill needs a name and a body - it is a procedure, not a placeholder");
      return;
    }
    setMaking(true);
    setError(null);
    try {
      const made = await skills.write({ title: title.trim(), body });
      nav(artifactPath(made) ?? `/p/_/_/${encodeURIComponent(made.id)}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setMaking(false);
    }
  }

  return (
    <div className="flex h-full flex-col">
      <header className="flex flex-wrap items-center gap-2 border-border border-b px-4 py-3">
        <h1 className="font-semibold text-base">skills</h1>
        <span className="text-muted-foreground text-xs">
          how to do a thing here, kept where the fleet can read it
        </span>
        <form
          className="ml-auto flex items-center gap-2"
          autoComplete="off"
          onSubmit={(event) => {
            event.preventDefault();
            void create();
          }}
        >
          <Input
            className="h-8 w-48"
            aria-label="new skill title"
            placeholder="name it…"
            value={title}
            autoComplete="off"
            onChange={(event) => setTitle(event.target.value)}
          />
          <textarea
            aria-label="new skill body"
            rows={2}
            placeholder="the procedure, markdown…"
            value={body}
            onChange={(event) => setBody(event.target.value)}
            className="w-96 rounded border border-border bg-background px-2 py-1 font-mono text-foreground text-xs"
          />
          <Button type="submit" size="sm" disabled={!signedIn || making}>
            {making ? "creating…" : "new"}
          </Button>
        </form>
      </header>

      {error ? <p className="px-4 pt-3 text-destructive text-sm">{error}</p> : null}

      {!signedIn ? (
        <p className="px-4 py-6 text-muted-foreground text-sm">
          paste a token to see the skills - signed out this is a locked shelf, not an empty one
        </p>
      ) : !loaded ? (
        <p className="px-4 py-6 text-muted-foreground text-sm">reading the skills…</p>
      ) : items.length === 0 ? (
        <p className="px-4 py-6 text-muted-foreground text-sm">
          no skills yet - name one above and write the procedure
        </p>
      ) : (
        <ul aria-label="skills" className="min-h-0 flex-1 overflow-y-auto">
          {items.map((d) => (
            <li
              key={d.id}
              data-skill={d.id}
              className="flex flex-col gap-1 border-border border-b px-4 py-3"
            >
              <Link
                className="font-medium text-sm hover:underline"
                to={artifactPath(d) ?? `/p/_/_/${encodeURIComponent(d.id)}`}
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
