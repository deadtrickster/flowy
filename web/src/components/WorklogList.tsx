import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import type { ActivityItem } from "@/lib/api";
import { artifactPath, isVouched } from "@/lib/api";
import { shortId } from "@/lib/utils";

/**
 * A worklog entry as a row, and the room pane that draws a list of them.
 *
 * The row lives here rather than in the worklog page because two surfaces draw
 * it now - the page at /worklog and the room's worklog tab - and a second copy
 * of it is a second answer to "what does a vouched entry look like". The page
 * keeps the branch picker and the query string; this keeps the row.
 */

/**
 * What an empty list says, which is never nothing.
 *
 * Signed out, read but empty, narrowed to a branch nobody wrote on, and a read
 * that failed are four different facts, and all four look like a blank page.
 * The last two are the ones that matter: "no entries" reads as "nothing
 * happened", and for a chronology that is a false statement rather than a
 * missing one.
 */
export function emptyReads({
  signedIn,
  loaded,
  failed,
  branch,
}: {
  signedIn: boolean;
  loaded: boolean;
  failed: boolean;
  branch: string;
}) {
  if (!signedIn) {
    return "log in, or paste a token, to read the worklog - signed out, there is nothing to read it with";
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
export function branchOf(item: ActivityItem) {
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
export function WorklogEntry({
  item,
  onBranch,
}: {
  item: ActivityItem;
  /** onBranch narrows the list to a branch, on the surfaces that have
   * somewhere to keep the answer. The room pane passes none. */
  onBranch?: (branch: string) => void;
}) {
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
        {/*
         * The branch is a CONTROL where there is somewhere to keep the answer -
         * the worklog page, which holds it in the query string - and a label in
         * the room's pane, which has none. A button that narrowed nothing would
         * be an affordance lying about itself.
         */}
        {branch && onBranch ? (
          <button
            type="button"
            onClick={() => onBranch(branch)}
            title={`narrow the worklog to ${branch}`}
          >
            <Badge variant="outline">branch {branch}</Badge>
          </button>
        ) : null}
        {branch && !onBranch ? <Badge variant="outline">branch {branch}</Badge> : null}
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
              to={artifactPath({ project: item.project, id: ref }) ?? "#"}
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

/**
 * The worklog beside a room, as one of the room's panes.
 *
 * IT IS THE WHOLE LOG, and it says so on its face. An entry is written into a
 * room of its own - worklog.go's worklogRoom, the same string for every seat -
 * so there is no such thing as this room's worklog to ask the node for: an
 * /api/activity read narrowed to #general comes back empty, and a pane that
 * drew that would report "nothing happened" about a log with forty entries in
 * it. Narrowing it properly is a store change and belongs in its own commit.
 */
export function RoomWorklog({
  items,
  error,
  loaded,
  signedIn,
}: {
  items: ActivityItem[];
  error: string | null;
  loaded: boolean;
  signedIn: boolean;
}) {
  return (
    <section className="flex min-h-0 flex-1 flex-col">
      <header className="flex items-center gap-2 border-border border-b px-4 py-3">
        <h2 className="font-semibold text-sm">worklog</h2>
        {/* Where these entries are from, said in the header rather than left to
            be inferred from the ones on screen. The count in the tab is the
            fleet's, and a reader who took it for this room's would read every
            number here wrong. */}
        <span className="text-muted-foreground text-xs">every seat, newest first</span>
        <span className="ml-auto text-muted-foreground text-xs">
          {items.length} entr{items.length === 1 ? "y" : "ies"}
        </span>
      </header>
      <ol aria-label="room worklog entries" className="min-h-0 flex-1 overflow-y-auto">
        {items.length === 0 ? (
          <li className="p-4 text-muted-foreground text-sm">
            {emptyReads({ signedIn, loaded, failed: Boolean(error), branch: "" })}
          </li>
        ) : null}
        {items.map((item) => (
          <WorklogEntry key={item.id} item={item} />
        ))}
      </ol>
    </section>
  );
}
