import { type ProjectsPage, api } from "@/lib/api";
import { useEffect, useState } from "react";

/**
 * WHAT A PROJECT IS, on a page, because until now it was two doors and no
 * surface.
 *
 * MEASURED on 2026-08-20: the node has GET and POST /api/projects, lib/api.ts
 * calls the first one, and NO ROUTE DREW EITHER. Projects existed, could be
 * declared, and had nowhere to be seen. The operator, in the room the same
 * morning: "still no project management what so ever".
 *
 * The page answers the question that actually cost time, which is not "what
 * projects exist" but WHICH ONE AM I IN. Two messages were written into pa's
 * #general instead of flowy's, by a token nobody had checked, and the only
 * thing that ever said so was the line `flowy say` prints AFTER the fact - see
 * saidWhere in say.go. Every project has a #general; a room name is not an
 * address once there is more than one project, and there are three here.
 *
 * READ-ONLY, DELIBERATELY. Declaring a project from the console is the next row
 * and a different one: POST /api/projects exists and nothing calls it, so
 * adding a write here would be the second thing this page does before the first
 * one has been used.
 *
 * WHAT THIS PAGE CANNOT SHOW, and says so rather than leaving a gap: what is
 * INSIDE another project. GET /api/rooms takes no project parameter
 * (paramguard.go:101) - it answers about the caller's own - so a room list per
 * project is a store change, not a fetch this page forgot to make.
 */
export function Projects() {
  const [page, setPage] = useState<ProjectsPage | null>(null);
  const [failed, setFailed] = useState("");
  // The rooms inside each project this token can read, keyed by project.
  //
  // ASKED ONLY FOR THE ONES `reads` NAMES. Asking about the others would earn a
  // 403 per row and put a refusal on screen for a rule the page already knows -
  // and a page that fires requests it knows will be refused teaches its reader
  // that refusals are normal.
  const [rooms, setRooms] = useState<Record<string, string[]>>({});

  useEffect(() => {
    let live = true;
    api
      .projects()
      .then((answer) => {
        if (live) setPage(answer);
      })
      .catch((err) => {
        // A LIST THAT COULD NOT BE READ IS NOT AN EMPTY LIST. Rendering nothing
        // here would say "this node has no projects", which is a fact about the
        // node rather than about the request that failed.
        if (live) setFailed(String(err));
      });
    return () => {
      live = false;
    };
  }, []);

  // A second pass once the registry is in, because which projects to ask about
  // is an answer from the first one.
  useEffect(() => {
    if (!page) return;
    let live = true;
    const readable = page.reads ?? [];
    for (const id of readable) {
      api
        .rooms(id)
        .then((answer) => {
          // KEYED BY WHAT THE NODE SAID IT WAS ABOUT, not by what was asked.
          // Three requests are in flight at once and they answer in whatever
          // order they answer; keying by the request would put one project's
          // rooms under another's name the first time that ordering surprised
          // somebody.
          if (live) {
            setRooms((was) => ({ ...was, [answer.project]: answer.rooms.map((r) => r.name) }));
          }
        })
        .catch(() => {
          // Left absent rather than set to empty: a project whose rooms could
          // not be read must not draw as a project with no rooms.
        });
    }
    return () => {
      live = false;
    };
  }, [page]);

  if (failed) {
    return (
      <div className="p-6" data-projects-error>
        <h1 className="font-semibold text-xl tracking-tight">projects</h1>
        <p className="mt-2 text-muted-foreground text-sm">
          the node did not answer this list: {failed}. That is not the same as having no projects -
          nothing here is known either way.
        </p>
      </div>
    );
  }
  if (!page) {
    return (
      <div className="p-6 text-muted-foreground text-sm" data-projects-loading>
        reading the registry…
      </div>
    );
  }

  const reads = page.reads ?? [];
  return (
    // The current project is on the OUTER element, so there is one place that
    // answers "which project is this page about" - the first cut put it on the
    // panel and the check read it off the container, which is two sources for
    // one fact and exactly the thing this page exists to stop.
    <div
      className="flex flex-col gap-4 p-6"
      data-projects
      data-current-project={page.current ?? ""}
    >
      <header className="flex flex-col gap-1">
        <h1 className="font-semibold text-xl tracking-tight">projects</h1>
        <p className="text-muted-foreground text-sm">
          the registry this token may be shown, and which of them it can actually read
        </p>
      </header>

      {/*
        WHICH ONE YOU ARE IN, first and on its own, because it is the question
        this page exists for. A token that resolves to no project is a real
        state - a person's session before a seat is chosen - and it says so
        rather than drawing a blank where a name goes.
      */}
      <section className="rounded-md border border-border bg-card/40 p-4" data-current-panel>
        <div className="text-muted-foreground text-xs uppercase tracking-wide">
          you are writing in
        </div>
        <div className="mt-1 font-mono text-lg">{page.current || "no project"}</div>
        {page.current ? (
          <p className="mt-1 text-muted-foreground text-xs">
            every room you open, every row you file and every message you say goes here. Each
            project has its own #general.
          </p>
        ) : (
          <p className="mt-1 text-muted-foreground text-xs">
            this credential resolves to no project, so it reads the fabric and writes nowhere.
          </p>
        )}
        {/*
          A FIXTURE IS NOT A PROJECT SOMEBODY MEANT. The node marks the ones a
          test or a seed made, and work filed into one is work filed into
          scaffolding - which looks exactly like real work until somebody goes
          looking for it.
        */}
        {page.current_is_fixture ? (
          <p className="mt-2 text-amber-600 text-xs dark:text-amber-500" data-current-is-fixture>
            this project is a FIXTURE - it was made by a test or a seed. Anything filed here is
            filed into scaffolding.
          </p>
        ) : null}
      </section>

      <section className="flex flex-col gap-2">
        <div className="text-muted-foreground text-xs uppercase tracking-wide">
          {page.count} in the registry
        </div>
        <ul className="flex flex-col gap-2">
          {page.projects.map((p) => {
            const readable = reads.includes(p.id);
            return (
              <li
                key={p.id}
                data-project={p.id}
                data-project-readable={readable ? "yes" : "no"}
                className="flex flex-col gap-1 rounded-md border border-border p-3 text-sm"
              >
                <div className="flex items-center gap-2">
                  <span className="font-mono">{p.id}</span>
                  {p.name && p.name !== p.id ? (
                    <span className="text-muted-foreground">{p.name}</span>
                  ) : null}
                  {p.id === page.current ? (
                    <span className="rounded bg-primary/10 px-1.5 py-0.5 text-primary text-xs">
                      yours
                    </span>
                  ) : null}
                  {p.fixture ? (
                    <span className="text-amber-600 text-xs dark:text-amber-500">fixture</span>
                  ) : null}
                </div>
                {/*
                  THE REGISTRY SHOWS A NAME ON AN EDGE IN EITHER DIRECTION AND
                  READING ONLY TRAVELS ALONG ONE. So a project can be listed and
                  unreadable, and this line is the difference between "there is
                  nothing in it" and "you cannot see into it" - the two answers
                  an empty list would collapse into one.
                */}
                <div className="text-muted-foreground text-xs">
                  {readable
                    ? "this token reads its rows"
                    : "listed, but this token cannot read its rows"}
                  {p.provenance ? ` · ${p.provenance}` : ""}
                  {p.origin ? ` · from ${p.origin}` : ""}
                </div>
                {/*
                  WHAT IS IN IT, for the projects this token reads. Absent is
                  its own state and says so: a project whose rooms have not
                  arrived, or could not be read, must not draw as a project
                  with no rooms in it.
                */}
                {readable ? (
                  <div className="text-muted-foreground text-xs" data-project-rooms={p.id}>
                    {rooms[p.id]
                      ? rooms[p.id].length > 0
                        ? `rooms: ${rooms[p.id].join(", ")}`
                        : "no rooms yet - nothing has been said in this project"
                      : "reading its rooms…"}
                  </div>
                ) : null}
                {p.superseded && p.superseded.length > 0 ? (
                  <div className="text-muted-foreground text-xs">
                    supersedes {p.superseded.join(", ")}
                  </div>
                ) : null}
              </li>
            );
          })}
        </ul>
      </section>

      <p className="text-muted-foreground text-xs">
        what is inside another project is not on this page: the rooms door answers about the project
        the token is in, so listing another one's rooms is a change to the node rather than a fetch
        this page skipped. Declaring a project is the same - the door exists, nothing calls it yet.
      </p>
    </div>
  );
}
