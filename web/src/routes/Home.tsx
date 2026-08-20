import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import { SpreadCard } from "@/components/SpreadCard";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { isAgent } from "@/lib/api";
import { useInboxFeed } from "@/lib/inboxfeed";
import { useSession } from "@/lib/session";
import { clock, shortId } from "@/lib/utils";

/**
 * The overview: who this token is, what was said while you were away, and a way
 * into any room by name.
 *
 * The inbox is the node's, not the console's: /api/inbox is everything you may
 * read and did not write, so what is listed here is exactly what the permission
 * filter allows and nothing has to be hidden after the fact.
 *
 * AND IT IS KEPT CURRENT. This page used to read it once, inside an effect
 * keyed on [token] with no timer in the file, so an overview left open showed
 * the inbox as it was at sign-in for as long as the tab lived. The read is not
 * expensive; the staleness was silent, which is worse.
 */
export function Home() {
  const { token, whoami } = useSession();
  // THE CARD FOLLOWS THE NODE'S WAITER now, rather than being read once per
  // sign-in and never again - see lib/inboxfeed.ts for why the waiter is the
  // signal and the snapshot is still the content.
  const { events: inbox, error } = useInboxFeed(token);
  const [room, setRoom] = useState("general");
  const navigate = useNavigate();

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

        {/* The board before the rooms: what is unowned and how the work is
            spread is the thing an arriving reader can act on, and it was
            reachable only from a terminal until now. */}
        <SpreadCard />

        <Card>
          <CardHeader>
            <CardTitle>open a room</CardTitle>
            <CardDescription>
              rooms are scoped by project - two projects may both have a general and neither reads
              the other's
            </CardDescription>
          </CardHeader>
          <CardContent>
            {/* One text box and a submit button is the shape a browser reads as
                a sign-in, so the form and the field both say what they are. */}
            <form
              className="flex gap-2"
              autoComplete="off"
              onSubmit={(event) => {
                event.preventDefault();
                const name = room.trim();
                if (name) navigate(`/chat/${encodeURIComponent(name)}`);
              }}
            >
              <Input
                name="room-name"
                aria-label="room to open"
                placeholder="a room name"
                value={room}
                onChange={(event) => setRoom(event.target.value)}
              />
              <Button type="submit" variant="secondary">
                open
              </Button>
            </form>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>inbox</CardTitle>
            <CardDescription>chat you may see and did not write, newest first</CardDescription>
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
