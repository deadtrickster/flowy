import { useCallback, useEffect, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { type Announcement, api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { cn } from "@/lib/utils";

/** How often the banner re-reads. An announcement is not chat: a minute is soon enough. */
const POLL_MS = 20000;

const TONE: Record<string, string> = {
  info: "border-border bg-muted/40 text-foreground",
  warning: "border-amber-500/40 bg-amber-500/10 text-foreground",
  maintenance: "border-amber-500/40 bg-amber-500/10 text-foreground",
  breaking: "border-destructive/40 bg-destructive/10 text-foreground",
};

/**
 * The banner: what the node wants everybody in the room to know, above the
 * conversation rather than in it.
 *
 * It reads GET /api/announcements, which answers with the active announcements
 * this token may see - so the banner does no filtering of its own, and cannot
 * show somebody an announcement the permission filter would not have handed
 * them. When an announcement is resolved it stops coming back and the banner
 * empties: there is no "dismiss", because the state that clears it is the
 * announcement's own and not this browser's.
 *
 * RESOLVE IS THAT STATE CHANGE, and until now this file described it and never
 * offered it. The one button here was ack, which renders only for an
 * announcement that names a resource - so a plain warning drew no affordance of
 * any kind, and the land-guard bypass of 2026-08-20 sat at the top of every page
 * for four hours after the condition it warned about was over. A banner nobody
 * can clear is a banner people learn to look past, which spends the surface the
 * next real warning needs.
 *
 * WHO SEES THE BUTTON IS THE NODE'S ANSWER. may_resolve rides each
 * announcement; this draws the control when it is true and nothing when it is
 * not. The rule is "the owner, or this node's operator on a node-scope
 * announcement", and the operator limb is unknowable from a browser.
 *
 * A read that fails renders nothing at all. A banner is a thing that appears
 * over the top of what somebody is doing, and one that appears to say it could
 * not read itself is worse than one that stays quiet - the room below it is
 * still working, and the error it would have shown is the same error the room
 * shows already.
 */
export function AnnouncementBanner() {
  const { token } = useSession();
  const [active, setActive] = useState<Announcement[]>([]);
  // One busy id for both controls: a row has one button in flight at a time,
  // and two pieces of state for one fact is two chances for them to disagree.
  const [busy, setBusy] = useState("");

  const read = useCallback(async () => {
    if (!token) {
      setActive([]);
      return;
    }
    try {
      const page = await api.announcements();
      setActive(page.announcements ?? []);
    } catch {
      setActive([]);
    }
  }, [token]);

  useEffect(() => {
    let stopped = false;
    const tick = async () => {
      await read();
      if (!stopped) timer = setTimeout(tick, POLL_MS);
    };
    let timer: ReturnType<typeof setTimeout> | undefined;
    void tick();
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
    };
  }, [read]);

  /**
   * Resolving is not undoable from here and it clears the banner for everybody,
   * so a failure must not look like a success. The re-read below is what the
   * button reports: if the node refused, the announcement is still in the list
   * and still on screen, which is the honest answer without a second place to
   * keep an error.
   */
  const resolve = useCallback(
    async (id: string) => {
      setBusy(id);
      try {
        await api.resolve(id);
      } catch {
        // The node's refusal stands and the re-read shows what is actually true.
      }
      setBusy("");
      await read();
    },
    [read],
  );

  const ack = useCallback(
    async (id: string) => {
      setBusy(id);
      try {
        await api.ack(id);
      } catch {
        // The refusal is the node's answer and the quiesce is unchanged. The
        // re-read below shows whatever is actually true.
      }
      setBusy("");
      await read();
    },
    [read],
  );

  if (active.length === 0) return null;

  return (
    <div data-testid="announcements">
      {active.map((item) => {
        const scope = item.fields?.scope ?? "node";
        const resource = item.fields?.resource ?? "";
        const mode = item.fields?.mode ?? "";
        return (
          <div
            key={item.id}
            className={cn(
              "flex flex-wrap items-center gap-2 border-b px-4 py-2 text-xs",
              TONE[item.severity] ?? TONE.info,
            )}
          >
            <Badge variant="outline">{item.severity}</Badge>
            <Badge variant="outline">{scope}</Badge>
            <span className="font-semibold">{item.title}</span>
            {item.body ? <span className="text-muted-foreground">{item.body}</span> : null}
            {resource ? (
              <span className="text-muted-foreground">
                quiescing {resource} ({mode || "drain"})
              </span>
            ) : null}
            {resource ? (
              <Button
                size="sm"
                variant="outline"
                className="ml-auto h-6"
                disabled={busy === item.id}
                onClick={() => void ack(item.id)}
              >
                {busy === item.id ? "acking" : "ack"}
              </Button>
            ) : null}
            {/*
              ml-auto on whichever control is leftmost, so the buttons sit at
              the end whether there is one of them or two.
            */}
            {item.may_resolve ? (
              <Button
                size="sm"
                variant="outline"
                data-announcement-resolve={item.id}
                className={cn("h-6", resource ? "" : "ml-auto")}
                disabled={busy === item.id}
                onClick={() => void resolve(item.id)}
                title="the condition this warns about is over - clears it for everybody, and keeps it as a resolved record"
              >
                {busy === item.id ? "resolving" : "resolve"}
              </Button>
            ) : null}
          </div>
        );
      })}
    </div>
  );
}
