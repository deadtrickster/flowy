import { useCallback, useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import type { ActivityItem } from "@/lib/api";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { clock, shortId } from "@/lib/utils";

/**
 * The activity timeline: every turn, run log line, chat message and steer this
 * token may read, in one order, searchable, and postable-into.
 *
 * The four kinds live in four places in most harnesses, and looking in four
 * places is how the one that mattered gets missed. Here they are rows of the
 * same log, so the timeline is one read with the same permission filter as
 * everything else - and the message box is on it rather than only in a room,
 * because the moment you find the run where something went wrong is the moment
 * you want to say something into it.
 *
 * Where the message goes is what the box asks: a room, or the thread of the
 * item you picked - which is a run, or a subagent's branch of one.
 */
export function Activity() {
  const { token } = useSession();
  const [params, setParams] = useSearchParams();
  const [items, setItems] = useState<ActivityItem[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<ActivityItem | null>(null);

  const q = params.get("q") ?? "";
  const kind = params.get("kind") ?? "";
  const thread = params.get("thread") ?? "";
  const [draft, setDraft] = useState(q);

  const load = useCallback(async () => {
    if (!token) return;
    try {
      const page = await api.activity({ q, kind, thread });
      setItems(page.items);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [token, q, kind, thread]);

  useEffect(() => {
    void load();
  }, [load]);

  const search = (next: Record<string, string>) => {
    const merged = new URLSearchParams(params);
    for (const [key, value] of Object.entries(next)) {
      if (value) merged.set(key, value);
      else merged.delete(key);
    }
    setParams(merged);
  };

  return (
    <div className="flex h-full flex-col">
      <header className="flex flex-wrap items-center gap-2 border-border border-b px-4 py-3">
        <h1 className="font-semibold text-base">activity</h1>
        <form
          className="flex items-center gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            search({ q: draft });
          }}
        >
          <Input
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            placeholder="search what was said…"
            aria-label="search the timeline"
            className="h-8 w-64"
          />
          <Button type="submit" size="sm" variant="secondary">
            search
          </Button>
        </form>
        <Select
          value={kind}
          aria-label="kind"
          onChange={(event) => search({ kind: event.target.value })}
        >
          <option value="">every kind</option>
          <option value="turn">turns</option>
          <option value="log">run logs</option>
          <option value="chat">chat</option>
          <option value="steer">steers</option>
          <option value="worklog">worklog</option>
        </Select>
        {thread ? (
          <Badge variant="outline">
            thread {shortId(thread, 8)}{" "}
            <button type="button" className="ml-1" onClick={() => search({ thread: "" })}>
              ×
            </button>
          </Badge>
        ) : null}
        <span className="ml-auto text-muted-foreground text-xs">
          {items.length} item{items.length === 1 ? "" : "s"}
        </span>
      </header>

      {error ? (
        <div className="border-destructive/40 border-b bg-destructive/10 px-4 py-2 text-destructive text-xs">
          {error}
        </div>
      ) : null}

      <ol className="min-h-0 flex-1 overflow-y-auto">
        {items.length === 0 ? (
          <li className="p-4 text-muted-foreground text-sm">
            {token
              ? "nothing you may read matches - which is not the same as nothing having happened"
              : "paste a token to see the timeline"}
          </li>
        ) : null}
        {items.map((item) => (
          <li
            key={item.id}
            className="flex flex-col gap-1 border-border border-b px-4 py-2 text-sm"
          >
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant={item.actor_kind === "agent" ? "agent" : "human"}>{item.kind}</Badge>
              <span className="text-muted-foreground text-xs">{clock(item.created)}</span>
              {/*
               * Who did it, by name where the line carries one. A turn or a
               * run log line does not, and neither does anything said before
               * the node recorded names, so the id is the fallback and stays
               * on the title.
               */}
              <span className="font-mono text-muted-foreground text-xs" title={item.actor}>
                {item.actor_name || shortId(item.actor, 8)}
              </span>
              {item.room ? <Badge variant="outline">#{item.room}</Badge> : null}
              {/*
               * A direct message on the everything-view. It has no room to put
               * in the column beside it, so without this it reads as a bare
               * thread - indistinguishable from a run. The reply box is on
               * this page, which is why the line has to say which of the two
               * somebody is about to answer.
               */}
              {item.private ? (
                <Badge
                  variant="outline"
                  className="border-amber-500/60 border-dashed"
                  title="only the sender and the addressee can read this"
                >
                  private to {shortId(item.addressee ?? "", 8)}
                </Badge>
              ) : null}
              {item.thread ? (
                <button
                  type="button"
                  className="text-muted-foreground text-xs underline"
                  onClick={() => search({ thread: item.thread ?? "" })}
                >
                  thread {shortId(item.thread, 6)}
                </button>
              ) : null}
              {item.trace ? (
                <Link
                  className="text-muted-foreground text-xs underline"
                  to={`/traces?trace=${item.trace}`}
                >
                  trace {shortId(item.trace, 8)}
                </Link>
              ) : null}
              <button
                type="button"
                className="ml-auto text-muted-foreground text-xs underline"
                onClick={() => setSelected(item)}
              >
                post into this
              </button>
            </div>
            <p className="whitespace-pre-wrap break-words">{item.body}</p>
          </li>
        ))}
      </ol>

      <PostBox
        into={selected}
        clear={() => setSelected(null)}
        disabled={!token}
        onPosted={() => void load()}
      />
    </div>
  );
}

/**
 * The message box, which is on the timeline and not only in a room.
 *
 * With an item picked it posts into that item's thread - the run, or the
 * subagent branch. With nothing picked it posts into a room. Either way the
 * node decides who said it, from the token: the kind is what this chooses, not
 * the speaker.
 */
function PostBox({
  into,
  clear,
  disabled,
  onPosted,
}: {
  into: ActivityItem | null;
  clear: () => void;
  disabled: boolean;
  onPosted: () => void;
}) {
  const [body, setBody] = useState("");
  const [kind, setKind] = useState("steer");
  const [room, setRoom] = useState("general");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const send = async () => {
    const said = body.trim();
    if (!said || sending) return;
    if (into?.private) {
      setError("that is a private conversation - answer it on the direct page");
      return;
    }
    setSending(true);
    setError(null);
    try {
      await api.postActivity({
        kind,
        body: said,
        ...(into?.thread ? { thread: into.thread } : { room }),
        ...(into?.room ? { room: into.room } : {}),
      });
      setBody("");
      clear();
      onPosted();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSending(false);
    }
  };

  return (
    <form
      className="flex flex-col gap-2 border-border border-t bg-card/40 p-3"
      onSubmit={(event) => {
        event.preventDefault();
        void send();
      }}
    >
      <div className="flex items-center gap-2 text-xs">
        <Select value={kind} aria-label="post as" onChange={(event) => setKind(event.target.value)}>
          <option value="steer">steer</option>
          <option value="chat">message</option>
          <option value="turn">turn</option>
          <option value="log">log line</option>
        </Select>
        {into?.private ? (
          /*
           * This box posts into a room or a run, and both are things other
           * people read. The node refuses a public write into a private
           * conversation - which is what would otherwise happen here, quietly,
           * to somebody who thought picking a line and typing was a reply - so
           * the button says where to go instead of waiting for the refusal.
           */
          <Badge variant="outline" className="border-amber-500/60 border-dashed">
            that one is private - answer it on <Link to="/direct">direct</Link>
            <button type="button" className="ml-1" onClick={clear}>
              ×
            </button>
          </Badge>
        ) : into ? (
          <Badge variant="outline">
            into thread {shortId(into.thread ?? "", 8)}
            <button type="button" className="ml-1" onClick={clear}>
              ×
            </button>
          </Badge>
        ) : (
          <span className="flex items-center gap-1 text-muted-foreground">
            <label htmlFor="activity-room">into room</label>
            <Input
              id="activity-room"
              value={room}
              onChange={(event) => setRoom(event.target.value)}
              aria-label="room"
              className="h-7 w-32"
            />
          </span>
        )}
        {error ? <span className="text-destructive">{error}</span> : null}
      </div>
      <Textarea
        value={body}
        disabled={disabled || sending}
        onChange={(event) => setBody(event.target.value)}
        placeholder={disabled ? "paste a token to post" : "say something into this run, or a room…"}
        aria-label="post into the timeline"
      />
      <Button type="submit" size="sm" className="self-end" disabled={disabled || sending}>
        {sending ? "posting…" : "post"}
      </Button>
    </form>
  );
}
