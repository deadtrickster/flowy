import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";

import { MessageBox } from "@/components/MessageBox";
import { MessageList } from "@/components/MessageList";
import { RoomTodos } from "@/components/RoomTodos";
import { Badge } from "@/components/ui/badge";
import { type Artifact, type FlowyEvent, api } from "@/lib/api";
import { useCitation } from "@/lib/cite";
import { useSession } from "@/lib/session";
import { speakerStyle } from "@/lib/speakercolour";
import { clock, speaker } from "@/lib/utils";

/**
 * The room a document owns.
 *
 * A document is talked about, and until this the talking had nowhere to go: a
 * report was read on its own page and discussed in #general, where the report
 * is a link somebody has to follow to know what the sentence is about. So every
 * document gets a room of its own, named after it, and it is a room in the
 * ordinary sense - the same log, the same permission filter, the same todos
 * narrowed by the same `room` field. Nothing on the node had to learn what a
 * document room is, which is the point: a room here is a name, not a registered
 * object, so /chat/doc-01J... is a real page and this panel is a second view of
 * it rather than a private inbox.
 *
 * The prefix is there so the name says what it is when it shows up in a log
 * line or a URL, and so a room somebody names by hand can never collide with a
 * document's. 4 + 26 characters of ULID is well inside the node's 64.
 */
export function documentRoom(id: string): string {
  return `doc-${id}`;
}

/** merge folds a page of new events into the ones on screen, by id, in log order. */
function merge(current: FlowyEvent[], incoming: FlowyEvent[]): FlowyEvent[] {
  if (incoming.length === 0) return current;
  const byId = new Map(current.map((event) => [event.id, event]));
  for (const event of incoming) byId.set(event.id, event);
  return [...byId.values()].sort((a, b) => a.seq_hlc - b.seq_hlc || a.id.localeCompare(b.id));
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

interface Props {
  /** The document this is the conversation about. */
  room: string;
  /**
   * quote is words out of the document to drop into the draft, or null. It is
   * an object rather than a string because quoting the same sentence twice is
   * two actions and a string would be the same value both times - the identity
   * of this object is what tells the box that somebody asked again.
   */
  quote: { text: string } | null;
}

/**
 * The conversation about one document, and the work it produced: the same
 * transcript, box and todo panel a room has, beside the document instead of
 * beside a room's messages.
 *
 * The components are the room's own - MessageList, MessageBox, RoomTodos - so a
 * message here is selected, cited and answered exactly as it is in #general,
 * and a todo raised here is the same row the queue counts. What this does NOT
 * carry is the rest of the room view: presence, pins and merges, and the full
 * thread pane. Threads are here as a directory - roots with their counts, each
 * one click from the room's list/DAG view and the composer that answers it -
 * because four panes in a 26rem column would push the two that matter off the
 * screen.
 */
export function DocumentPanes({ room, quote }: Props) {
  const { whoami } = useSession();
  const signedIn = whoami != null;
  const [events, setEvents] = useState<FlowyEvent[]>([]);
  const [todos, setTodos] = useState<Artifact[]>([]);
  const [live, setLive] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [todoError, setTodoError] = useState<string | null>(null);
  // How long each thread on this page is, as the node counted it - the same
  // map the room's thread tab reads, merged over the same pages this pane
  // polls. A thread missing from here is drawn with the events in the
  // window, never as zero.
  const [threadSizes, setThreadSizes] = useState<Record<string, number>>({});
  const { selected, citation, cite, select, citeSpan, clear } = useCitation();

  // Which read of the todos is the current one, for the room panel's reason:
  // the write's own reload and the long poll's are in flight together whenever
  // somebody edits the panel, and the older answer is a picture of the queue
  // from before the edit.
  const todoRead = useRef(0);

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

  // The same long poll the room runs, against this document's room. A loop of
  // finite requests rather than a socket: the window is bounded on the server,
  // every request carries the same bearer token, and a node that goes away is a
  // failed fetch and a retry.
  useEffect(() => {
    setEvents([]);
    setTodos([]);
    setThreadSizes({});
    setLive(false);
    setError(null);
    clear();
    if (!signedIn) return;

    let stopped = false;
    const controller = new AbortController();

    const watch = async () => {
      let cursor = 0;
      try {
        const page = await api.room(room);
        if (stopped) return;
        setEvents(page.events);
        if (page.threads) setThreadSizes((current) => ({ ...current, ...page.threads }));
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
          if (page.threads) setThreadSizes((current) => ({ ...current, ...page.threads }));
          // Advance on every successful return, not only when something landed
          // on screen: the server answers `seq_hlc > cursor`, so a cursor that
          // only moves when there is something to draw sticks, and a stuck
          // cursor turns this loop into a flood aimed at your own node.
          if (page.cursor > cursor) cursor = page.cursor;
          setLive(true);
          setError(null);
          void loadTodos();
          // A successful wait either blocked out its window or moved the
          // cursor. If it did neither, this loop is spinning - pause before
          // going round again.
          if (cursor === before) await sleep(1000);
        } catch (err) {
          if (stopped) return;
          setLive(false);
          setError(err instanceof Error ? err.message : String(err));
          await sleep(2000);
        }
      }
    };

    void watch();
    return () => {
      stopped = true;
      controller.abort();
    };
  }, [room, signedIn, loadTodos, clear]);

  const send = useCallback(
    async (body: string, to: string, attachments: string[]) => {
      const said = await api.say(
        room,
        body,
        selected ? [selected.id] : [],
        selected?.thread,
        to,
        cite,
        attachments,
      );
      // The poll brings it back anyway; showing it now is what makes the box
      // feel like it did something.
      setEvents((current) => merge(current, [said]));
    },
    [room, selected, cite],
  );

  const raise = useCallback(
    async (title: string, category?: string) => {
      await api.raiseTodo(room, title, "", selected?.id, category);
      await loadTodos();
    },
    [room, selected, loadTodos],
  );

  const assign = useCallback(
    async (id: string, assignee: string, expect: string) => {
      await api.assignTodo(room, id, assignee, expect);
      await loadTodos();
    },
    [room, loadTodos],
  );

  /**
   * WHAT TO DO FIRST, the same verb the room panel uses. A document's todo
   * panel is the same component, so it takes the same handler rather than a
   * second one that would drift.
   */
  const rank = useCallback(
    async (id: string, priority: string) => {
      await api.setPriority(id, priority);
      await loadTodos();
    },
    [loadTodos],
  );

  // The messages that STARTED a thread, in log order. The say door hands a
  // message answering nothing a fresh thread id, and a reply the thread of the
  // message it answers - so a thread's starter is the OLDEST event in the
  // window carrying that thread. Never the event whose thread is its own id:
  // the store writes no such thing (chat.go's sayInRoom), and this pane's
  // first cut looked for exactly that and listed nothing - the gate caught it
  // in the check's own words, "the discussion pane lists no threads".
  // A thread of one message is not listed - the empty state says what a
  // thread is, a reply starts one - and the size is the node's own number
  // when it has one, the window's otherwise.
  const threadRoots = (() => {
    const starters: FlowyEvent[] = [];
    const seen = new Set<string>();
    for (const event of events) {
      if (seen.has(event.thread)) continue;
      seen.add(event.thread);
      starters.push(event);
    }
    return starters.filter(
      (event) =>
        (threadSizes[event.thread] ??
          events.filter((candidate) => candidate.thread === event.thread).length) > 1,
    );
  })();

  // THE SECOND PANEL, and it gets the same verb rather than a narrower one.
  // This pane draws the same RoomTodos: a row waiting on somebody has to read
  // the same here as it does beside a room, or the two surfaces disagree about
  // what a row is. The compiler found this call site, which is the argument for
  // the prop being required rather than optional.
  const waitOn = useCallback(
    async (id: string, who: string, asked: string) => {
      await api.setWaitingOn(id, who, asked);
      await loadTodos();
    },
    [loadTodos],
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <section className="flex min-h-0 flex-[3] flex-col border-border border-b">
        <header className="flex items-center gap-2 border-border border-b px-4 py-2">
          <h2 className="font-semibold text-sm">discussion</h2>
          <Badge variant={live ? "default" : "outline"}>{live ? "watching" : "idle"}</Badge>
          <span className="text-muted-foreground text-xs">
            {events.length} message{events.length === 1 ? "" : "s"}
          </span>
          {/*
            The same conversation at full width, for when the discussion is the
            thing being done rather than the document. It is an ordinary room
            and reading it in the room view proves it.
          */}
          <Link
            to={`/chat/${room}`}
            className="ml-auto text-muted-foreground text-xs hover:text-foreground"
          >
            open as a room
          </Link>
        </header>

        {error ? (
          <div className="border-destructive/40 border-b bg-destructive/10 px-4 py-2 text-destructive text-xs">
            {error}
          </div>
        ) : null}

        {/*
          THE TRANSCRIPT IS A FLEX CHILD OF THIS SECTION, with nothing between.
          It was wrapped in a plain block div, and that div is what the operator
          reported as "reports chat has send button but no text area": MessageList
          sizes itself with `flex min-h-0 flex-1`, which needs a flex parent to
          mean anything. Inside a block it fell back to height:auto, grew to the
          full length of the conversation, and - being position:relative, so it
          paints after the static form below it - covered the message box. The
          box was there the whole time, correctly sized and enabled, and no click
          could reach it. Every unit test and every reading of the DOM agreed it
          was fine.

          It only shows once the conversation is longer than the pane, which is
          why a document room with three messages in it looks perfect. See
          web/scripts/docchat-check.mjs, which drives both lengths.
        */}
        <MessageList
          events={events}
          selected={selected}
          onSelect={select}
          onCite={citeSpan}
          me={{ user: whoami?.user, agent: whoami?.agent }}
        />

        <MessageBox
          citation={citation}
          clearReply={clear}
          disabled={!signedIn}
          onSend={send}
          quote={quote}
          room={room}
        />
      </section>

      {/*
        WHAT THE CONVERSATION HAS BEEN, as a directory rather than the pane. The
        full thread view - the reading list, the DAG, the composer that answers
        it - lives in the room and is one click away per thread; the operator's
        p5 answer put threads in this pane and said the rest of it stays a link.
        The count is the node's own page.threads number when it has one, and the
        window's otherwise - a root older than the window has no events to
        count here, and drawing it as zero would be a lie about the thread.
      */}
      <section className="flex min-h-0 flex-[2] flex-col border-border border-b">
        <header className="flex items-center gap-2 border-border border-b px-4 py-2">
          <h2 className="font-semibold text-sm">threads</h2>
          <span className="text-muted-foreground text-xs">{threadRoots.length}</span>
        </header>
        <ul className="min-h-0 flex-1 overflow-y-auto">
          {threadRoots.length === 0 ? (
            <li className="px-4 py-3 text-muted-foreground text-xs">
              no threads yet - a reply starts one
            </li>
          ) : (
            threadRoots.map((event) => {
              const name = speaker(event);
              const count =
                threadSizes[event.thread] ??
                events.filter((candidate) => candidate.thread === event.thread).length;
              return (
                <li key={event.id} className="border-border/60 border-b px-4 py-2 text-xs">
                  <Link
                    to={`/chat/${room}/thread/${event.id}`}
                    data-thread-link={event.id}
                    className="flex items-center gap-2 hover:underline"
                  >
                    <span className="rounded px-1 font-mono" style={speakerStyle(name)}>
                      {name}
                    </span>
                    <span className="text-muted-foreground">
                      {count} message{count === 1 ? "" : "s"}
                    </span>
                    <span className="ml-auto text-muted-foreground">{clock(event.created)}</span>
                  </Link>
                </li>
              );
            })
          )}
        </ul>
      </section>

      <section className="flex min-h-0 flex-[2] flex-col overflow-y-auto">
        <RoomTodos
          room={room}
          todos={todos}
          raiseFrom={selected}
          disabled={!signedIn}
          error={todoError}
          onRaise={raise}
          onAssign={assign}
          onPriority={rank}
          onWaitingOn={waitOn}
        />
      </section>
    </div>
  );
}
