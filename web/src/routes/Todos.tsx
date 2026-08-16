import { useEffect, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { type Artifact, api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { countTodos, sortTodos, todoDetail, todoOwner, todoRoom } from "@/lib/todos";

/**
 * The todos: what the fleet is doing, who owns each piece, and what it waits on.
 *
 * It exists because the queue lived in chat messages and summaries, so nobody -
 * agent or human - could see the state without reading back through a
 * conversation. A todo is an ordinary artifact (kind `todo`), so this is a read
 * of the same permission-filtered store as everything else and needs no new
 * concept.
 *
 * The ordering is the feature. Active first, then open, then done: a list that
 * buries what is in flight under what is finished answers no question anybody
 * asked. Within a status they keep the order the store returned.
 *
 * It lists every todo the caller may read, room or no room. The room panel in
 * the chat view narrows to one room, and this page is the one that has to keep
 * showing the items nobody raised in a room at all - a change that only handled
 * room-tagged todos would empty this page and pass its own tests. The reading
 * order, the owner line and the room live in lib/todos, shared with that panel,
 * so the two surfaces cannot drift into two ideas of what a todo is.
 */

export function Todos() {
  const { token } = useSession();
  const [todos, setTodos] = useState<Artifact[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!token) {
      setTodos([]);
      return;
    }
    let stopped = false;
    api
      .todos()
      .then((page) => {
        if (!stopped) {
          setTodos(page.artifacts);
          setError(null);
        }
      })
      .catch((e: Error) => {
        if (!stopped) setError(e.message);
      });
    return () => {
      stopped = true;
    };
  }, [token]);

  const sorted = sortTodos(todos);
  const counts = countTodos(todos);

  return (
    <div className="flex flex-col gap-3">
      <h2 className="text-lg font-semibold">
        todos{" "}
        <span className="text-muted-foreground text-sm font-normal">
          ({counts.active} active, {counts.open} open, {counts.done} done)
        </span>
      </h2>
      {error ? <div className="text-destructive text-sm">{error}</div> : null}
      {/* Signed out is said out loud. An empty list here would read as "there is
          nothing to do", which is a different and false statement. */}
      {!token ? <div className="text-muted-foreground text-sm">no token</div> : null}
      {token && !error && todos.length === 0 ? (
        <div className="text-muted-foreground text-sm">
          no todos yet - written with mem_write, kind todo
        </div>
      ) : null}
      {sorted.map((t) => {
        const detail = todoDetail(t.body ?? "");
        const room = todoRoom(t);
        return (
          <Card key={t.id}>
            <CardHeader>
              <CardTitle className="text-base">{t.title || t.id}</CardTitle>
              <div className="flex flex-wrap gap-1 pt-1">
                <Badge variant={t.status === "active" ? "default" : "secondary"}>
                  {t.status || "todo"}
                </Badge>
                <Badge variant="outline">{todoOwner(t.body ?? "") || "unowned"}</Badge>
                {/* Where it was raised, when it was raised anywhere. A todo with
                    no room is not lesser: it is where the whole queue was
                    before rooms had panels. */}
                {room ? <Badge variant="outline">#{room}</Badge> : null}
                {(t.user_tags ?? []).map((tag) => (
                  <Badge key={tag} variant="outline">
                    {tag}
                  </Badge>
                ))}
              </div>
            </CardHeader>
            {detail ? (
              <CardContent className="text-muted-foreground whitespace-pre-wrap text-sm">
                {detail}
              </CardContent>
            ) : null}
          </Card>
        );
      })}
    </div>
  );
}
