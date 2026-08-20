import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { type Artifact, api, artifactPath } from "@/lib/api";
import { shortId } from "@/lib/utils";
import { useEffect, useState } from "react";

/**
 * The rows a `related` list points at, each one readable and each one a link.
 *
 * THE OPERATOR: "pathetic - artifacts are just names not lists." Literally what
 * it was: every related artifact rendered as eight characters of a ULID in a
 * monospace badge. A reader could not tell what any of them was, could not tell
 * two apart, and could not get to one without copying the id somewhere else.
 *
 * The old comment gave a real reason for the bare id - a link built from THIS
 * artifact's project and type would point at the wrong row as often as not,
 * which is what refPath exists to refuse. That was true, and the conclusion
 * drawn from it was wrong: "cannot GUESS it" was treated as "cannot KNOW it".
 * The node knows. /api/artifact/{id} takes a bare id and answers with the row's
 * own project, type and title, so the link is read off the child rather than
 * assembled out of the parent - the same rule refPath keeps, not a way round it.
 */
export function RelatedRows({ ids }: { ids: string[] }) {
  if (ids.length === 0) return null;
  return (
    <div className="flex flex-col gap-1">
      {ids.map((id) => (
        <RelatedRow key={id} id={id} />
      ))}
    </div>
  );
}

/**
 * One of them, resolved on its own.
 *
 * Separately rather than in one batch because there is no batch door, and a
 * loop of small reads that each render when they land beats one wait for the
 * slowest: a related list of eight should not be blank until the eighth
 * arrives.
 */
function RelatedRow({ id }: { id: string }) {
  const [item, setItem] = useState<Artifact | null>(null);
  // Why it is not here, kept apart from "not loaded yet". A row nobody can read
  // and a row that has not arrived look identical while the fetch is in flight,
  // and only one of them is worth telling the reader about.
  const [why, setWhy] = useState<string | null>(null);

  useEffect(() => {
    let stopped = false;
    setItem(null);
    setWhy(null);
    api
      .artifact(id)
      .then((got) => {
        if (!stopped) setItem(got);
      })
      .catch((err: Error) => {
        if (!stopped) setWhy(err.message);
      });
    return () => {
      stopped = true;
    };
  }, [id]);

  // A ROW THAT CANNOT BE RESOLVED SAYS SO, IN ITS OWN ROW. Dropping it would
  // make the list disagree with the count beside it, and rendering it as a bare
  // id is what this component exists to stop. "You may not read it" and "this
  // node does not have it" are the same answer from the read door on purpose,
  // so the sentence it sends is the honest thing to show.
  if (why) {
    return (
      <div
        data-related={id}
        data-related-state="refused"
        className="flex items-center gap-2 rounded-md border border-border border-dashed px-2 py-1 text-xs"
      >
        <span className="font-mono text-muted-foreground">{shortId(id)}</span>
        <span className="min-w-0 truncate text-muted-foreground">{why}</span>
      </div>
    );
  }

  if (!item) {
    return (
      <div
        data-related={id}
        data-related-state="loading"
        className="flex items-center gap-2 rounded-md border border-border px-2 py-1 text-muted-foreground text-xs"
      >
        <span className="font-mono">{shortId(id)}</span>
        <span>reading…</span>
      </div>
    );
  }

  const to = artifactPath(item);
  const title = item.title?.trim() || <span className="text-muted-foreground">no title</span>;
  const inside = (
    <>
      {item.type ? <Badge variant="outline">{item.type}</Badge> : null}
      <span className="min-w-0 truncate">{title}</span>
      {item.project ? (
        <span className="ml-auto shrink-0 text-muted-foreground">{item.project}</span>
      ) : null}
    </>
  );

  // artifactPath needs an id and nothing else, so `to` is only ever undefined
  // for a row with no id at all - which cannot happen for one the node just
  // answered with. The branch stays because a link is the one thing here that
  // must not be built on an assumption.
  if (!to) {
    return (
      <div
        data-related={id}
        data-related-state="unlinkable"
        className="flex items-center gap-2 rounded-md border border-border px-2 py-1 text-xs"
      >
        {inside}
      </div>
    );
  }

  return (
    <Link
      to={to}
      data-related={id}
      data-related-state="linked"
      className="flex items-center gap-2 rounded-md border border-border px-2 py-1 text-xs hover:bg-accent/60"
      title={id}
    >
      {inside}
    </Link>
  );
}
