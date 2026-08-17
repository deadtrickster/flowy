import { useCallback, useEffect, useRef, useState } from "react";
import { useParams } from "react-router-dom";

import { AnnouncementBanner } from "@/components/AnnouncementBanner";
import { MessageBox } from "@/components/MessageBox";
import { MessageList } from "@/components/MessageList";
import { RoomRoster } from "@/components/RoomRoster";
import { RoomTodos } from "@/components/RoomTodos";
import { ThreadDag } from "@/components/ThreadDag";
import { Badge } from "@/components/ui/badge";
import { type Artifact, type FlowyEvent, type Presence, api } from "@/lib/api";
import { useCitation } from "@/lib/cite";
import { useSession } from "@/lib/session";
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
  const { room = "general" } = useParams();
  const { token, whoami } = useSession();
  const [events, setEvents] = useState<FlowyEvent[]>([]);
  // What a reply attaches to and what it cites: the selected message, whole, or
  // the span of it somebody selected with the mouse. Selecting a message has
  // always named it as the parent here; now the reply says so on its face.
  const { selected, citation, cite, select, citeSpan, clear } = useCitation();
  const [live, setLive] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [presence, setPresence] = useState<Presence | null>(null);
  const [todos, setTodos] = useState<Artifact[]>([]);
  const [todoError, setTodoError] = useState<string | null>(null);

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
      const page = await api.roomTodos(room);
      if (mine !== todoRead.current) return;
      setTodos(page.artifacts);
      setTodoError(null);
    } catch (err) {
      if (mine !== todoRead.current) return;
      setTodoError(err instanceof Error ? err.message : String(err));
    }
  }, [room]);

  useEffect(() => {
    setEvents([]);
    clear();
    setLive(false);
    setError(null);
    setTodos([]);
    if (!token) return;

    let stopped = false;
    const controller = new AbortController();

    const watch = async () => {
      let cursor = 0;
      try {
        const page = await api.room(room);
        if (stopped) return;
        setEvents(page.events);
        cursor = page.cursor;
        setLive(true);
        void loadTodos();
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
  }, [room, token, loadTodos, clear]);

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
    async (title: string) => {
      await api.raiseTodo(room, title, "", selected?.id);
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
    async (id: string, assignee: string) => {
      await api.assignTodo(room, id, assignee);
      await loadTodos();
    },
    [room, loadTodos],
  );

  const thread = selected?.thread ?? events.at(-1)?.thread;
  const threadEvents = thread ? events.filter((event) => event.thread === thread) : [];

  return (
    <div className="flex h-full">
      <section className="flex min-w-0 flex-1 flex-col">
        <header className="flex items-center gap-3 border-border border-b px-4 py-3">
          <h1 className="font-semibold text-base">#{room}</h1>
          {whoami?.project ? <Badge variant="outline">{whoami.project}</Badge> : null}
          <Badge variant={live ? "default" : "outline"}>{live ? "watching" : "idle"}</Badge>
          <span className="ml-auto text-muted-foreground text-xs">
            {events.length} message{events.length === 1 ? "" : "s"}
          </span>
        </header>

        {/*
          Above the transport, not in it. An announcement that the node is
          going down is not a message somebody said in this room - it does not
          belong in the log, it must not scroll away with it, and it has to be
          the same on every route that shows it.
        */}
        <AnnouncementBanner />

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
        />

        <MessageBox citation={citation} clearReply={clear} disabled={!token} onSend={send} />
      </section>

      {/*
        The side column, beside the conversation rather than a page away from
        it: who is here, what this room has decided to do, and the DAG of
        whichever message is selected. The todos sit above the thread because
        the plan is what somebody in a room glances at; the thread is what they
        open when they are answering one message.
      */}
      <aside className="flex w-[26rem] shrink-0 flex-col border-border border-l">
        <RoomRoster presence={presence} />

        <RoomTodos
          room={room}
          todos={todos}
          raiseFrom={selected}
          disabled={!token}
          error={todoError}
          onRaise={raise}
          onAssign={assign}
        />

        <section className="flex min-h-0 flex-1 flex-col">
          <header className="flex items-center gap-2 border-border border-b px-4 py-3">
            <h2 className="font-semibold text-sm">thread</h2>
            {thread ? (
              <span className="font-mono text-muted-foreground text-xs">{shortId(thread, 10)}</span>
            ) : null}
            <span className="ml-auto text-muted-foreground text-xs">
              {threadEvents.length} event{threadEvents.length === 1 ? "" : "s"}
            </span>
          </header>
          <div className="min-h-0 flex-1">
            <ThreadDag events={threadEvents} />
          </div>
        </section>
      </aside>
    </div>
  );
}
