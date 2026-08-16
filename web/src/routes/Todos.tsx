import { useEffect, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { type Artifact, api } from "@/lib/api";
import { useSession } from "@/lib/session";

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
 */

/** The order statuses are shown in, and everything unknown sorts with `todo`. */
const RANK: Record<string, number> = { active: 0, todo: 1, done: 2 };

function rank(status: string): number {
  return RANK[status] ?? RANK.todo;
}

/**
 * The owner is the first line of the body, not the artifact's owner_user.
 *
 * Every one of these was written through the same operator principal, so
 * owner_user says "operator" for all of them and answers nothing. The body
 * carries `OWNER: <name>` as its first line, which is the claim the writer
 * actually made about who is doing the work.
 */
function ownerOf(body: string): string {
  const line = body.split("\n").find((l) => l.startsWith("OWNER:"));
  const name = line?.slice("OWNER:".length).trim();
  return name && name !== "?" ? name : "unowned";
}

/** The body without the OWNER line, which is rendered as its own column. */
function detailOf(body: string): string {
  return body
    .split("\n")
    .filter((l) => !l.startsWith("OWNER:"))
    .join("\n")
    .trim();
}

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

  const sorted = [...todos].sort((a, b) => rank(a.status) - rank(b.status));
  const count = (s: string) => todos.filter((t) => rank(t.status) === RANK[s]).length;

  return (
    <div className="flex flex-col gap-3">
      <h2 className="text-lg font-semibold">
        todos{" "}
        <span className="text-muted-foreground text-sm font-normal">
          ({count("active")} active, {count("todo")} open, {count("done")} done)
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
        const detail = detailOf(t.body ?? "");
        return (
          <Card key={t.id}>
            <CardHeader>
              <CardTitle className="text-base">{t.title || t.id}</CardTitle>
              <div className="flex flex-wrap gap-1 pt-1">
                <Badge variant={t.status === "active" ? "default" : "secondary"}>
                  {t.status || "todo"}
                </Badge>
                <Badge variant="outline">{ownerOf(t.body ?? "")}</Badge>
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
