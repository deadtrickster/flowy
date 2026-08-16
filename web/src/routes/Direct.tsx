import { CornerDownLeft, Lock } from "lucide-react";
import { type KeyboardEvent, useCallback, useEffect, useState } from "react";

import { MessageList } from "@/components/MessageList";
import { ThreadDag } from "@/components/ThreadDag";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { type FlowyEvent, api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { shortId } from "@/lib/utils";

/** merge folds a page of new messages into the ones on screen, by id, in log order. */
function merge(current: FlowyEvent[], incoming: FlowyEvent[]): FlowyEvent[] {
  if (incoming.length === 0) return current;
  const byId = new Map(current.map((event) => [event.id, event]));
  for (const event of incoming) byId.set(event.id, event);
  return [...byId.values()].sort((a, b) => a.seq_hlc - b.seq_hlc || a.id.localeCompare(b.id));
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

/**
 * Direct messages: every private conversation this token is a party to.
 *
 * It is a route of its own and not a room called "dm", because there is no such
 * room. A direct message carries no room and no project at all - that shape is
 * what makes it private, and the node's read filter widens the projectless floor
 * by exactly one named principal to let the addressee in. So there is nothing to
 * put in a room path here, and nothing on this page decides who may read what:
 * the messages that arrive are the ones the database already agreed to hand over.
 *
 * The compose box asks for the addressee first and refuses to send without one.
 * A private message with nobody to send it to is the one mistake on this page
 * that would be quiet - the room routes fall back to the room, and this must not.
 */
export function Direct() {
  const { token, whoami } = useSession();
  const [events, setEvents] = useState<FlowyEvent[]>([]);
  const [selected, setSelected] = useState<FlowyEvent | null>(null);
  const [live, setLive] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [to, setTo] = useState("");
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);

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
        const page = await api.dms();
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
          const page = await api.dmWait(cursor, controller.signal);
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
          // Back off rather than spin: a poll loop with no pause in its failure
          // path is a denial of service aimed at your own node.
          await sleep(2000);
        }
      }
    };

    void watch();
    return () => {
      stopped = true;
      controller.abort();
    };
  }, [token]);

  const send = useCallback(async () => {
    const body = draft.trim();
    const addressee = to.trim();
    if (!body || sending) return;
    if (!addressee) {
      setError("a direct message needs somebody to send it to");
      return;
    }
    setSending(true);
    setError(null);
    try {
      // A reply stays in the conversation it answers, and the node refuses one
      // addressed at anybody who is not already in that conversation - so the
      // set of people who can read a thread is fixed by its first message.
      const said = await api.sendDm(
        addressee,
        body,
        selected ? [selected.id] : [],
        selected?.thread,
      );
      setEvents((current) => merge(current, [said]));
      setDraft("");
      setSelected(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSending(false);
    }
  }, [draft, to, sending, selected]);

  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      void send();
    }
  };

  // Picking a message picks who the reply is for as well: an addressee typed by
  // hand into a thread that already has two people in it is a refusal waiting to
  // happen, and the person it belongs to is on the message.
  const pick = (event: FlowyEvent) => {
    setSelected(event);
    const other = event.actor === whoami?.user ? event.addressee : event.actor;
    if (other) setTo(other);
  };

  const thread = selected?.thread ?? events.at(-1)?.thread;
  const threadEvents = thread ? events.filter((event) => event.thread === thread) : [];

  return (
    <div className="flex h-full">
      <section className="flex min-w-0 flex-1 flex-col">
        <header className="flex items-center gap-3 border-border border-b px-4 py-3">
          <Lock className="h-4 w-4" />
          <h1 className="font-semibold text-base">direct</h1>
          <Badge variant="outline">private - no room, no project</Badge>
          <Badge variant={live ? "default" : "outline"}>{live ? "watching" : "idle"}</Badge>
          <span className="ml-auto text-muted-foreground text-xs">
            {events.length} message{events.length === 1 ? "" : "s"}
          </span>
        </header>

        {error ? (
          <div className="border-destructive/40 border-b bg-destructive/10 px-4 py-2 text-destructive text-xs">
            {error}
          </div>
        ) : null}

        {events.length === 0 && !error ? (
          <div className="px-4 py-3 text-muted-foreground text-sm">
            nothing here - a message sent from this box is readable by you and the person you name,
            and by nobody else in either of your projects
          </div>
        ) : null}

        <MessageList
          events={events}
          selected={selected}
          onSelect={pick}
          me={{ user: whoami?.user, agent: whoami?.agent }}
        />

        <form
          className="flex flex-col gap-2 border-border border-t bg-card/40 p-3"
          onSubmit={(event) => {
            event.preventDefault();
            void send();
          }}
        >
          {selected ? (
            <div className="flex items-center gap-2 text-xs">
              <Badge variant="outline">replying in thread {shortId(selected.thread, 8)}</Badge>
              <span className="truncate text-muted-foreground">{selected.body}</span>
            </div>
          ) : null}

          <Input
            value={to}
            disabled={!token || sending}
            onChange={(event) => setTo(event.target.value)}
            placeholder="to (a user or agent id) - required, this is not a room"
            aria-label="addressee"
            className="h-8 text-xs"
          />

          <Textarea
            value={draft}
            disabled={!token || sending}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={onKeyDown}
            placeholder={token ? "say something private…" : "paste a token to say something"}
            aria-label="message"
          />

          <div className="flex items-center gap-3">
            <span className="text-muted-foreground text-xs">
              only you and the person you name can read this
            </span>
            <Button type="submit" size="sm" className="ml-auto" disabled={!token || sending}>
              <CornerDownLeft className="h-3.5 w-3.5" />
              {sending ? "sending…" : "send privately"}
            </Button>
          </div>
        </form>
      </section>

      <aside className="flex w-[26rem] shrink-0 flex-col border-border border-l">
        <header className="flex items-center gap-2 border-border border-b px-4 py-3">
          <h2 className="font-semibold text-sm">conversation</h2>
          {thread ? (
            <span className="font-mono text-muted-foreground text-xs">{shortId(thread, 10)}</span>
          ) : null}
          <span className="ml-auto text-muted-foreground text-xs">
            {threadEvents.length} message{threadEvents.length === 1 ? "" : "s"}
          </span>
        </header>
        <div className="min-h-0 flex-1">
          <ThreadDag events={threadEvents} />
        </div>
      </aside>
    </div>
  );
}
