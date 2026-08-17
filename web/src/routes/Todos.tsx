import { useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import type { Artifact } from "@/lib/api";
import { TODO_PAGE, api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { speakerStyle } from "@/lib/speakercolour";
import { countTodos, sortTodos, statusStyle, todoAssignee, todoRoom } from "@/lib/todos";

/**
 * The queue across projects: every todo this token can read, wherever it is.
 *
 * The fleet drains this list by starting a run per ready item, and until now the
 * list was per project - a todo is a project-scoped artifact, so "the queue"
 * meant "the queue in whichever project you were pointed at". The operator works
 * across flowy, firecode, pgfuse and more, and asked for one queue.
 *
 * THE UNION IS NOT THE FEATURE. Saying whose union it is, is. Todos are
 * permission-filtered, so "every project" means "every project THIS READER may
 * read": the operator sees the fleet, an agent sees its own work, and both of
 * them call it "the list". Two people reading a different list under one name do
 * not find out by talking - they find out hours later by disagreeing about
 * whether a piece of work exists, with one of them certain. So the scope is on
 * the page, in words, above the rows, and every row says which project it is in.
 *
 * The reach comes from `reads` and not from the project registry. The registry
 * shows a project on a grant edge in EITHER direction and reading only travels
 * along one of them, so a reader in pa with pb granted into pa is shown pb and
 * can read nothing in it - a scope line off that list would have claimed two
 * projects while handing over one project's rows, which is precisely the lie
 * this page exists to not tell. See store.ReadableProjects.
 *
 * The room panel in the chat view is untouched and stays room-scoped. A room's
 * panel showing another project's work is the confusion this page is meant to
 * end, not spread; what the two share is lib/todos, so the reading order, the
 * statuses and the owner line cannot drift into two ideas of what a todo is.
 */

export function Todos() {
  const { token } = useSession();
  const [todos, setTodos] = useState<Artifact[]>([]);
  /** The projects this token can read a row in, whether or not any todo is in
   * them. It is what makes an empty answer a statement rather than a blank. */
  const [reach, setReach] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    if (!token) {
      setTodos([]);
      setReach([]);
      setLoaded(false);
      return;
    }
    let stopped = false;
    // Both reads together: a count with no scope beside it is the sentence this
    // page exists to avoid, so neither half is rendered until both have landed.
    Promise.all([api.todos(), api.projects()])
      .then(([queue, registry]) => {
        if (stopped) return;
        setTodos(queue.artifacts);
        setReach(registry.reads ?? []);
        setError(null);
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

  const sorted = sortTodos(todos);
  const counts = countTodos(todos);
  const { projects, personal } = scopeOf(todos, reach);
  const capped = todos.length >= TODO_PAGE;
  /**
   * Whether there is an answer to state the size of. Before the reads land,
   * every number here is zero and "0 todos across 0 projects you can read" is a
   * false sentence rather than an empty one - which is the sentence this page
   * exists to not print. The list underneath says it is still reading.
   */
  const answered = Boolean(token) && loaded && !error;

  return (
    <div className="flex h-full flex-col">
      <header className="flex flex-col gap-1 border-border border-b px-4 py-3">
        <div className="flex flex-wrap items-center gap-2">
          <h1 className="font-semibold text-base">todos</h1>
          <span className="text-muted-foreground text-xs">
            every project you can read, not just the one you write into
          </span>
          {answered ? (
            <span className="ml-auto text-muted-foreground text-xs">
              {counts.active} active, {counts.open} open, {counts.done} done
            </span>
          ) : null}
        </div>
        {/*
          The scope, in words, on the page. Not a tooltip and not a console line:
          somebody reading this list has to see how far it reaches without
          knowing to ask, because the whole failure mode is two readers who never
          learn their lists differ.

          Drawn only once there is an answer to describe. Every number in it is
          zero until both reads land, and a page whose whole point is not
          misstating its own scope must not spend a paint saying it reaches
          nothing.
        */}
        {answered ? (
          <p
            data-todo-scope=""
            data-todo-count={todos.length}
            data-project-count={projects.length}
            className="flex flex-wrap items-center gap-1.5 text-muted-foreground text-xs"
          >
            <span>{scopeLine({ todos: todos.length, projects: projects.length, personal })}</span>
            {/* Named, not just counted. A number says how much is out of reach
                and a name says what, and "3 projects" with no names is the same
                unanswerable question one step later. */}
            {projects.map((name) => (
              <Badge key={name} variant="outline" data-scope-project={name}>
                {name}
              </Badge>
            ))}
            {capped ? (
              <span data-todo-capped="">
                - the node stopped at {TODO_PAGE} rows, so there may be more
              </span>
            ) : null}
          </p>
        ) : null}
      </header>

      {error ? (
        <div className="border-destructive/40 border-b bg-destructive/10 px-4 py-2 text-destructive text-xs">
          {error}
        </div>
      ) : null}

      <ol aria-label="todos across projects" className="min-h-0 flex-1 overflow-y-auto">
        {sorted.length === 0 ? (
          <li className="p-4 text-muted-foreground text-sm">
            {emptyReads({
              token: Boolean(token),
              loaded,
              failed: Boolean(error),
              projects: projects.length,
            })}
          </li>
        ) : null}
        {sorted.map((todo) => (
          <Row key={todo.id} todo={todo} />
        ))}
      </ol>
    </div>
  );
}

/**
 * Which projects this list is drawn from, and whether any of it is personal.
 *
 * The union of two answers, and both are needed. `reads` is the reach: it holds
 * projects with no todos in them, which is the whole of what makes "no todos in
 * the 3 projects you can read" a different statement from "no todos". The rows'
 * own projects are the other half, because a single artifact can be shared
 * across a boundary by name - that reaches one row and does NOT make its project
 * readable, so it is absent from `reads` and present in the list, and a scope
 * line that named fewer projects than the rows do would be visibly wrong.
 *
 * A todo with no project is personal to whoever wrote it. It is counted apart
 * rather than as a project, because it is not in one.
 */
function scopeOf(todos: Artifact[], reads: string[]) {
  const projects = new Set(reads);
  let personal = 0;
  for (const todo of todos) {
    if (todo.project) projects.add(todo.project);
    else personal += 1;
  }
  return { projects: [...projects].sort(), personal };
}

/** The sentence above the rows. Plural agreement matters here: this line is read
 * by people deciding whether the thing they are looking for is missing or
 * merely out of reach. */
function scopeLine({
  todos,
  projects,
  personal,
}: {
  todos: number;
  projects: number;
  personal: number;
}) {
  const own = personal > 0 ? `, plus ${personal} of your own with no project` : "";
  const across = `${projects} project${projects === 1 ? "" : "s"} you can read`;
  return `${todos} todo${todos === 1 ? "" : "s"} across ${across}${own}:`;
}

/**
 * What an empty list says, which is never nothing.
 *
 * Four different facts look identical as a blank page, and the two that matter
 * are the last two. "no todos" reads as "there is no work", and for a reader
 * whose token reaches one project out of five that is a false statement about
 * the fleet rather than a true one about their reach.
 */
function emptyReads({
  token,
  loaded,
  failed,
  projects,
}: {
  token: boolean;
  loaded: boolean;
  failed: boolean;
  projects: number;
}) {
  if (!token) {
    return "paste a token to read the queue - signed out, there is no reader to scope it to";
  }
  if (failed) {
    return "the queue could not be read, so this page is not saying there is no work";
  }
  if (!loaded) {
    return "reading the queue…";
  }
  if (projects === 0) {
    return "this token reaches no project, so there is no queue to draw - not an empty one";
  }
  return `no todos in the ${projects} project${projects === 1 ? "" : "s"} you can read - other projects may hold work this token cannot see`;
}

/**
 * One todo: its state, who is carrying it, WHICH PROJECT IT IS IN, and its
 * title.
 *
 * The project is on every row and not only in the heading. Two projects filing
 * "fix the flaky sync test" put two identical rows side by side, and a
 * cross-project list where those cannot be told apart is worse than no list -
 * somebody closes one believing they closed the other.
 */
function Row({ todo }: { todo: Artifact }) {
  const owner = todoAssignee(todo);
  const room = todoRoom(todo);
  const project = todo.project ?? "";
  return (
    <li
      data-todo-row={todo.id}
      className="flex flex-wrap items-baseline gap-2 border-border/60 border-b px-4 py-2 text-sm"
    >
      <Badge variant="secondary" style={statusStyle(todo.status)}>
        {todo.status || "todo"}
      </Badge>
      {/* Which project, in the same colour scheme the rest of the console names
          things in. The empty case is a todo with no project at all: personal to
          its owner, and said in those words rather than left blank, because a
          blank cell reads as a project whose name did not load. */}
      <Badge variant="outline" data-todo-project={project} title={project || "no project"}>
        {project || "personal"}
      </Badge>
      <span
        className="shrink-0 text-muted-foreground text-xs"
        style={owner ? speakerStyle(owner) : undefined}
      >
        {owner || "unowned"}
      </span>
      <Link
        to={`/p/${encodeURIComponent(project || "_")}/memory/${todo.id}`}
        className="min-w-0 flex-1 break-words hover:underline"
      >
        {todo.title || todo.id}
      </Link>
      {/* Where it was agreed, when it was agreed anywhere. It is a link back to
          that room's own panel, which is the surface that can answer it. */}
      {room ? (
        <Link to={`/chat/${encodeURIComponent(room)}`} className="text-muted-foreground text-xs">
          #{room}
        </Link>
      ) : null}
    </li>
  );
}
