import { useEffect, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";

import { MergeQueue } from "@/components/MergeQueue";
import { Badge } from "@/components/ui/badge";
import type { Artifact, MergeRequest, Refused, Withheld } from "@/lib/api";
import { TODO_PAGE, api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { speakerStyle } from "@/lib/speakercolour";
import {
  TODO_KINDS,
  countTodos,
  kindStyle,
  sortTodos,
  statusStyle,
  tagsIn,
  todoAssignee,
  todoKind,
  todoRoom,
  todoTags,
} from "@/lib/todos";

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
  /** What the node refused to hand over, and why. Null when it refused nothing:
   * see Withheld, and emptyReads for why a shorter list has to say it is one. */
  const [withheld, setWithheld] = useState<Withheld | null>(null);
  /** And what it refused for good: a claim it will not judge again, however the
   * rule changes afterwards. A different statement from the one above and shown
   * beside it rather than instead of it - see Refused. */
  const [refused, setRefused] = useState<Refused | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  /**
   * The two narrowings, and they are two because the things they narrow are two.
   *
   * kind is one value out of the closed set the node enforces - so this control
   * is a fixed list, offers "unclassified" as the state most of the queue is in,
   * and can be trusted to mean the same thing on every row. tag is a free label
   * with no schema, so its list is built from what is actually on the page.
   *
   * Both are held here rather than in the URL because they are a way of reading
   * this page rather than a place: what is worth linking to is the queue, and a
   * shared link that silently hides two thirds of it is the sort of short list
   * this page spends its whole header refusing to hand anybody.
   */
  const [kind, setKind] = useState("");
  const [tag, setTag] = useState("");
  /**
   * Which tab is open. Two views of one queue rather than two queues: a merge
   * request is a work item, it waits on the same dependency edges as everything
   * else, and the store holds it in the same list. What differs is the question
   * being asked - "what is outstanding" against "what may land" - so the split
   * is in the reading, not in the data.
   *
   * Read from the path, unlike kind/tag below: those narrow the rows you are
   * looking at from a page you are already on, but the tab decides which page
   * that is - "the merge queue" is a thing a person links to, bookmarks and
   * expects the back button to leave, not a filter on "todos". Keeping it in
   * useState made /todos and the merge queue the same URL, so a reload or a
   * back-button press after opening the merge queue silently dropped you back
   * on the todo list with no way to tell that had happened.
   */
  const location = useLocation();
  const navigate = useNavigate();
  const tab: "todos" | "merge" = location.pathname === "/todos/merge" ? "merge" : "todos";
  const setTab = (next: "todos" | "merge") => {
    if (next === tab) return;
    navigate(next === "merge" ? "/todos/merge" : "/todos");
  };
  const [merges, setMerges] = useState<MergeRequest[]>([]);
  const [mergeTip, setMergeTip] = useState("");
  const [mergeTipFrom, setMergeTipFrom] = useState<"stated" | "deployed" | "none">("none");
  const [mergeDecided, setMergeDecided] = useState(false);
  const [mergesLoaded, setMergesLoaded] = useState(false);

  useEffect(() => {
    if (!token) {
      setTodos([]);
      setReach([]);
      setWithheld(null);
      setRefused(null);
      setLoaded(false);
      return;
    }
    let stopped = false;
    // Both reads together: a count with no scope beside it is the sentence this
    // page exists to avoid, so neither half is rendered until both have landed.
    api
      .mergeQueue()
      .then((q) => {
        if (stopped) return;
        setMerges(q.items ?? []);
        setMergeTip(q.target_tip ?? "");
        setMergeTipFrom(q.tip_from ?? "none");
        setMergeDecided(Boolean(q.decided));
      })
      .catch(() => {
        // A node without this endpoint is an older node, not a broken page.
        // The tab says zero and the todos half is unaffected.
        if (!stopped) setMerges([]);
      })
      .finally(() => {
        if (!stopped) setMergesLoaded(true);
      });
    Promise.all([api.todos(), api.projects()])
      .then(([queue, registry]) => {
        if (stopped) return;
        setTodos(queue.artifacts);
        setReach(registry.reads ?? []);
        setWithheld(queue.withheld ?? null);
        setRefused(queue.refused ?? null);
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

  const shown = narrow(todos, kind, tag);
  const sorted = sortTodos(shown);
  const counts = countTodos(todos);
  const tags = tagsIn(todos);
  const filtered = kind !== "" || tag !== "";
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
      {/*
        Two views of one queue. The counts live in the tab titles because the
        question "is there anything waiting to land" should be answerable
        without opening the tab - a board you have to click through to find out
        whether it needs you is a board people stop opening.
      */}
      <div className="flex items-center gap-1 border-border border-b px-2 pt-2" role="tablist">
        <TabButton
          selected={tab === "todos"}
          onClick={() => setTab("todos")}
          label="todos"
          stats={
            answered ? (
              <>
                <Stat colour="#e0a03f" n={counts.active} what="active" />
                <Stat colour="#8b93a7" n={counts.open} what="open" />
              </>
            ) : null
          }
        />
        <TabButton
          selected={tab === "merge"}
          onClick={() => setTab("merge")}
          label="merge queue"
          stats={
            mergesLoaded ? (
              <>
                <Stat
                  colour="#4fae7a"
                  n={merges.filter((m) => m.admissible === true).length}
                  what="may land"
                />
                <Stat
                  colour="#d1585f"
                  n={merges.filter((m) => m.admissible === false).length}
                  what="refused"
                />
              </>
            ) : null
          }
        />
      </div>
      {tab === "merge" ? (
        <MergeQueue
          items={merges}
          tip={mergeTip}
          tipFrom={mergeTipFrom}
          decided={mergeDecided}
          loaded={mergesLoaded}
        />
      ) : (
        <>
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
          The two labels a todo carries, as two controls, because they are two
          different things and reading them as one is the whole confusion this
          round is about. KIND is one word out of a closed set the node refuses
          anything outside of - a fixed list, and "unclassified" is on it because
          that is the state most of this queue is in and it has to be askable
          for. TAG is a free label with no schema, so its list is whatever is
          actually written on the rows in front of you.

          Both narrow the ROWS and neither narrows the counts or the scope line
          above: those describe the queue, and a page that quietly restated its
          own reach every time somebody picked a filter would be lying in the one
          place it exists not to.
        */}
            {answered ? (
              <div className="flex flex-wrap items-center gap-2 text-xs">
                <label className="flex items-center gap-1 text-muted-foreground">
                  kind
                  <select
                    data-todo-kind-filter=""
                    aria-label="kind of work"
                    value={kind}
                    onChange={(e) => setKind(e.target.value)}
                    className="rounded border border-border bg-background px-1 py-0.5 text-foreground"
                  >
                    <option value="">any</option>
                    {TODO_KINDS.map((name) => (
                      <option key={name} value={name}>
                        {name}
                      </option>
                    ))}
                    <option value={UNCLASSIFIED}>unclassified</option>
                  </select>
                </label>
                <label className="flex items-center gap-1 text-muted-foreground">
                  tag
                  <select
                    data-todo-tag-filter=""
                    aria-label="tag"
                    value={tag}
                    onChange={(e) => setTag(e.target.value)}
                    className="rounded border border-border bg-background px-1 py-0.5 text-foreground"
                  >
                    <option value="">any</option>
                    {tags.map((name) => (
                      <option key={name} value={name}>
                        {name}
                      </option>
                    ))}
                  </select>
                </label>
                {/* What the filter is doing to the list, in numbers, beside the
                control that did it. A short list with nothing saying it is short
                is the same failure as a capped one - see the cap notice above. */}
                {filtered ? (
                  <span data-todo-filtered={shown.length} className="text-muted-foreground">
                    showing {shown.length} of {todos.length}
                    <button
                      type="button"
                      data-todo-filter-clear=""
                      onClick={() => {
                        setKind("");
                        setTag("");
                      }}
                      className="ml-2 underline hover:no-underline"
                    >
                      clear
                    </button>
                  </span>
                ) : null}
              </div>
            ) : null}
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
                <span>
                  {scopeLine({ todos: todos.length, projects: projects.length, personal })}
                </span>
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
                {/* And what the node would not hand over, which is the other way a
                list is short. The cap is "there may be more"; this is "there IS
                more, and here is why you are not being shown it" - a stronger
                statement and the one that must never be silent. It carries no
                data-scope-project: those name the projects the list is drawn
                from, and this is not one of them. */}
                {withheld ? (
                  <span
                    data-todo-withheld={withheld.rows}
                    title="Rows this node refused because the principal named on them did not
sign them and it holds that principal's key. They are refused, not hidden - and
this says so rather than letting the list read as all the work there is."
                  >
                    - {withheld.rows} row{withheld.rows === 1 ? "" : "s"} withheld:{" "}
                    {withheld.reason}
                  </span>
                ) : null}
                {/* And the decisions behind them, which are not the same set. A
                withheld row may arrive on the next pull; a refused claim never
                will, unless somebody signs for it. Shown separately so a reader
                can tell "not here yet" from "not coming". */}
                {refused ? (
                  <span
                    data-todo-refused={refused.claims}
                    title="Authorship claims this node refused and will not reconsider. A refusal
here is a decision, not a delay: it is not re-judged when a key is pinned or an
epoch moves. The same content signed by the person it names is a different claim
and lands."
                  >
                    - {refused.claims} claim{refused.claims === 1 ? "" : "s"} refused:{" "}
                    {refused.reason}
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
                {/* A filter that matched nothing is its own empty, and it is the
                one this page must not report as any of the other four: "no
                todos in the 3 projects you can read" is a false statement about
                the fleet when the reader has narrowed to bugs and there are
                none. So the filter is answered for first, in its own words. */}
                {filtered && loaded && !error && todos.length > 0
                  ? `none of the ${todos.length} todos here are ${describe(kind, tag)} - the rest of the queue is behind the filter, not missing`
                  : emptyReads({
                      token: Boolean(token),
                      loaded,
                      failed: Boolean(error),
                      projects: projects.length,
                      withheld,
                      refused,
                    })}
              </li>
            ) : null}
            {sorted.map((todo) => (
              <Row key={todo.id} todo={todo} onTag={setTag} />
            ))}
          </ol>
        </>
      )}
    </div>
  );
}

/**
 * One tab, with its own numbers in the title.
 *
 * aria-selected and role are not decoration: this is the only control on the
 * page that changes what the whole panel is about, and a person driving it by
 * keyboard has to be told which view they are in.
 */
function TabButton({
  selected,
  onClick,
  label,
  stats,
}: {
  selected: boolean;
  onClick: () => void;
  label: string;
  stats: React.ReactNode;
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={selected}
      data-queue-tab={label}
      onClick={onClick}
      className={`flex items-center gap-2 rounded-t px-3 py-1.5 text-sm ${
        selected
          ? "border-border border-x border-t bg-background font-medium"
          : "text-muted-foreground hover:text-foreground"
      }`}
    >
      {label}
      {stats}
    </button>
  );
}

/**
 * One coloured count. The number and the word both appear - colour is the
 * second signal and never the only one, the same rule the status badges follow,
 * because a reader who cannot separate these hues gets exactly the same
 * information a fraction slower.
 */
function Stat({ colour, n, what }: { colour: string; n: number; what: string }) {
  return (
    <span className="text-xs" style={{ color: colour }} title={`${n} ${what}`}>
      {n} {what}
    </span>
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
 * FIVE different facts look identical as a blank page, and the ones that matter
 * are the last three. "no todos" reads as "there is no work", and for a reader
 * whose token reaches one project out of five that is a false statement about
 * the fleet rather than a true one about their reach.
 *
 * The fifth is a refusal, and it is the one that cannot be inferred from
 * anything else on the page: the node holds rows it will not carry, because they
 * name a principal whose signing key it holds and they are not signed with it.
 * Those rows are missing from this answer for a reason no count of projects can
 * express, so the reason is said outright and the count with it. A refusal
 * nobody sees is indistinguishable from success, which is what an empty queue
 * that had one looks like.
 */
function emptyReads({
  token,
  loaded,
  failed,
  projects,
  withheld,
  refused,
}: {
  token: boolean;
  loaded: boolean;
  failed: boolean;
  projects: number;
  withheld: Withheld | null;
  refused: Refused | null;
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
  // Appended rather than substituted: the reach is still true and still worth
  // saying, and a reader deciding whether their work is missing needs both
  // sentences. Nothing that already read this page loses a word of it.
  const short = withheld
    ? ` - and ${withheld.rows} row${withheld.rows === 1 ? "" : "s"} withheld: ${withheld.reason}`
    : "";
  // The second sentence, appended for the same reason the first one is: a reader
  // who is being told the queue is empty needs to know the node has also decided
  // some of it is never arriving.
  const stood = refused
    ? ` - and ${refused.claims} claim${refused.claims === 1 ? "" : "s"} refused: ${refused.reason}`
    : "";
  if (projects === 0) {
    return `this token reaches no project, so there is no queue to draw - not an empty one${short}${stood}`;
  }
  return `no todos in the ${projects} project${projects === 1 ? "" : "s"} you can read - other projects may hold work this token cannot see${short}${stood}`;
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
function Row({ todo, onTag }: { todo: Artifact; onTag: (tag: string) => void }) {
  const owner = todoAssignee(todo);
  const room = todoRoom(todo);
  const project = todo.project ?? "";
  const kind = todoKind(todo);
  const tags = todoTags(todo);
  return (
    <li
      data-todo-row={todo.id}
      data-todo-kind={kind}
      className="flex flex-wrap items-baseline gap-2 border-border/60 border-b px-4 py-2 text-sm"
    >
      <Badge variant="secondary" style={statusStyle(todo.status)}>
        {todo.status || "todo"}
      </Badge>
      {/* What kind of work it is, beside what state it is in, because those are
          the two questions somebody scanning a queue is asking at once. Drawn
          only when there is one: a row nobody classified says nothing rather
          than being labelled "unclassified" on every line, which would put the
          least informative word on the page more often than any other. The
          filter above is where "which ones have no kind" is asked. */}
      {kind ? (
        <Badge variant="secondary" data-todo-kind-badge={kind} style={kindStyle(kind)}>
          {kind}
        </Badge>
      ) : null}
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
      {/* The free labels, which are as many as somebody wrote and are nobody's
          schema. Each one is the control that filters by it: a tag is only worth
          drawing if the answer to "what else is tagged like this" is one click
          away, and a list of words you can read and not act on is decoration. */}
      {tags.map((name) => (
        <button
          key={name}
          type="button"
          data-todo-tag={name}
          onClick={() => onTag(name)}
          title={`show only todos tagged ${name}`}
        >
          <Badge variant="outline" className="cursor-pointer text-xs">
            {name}
          </Badge>
        </button>
      ))}
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

/**
 * UNCLASSIFIED is the filter value for "has no kind at all", and it is a word
 * the node never sees: the closed set holds four kinds and absence is not one of
 * them. It has to be askable for anyway - most of the queue is in that state,
 * and "which of these has nobody classified" is the question somebody
 * classifying them needs answered.
 */
const UNCLASSIFIED = "-none-";

/** The rows a filter leaves. Both narrowings are ANDed: two controls that meant
 * OR would make picking a second one WIDEN the list, which is not what a person
 * setting two filters is asking for. */
function narrow(todos: Artifact[], kind: string, tag: string): Artifact[] {
  return todos.filter((todo) => {
    if (kind === UNCLASSIFIED && todoKind(todo) !== "") return false;
    if (kind !== "" && kind !== UNCLASSIFIED && todoKind(todo) !== kind) return false;
    if (tag !== "" && !todoTags(todo).includes(tag)) return false;
    return true;
  });
}

/** What the filter is asking for, in the sentence an empty result gets. */
function describe(kind: string, tag: string): string {
  const parts: string[] = [];
  if (kind === UNCLASSIFIED) parts.push("unclassified");
  else if (kind !== "") parts.push(kind);
  if (tag !== "") parts.push(`tagged ${tag}`);
  return parts.join(" and ");
}
