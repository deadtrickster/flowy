import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";

import { DocumentPanes, documentRoom } from "@/components/DocumentPanes";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { type Artifact, type OpenspecConflict, api, artifactPath } from "@/lib/api";
import { renderDocument } from "@/lib/markdown";
import { openspecFilesOf, openspecStateOf, openspecVerdictOf } from "@/lib/openspec";
import { useSession } from "@/lib/session";

/**
 * One openspec row, at /openspec/:id.
 *
 * A change is a DIRECTORY of markdown files (proposal.md, tasks.md, ...) held
 * in fields.openspec.files, so this page is a viewer over that map rather than
 * over a single body: each file rendered as a document, each with a discuss
 * button that drops its path into the draft as a quote - the same way the
 * selection bar drops a passage. A spec is one document and renders like any
 * other artifact body.
 *
 * Beside the files, what the store derived from them: the lifecycle state, the
 * validate door's verdict (p4), the todos tasks.md derived (p2, read through
 * /api/openspec/{id}/todos - the only door that names them) and the change's
 * clash edges. And beside it all the row's own discussion, the same
 * DocumentPanes every memory item gets - the operator's p5 answer: individual
 * elements get threads, and this pane matches rooms chat in rendering, threads
 * and mentions.
 */
export function OpenspecView() {
  const { id = "" } = useParams();
  const { token } = useSession();
  const [artifact, setArtifact] = useState<Artifact | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [todos, setTodos] = useState<Artifact[]>([]);
  const [conflicts, setConflicts] = useState<OpenspecConflict[]>([]);
  const [selection, setSelection] = useState("");
  const [quote, setQuote] = useState<{ text: string } | null>(null);

  // The same listener ArtifactView runs, for the same reason: selecting is
  // reading, and the draft is only written when somebody says so. The quote
  // bar reads the window's selection rather than offsets into rendered HTML -
  // see ArtifactView, where the two-rule contract lives.
  const captureSelection = () => {
    const selected = (window.getSelection()?.toString() ?? "").trim();
    if (selected) setSelection(selected);
  };

  useEffect(() => {
    if (!token || !id) return;
    let stopped = false;
    api
      .artifact(id)
      .then((found) => {
        if (stopped) return;
        setArtifact(found);
        setError(null);
        // Only a change has tasks.md and clash edges; the doors refuse a spec
        // and asking them anyway would paint refusals as page errors.
        if (found.kind === "change") {
          api
            .openspecTodos(id)
            .then((page) => {
              if (!stopped) setTodos(page.todos ?? []);
            })
            .catch(() => {
              if (!stopped) setTodos([]);
            });
          api
            .openspecConflicts(id)
            .then((page) => {
              if (!stopped) setConflicts(page.conflicts ?? []);
            })
            .catch(() => {
              if (!stopped) setConflicts([]);
            });
        } else {
          setTodos([]);
          setConflicts([]);
        }
      })
      .catch((err: Error) => {
        if (!stopped) {
          setArtifact(null);
          setError(err.message);
        }
      });
    return () => {
      stopped = true;
    };
  }, [token, id]);

  const files = artifact ? openspecFilesOf(artifact) : null;
  const state = artifact ? openspecStateOf(artifact) : undefined;
  const verdict = artifact ? openspecVerdictOf(artifact) : null;
  // proposal.md is the change's own words and reads first; the rest follow in
  // path order, so the table of contents is the file tree, not a shuffle.
  const filePaths = files
    ? Object.keys(files).sort((a, b) => {
        if (a === "proposal.md") return -1;
        if (b === "proposal.md") return 1;
        return a.localeCompare(b);
      })
    : [];

  return (
    <div className="flex h-full min-h-0 flex-col lg:flex-row">
      <div
        className="min-w-0 flex-1 overflow-y-auto p-6"
        onMouseUp={captureSelection}
        onKeyUp={captureSelection}
      >
        <div className="mx-auto flex max-w-3xl flex-col gap-4">
          <div className="flex items-center gap-2 text-muted-foreground text-xs">
            <Link to="/openspec" className="hover:text-foreground">
              openspec
            </Link>
            <span>/</span>
            <span className="font-mono">{id}</span>
          </div>

          {error ? <div className="text-destructive text-sm">{error}</div> : null}
          {!token ? <div className="text-muted-foreground text-sm">no token</div> : null}

          {artifact ? (
            <Card>
              <CardHeader>
                <CardTitle
                  className="text-base"
                  data-openspec-title={artifact.id}
                  data-openspec-kind={artifact.kind ?? ""}
                >
                  {artifact.title || artifact.id}
                </CardTitle>
                <div className="flex flex-wrap gap-1 pt-1">
                  <Badge variant="outline">{artifact.kind}</Badge>
                  {state ? (
                    <Badge variant={state === "archived" ? "outline" : "default"}>{state}</Badge>
                  ) : null}
                  {artifact.visibility ? (
                    <Badge variant="outline">{artifact.visibility}</Badge>
                  ) : null}
                  <Badge variant="outline">{artifact.project ?? "no project"}</Badge>
                  {/*
                    THE VALIDATE DOOR'S VERDICT (p4), and its absence said out
                    loud. "No verdict" is its own state - the row has not been
                    through the door - and a page that showed nothing would read
                    as a passed validation. problems is drawn in the door's own
                    words, one sentence per line.
                  */}
                  {verdict ? (
                    verdict.ok ? (
                      <Badge
                        data-openspec-verdict="ok"
                        className="border-transparent bg-human/15 text-human"
                      >
                        valid
                      </Badge>
                    ) : (
                      <Badge
                        data-openspec-verdict="problems"
                        className="border-destructive/40 bg-destructive/10 text-destructive"
                      >
                        {verdict.problems.length} problem{verdict.problems.length === 1 ? "" : "s"}
                      </Badge>
                    )
                  ) : (
                    <Badge variant="outline" data-openspec-verdict="none">
                      not validated
                    </Badge>
                  )}
                </div>
                {verdict && !verdict.ok ? (
                  <ul className="flex flex-col gap-1 pt-2 text-destructive text-xs">
                    {verdict.problems.map((problem) => (
                      <li key={problem}>{problem}</li>
                    ))}
                  </ul>
                ) : null}
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                {files ? (
                  filePaths.map((path) => (
                    <section key={path} data-openspec-file={path} className="flex flex-col gap-2">
                      <header className="flex items-center gap-2 border-border border-b pb-1">
                        <span className="font-mono font-medium text-xs">{path}</span>
                        {/*
                          THE PER-FILE DISCUSS AFFORDANCE. The selection bar
                          quotes a passage; this quotes the file - its path into
                          the draft as a blockquote, so the discussion names
                          what it is about without a citation that points at
                          nothing checkable.
                        */}
                        <button
                          type="button"
                          data-file-discuss={path}
                          className="ml-auto rounded border border-border px-1.5 py-0.5 text-muted-foreground text-xs hover:bg-accent/60"
                          onClick={() => setQuote({ text: path })}
                        >
                          discuss
                        </button>
                      </header>
                      <div
                        className="report-body text-sm"
                        dangerouslySetInnerHTML={{
                          __html: renderDocument(files[path] ?? ""),
                        }}
                      />
                    </section>
                  ))
                ) : (
                  <div
                    data-openspec-body=""
                    className="report-body text-sm"
                    dangerouslySetInnerHTML={{ __html: renderDocument(artifact.body) }}
                  />
                )}

                {/*
                  WHAT THE TASKS DERIVED (p2), named by the one door that can.
                  Each row is an ordinary todo - the link is its queue page,
                  where the work happens; the derive line is origin fields a
                  queue filter cannot reach.
                */}
                {artifact.kind === "change" && todos.length > 0 ? (
                  <div data-openspec-todos={todos.length}>
                    <div className="pb-1 font-medium text-muted-foreground text-xs">
                      derived todos
                    </div>
                    <ul className="flex flex-col gap-1">
                      {todos.map((todo) => {
                        const path = artifactPath(todo);
                        return (
                          <li
                            key={todo.id}
                            data-openspec-todo={todo.id}
                            className="flex items-center gap-2 text-xs"
                          >
                            {path ? (
                              <Link className="hover:underline" to={path}>
                                {todo.title || todo.id}
                              </Link>
                            ) : (
                              <span className="font-mono">{todo.id}</span>
                            )}
                            {todo.status ? <Badge variant="outline">{todo.status}</Badge> : null}
                          </li>
                        );
                      })}
                    </ul>
                  </div>
                ) : null}

                {/*
                  WHICH OTHER CHANGES THIS ONE CLASHES WITH (p2's conflict
                  edges, read through the door p1 built). The spec column is
                  the capability both edits touch; the change column links into
                  this same console, where the other side of the pair lives.
                */}
                {artifact.kind === "change" && conflicts.length > 0 ? (
                  <div data-openspec-conflicts={conflicts.length}>
                    <div className="pb-1 font-medium text-muted-foreground text-xs">conflicts</div>
                    <ul className="flex flex-col gap-1">
                      {conflicts.map((conflict) => (
                        <li
                          key={`${conflict.change}/${conflict.spec}`}
                          data-openspec-conflict={conflict.change}
                          className="flex items-center gap-2 text-xs"
                        >
                          <span className="text-muted-foreground">with</span>
                          <Link
                            className="hover:underline"
                            to={`/openspec/${encodeURIComponent(conflict.change)}`}
                          >
                            {conflict.change}
                          </Link>
                          <span className="text-muted-foreground">over</span>
                          <Link
                            className="hover:underline"
                            to={`/openspec/${encodeURIComponent(conflict.spec)}`}
                          >
                            {conflict.spec}
                          </Link>
                        </li>
                      ))}
                    </ul>
                  </div>
                ) : null}
              </CardContent>
            </Card>
          ) : null}

          {selection ? (
            <div className="sticky bottom-0 flex items-start gap-3 rounded-md border border-border bg-card px-3 py-2 shadow">
              <span className="min-w-0 flex-1 truncate text-muted-foreground text-xs italic">
                “{selection}”
              </span>
              <button
                type="button"
                className="shrink-0 rounded border border-border px-1.5 py-0.5 text-xs hover:bg-accent/60"
                onClick={() => {
                  setQuote({ text: selection });
                  window.getSelection()?.removeAllRanges();
                  setSelection("");
                }}
              >
                quote in the discussion
              </button>
              <button
                type="button"
                className="shrink-0 text-muted-foreground text-xs hover:text-foreground"
                aria-label="put the quote bar away"
                onClick={() => {
                  window.getSelection()?.removeAllRanges();
                  setSelection("");
                }}
              >
                ✕
              </button>
            </div>
          ) : null}
        </div>
      </div>

      {/*
        The row's own room, beside it - named after the id in the path, so the
        conversation is readable even before the document itself has loaded,
        exactly as ArtifactView does it.
      */}
      <aside className="flex h-[28rem] w-full shrink-0 flex-col border-border border-t lg:h-auto lg:w-[26rem] lg:border-t-0 lg:border-l">
        {id ? <DocumentPanes room={documentRoom(id)} quote={quote} /> : null}
      </aside>
    </div>
  );
}
