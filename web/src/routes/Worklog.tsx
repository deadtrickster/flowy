import { useCallback, useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";

import { WorklogEntry, branchOf, emptyReads } from "@/components/WorklogList";
import { Select } from "@/components/ui/select";
import type { ActivityItem } from "@/lib/api";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";

/**
 * The worklog: what the last few seats did, and where they stopped.
 *
 * It is the fleet's memory across sessions - a fresh seat is supposed to read
 * this instead of somebody's session transcript - and until now it had no human
 * surface at all: written and read over MCP, so the one thing here whose whole
 * purpose is "what happened and what is next" could only be reached by an agent
 * holding an MCP client, and the person the fleet works for had to ask one of us
 * to read it out.
 *
 * Newest first, because the question a worklog answers is what just happened.
 * The read is /api/activity narrowed to the kind, which is where the permission
 * filter lives - there is deliberately no worklog endpoint of its own, because
 * a second door onto the same rows is a second place for that filter to be
 * missing.
 */
export function Worklog() {
  const { token } = useSession();
  const [params, setParams] = useSearchParams();
  const [items, setItems] = useState<ActivityItem[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);

  // The branch narrows the list and nothing else: it is not a heading, and it
  // is not what the read asks the node for. Several seats work at once on
  // separate branches, so a worklog scoped to one by default would hide the
  // work somebody else did, which is the opposite of what it is for.
  const branch = params.get("branch") ?? "";

  const load = useCallback(async () => {
    if (!token) {
      setItems([]);
      setLoaded(false);
      return;
    }
    try {
      const page = await api.worklog();
      setItems(page.items);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoaded(true);
    }
  }, [token]);

  useEffect(() => {
    void load();
  }, [load]);

  // Every branch any visible entry names, for the picker. It is built from the
  // whole list rather than from the narrowed one, or choosing a branch would
  // remove every other option and leave no way back to everything.
  const branches = [...new Set(items.map(branchOf).filter((name) => name !== ""))].sort();
  const shown = branch ? items.filter((item) => branchOf(item) === branch) : items;

  const narrow = (next: string) => {
    const merged = new URLSearchParams(params);
    if (next) merged.set("branch", next);
    else merged.delete("branch");
    setParams(merged);
  };

  return (
    <div className="flex h-full flex-col">
      <header className="flex flex-wrap items-center gap-2 border-border border-b px-4 py-3">
        <h1 className="font-semibold text-base">worklog</h1>
        <span className="text-muted-foreground text-xs">
          what the last few seats did, newest first
        </span>
        <Select value={branch} aria-label="branch" onChange={(event) => narrow(event.target.value)}>
          <option value="">every branch</option>
          {branches.map((name) => (
            <option key={name} value={name}>
              {name}
            </option>
          ))}
        </Select>
        <span className="ml-auto text-muted-foreground text-xs">
          {shown.length} entr{shown.length === 1 ? "y" : "ies"}
        </span>
      </header>

      {error ? (
        <div className="border-destructive/40 border-b bg-destructive/10 px-4 py-2 text-destructive text-xs">
          {error}
        </div>
      ) : null}

      <ol aria-label="worklog entries" className="min-h-0 flex-1 overflow-y-auto">
        {/* An empty list says which empty it is - see emptyReads in WorklogList. */}
        {shown.length === 0 ? (
          <li className="p-4 text-muted-foreground text-sm">
            {emptyReads({
              token: Boolean(token),
              loaded,
              failed: Boolean(error),
              branch,
            })}
          </li>
        ) : null}
        {shown.map((item) => (
          <WorklogEntry key={item.id} item={item} onBranch={narrow} />
        ))}
      </ol>
    </div>
  );
}
