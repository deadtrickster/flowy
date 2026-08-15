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
 * A read that fails renders nothing at all. A banner is a thing that appears
 * over the top of what somebody is doing, and one that appears to say it could
 * not read itself is worse than one that stays quiet - the room below it is
 * still working, and the error it would have shown is the same error the room
 * shows already.
 */
export function AnnouncementBanner() {
  const { token } = useSession();
  const [active, setActive] = useState<Announcement[]>([]);
  const [acking, setAcking] = useState("");

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

  const ack = useCallback(
    async (id: string) => {
      setAcking(id);
      try {
        await api.ack(id);
      } catch {
        // The refusal is the node's answer and the quiesce is unchanged. The
        // re-read below shows whatever is actually true.
      }
      setAcking("");
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
                disabled={acking === item.id}
                onClick={() => void ack(item.id)}
              >
                {acking === item.id ? "acking" : "ack"}
              </Button>
            ) : null}
          </div>
        );
      })}
    </div>
  );
}
