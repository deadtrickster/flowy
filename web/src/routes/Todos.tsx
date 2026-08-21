import { useEffect, useRef, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";

import { MergeQueue } from "@/components/MergeQueue";
import { StreamAsOf } from "@/components/StreamAsOf";
import { Badge } from "@/components/ui/badge";
import type { Artifact, MergeLock, MergeRequest, Refused, Withheld } from "@/lib/api";
import { TODO_PAGE, api, artifactPath } from "@/lib/api";
import { useSession } from "@/lib/session";
import { speakerStyle } from "@/lib/speakercolour";
import { watch } from "@/lib/stream";
import {
  TODO_KINDS,
  countTodos,
  kindStyle,
  sortTodos,
  statusStyle,
  tagsIn,
  todoAssignee,
  todoKind,
  todoRaiser,
  todoRoom,
  todoTags,
} from "@/lib/todos";

/**
 * How long to let a burst of envelopes settle before re-reading.
 *
 * A batch landing writes one event per row, and twenty envelopes are one answer
 * to the same question: "what does the queue look like now". Short enough that
 * a single claim still feels immediate, long enough that a fifteen-row batch is
 * one read rather than fifteen.
 */
const STREAM_SETTLE_MS = 250;

/**
 * The slow backstop, and it is a stated compromise rather than a second
 * mechanism - see the effect below for what emits no event and why.
 */
const BACKSTOP_MS = 60_000;
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
 *
 * AND IT IS EVERY ROOM, INCLUDING NO ROOM. api.todos() asks for no room and the
 * node narrows by one only when it is asked to, so this list has always held the
 * roomless rows - what it did not do is SAY SO. A row filed through POST
 * /api/artifacts carries no fields.room, because that door sets none, and about
 * half the open queue is filed that way; those rows drew a blank where a room
 * goes, which reads as a room that failed to load rather than as a row filed
 * nowhere. So the room cell is drawn on every row either way, and the count of
 * rows carrying none is in the scope line.
 *
 * That split is the point and it is written on the page rather than implied: THE
 * BOARD IS EVERYTHING AND THE ROOM'S PANEL IS THE FILTER. A row nobody filed in
 * a room is in no panel at all, so if this page narrowed too it would be the
 * only surface those rows could appear on, narrowing them away - which is what
 * an afternoon of the operator reading 24 and the API answering 46 felt like
 * from the inside.
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
  // undefined until a node answers, and undefined again if one stops answering:
  // "no gate is running" is a measurement, and this page must not make it up
  // from a read that never came back.
  const [mergeLock, setMergeLock] = useState<MergeLock | undefined>(undefined);

  /**
   * WHICH READ IS THE NEWEST, so a slow answer never paints over a fast one.
   *
   * Two reads are in flight whenever a change lands while one is running - the
   * stream fires on the change, and the backstop may be mid-flight - and they
   * can answer out of order. The older answer is a picture of the queue from
   * before the change, and letting it land shows the board reverting for as
   * long as it takes the next read to correct it. RoomTodos learned this the
   * hard way and says so; this is the same device.
   */
  const read = useRef(0);
  /** Whether a read is in flight. The stream can fire several times in a burst
   * and a slow node must not end up with a queue of stacked requests behind a
   * console that has already flooded a node once. */
  const inFlight = useRef(false);
  /** Whether anything has ever landed. It decides whether a failure is "this
   * page could not be read" or "the board is a few seconds old", which are
   * different sentences and only the second one keeps the rows. */
  const everHeard = useRef(false);

  useEffect(() => {
    if (!token) {
      setTodos([]);
      setReach([]);
      setWithheld(null);
      setRefused(null);
      setLoaded(false);
      everHeard.current = false;
      return;
    }
    let stopped = false;

    const look = async () => {
      if (stopped || inFlight.current) return;
      inFlight.current = true;
      const mine = ++read.current;
      try {
        // Both reads together: a count with no scope beside it is the sentence
        // this page exists to avoid, so neither half is rendered until both
        // have landed. The merge queue rides along because the two tabs are
        // two views of one queue, and a tab that refreshed only when it was
        // open would be stale for exactly as long as somebody was reading the
        // other one.
        const [queue, registry, merge] = await Promise.all([
          api.todos(),
          api.projects(),
          // A node without this endpoint is an older node, not a broken page.
          // Its own tab says so; the todos half is unaffected.
          api
            .mergeQueue()
            .catch(() => null),
        ]);
        if (stopped || mine !== read.current) return;
        setTodos(queue.artifacts);
        setReach(registry.reads ?? []);
        setWithheld(queue.withheld ?? null);
        setRefused(queue.refused ?? null);
        if (merge) {
          setMerges(merge.items ?? []);
          setMergeTip(merge.target_tip ?? "");
          setMergeTipFrom(merge.tip_from ?? "none");
          setMergeDecided(Boolean(merge.decided));
          // An older node sends no lock. Undefined draws nothing rather than
          // "free", which would be this page answering a question the node it
          // is talking to never answered.
          setMergeLock(merge.lock);
        } else if (!everHeard.current) {
          setMerges([]);
        }
        setError(null);
        everHeard.current = true;
      } catch (err: unknown) {
        if (stopped || mine !== read.current) return;
        const why = err instanceof Error ? err.message : String(err);
        // KEPT, NOT EMPTIED, once anything has ever landed. A queue that
        // vanishes on one bad request looks like a fleet that finished its
        // work, which is a worse lie than a board a few seconds out of date -
        // and the "as of" beside it is what stops the stale rows reading as
        // current. Only a page that never had an answer says it has none.
        if (!everHeard.current) setError(why);
      } finally {
        inFlight.current = false;
        if (!stopped && mine === read.current) {
          setLoaded(true);
          setMergesLoaded(true);
        }
      }
    };

    void look();

    /**
     * THE STREAM IS THE MECHANISM. One connection per tab, shared with every
     * other panel that wants one, carrying envelopes that say a topic moved and
     * never what it now holds - so this re-reads the list it already knows how
     * to read, and no half-row is ever applied over what somebody is doing.
     *
     * Debounced, because a batch landing writes an envelope per row and twenty
     * of them are one answer to the same question.
     */
    let soon: ReturnType<typeof setTimeout> | undefined;
    const stopWatching = watch(["todos", "queue"], () => {
      clearTimeout(soon);
      soon = setTimeout(look, STREAM_SETTLE_MS);
    });

    /**
     * And one slow backstop, which is a stated compromise rather than a second
     * mechanism.
     *
     * POST /api/artifacts emits no event at all - handleCreateArtifact writes
     * the row with no events argument - so a todo filed through the raw create
     * door produces nothing for any stream to carry. The doors people actually
     * use do emit (the room raise writes a chat event naming the artifact, and
     * mem_write writes a todo.status entry), so the gap is narrow and silent.
     * A minute is slow enough not to be the mechanism and fast enough that such
     * a row is late rather than invisible. The fix is a create event, which is
     * a minted-type decision and belongs in its own row.
     */
    const backstop = setInterval(look, BACKSTOP_MS);

    return () => {
      stopped = true;
      clearTimeout(soon);
      stopWatching();
      clearInterval(backstop);
    };
  }, [token]);

  /**
   * The rows the rail's dot is counting, from the node.
   *
   * NOT DERIVED HERE, and that is the whole point. "Assigned to me and not
   * started" is a rule api_nag.go applies while counting; re-applying it in the
   * console would be a second implementation whose first act is to disagree
   * with the number beside it - which is this page's complaint, rebuilt one
   * layer up. An older node sends no ids and nothing is marked, which is the
   * honest answer to "cannot say which".
   */
  const [waiting, setWaiting] = useState<Set<string>>(new Set());
  useEffect(() => {
    if (!token) {
      setWaiting(new Set());
      return;
    }
    let stopped = false;
    api
      .nag()
      .then((view) => {
        if (!stopped) setWaiting(new Set(view.mine_todo_ids ?? []));
      })
      .catch(() => {
        // Left unmarked rather than guessed. A nag that could not be read is
        // not "nothing is waiting" - see the rail, which draws no dot for the
        // same reason.
        if (!stopped) setWaiting(new Set());
      });
    return () => {
      stopped = true;
    };
  }, [token]);

  const shown = narrow(todos, kind, tag);
  const sorted = sortTodos(shown);
  const counts = countTodos(todos);
  // The tags on offer, PLUS whatever is currently selected even when the last
  // row carrying it has left the queue. A poll that removed the option out from
  // under the control would leave the select drawn blank while the filter it
  // set was still narrowing the list - a control saying one thing and doing
  // another, which is the class of defect this whole row is about.
  const tags = withSelected(tagsIn(todos), tag);
  const filtered = kind !== "" || tag !== "";
  const { projects, personal } = scopeOf(todos, reach);
  const capped = todos.length >= TODO_PAGE;
  /**
   * How many of these rows were filed in no room at all.
   *
   * It is on the page because it is a DEFECT COUNT, not decoration: a row gets
   * no room when it is filed through POST /api/artifacts, which sets none, and
   * that is most of what the agents file. Nothing anywhere showed that number,
   * so "half the queue was raised through a door that loses where it came from"
   * was a thing you could only learn by reading the API yourself - which is
   * exactly how the operator and every agent spent an afternoon each certain
   * about a different set of rows.
   */
  const roomless = todos.filter((todo) => todoRoom(todo) === "").length;
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
                {/*
                  THE RAIL'S NUMBER, ON THE PAGE THE RAIL SENDS YOU TO. It is
                  drawn only when there is one, because "0 waiting" beside a
                  rail with no dot is a sentence nobody needs - and it is the
                  node's count of the ids marked below, so a reader can check
                  the two against each other by eye.
                */}
                {waiting.size > 0 ? (
                  <Stat colour="#5b8dd6" n={waiting.size} what="waiting on you" />
                ) : null}
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
                {/*
                  BLOCKED IS THE ROW THAT CARRIES A REASON, and queued is
                  everything else. The operator's row (01M0G82K03) was filed as
                  "merges tab is always 0"; measured, the tab read
                  "0 may land, 2 refused" while both of those rows said "no gate
                  has measured it" on their own cards and one of them was
                  gating. Nothing had refused either.

                  The cause was `admissible === false` standing in for refused.
                  Absent means nobody asked; false means asked and no; and the
                  node answers false for a row it has simply not measured yet -
                  its own reason says so in words. So on a working queue almost
                  every row was drawn red.

                  claude-host fixed the identical shape in board-nag.sh
                  (b2187ce, 5d0afcb) and the rule from it is the one applied
                  here: THE ELSE-BRANCH NAMES THE COMMON CASE. An else wearing an
                  alarming word borrows alarm it has not earned, and a reader who
                  learns the red means nothing stops reading the red.

                  "0 may land" is left alone deliberately. It is correct and it
                  is almost always zero, because admissible is the momentary
                  window between a green gate and the land - that is a fact about
                  the queue, not a defect in the counter, and hiding it would
                  hide the thing the operator is actually looking at.
                */}
                <Stat colour="#d1585f" n={merges.filter((m) => m.blocked).length} what="blocked" />
                <Stat
                  colour="#8b93a7"
                  n={merges.filter((m) => m.admissible !== true && !m.blocked).length}
                  what="queued"
                />
              </>
            ) : null
          }
        />
        {/*
          When the panel last heard from the node, on the tab bar so it is on
          screen for BOTH views - the merge queue goes stale the same way the
          todo list does, and a freshness mark that only one tab carried would
          leave the other one claiming to be current with nothing to check it
          against. It is the one thing here that must never be behind a click.
        */}
        <span className="ml-auto pr-2">
          <StreamAsOf />
        </span>
      </div>
      {tab === "merge" ? (
        <MergeQueue
          items={merges}
          tip={mergeTip}
          tipFrom={mergeTipFrom}
          decided={mergeDecided}
          loaded={mergesLoaded}
          lock={mergeLock}
        />
      ) : (
        <>
          <header className="flex flex-col gap-1 border-border border-b px-4 py-3">
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="font-semibold text-base">todos</h1>
              <span className="text-muted-foreground text-xs">
                every project you can read and every room, including the rows filed in none
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
                {/* And how many of them name no room, which is a statement about
                the DOOR they came through rather than about this page. This
                board is every room by construction - the fetch asks for no room
                and the node narrows by one only when it is asked to - so the
                number that is worth saying out loud is how much of the queue
                carries no room at all. Said at zero too: "none of them" is the
                answer somebody chasing this defect needs, and a count that
                appears only when it is bad is a count nobody trusts is running. */}
                <span
                  data-todo-roomless-count={roomless}
                  title="A todo carries the room it was raised in under fields.room. One filed through
POST /api/artifacts carries none, because that door sets none - so it belongs to
no room's panel and is only ever seen here. This board never narrows by room;
the todo panel inside a chat room is the surface that does."
                >
                  - {roomless > 0 ? `${roomless} filed in no room` : "all filed in a room"}
                </span>
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
              <Row key={todo.id} todo={todo} onTag={setTag} waiting={waiting.has(todo.id)} />
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
function Row({
  todo,
  onTag,
  waiting,
}: {
  todo: Artifact;
  onTag: (tag: string) => void;
  waiting: boolean;
}) {
  const owner = todoAssignee(todo);
  const raiser = todoRaiser(todo);
  const room = todoRoom(todo);
  const project = todo.project ?? "";
  const kind = todoKind(todo);
  const tags = todoTags(todo);
  return (
    <li
      data-todo-row={todo.id}
      data-todo-kind={kind}
      /* WHICH ROW THE RAIL MEANS. The dot beside "todos" counts mine_todo -
         assigned to you and not started - and until this nothing on the page
         said which those were. The operator: "have one unread todo, went to
         todo list - no idea which one, fix". The ids come from the node, from
         the loop that produced the number, so the mark and the count are one
         answer rather than two that can disagree. */
      data-todo-waiting={waiting ? "" : undefined}
      className={`flex flex-wrap items-baseline gap-2 border-border/60 border-b px-4 py-2 text-sm${
        waiting ? " border-primary/40 border-l-2 bg-primary/5 pl-3.5" : ""
      }`}
    >
      {/* The state on the element as well as in the badge, on the same terms as
          data-todo-kind beside it: a check that had to read the word back out of
          the label would be asserting against a sentence, and the whole point of
          the colour is that it is legible before the word is. */}
      <Badge
        variant="secondary"
        data-todo-status={todo.status || "todo"}
        style={statusStyle(todo.status)}
      >
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
      {/* Raised by X, carried by Y - two facts, drawn as two, in the colour
          each party speaks in. The raiser is only there when the row says one:
          most of this queue predates the field and drawing "raised by nobody"
          on all of it would put the least informative words on the page more
          often than any others. Who is carrying it is always drawn, unowned
          included, because an unowned todo is work nobody has picked up and
          that is the thing to see. */}
      {raiser ? (
        <span
          data-todo-raiser={raiser}
          className="shrink-0 text-muted-foreground text-xs"
          title="who this work came from"
        >
          raised by <span style={speakerStyle(raiser)}>{raiser}</span>
        </span>
      ) : null}
      <span
        data-todo-assignee={owner}
        className="shrink-0 text-muted-foreground text-xs"
        title="who is carrying this"
        style={owner ? speakerStyle(owner) : undefined}
      >
        {owner || "unowned"}
      </span>
      <Link
        to={artifactPath({ project, type: todo.type, id: todo.id }) ?? "#"}
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
      {/* Where it was agreed - and, when it was agreed nowhere, THOSE WORDS.
          Drawn on every row rather than only on the ones that have a room,
          which is the whole of this fix.

          A blank here read as neither: half this queue is filed through POST
          /api/artifacts, which sets no room, and those rows appeared beside the
          roomed ones with nothing where a room goes. "Filed nowhere" and "filed
          in general" are not the same fact - the first is a defect at the create
          door that somebody should fix, the second is ordinary - and a reader
          scanning a column cannot see the absence of a link.

          The room is a link, because it goes to that room's own panel. The
          absence is not, because there is nowhere for it to go: a row with no
          room is in no room's panel, and this board is the only place it is
          ever seen. */}
      {room ? (
        <Link
          to={`/chat/${encodeURIComponent(room)}`}
          data-todo-room={room}
          className="shrink-0 text-muted-foreground text-xs"
          title={`raised in #${room} - open that room's panel`}
        >
          #{room}
        </Link>
      ) : (
        <span
          data-todo-room=""
          data-todo-roomless=""
          className="shrink-0 text-muted-foreground/70 text-xs italic"
          title="Filed in no room: this row carries no fields.room, which is what POST /api/artifacts
leaves behind. It is in no room's todo panel and is only ever seen on this board."
        >
          no room
        </span>
      )}
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

/** withSelected keeps a chosen tag on the list of offered ones. See the call
 * site: an arriving list must never silently un-draw the control the operator
 * is filtering with. */
function withSelected(tags: string[], selected: string): string[] {
  if (!selected || tags.includes(selected)) return tags;
  return [...tags, selected].sort((a, b) => a.localeCompare(b));
}
