import { motion } from "framer-motion";
import { useCallback, useEffect, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { type Artifact, type History, api } from "@/lib/api";
import { clock, shortId } from "@/lib/utils";

interface Props {
  artifact: Artifact;
  /** onMoved hands the updated artifact back, so the page around this agrees. */
  onMoved: (artifact: Artifact) => void;
}

/**
 * The lifecycle control: where this issue is, where it may go, and how it got
 * there.
 *
 * The options come from the node - /history answers with `next` - rather than
 * from a copy of the workflow kept over here. A console that knows the rules
 * itself is a console that disagrees with the server the first time the rules
 * change, and the disagreement shows up as a button that does nothing.
 */
export function StatusControl({ artifact, onMoved }: Props) {
  const [history, setHistory] = useState<History | null>(null);
  const [choice, setChoice] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    const trail = await api.history(artifact.id);
    setHistory(trail);
    setChoice(trail.next[0] ?? "");
  }, [artifact.id]);

  useEffect(() => {
    load().catch((err: Error) => setError(err.message));
  }, [load]);

  const move = async () => {
    if (!choice || busy) return;
    setBusy(true);
    setError(null);
    try {
      const moved = await api.status(artifact.id, choice);
      onMoved(moved.artifact);
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const next = history?.next ?? [];

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-muted-foreground text-xs uppercase tracking-wide">
          status
        </span>
        <Badge>{history?.status ?? (artifact.status || "open")}</Badge>

        {next.length > 0 ? (
          <>
            <Select
              aria-label="move to"
              value={choice}
              disabled={busy}
              onChange={(event) => setChoice(event.target.value)}
              className="ml-auto"
            >
              {next.map((status) => (
                <option key={status} value={status}>
                  {status}
                </option>
              ))}
            </Select>
            <Button size="sm" variant="secondary" disabled={busy} onClick={() => void move()}>
              {busy ? "moving…" : "move"}
            </Button>
          </>
        ) : (
          <span className="ml-auto text-muted-foreground text-xs">
            terminal - nowhere left to go
          </span>
        )}
      </div>

      {error ? <div className="text-destructive text-xs">{error}</div> : null}

      <div className="flex flex-col gap-1">
        <div className="font-medium text-muted-foreground text-xs">history</div>
        {history && history.events.length > 0 ? (
          history.events.map((event) => (
            <motion.div
              key={event.id}
              initial={{ opacity: 0, x: -6 }}
              animate={{ opacity: 1, x: 0 }}
              transition={{ duration: 0.16, ease: "easeOut" }}
              className="flex items-center gap-2 font-mono text-xs"
            >
              <span className="text-foreground">{event.body}</span>
              <span className="text-muted-foreground">by {shortId(event.actor, 8)}</span>
              <span className="ml-auto text-muted-foreground">{clock(event.created)}</span>
            </motion.div>
          ))
        ) : (
          <div className="text-muted-foreground text-xs">no transitions yet</div>
        )}
      </div>
    </div>
  );
}
