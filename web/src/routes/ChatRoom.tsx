import { type ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";

import { AnnouncementBanner } from "@/components/AnnouncementBanner";
import { MergeQueue } from "@/components/MergeQueue";
import { MessageBox } from "@/components/MessageBox";
import { MessageList } from "@/components/MessageList";
import { PinnedStrip } from "@/components/PinnedStrip";
import { RoomRoster } from "@/components/RoomRoster";
import { RoomSearch } from "@/components/RoomSearch";
import { RoomTodos } from "@/components/RoomTodos";
import { ThreadDag } from "@/components/ThreadDag";
import { ThreadList } from "@/components/ThreadList";
import { RoomWorklog } from "@/components/WorklogList";
import { Badge } from "@/components/ui/badge";
import {
  type ActivityItem,
  type Artifact,
  type FlowyEvent,
  type MergeRequest,
  type Presence,
  api,
} from "@/lib/api";
import { useCitation } from "@/lib/cite";
import { useSession } from "@/lib/session";
import { useUnread } from "@/lib/unread";
import { shortId } from "@/lib/utils";

/** merge folds a page of new events into the ones on screen, by id, in log order. */
function merge(current: FlowyEvent[], incoming: FlowyEvent[]): FlowyEvent[] {
  if (incoming.length === 0) return current;
  const byId = new Map(current.map((event) => [event.id, event]));
  for (const event of incoming) byId.set(event.id, event);
  return [...byId.values()].sort((a, b) => a.seq_hlc - b.seq_hlc || a.id.localeCompare(b.id));
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

/**
 * How much of a room is on screen when it opens, and how much one scroll back
 * fetches.
 *
 * A room used to open on `since=0`, which pages FORWARDS: the first answer was
 * the OLDEST 200 messages and the long poll then dragged the rest in, batch
 * after batch, until the whole history was in memory. The operator reported it
 * as "on reload the whole chat history loads", and it is also what put four of
 * eight measured loads of a 718 message room hundreds of thousands of pixels
 * short of the end - a view cannot pin itself to a transcript that keeps
 * growing underneath it.
 *
 * So the room opens on its last screenful and asks for more only when somebody
 * scrolls up to look. 60 is several screens of chat at any window size this
 * console is usable at, which matters: a window shorter than the viewport does
 * not scroll, and a transcript that does not scroll cannot ask for more.
 */
const CHAT_WINDOW = 60;

/** Pane is which of the side column's four tabs is showing. */
type Pane = "todos" | "merges" | "thread" | "worklog" | "listening";

/**
 * Every pane there is, in the order the strip draws them, and the list the path
 * is checked against. One place rather than two: a name in the union and not in
 * this list would be a tab nobody could link to, and the failure would be a
 * link that silently opens the queue instead.
 */
const PANES: Pane[] = ["todos", "merges", "thread", "worklog", "listening"];

/**
 * The colours the tab counts are drawn in, and what each one means.
 *
 * They are the same values the panes below them use - the todo statuses come
 * from lib/todos, the verdicts from MergeQueue - so a tab and the pane it opens
 * agree about what amber and red mean. The two new ones are the speaker scale's
 * cyan and violet, which are already in this console and belong to nothing that
 * appears in the side column.
 */
const COUNT_ACTIVE = "#e0a03f"; // amber - somebody is on it
const COUNT_OPEN = "#8b93a7"; // grey - waiting, and quiet on purpose
const COUNT_LAND = "#4fae7a"; // green - the node says it may land
const COUNT_REFUSED = "#d1585f"; // red - the node says it may not
const COUNT_THREAD = "#3fa3c9"; // cyan - how much has been said in the thread
const COUNT_WORKLOG = "#b07ae0"; // violet - how much the fleet has written down
const COUNT_LISTENING = "#4fae7a"; // green - somebody has an ear on this room

/**
 * One room: the messages, a box to say something as the person holding the
 * token, and the thread of whichever message is selected drawn as its DAG.
 *
 * Live updates are a long poll against /api/chat/{room}/wait. It is a loop of
 * finite requests rather than a socket on purpose - the window is bounded on
 * the server, every request carries the same bearer token as any other, and a
 * node that goes away is a failed fetch and a retry rather than a connection
 * the console has to keep alive itself.
 */
export function ChatRoom() {
  const { room = "general", pane: asked } = useParams();
  const navigate = useNavigate();
  const { token, whoami } = useSession();
  const { markRead } = useUnread();
  const [events, setEvents] = useState<FlowyEvent[]>([]);
  // What a reply attaches to and what it cites: the selected message, whole, or
  // the span of it somebody selected with the mouse. Selecting a message has
  // always named it as the parent here; now the reply says so on its face.
  const { selected, citation, cite, select, citeSpan, clear } = useCitation();
  const [live, setLive] = useState(false);
  // WHAT THIS ROOM DECIDED. Held here rather than inside the strip because the
  // strip draws messages this view already has - the pin carries an id and
  // nothing else, so the text comes from the transcript and not from a second
  // copy that could disagree with it.
  const [pinned, setPinned] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [presence, setPresence] = useState<Presence | null>(null);
  const [todos, setTodos] = useState<Artifact[]>([]);
  /** The merges raised out of this room, with the node's own verdicts. */
  const [merges, setMerges] = useState<MergeRequest[]>([]);
  const [mergesDecided, setMergesDecided] = useState(false);
  const [mergeTip, setMergeTip] = useState("");
  /**
   * WHICH PANE THE SIDE COLUMN IS SHOWING - five of them now, in one bar.
   *
   * The thread used to sit BELOW the todos and the merges permanently, so the
   * column was two tabs and two stacked panes and everything in it had half the
   * height it wanted. It is a tab like the rest now; the worklog - which until
   * recently was only ever a page away at /worklog - is the fourth, and who has
   * an ear on is the fifth.
   *
   * IN THE PATH, NOT IN STATE. A pane is a place: choosing one is somewhere a
   * person can go back from, and somewhere they can send somebody else. Held as
   * component state it was neither - the back button left the room entirely,
   * and "look at the listening tab in #general" was a sentence rather than a
   * link. /todos/merge already worked this way for the queue page; this is the
   * same shape for the room, and the diagram row needs one more level of it
   * before a shape inside a document can be cited at all.
   *
   * An unknown segment falls back to the queue rather than to a 404. A stale
   * link to a pane that has been renamed should land somebody in the room -
   * the room is what the URL is mostly about - and the strip then says what is
   * actually there.
   */
  const pane: Pane = PANES.includes(asked as Pane) ? (asked as Pane) : "todos";
  const setPane = (next: Pane) => navigate(`/chat/${encodeURIComponent(room)}/${next}`);
  // The roster's two numbers, read where the tab is drawn rather than inside
  // the pane: the strip has to report them without being opened, which is the
  // whole reason these are tabs. A presence that has not arrived is not zero -
  // it is unknown - so both read as nothing until it does, and the tab simply
  // carries no counts rather than claiming an empty room.
  const listening = presence?.listeners.filter((l) => l.state !== "lost").length ?? 0;
  const lost = presence?.listeners.filter((l) => l.state === "lost").length ?? 0;
  /** The worklog, for the pane and for the count in its tab. See the read below. */
  const [worklog, setWorklog] = useState<ActivityItem[]>([]);
  const [worklogError, setWorklogError] = useState<string | null>(null);
  const [worklogLoaded, setWorklogLoaded] = useState(false);
  const [todoError, setTodoError] = useState<string | null>(null);
  // HOW FAR BACK THIS VIEW HAS BEEN, held in a ref because the scroll handler
  // that reads it fires between renders and a reading React has not committed
  // yet is how the last three scrolling bugs in here happened.
  //
  // `before` is the node's cursor for the next older page, strictly exclusive;
  // zero means the beginning of the room has been reached and there is nothing
  // left to ask for. `read` is the generation of the room this belongs to - a
  // page of #general still in flight when the reader opens #build must not be
  // prepended into #build, which is the same out-of-order answer the todo panel
  // drops below, arriving at the top of the transcript instead of in a panel.
  const older = useRef({ before: 0, loading: false, read: 0 });
  const [moreOlder, setMoreOlder] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  // Which thread view. The list is the default because almost every thread here
  // is a straight line and a straight line drawn as a graph makes the reader do
  // layout in their head; the DAG is the honest structure and stays one key
  // away. Not persisted: it is a way of looking at THIS thread, not a setting.
  const [threadGraph, setThreadGraph] = useState(false);

  // `d` toggles the graph, because the ThreadList tells the reader it does -
  // "press d for the graph" on a message with several parents. A promise in one
  // component and no binding anywhere is the affordance lying about itself.
  //
  // It is ignored while somebody is typing: the message box is a textarea and a
  // room where the letter d switches panes mid-sentence is unusable.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "d" || e.metaKey || e.ctrlKey || e.altKey) return;
      const el = e.target as HTMLElement | null;
      const tag = el?.tagName;
      if (tag === "INPUT" || tag === "TEXTAREA" || el?.isContentEditable) return;
      setThreadGraph((on) => !on);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // Presence refreshes on its own clock. The long poll owns the messages; the
  // roster is a slower fact, and polling it on every message would make a busy
  // room the only place the roster ever updates - the exact conflation the
  // poll-based signal exists to avoid.
  useEffect(() => {
    if (!token) {
      setPresence(null);
      return;
    }
    let stopped = false;
    const load = () => {
      api
        .presence()
        .then((p) => {
          if (!stopped) setPresence(p);
        })
        .catch(() => {});
    };
    load();
    const every = setInterval(load, 15_000);
    return () => {
      stopped = true;
      clearInterval(every);
    };
  }, [token]);

  /**
   * The worklog, on a clock of its own, and NOT narrowed to this room.
   *
   * There is no such thing as this room's worklog to ask the node for. Every
   * entry is written into a room of its own - worklogRoom in worklog.go, the
   * same string for every seat and every project - so /api/activity narrowed to
   * #general comes back empty, and a tab that read it that way would say "0
   * entries" about a log with forty in it. So this reads the whole log, the
   * pane says whose entries they are, and narrowing one properly is a store
   * change rather than something to guess at from here.
   *
   * It rides its own timer rather than the room's poll, like the roster does: a
   * seat writes an entry at the end of a shift, which is a far slower fact than
   * a message, and putting it on the poll would be a third read on every
   * returning wait.
   */
  useEffect(() => {
    if (!token) {
      setWorklog([]);
      setWorklogLoaded(false);
      return;
    }
    let stopped = false;
    const read = () => {
      api
        .worklog()
        .then((page) => {
          if (stopped) return;
          // ?? [] for the same reason the pins read has it: Go marshals an
          // empty slice as null, and a null here reaches the pane and the tab's
          // count as a value that cannot be mapped over or measured.
          setWorklog(page.items ?? []);
          setWorklogError(null);
          setWorklogLoaded(true);
        })
        .catch((err) => {
          if (stopped) return;
          // Kept, not emptied. An empty list under a heading that says what
          // happened reads as "nothing happened", which is a false statement
          // rather than a missing one - see the pane's empty reads.
          setWorklogError(err instanceof Error ? err.message : String(err));
          setWorklogLoaded(true);
        });
    };
    read();
    const every = setInterval(read, 15_000);
    return () => {
      stopped = true;
      clearInterval(every);
    };
  }, [token]);

  // Which read of the todos is the current one. Two of them are in flight at
  // once whenever somebody edits the panel - the write's own reload, and the
  // long poll's, because the write puts a message in the room and the poll
  // comes back for it - and they can answer out of order. The older answer is
  // dropped rather than painted: it is a picture of the queue from before the
  // edit, and letting it land would show the assignee reverting for exactly as
  // long as it takes the next poll to correct it.
  const todoRead = useRef(0);

  /**
   * The room's todos, read through the same permission filter as everything
   * else: room is a narrowing on the artifact list, not a second visibility.
   */
  const loadTodos = useCallback(async () => {
    const mine = ++todoRead.current;
    try {
      api
        .roomMergeQueue(room)
        .then((q) => {
          setMerges(q.items ?? []);
          setMergesDecided(Boolean(q.decided));
          setMergeTip(q.target_tip ?? "");
        })
        .catch(() => setMerges([]));
      const page = await api.roomTodos(room);
      if (mine !== todoRead.current) return;
      setTodos(page.artifacts);
      setTodoError(null);
    } catch (err) {
      if (mine !== todoRead.current) return;
      setTodoError(err instanceof Error ? err.message : String(err));
    }
  }, [room]);

  // The strip rides the room's clock, the way the todo panel does: the poll
  // returns when somebody speaks and when its window runs out, which is exactly
  // when a pin could have been put up or taken down. A second timer would be a
  // second idea of how often this room is alive.
  //
  // A failed read leaves the strip as it was rather than emptying it. A room
  // whose decisions vanish on one bad request looks like a room that unpinned
  // them, and that is a worse lie than a strip a few seconds out of date.
  const loadPins = useCallback(async () => {
    try {
      const view = await api.pins(room);
      // ?? [] because Go marshals an empty slice as null, and a null here
      // reaches the strip as a value that cannot be mapped over.
      setPinned(view.pinned ?? []);
    } catch {
      /* keep what is on screen */
    }
  }, [room]);

  useEffect(() => {
    setEvents([]);
    clear();
    setLive(false);
    setError(null);
    setTodos([]);
    setMerges([]);
    // A room's decisions are its own. Carrying the last room's strip across
    // would draw pins pointing at messages this room does not have, which the
    // strip then hides - so it would look like the new room had unpinned
    // everything rather than like the console had not caught up.
    setPinned([]);
    older.current = { before: 0, loading: false, read: older.current.read + 1 };
    setMoreOlder(false);
    setLoadingOlder(false);
    if (!token) return;

    let stopped = false;
    const controller = new AbortController();

    const watch = async () => {
      let cursor = 0;
      try {
        // THE LAST SCREENFUL, NOT THE FIRST. See CHAT_WINDOW: a forward read
        // from zero opens the room on its oldest page and then pages the entire
        // history in behind the poll. The window comes back in log order and
        // carries both ends - `cursor` to follow the room forwards, `before` to
        // walk it backwards when somebody scrolls up.
        const page = await api.roomWindow(room, CHAT_WINDOW);
        if (stopped) return;
        setEvents(page.events);
        // Short of the window means the whole room fits on screen, so there is
        // nothing older to offer and the transcript says where it begins.
        older.current.before = page.before ?? 0;
        setMoreOlder(page.events.length >= CHAT_WINDOW && (page.before ?? 0) > 0);
        cursor = page.cursor;
        setLive(true);
        void loadTodos();
        void loadPins();
      } catch (err) {
        if (!stopped) setError(err instanceof Error ? err.message : String(err));
        return;
      }

      while (!stopped) {
        try {
          const before = cursor;
          const page = await api.wait(room, cursor, controller.signal);
          if (stopped) return;
          if (page.events.length > 0) setEvents((current) => merge(current, page.events));
          // Advance on every successful return, not only when something landed
          // on screen. The server answers `seq_hlc > cursor`, so a cursor that
          // moves only when the UI has events to show is a cursor that can
          // stick - and a stuck cursor makes every subsequent request return
          // instantly rather than block, which is the whole loop turning into a
          // flood aimed at your own node.
          if (page.cursor > cursor) cursor = page.cursor;
          setLive(true);
          setError(null);
          // The panel rides the room's clock rather than one of its own. The
          // poll comes back when somebody says something and when its window
          // runs out, which is exactly when a plan agreed in this room could
          // have moved - and a second timer would be a second idea of how
          // often this room is alive.
          void loadTodos();
          void loadPins();
          // The invariant, enforced rather than assumed: a successful wait
          // either blocked out its window or moved the cursor. If it did
          // neither, this loop is spinning - 567 requests a second, measured
          // against a stand-in node - so pause before going round again. On
          // real traffic an answered wait always advances the cursor, so this
          // costs a busy room nothing. It matters more now than it did an hour
          // ago: riding this loop is a todo read as well as a message read, so
          // an unbounded loop is two floods rather than one.
          if (cursor === before) await sleep(1000);
        } catch (err) {
          if (stopped) return;
          setLive(false);
          setError(err instanceof Error ? err.message : String(err));
          // The node is down or the token stopped working. Back off rather
          // than spin: the poll is a loop, and a loop with no pause in the
          // failure path is a denial of service aimed at your own node.
          await sleep(2000);
        }
      }
    };

    void watch();
    return () => {
      stopped = true;
      controller.abort();
    };
  }, [room, token, loadTodos, loadPins, clear]);

  /**
   * The page before the one on screen, asked for because somebody scrolled up
   * to look for it.
   *
   * It is safe to call as often as a scroll fires: one read at a time, and
   * nothing at all once the beginning of the room has been reached. The node's
   * `before` is strictly exclusive and its window ends on a complete clock
   * reading, so a page never repeats a message and never steps over one - see
   * store.EventsBefore.
   *
   * WHAT THE READER SEES IS THE TRANSCRIPT'S JOB, not this one's. Prepending
   * moves everything on screen down by the height of what arrived, and holding
   * the reader on the message they were looking at has to happen before the
   * browser paints - see MessageList, which does it in a layout effect.
   */
  const loadOlder = useCallback(async () => {
    const at = older.current;
    if (at.loading || at.before <= 0) return;
    const mine = at.read;
    at.loading = true;
    setLoadingOlder(true);
    try {
      const page = await api.roomWindow(room, CHAT_WINDOW, at.before);
      // A different room now, so this page belongs to a transcript that is no
      // longer on screen. Dropped rather than merged: the ids would not collide
      // but the messages would, and a room showing another room's history is a
      // worse failure than a scroll that fetched nothing.
      if (older.current.read !== mine) return;
      if (page.events.length === 0) {
        older.current.before = 0;
        setMoreOlder(false);
        return;
      }
      setEvents((current) => merge(current, page.events));
      older.current.before = page.before ?? 0;
      setMoreOlder(page.events.length >= CHAT_WINDOW && (page.before ?? 0) > 0);
    } catch (err) {
      // Left as it was on purpose: `before` still points at the same page, so
      // the next scroll retries it. The room's own error line carries this
      // rather than a second one, because a failed look further back is the
      // same node being unreachable as a failed poll.
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (older.current.read === mine) {
        older.current.loading = false;
        setLoadingOlder(false);
      }
    }
  }, [room]);

  const send = useCallback(
    async (body: string, to: string) => {
      const said = await api.say(
        room,
        body,
        selected ? [selected.id] : [],
        selected?.thread,
        to,
        cite,
      );
      // The poll will bring it back anyway; showing it now is what makes the
      // box feel like it did something.
      setEvents((current) => merge(current, [said]));
    },
    [room, selected, cite],
  );

  /**
   * Raising one from the room: the message the room has selected is what it is
   * raised out of, so a conversation becomes a plan without leaving the
   * conversation. The node writes the todo and one message in the room
   * together, and the poll brings that message back like anybody else's.
   */
  const raise = useCallback(
    async (title: string, category?: string) => {
      await api.raiseTodo(room, title, "", selected?.id, category);
      await loadTodos();
    },
    [room, selected, loadTodos],
  );

  /**
   * Who is carrying one of them. The write goes to the node and the panel is
   * refilled from the node's answer - the same list the long poll refills it
   * with - so there is no second idea of who has what for a poll to overwrite.
   */
  const assign = useCallback(
    async (id: string, assignee: string, expect: string) => {
      await api.assignTodo(room, id, assignee, expect);
      await loadTodos();
    },
    [room, loadTodos],
  );

  /**
   * What this room has been read to. The transcript decides when - it is the
   * one thing that knows whether the reader is at the bottom - and the mark it
   * moves is the node's, so the badge clears on every device rather than in
   * the tab that happened to be open. See lib/unread.
   */
  const seen = useCallback((through: string) => markRead(room, through), [room, markRead]);

  const thread = selected?.thread ?? events.at(-1)?.thread;
  const threadEvents = thread ? events.filter((event) => event.thread === thread) : [];

  return (
    <div className="flex h-full">
      <section className="flex min-w-0 flex-1 flex-col">
        <header className="flex items-center gap-3 border-border border-b px-4 py-3">
          <h1 className="font-semibold text-base">#{room}</h1>
          {whoami?.project ? <Badge variant="outline">{whoami.project}</Badge> : null}
          <Badge variant={live ? "default" : "outline"}>{live ? "watching" : "idle"}</Badge>
          {/*
            ON SCREEN, not "in the room". It used to be able to say either,
            because the view held everything the room had ever said; it opens on
            a window now, so a count with no qualifier would report 60 for a room
            of seven hundred messages and be read as the room's size.
          */}
          <span className="text-muted-foreground text-xs">
            {events.length} message{events.length === 1 ? "" : "s"} on screen
          </span>
          <RoomSearch />
        </header>

        {/*
          Above the transport, not in it. An announcement that the node is
          going down is not a message somebody said in this room - it does not
          belong in the log, it must not scroll away with it, and it has to be
          the same on every route that shows it.
        */}
        <AnnouncementBanner />

        {/*
          What this room decided, above the transcript rather than in it. A pin
          is not a message and must not scroll away with them - that is the
          whole reason a room pins anything.
        */}
        <PinnedStrip
          pinned={pinned}
          events={events}
          onSelect={select}
          onUnpin={(id) => {
            // Optimistic on purpose, unlike the assignee cell: unpinning is
            // reversible in one click and the poll corrects it within a window,
            // whereas leaving the line up until the next poll reads as a button
            // that did nothing.
            setPinned((current) => current.filter((pin) => pin !== id));
            api.unpin(room, id).catch(() => void loadPins());
          }}
        />

        {error ? (
          <div className="border-destructive/40 border-b bg-destructive/10 px-4 py-2 text-destructive text-xs">
            {error}
          </div>
        ) : null}

        <MessageList
          events={events}
          selected={selected}
          onSelect={select}
          onCite={citeSpan}
          me={{ user: whoami?.user, agent: whoami?.agent }}
          onSeen={seen}
          onOlder={loadOlder}
          moreOlder={moreOlder}
          loadingOlder={loadingOlder}
          room={room}
        />

        {/*
          Pinning acts on the SELECTED message, and it lives here rather than on
          the row itself. Selecting is already how a reply attaches and how a
          span is cited; adding a third control to every row would put three
          affordances on a thing whose main job is to be readable. Selected is
          also the message the reader has just decided matters, which is exactly
          when they reach for the pin.
        */}
        {selected && !pinned.includes(selected.id) ? (
          <div className="border-border border-t px-4 py-1 text-xs">
            <button
              type="button"
              className="text-muted-foreground hover:text-foreground"
              onClick={() => {
                setPinned((current) => [...current, selected.id]);
                api.pin(room, selected.id).catch(() => void loadPins());
              }}
            >
              📌 pin the selected message
            </button>
          </div>
        ) : null}

        <MessageBox citation={citation} clearReply={clear} disabled={!token} onSend={send} />
      </section>

      {/*
        The side column, beside the conversation rather than a page away from
        it: who is here, what this room has decided to do, what is waiting to
        land, the thread of whichever message is selected, and what the last few
        seats wrote down. One of them at a time, chosen from a bar whose titles
        carry the numbers - so the column is one pane deep and a person can see
        which of the four wants them without opening any.
      */}
      <aside className="flex w-[26rem] shrink-0 flex-col border-border border-l">
        {/*
          Tabs, not a stack. The operator asked four times: the counts belong in
          the tab title so a person can see whether a pane needs them without
          opening it. A merge list below the todos answers the same question
          only after you scroll to it.

          FOUR of them now, and the thread is one of the four. The thread used
          to be rendered under whichever pane was chosen, permanently - so the
          column was two tabs and two stacked panes, the thread with a third of
          the height and the pane above it with the rest, whether or not anybody
          was reading either. The worklog was worse off than that: it was a
          whole page away, at /worklog, so reading what the last few seats did
          meant leaving the room.

          The bar WRAPS rather than shrinking what is in it. The counts are the
          point of these titles, and a bar that fits four of them onto one line
          by dropping them is a bar that has thrown away the thing it is for.
        */}
        <div
          className="flex flex-wrap items-center gap-1 border-border border-b px-2 pt-2"
          role="tablist"
        >
          <PaneTab name="todos" on={pane === "todos"} pick={setPane}>
            <Count colour={COUNT_ACTIVE}>
              {todos.filter((t) => (t.status || "todo") === "active").length} active
            </Count>
            <Count colour={COUNT_OPEN}>
              {todos.filter((t) => (t.status || "todo") !== "done").length} open
            </Count>
          </PaneTab>
          <PaneTab name="merges" on={pane === "merges"} pick={setPane}>
            <Count colour={COUNT_LAND}>
              {merges.filter((m) => m.admissible === true).length} may land
            </Count>
            <Count colour={COUNT_REFUSED}>
              {merges.filter((m) => m.admissible === false).length} refused
            </Count>
          </PaneTab>
          {/*
            The thread's count is the one the pane's own header already draws,
            and it is the number that says whether the tab is worth opening: a
            reply that landed on the message somebody is looking at moves it.
          */}
          <PaneTab name="thread" on={pane === "thread"} pick={setPane}>
            <Count colour={COUNT_THREAD}>
              {threadEvents.length} event{threadEvents.length === 1 ? "" : "s"}
            </Count>
          </PaneTab>
          {/*
            The worklog's count is the WHOLE log's, because the log is not per
            room - see the read above, and RoomWorklog, which says so where a
            reader of the numbers will see it.
          */}
          <PaneTab name="worklog" on={pane === "worklog"} pick={setPane}>
            <Count colour={COUNT_WORKLOG}>
              {worklog.length} entr{worklog.length === 1 ? "y" : "ies"}
            </Count>
          </PaneTab>
          {/*
            AND WHO HAS AN EAR ON IS A TAB, not the header it used to be.
            It was drawn above the strip, permanently, so it took height from
            the queue the column exists for and took more of it as the fleet
            grew - "listening panel grew so much i cant see todos anymore". A
            roster is a list of rows, and the rule this pane now follows is
            that a list of rows is a tab.
            Its counts are the two states worth acting on: who is listening,
            and who was armed and stopped. The second is the one that answers
            "why is that agent not replying", so it is on the strip rather
            than a click away, and it stays off the strip entirely when it is
            zero - a red nought is a thing to check every time you look at it.
          */}
          <PaneTab name="listening" on={pane === "listening"} pick={setPane}>
            <Count colour={COUNT_LISTENING}>{listening} listening</Count>
            {lost > 0 ? <Count colour={COUNT_REFUSED}>{lost} lost</Count> : null}
          </PaneTab>
        </div>

        {/*
          One body at a time, each named on the element. The tab and its body
          are two halves of one claim - a bar whose buttons all draw the same
          pane looks correct in a screenshot - so the body says which pane it is
          and the check reads it off the page rather than off the tab it just
          clicked.
        */}
        {pane === "todos" ? (
          <div data-room-pane-body="todos" className="flex min-h-0 flex-1 flex-col">
            <RoomTodos
              room={room}
              todos={todos}
              raiseFrom={selected}
              disabled={!token}
              error={todoError}
              onRaise={raise}
              onAssign={assign}
            />
          </div>
        ) : null}
        {pane === "merges" ? (
          <div data-room-pane-body="merges" className="min-h-0 flex-1 overflow-y-auto">
            <MergeQueue
              items={merges}
              tip={mergeTip}
              tipFrom="deployed"
              decided={mergesDecided}
              loaded={true}
            />
          </div>
        ) : null}
        {pane === "thread" ? (
          <section data-room-pane-body="thread" className="flex min-h-0 flex-1 flex-col">
            <header className="flex items-center gap-2 border-border border-b px-4 py-3">
              <h2 className="font-semibold text-sm">thread</h2>
              {thread ? (
                <span className="font-mono text-muted-foreground text-xs">
                  {shortId(thread, 10)}
                </span>
              ) : null}
              {/*
                Reading is the default and the graph is a keystroke away. The
                button says which view you would get rather than which you are
                in, because a toggle that names the current state reads as a
                label and gets ignored.
              */}
              <button
                type="button"
                onClick={() => setThreadGraph((on) => !on)}
                className="rounded border border-border px-1.5 py-0.5 text-muted-foreground text-xs hover:bg-accent/60"
                title="d"
              >
                {threadGraph ? "list" : "graph"}
              </button>
              <span className="ml-auto text-muted-foreground text-xs">
                {threadEvents.length} event
                {threadEvents.length === 1 ? "" : "s"}
              </span>
            </header>
            <div className="min-h-0 flex-1">
              {threadGraph ? (
                <ThreadDag events={threadEvents} />
              ) : (
                <ThreadList events={threadEvents} selected={selected} onSelect={select} />
              )}
            </div>
          </section>
        ) : null}
        {pane === "listening" ? (
          <div data-room-pane-body="listening" className="min-h-0 flex-1 overflow-y-auto">
            <RoomRoster presence={presence} />
          </div>
        ) : null}
        {pane === "worklog" ? (
          <div data-room-pane-body="worklog" className="flex min-h-0 flex-1 flex-col">
            <RoomWorklog
              items={worklog}
              error={worklogError}
              loaded={worklogLoaded}
              token={Boolean(token)}
            />
          </div>
        ) : null}
      </aside>
    </div>
  );
}

/**
 * One tab in the side column's bar: its name, its counts, and whether it is the
 * one showing.
 *
 * The counts are children rather than a prop because each pane counts different
 * things - two for the todos, two for the merges, one each for the thread and
 * the worklog - and a component that took `active` and `open` would be a todo
 * tab that the other three borrowed.
 */
function PaneTab({
  name,
  on,
  pick,
  children,
}: {
  name: Pane;
  on: boolean;
  pick: (pane: Pane) => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={on}
      data-room-pane={name}
      onClick={() => pick(name)}
      className={`flex items-center gap-2 rounded-t px-3 py-1.5 text-sm ${
        on
          ? "border-border border-x border-t bg-background font-medium"
          : "text-muted-foreground hover:text-foreground"
      }`}
    >
      {name}
      {children}
    </button>
  );
}

/**
 * One number in a tab title, in the colour of the thing it counts.
 *
 * Named on the element as well as coloured: "0 refused" and "0 open" are the
 * same string to a page-text search, and the check that reads these has to be
 * able to say WHICH tab it read a number off.
 */
function Count({ colour, children }: { colour: string; children: ReactNode }) {
  return (
    <span data-room-count="" className="text-xs" style={{ color: colour }}>
      {children}
    </span>
  );
}
