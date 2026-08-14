import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { type FlowyEvent, api, isAgent } from "@/lib/api";
import { useSession } from "@/lib/session";
import { clock, shortId } from "@/lib/utils";

/**
 * The overview: who this token is, what was said while you were away, and a way
 * into any room by name.
 *
 * The inbox is the node's, not the console's: /api/inbox is everything you may
 * read and did not write, so what is listed here is exactly what the permission
 * filter allows and nothing has to be hidden after the fact.
 */
export function Home() {
  const { token, whoami } = useSession();
  const [inbox, setInbox] = useState<FlowyEvent[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [room, setRoom] = useState("general");
  const navigate = useNavigate();

  useEffect(() => {
    if (!token) {
      setInbox([]);
      return;
    }
    let stopped = false;
    api
      .inbox()
      .then((page) => {
        if (!stopped) {
          setInbox(page.events);
          setError(null);
        }
      })
      .catch((err: Error) => {
        if (!stopped) setError(err.message);
      });
    return () => {
      stopped = true;
    };
  }, [token]);

  return (
    <div className="h-full overflow-y-auto p-6">
      <div className="mx-auto flex max-w-3xl flex-col gap-4">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">overview</h1>
          <p className="text-muted-foreground text-sm">
            {whoami
              ? `signed in as ${shortId(whoami.user, 10)}${whoami.project ? ` in ${whoami.project}` : ""}`
              : "paste a bearer token to see anything"}
          </p>
        </div>

        <Card>
          <CardHeader>
            <CardTitle>open a room</CardTitle>
            <CardDescription>
              rooms are scoped by project - two projects may both have a general and neither reads
              the other's
            </CardDescription>
          </CardHeader>
          <CardContent>
            <form
              className="flex gap-2"
              onSubmit={(event) => {
                event.preventDefault();
                const name = room.trim();
                if (name) navigate(`/chat/${encodeURIComponent(name)}`);
              }}
            >
              <Input value={room} onChange={(event) => setRoom(event.target.value)} />
              <Button type="submit" variant="secondary">
                open
              </Button>
            </form>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>inbox</CardTitle>
            <CardDescription>chat you may see and did not write, oldest first</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {error ? <div className="text-destructive text-sm">{error}</div> : null}
            {inbox.length === 0 ? (
              <div className="text-muted-foreground text-sm">nothing waiting</div>
            ) : (
              inbox.map((event) => (
                <Link
                  key={event.id}
                  to={`/chat/${encodeURIComponent(event.room || "general")}`}
                  className="rounded-md border border-border p-2 transition-colors hover:border-primary/40"
                >
                  <div className="flex items-center gap-2 pb-1 text-xs">
                    <Badge variant={isAgent(event) ? "agent" : "human"}>
                      {isAgent(event) ? "agent" : "human"}
                    </Badge>
                    <span className="font-mono text-muted-foreground">#{event.room}</span>
                    <span className="ml-auto text-muted-foreground">{clock(event.created)}</span>
                  </div>
                  <div className="truncate text-sm">{event.body}</div>
                </Link>
              ))
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
