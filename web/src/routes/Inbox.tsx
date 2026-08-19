import { AnimatePresence, motion } from "framer-motion";
import { Bot, Check, Inbox as InboxIcon } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { type Task, type TaskState, api, artifactPath } from "@/lib/api";
import { useSession } from "@/lib/session";
import { shortId } from "@/lib/utils";

/** stateVariant colours a task by where it got to. */
function stateVariant(state: TaskState) {
  if (state === "delegated") return "agent" as const;
  if (state === "done") return "secondary" as const;
  return "human" as const;
}

/**
 * The inbox: work handed to this token, newest first.
 *
 * It is tasks, not messages - /api/inbox is the chat you have not read and this
 * is the work you have not done. Each row is one handoff, and the two controls
 * on it are the two things an assignee ever does with one: pass it to their
 * agent, or say it is finished.
 *
 * The delegate button is not hidden when auto_delegate is on. A task can arrive
 * already delegated, and it can be taken back and delegated again; what the
 * switch decides is the default, not what is possible.
 */
export function Inbox() {
  const { token, whoami } = useSession();
  const [tasks, setTasks] = useState<Task[]>([]);
  const [auto, setAuto] = useState<boolean | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState("");

  const load = useCallback(async () => {
    const page = await api.tasks();
    setTasks(page.tasks);
    setError(null);
  }, []);

  useEffect(() => {
    if (!token) {
      setTasks([]);
      return;
    }
    load().catch((err: Error) => setError(err.message));
  }, [token, load]);

  const act = async (id: string, what: "delegate" | TaskState) => {
    setBusy(id);
    setError(null);
    try {
      if (what === "delegate") {
        await api.delegate(id);
      } else {
        await api.taskState(id, what);
      }
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  };

  const flipAuto = async (on: boolean) => {
    setError(null);
    try {
      const user = await api.autoDelegate(on);
      setAuto(user.auto_delegate);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <div className="h-full overflow-y-auto p-6">
      <div className="mx-auto flex max-w-3xl flex-col gap-4">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">inbox</h1>
          <p className="text-muted-foreground text-sm">
            {whoami
              ? `work assigned to ${shortId(whoami.user, 10)}`
              : "paste a bearer token to see anything"}
          </p>
        </div>

        <Card>
          <CardHeader>
            <CardTitle>auto delegate</CardTitle>
            <CardDescription>
              your standing answer to inbound work - on, and a task arrives already handed to your
              agent
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center gap-2">
            <Button size="sm" variant="secondary" onClick={() => void flipAuto(true)}>
              on
            </Button>
            <Button size="sm" variant="outline" onClick={() => void flipAuto(false)}>
              off
            </Button>
            {auto === null ? null : <Badge variant="outline">now {auto ? "on" : "off"}</Badge>}
          </CardContent>
        </Card>

        {error ? <div className="text-destructive text-sm">{error}</div> : null}

        <div className="flex flex-col gap-2">
          {tasks.length === 0 ? (
            <div className="flex items-center gap-2 text-muted-foreground text-sm">
              <InboxIcon className="h-4 w-4" />
              nothing assigned
            </div>
          ) : null}

          <AnimatePresence initial={false}>
            {tasks.map((task) => (
              <motion.div
                key={task.id}
                layout
                initial={{ opacity: 0, y: 8 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.18, ease: "easeOut" }}
                className="flex flex-col gap-2 rounded-lg border border-border bg-card p-3"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <Badge variant={stateVariant(task.state)}>{task.state}</Badge>
                  {task.artifact_type ? (
                    <Badge variant="outline">{task.artifact_type}</Badge>
                  ) : null}
                  <Link to={`/task/${task.id}`} className="font-medium text-sm hover:underline">
                    {task.artifact_title || `task ${shortId(task.id, 8)}`}
                  </Link>
                  <span className="ml-auto font-mono text-muted-foreground text-xs">
                    from {shortId(task.from_user, 8)}
                  </span>
                </div>

                <div className="flex flex-wrap items-center gap-2 font-mono text-[11px] text-muted-foreground">
                  <span>thread {shortId(task.thread)}</span>
                  {task.project ? <span>project {task.project}</span> : null}
                  {task.assignee_agent ? (
                    <span>agent {shortId(task.assignee_agent, 8)}</span>
                  ) : null}
                  {task.artifact_type && task.project ? (
                    <Link
                      to={
                        artifactPath({
                          project: task.project,
                          type: task.artifact_type,
                          id: task.artifact,
                        }) ?? "#"
                      }
                      className="hover:text-foreground"
                    >
                      open the artifact
                    </Link>
                  ) : null}
                </div>

                <div className="flex items-center gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={busy === task.id || task.state === "done"}
                    onClick={() => void act(task.id, "delegate")}
                  >
                    <Bot className="h-3.5 w-3.5" />
                    delegate
                  </Button>
                  <Button
                    size="sm"
                    variant="secondary"
                    disabled={busy === task.id || task.state === "done"}
                    onClick={() => void act(task.id, "done")}
                  >
                    <Check className="h-3.5 w-3.5" />
                    done
                  </Button>
                  <Link
                    to={`/task/${task.id}`}
                    className="ml-auto text-muted-foreground text-xs hover:text-foreground"
                  >
                    open the thread
                  </Link>
                </div>
              </motion.div>
            ))}
          </AnimatePresence>
        </div>
      </div>
    </div>
  );
}
