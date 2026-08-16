import { useCallback, useEffect, useState } from "react";
import { useParams } from "react-router-dom";

import { AnnouncementBanner } from "@/components/AnnouncementBanner";
import { MessageBox } from "@/components/MessageBox";
import { MessageList } from "@/components/MessageList";
import { ThreadDag } from "@/components/ThreadDag";
import { Badge } from "@/components/ui/badge";
import { type FlowyEvent, api } from "@/lib/api";
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
  const [selected, setSelected] = useState<FlowyEvent | null>(null);
  const [live, setLive] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setEvents([]);
    setSelected(null);
    setLive(false);
    setError(null);
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
      } catch (err) {
        if (!stopped) setError(err instanceof Error ? err.message : String(err));
        return;
      }

      while (!stopped) {
        try {
          const page = await api.wait(room, cursor, controller.signal);
          if (stopped) return;
          if (page.events.length > 0) {
            setEvents((current) => merge(current, page.events));
            cursor = page.cursor;
          }
          setLive(true);
          setError(null);
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
  }, [room, token]);

  const send = useCallback(
    async (body: string, to: string) => {
      const said = await api.say(room, body, selected ? [selected.id] : [], selected?.thread, to);
      // The poll will bring it back anyway; showing it now is what makes the
      // box feel like it did something.
      setEvents((current) => merge(current, [said]));
    },
    [room, selected],
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
          onSelect={setSelected}
          me={{ user: whoami?.user, agent: whoami?.agent }}
        />

        <MessageBox
          replyTo={selected}
          clearReply={() => setSelected(null)}
          disabled={!token}
          onSend={send}
        />
      </section>

      <aside className="flex w-[26rem] shrink-0 flex-col border-border border-l">
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
      </aside>
    </div>
  );
}
