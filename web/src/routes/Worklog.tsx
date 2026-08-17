import { useCallback, useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Select } from "@/components/ui/select";
import type { ActivityItem } from "@/lib/api";
import { api, isVouched } from "@/lib/api";
import { useSession } from "@/lib/session";
import { shortId } from "@/lib/utils";

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
        {/* An empty list says which empty it is - see emptyReads below. */}
        {shown.length === 0 ? (
          <li className="p-4 text-muted-foreground text-sm">
            {emptyReads({ token: Boolean(token), loaded, failed: Boolean(error), branch })}
          </li>
        ) : null}
        {shown.map((item) => (
          <Entry key={item.id} item={item} onBranch={narrow} />
        ))}
      </ol>
    </div>
  );
}

/**
 * What an empty list says, which is never nothing.
 *
 * Signed out, read but empty, narrowed to a branch nobody wrote on, and a read
 * that failed are four different facts, and all four look like a blank page.
 * The last two are the ones that matter: "no entries" reads as "nothing
 * happened", and for a chronology that is a false statement rather than a
 * missing one.
 */
function emptyReads({
  token,
  loaded,
  failed,
  branch,
}: {
  token: boolean;
  loaded: boolean;
  failed: boolean;
  branch: string;
}) {
  if (!token) {
    return "paste a token to read the worklog - signed out, there is nothing to read it with";
  }
  if (failed) {
    return "the worklog could not be read, so this page is not saying that nothing happened";
  }
  if (!loaded) {
    return "reading the worklog…";
  }
  if (branch) {
    return `no entries you can read were written on ${branch} - the branch narrows this list, it does not empty the log`;
  }
  return "no entries you can read - which is not the same as nothing having happened";
}

/** branchOf is the branch an entry names, and "" for the ones that name none. */
function branchOf(item: ActivityItem) {
  return item.meta?.branch?.trim() ?? "";
}

/**
 * One entry: who wrote it, whose work it is about, when, the branch it belongs
 * to if it has one, and the body.
 *
 * What it says is read off meta, where the write put it, and the event body is
 * the fallback - an entry from a peer running a build older than the fields is
 * still an entry and still says something, and dropping it here would be a gap
 * in a chronology with nothing to say there was one.
 *
 * A VOUCHED entry is drawn as vouched, and that is the half of this row that
 * matters. The drainer writes entries on behalf of runs - the harness knows the
 * run id and the verify status and cannot lie about whether the gate passed, so
 * it is the right author - but an entry written by the harness ABOUT
 * flowy-claude appearing as flowy-claude's own entry is the same shape as the
 * impersonation finding this project has open. So the badge says vouched, the
 * subject is named as whose work it is, and the writer is labelled "by" rather
 * than left in the position a reader reads as the author of the account.
 */
function Entry({ item, onBranch }: { item: ActivityItem; onBranch: (branch: string) => void }) {
  const meta = item.meta ?? {};
  const what = meta.what?.trim() || item.body;
  const branch = branchOf(item);
  const refs = meta.refs ?? [];
  const vouched = isVouched(item);
  const subject = meta.subject?.trim() ?? "";
  return (
    <li
      data-worklog-entry={item.id}
      data-branch={branch}
      data-vouched={vouched ? "" : undefined}
      data-worklog-subject={vouched ? subject : undefined}
      className="flex flex-col gap-1 border-border border-b px-4 py-3 text-sm"
    >
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={item.actor_kind === "agent" ? "agent" : "human"}>
          {item.actor_kind === "agent" ? "agent" : "person"}
        </Badge>
        {/*
         * Whose work it is, on the entries that are somebody's report of
         * somebody else's shift. It comes FIRST, ahead of the writer: the
         * question this row answers is "whose work is this", and putting the
         * writer where the reader looks for the subject is what makes a vouched
         * entry read as an authored one.
         */}
        {vouched ? (
          <>
            <Badge
              variant="outline"
              title="written by one seat about another's work - not that seat's own account"
            >
              vouched
            </Badge>
            <span data-worklog-subject-id="" className="font-mono text-xs" title={subject}>
              {shortId(subject, 8)}
            </span>
            <span className="text-muted-foreground text-xs">by</span>
          </>
        ) : null}
        {/*
         * Who wrote it, by the name the node stamped when they wrote it. The id
         * is the fallback and stays on the title, because every entry written
         * before names were recorded has no other answer.
         */}
        <span data-worklog-actor="" className="font-mono text-xs" title={item.actor}>
          {item.actor_name || shortId(item.actor, 8)}
        </span>
        <span className="text-muted-foreground text-xs">{stamp(item.created)}</span>
        {branch ? (
          <button
            type="button"
            onClick={() => onBranch(branch)}
            title={`narrow the worklog to ${branch}`}
          >
            <Badge variant="outline">branch {branch}</Badge>
          </button>
        ) : null}
        {meta.as_of ? <Badge variant="outline">as of {meta.as_of}</Badge> : null}
        {/*
         * The run and what the gate said about it, verbatim. verify is drawn as
         * written rather than mapped onto a pass/fail colour: what a gate said is
         * a measurement - "428/0", "green", "four failures, all pre-existing" -
         * and a view that sorted those into two buckets would be inventing the
         * half of the answer it did not get. This is the evidence a reader of a
         * vouched entry decides on, so it is shown and not summarised.
         */}
        {meta.run ? <Badge variant="outline">run {meta.run}</Badge> : null}
        {meta.verify ? (
          <Badge variant="outline" title="what the gate said, as the entry recorded it">
            verify {meta.verify}
          </Badge>
        ) : null}
      </div>
      <p data-worklog-what="" className="whitespace-pre-wrap break-words">
        {what}
      </p>
      {meta.next ? (
        <p className="whitespace-pre-wrap break-words text-muted-foreground">
          <span className="font-medium">next: </span>
          {meta.next}
        </p>
      ) : null}
      {refs.length > 0 ? (
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <span className="text-muted-foreground">about</span>
          {refs.map((ref) => (
            <Link
              key={ref}
              className="font-mono underline"
              to={`/p/${item.project ?? "_"}/artifact/${ref}`}
            >
              {shortId(ref, 8)}
            </Link>
          ))}
        </div>
      ) : null}
    </li>
  );
}

/**
 * When the entry was written, with the date on it.
 *
 * The timeline's clock() is the time alone, which is right for a run somebody
 * is watching and wrong here: a worklog is read across days, and "14:07" with
 * no date is a reading somebody has to guess the age of.
 */
function stamp(iso: string) {
  const at = new Date(iso);
  return Number.isNaN(at.getTime()) ? "" : at.toLocaleString();
}
