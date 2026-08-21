import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";

import { MessageBox } from "@/components/MessageBox";
import { MessageList } from "@/components/MessageList";
import { ThreadDag } from "@/components/ThreadDag";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  ASSIGN_ROOM,
  type FlowyEvent,
  type Task,
  type TaskState,
  api,
  artifactPath,
} from "@/lib/api";
import { useCitation } from "@/lib/cite";
import { useSession } from "@/lib/session";
import { shortId } from "@/lib/utils";

/** merge folds new events into the ones on screen, by id, in log order. */
function merge(current: FlowyEvent[], incoming: FlowyEvent[]): FlowyEvent[] {
  if (incoming.length === 0) return current;
  const byId = new Map(current.map((event) => [event.id, event]));
  for (const event of incoming) byId.set(event.id, event);
  return [...byId.values()].sort((a, b) => a.seq_hlc - b.seq_hlc || a.id.localeCompare(b.id));
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

/**
 * One assignment, at /task/:id: the handoff, and the thread it opened rendered
 * as what it is - chat.
 *
 * The thread holds both halves of the story. What the two sides said is in
 * there as chat events, and what happened to the task is in there as task
 * events, in one order, chained by parents. So "delegated it, then asked a
 * question, then closed it" reads top to bottom without joining anything.
 */
export function TaskView() {
  const { id = "" } = useParams();
  const { token, whoami } = useSession();
  const [task, setTask] = useState<Task | null>(null);
  const [events, setEvents] = useState<FlowyEvent[]>([]);
  // The same citation state the room has, from the same place: this view is a
  // transcript too, and the corrections a handoff needs are exactly the ones
  // that have to say which half of a message they answer.
  const { selected, citation, cite, select, citeSpan, clear } = useCitation();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const found = await api.task(id);
    setTask(found);
    const page = await api.thread(found.thread);
    setEvents((current) => merge(current, page.events));
    setError(null);
    return found;
  }, [id]);

  useEffect(() => {
    setTask(null);
    setEvents([]);
    clear();
    if (!token || !id) return;

    let stopped = false;
    const controller = new AbortController();

    const watch = async () => {
      let found: Task;
      try {
        found = await load();
      } catch (err) {
        if (!stopped) setError(err instanceof Error ? err.message : String(err));
        return;
      }
      // The thread is a room read narrowed to one thread, so it long-polls the
      // same way a room does and stops when the view goes away.
      let cursor = 0;
      while (!stopped) {
        try {
          const page = await api.wait(ASSIGN_ROOM, cursor, controller.signal, found.thread);
          if (stopped) return;
          if (page.events.length > 0) {
            setEvents((current) => merge(current, page.events));
            cursor = page.cursor;
          }
        } catch (err) {
          if (stopped) return;
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
  }, [token, id, load, clear]);

  const send = useCallback(
    async (body: string, _to: string, attachments: string[]) => {
      if (!task) return;
      const said = await api.say(
        ASSIGN_ROOM,
        body,
        selected ? [selected.id] : [],
        task.thread,
        undefined,
        cite,
        attachments,
      );
      setEvents((current) => merge(current, [said]));
    },
    [task, selected, cite],
  );

  const act = async (what: "delegate" | TaskState) => {
    if (!task || busy) return;
    setBusy(true);
    setError(null);
    try {
      const moved =
        what === "delegate" ? await api.delegate(task.id) : await api.taskState(task.id, what);
      setTask(moved.task);
      setEvents((current) => merge(current, [moved.event]));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const mine = task && whoami ? task.to_user === whoami.user : false;

  return (
    <div className="flex h-full">
      <section className="flex min-w-0 flex-1 flex-col">
        <header className="flex flex-wrap items-center gap-2 border-border border-b px-4 py-3">
          <h1 className="font-semibold text-base">
            {task?.artifact_title || `task ${shortId(id, 8)}`}
          </h1>
          {task ? <Badge variant="outline">{task.state}</Badge> : null}
          {task?.project ? <Badge variant="outline">{task.project}</Badge> : null}
          {task?.artifact_type && task.project ? (
            <Link
              to={
                artifactPath({
                  project: task.project,
                  type: task.artifact_type,
                  id: task.artifact,
                }) ?? "#"
              }
              className="text-muted-foreground text-xs hover:text-foreground"
            >
              the artifact
            </Link>
          ) : null}
          <div className="ml-auto flex items-center gap-2">
            <Button
              size="sm"
              variant="outline"
              disabled={!mine || busy || task?.state === "done"}
              onClick={() => void act("delegate")}
            >
              delegate
            </Button>
            <Button
              size="sm"
              variant="secondary"
              disabled={!task || busy || task.state === "done"}
              onClick={() => void act("done")}
            >
              done
            </Button>
          </div>
        </header>

        {error ? (
          <div className="border-destructive/40 border-b bg-destructive/10 px-4 py-2 text-destructive text-xs">
            {error}
          </div>
        ) : null}

        {/*
          me is what rings a mention of the reader - see lib/markdown - and
          this call site did not pass it, so every chip on a handoff was drawn
          unringed for everybody and a mention of YOU looked like a mention of
          somebody else. On the one surface where a seat asks another seat for
          something, the ring is the only thing on the page that says the ask
          is yours. Found by flowy-claude while measuring 01M0GGSM99;
          checks.d/console/mention-me.sh is what stops the fifth call site.
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
          disabled={!token || !task}
          onSend={send}
          room={ASSIGN_ROOM}
        />
      </section>

      <aside className="flex w-[26rem] shrink-0 flex-col border-border border-l">
        <header className="flex items-center gap-2 border-border border-b px-4 py-3">
          <h2 className="font-semibold text-sm">thread</h2>
          {task ? (
            <span className="font-mono text-muted-foreground text-xs">
              {shortId(task.thread, 10)}
            </span>
          ) : null}
          <span className="ml-auto text-muted-foreground text-xs">
            {events.length} event{events.length === 1 ? "" : "s"}
          </span>
        </header>
        <div className="min-h-0 flex-1">
          <ThreadDag events={events} />
        </div>
      </aside>
    </div>
  );
}
