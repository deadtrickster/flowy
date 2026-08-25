import { Bookmark, BookmarkX } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { Button } from "@/components/ui/button";
import { type BookmarksView, type FlowyEvent, api } from "@/lib/api";
import { useSignedIn } from "@/lib/session";
import { speakerStyle } from "@/lib/speakercolour";
import { shortId } from "@/lib/utils";

/**
 * What this reader kept, newest first.
 *
 * 01M0HGTV9B: "I should be able to bookmark messages". The room has pins and
 * they are a different thing - a pin is the room saying "this is what we
 * decided", it changes everybody's strip, and putting one up is a claim about
 * the conversation. Somebody who wants to find their own way back to a message
 * tomorrow has no business rearranging what four other seats see.
 *
 * IT IS A PAGE AND NOT A STRIP, so it shows the MESSAGES rather than their ids:
 * a strip sits over a transcript where the ids resolve against what is already
 * on screen, and this list holds messages from every room somebody reads.
 *
 * A KEPT MESSAGE THAT IS NO LONGER READABLE IS SAID SO, not silently dropped.
 * The node hands back both the ids and the messages it could read, and they are
 * deliberately not the same length - the bookmark is a pointer and no copy was
 * ever kept. A page that quietly showed the shorter list would tell a reader
 * they had unkept something they did not.
 */
export function Bookmarks() {
  const signedIn = useSignedIn();
  const [page, setPage] = useState<BookmarksView | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    if (!signedIn) {
      setPage(null);
      return;
    }
    try {
      setPage(await api.bookmarks());
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [signedIn]);

  useEffect(() => {
    void load();
  }, [load]);

  const drop = async (message: string) => {
    try {
      await api.unbookmark(message);
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  const messages = page?.messages ?? [];
  // How many are kept but unreachable. Counted from the two lists the node
  // sent rather than inferred from one of them.
  const gone = (page?.kept.length ?? 0) - messages.length;

  return (
    <section className="flex min-h-0 flex-1 flex-col gap-3 p-4" data-bookmarks-page>
      <header className="flex items-baseline gap-2">
        <h1 className="font-semibold text-lg">kept</h1>
        <span className="text-muted-foreground text-xs">
          messages you kept for yourself - nobody else sees this list
        </span>
        <span
          className="ml-auto text-muted-foreground text-xs"
          data-bookmark-count={messages.length}
        >
          {messages.length} kept
        </span>
      </header>

      {error ? <div className="text-destructive text-sm">{error}</div> : null}

      {!signedIn ? (
        <div className="text-muted-foreground text-sm">
          log in, or paste a token, to see what you kept
        </div>
      ) : messages.length === 0 && gone <= 0 ? (
        <div className="rounded-md border border-dashed border-border p-3 text-muted-foreground text-sm">
          nothing kept yet. `keep` under any message in a room puts it here, and it stays private to
          you - a pin is the room's, this is yours.
        </div>
      ) : null}

      <ul className="flex flex-col gap-2">
        {messages.map((event: FlowyEvent) => {
          const speaker = event.meta?.actor_name || event.actor;
          return (
            <li
              key={event.id}
              data-kept-message={event.id}
              className="rounded-md border border-border p-3 text-sm"
            >
              <div className="flex items-baseline gap-2 text-xs">
                <Bookmark className="h-3 w-3 shrink-0 text-primary" />
                <span style={speakerStyle(speaker)}>{speaker}</span>
                {/* WHERE IT WAS SAID, as a link, because "find my way back" is
                    the whole reason somebody kept it. A message with no room is
                    a direct message and its home is the private log. */}
                {event.room ? (
                  <Link
                    to={`/chat/${encodeURIComponent(event.room)}/thread/${encodeURIComponent(event.id)}`}
                    data-kept-in={event.room}
                    className="text-primary underline"
                  >
                    #{event.room}
                  </Link>
                ) : (
                  <Link to="/direct" className="text-primary underline">
                    direct
                  </Link>
                )}
                <span className="font-mono text-muted-foreground">#{shortId(event.id)}</span>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  className="ml-auto h-6 gap-1 px-2 text-xs"
                  data-drop={event.id}
                  onClick={() => void drop(event.id)}
                  aria-label={`stop keeping message ${shortId(event.id)}`}
                >
                  <BookmarkX className="h-3 w-3" />
                  drop
                </Button>
              </div>
              <p className="whitespace-pre-wrap break-words pt-2">{event.body}</p>
            </li>
          );
        })}
      </ul>

      {gone > 0 ? (
        <div className="text-muted-foreground text-xs" data-bookmarks-gone={gone}>
          {gone} kept {gone === 1 ? "message is" : "messages are"} no longer readable by you. The
          bookmark is a pointer and the node never kept a copy, so what you can reach is what is
          above.
        </div>
      ) : null}
    </section>
  );
}
