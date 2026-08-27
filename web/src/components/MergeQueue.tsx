import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import type { KnownIssue, MergeLock, MergeRequest } from "@/lib/api";
import { artifactPath, refPath } from "@/lib/api";
import { toneStyle, verdictTone } from "@/lib/statecolour";
import { priorityClass, statusStyle } from "@/lib/todos";
import { clock, shortId } from "@/lib/utils";

/**
 * The verdict colours now come from lib/statecolour.ts rather than from a
 * GREEN and a RED defined in this file.
 *
 * The argument that put them here is still the right one and is why they are
 * not statusStyle's: the first version passed statusStyle("blocked"), there is
 * no "blocked" rank, and it fell through to the same grey as "waiting" - so a
 * refusal was drawn as a shrug. A colour that silently falls back to the wrong
 * meaning is worse than no colour.
 *
 * What changed is where the answer lives. A verdict is a state, the console now
 * has one vocabulary for states, and a local pair here was a second definition
 * of "this failed" that nothing kept equal to the others. The word in the badge
 * is still the real signal; the colour only makes it faster to scan.
 */

/**
 * The merge queue, flattened: every merge request this reader can see, and
 * whether each one may land right now.
 *
 * WHY THE VERDICT IS NOT COMPUTED HERE. Whether a branch may land is one rule -
 * it compares the tip the gate measured against the tip the merge would land on
 * - and it lives in the store, where it is tested. A copy of it in this file
 * would be a second answer, and the day the two disagree is the day somebody
 * merges on the wrong one. So this component draws what the node decided and
 * never decides anything itself.
 *
 * A browser has no git, so it cannot know where master is. That is not a gap to
 * paper over: when the node had no tip to judge against, THIS SAYS SO instead of
 * drawing a row that looks approved. `decided` is the node's own word for it.
 */
export function MergeQueue({
  items,
  tip,
  tipFrom,
  decided,
  loaded,
  lock,
  onPriority,
}: {
  items: MergeRequest[];
  tip: string;
  tipFrom: "stated" | "landed" | "deployed" | "none";
  decided: boolean;
  loaded: boolean;
  /** Rank a row - now, next, later, or "" to take one back. The door is one
   * for todos AND merges: POST /api/todo/{id}/priority. */
  onPriority: (id: string, priority: string) => Promise<void>;
  /**
   * The state of the MACHINE, as against the state of each row. Optional
   * because an older node does not send it and a page that has not read yet
   * has none - both of which draw nothing, rather than drawing "free".
   */
  lock?: MergeLock;
}) {
  if (!loaded) {
    return <p className="px-4 py-6 text-muted-foreground text-sm">reading the queue…</p>;
  }
  if (items.length === 0) {
    return (
      <div className="px-4 py-6">
        {/*
          THE EMPTY QUEUE IS WHERE THIS MATTERS MOST, which is why the gate line
          is drawn before the explanation rather than only above a list. At one
          gate at a time and about twelve minutes a pass, the queue is empty or
          unlandable for nearly all of its life - correctly - and every "is it
          stuck?" this evening was somebody reading that and having no way to
          tell a working drainer from a dead one. An empty list plus a running
          gate is the single most reassuring thing this pane can say, and it
          used to say nothing at all.
        */}
        <GateState lock={lock} items={items} />
        <p className="text-muted-foreground text-sm">
          nothing is waiting to land. A merge request is a work item of kind{" "}
          <code className="text-xs">merge</code>, filed with the branch it would land and the tip
          its gate measured.
        </p>
      </div>
    );
  }

  return (
    <div className="flex flex-col">
      <div className="px-4 pt-3">
        <GateState lock={lock} items={items} />
      </div>
      {/*
        What the verdicts were measured against, said above the rows rather than
        left to be assumed. "deployed" is the honest hedge: nobody passed a tip,
        so these answer "may this land on what is running here" - a real
        question, and not the same as "may this land on master right now".
      */}
      {/*
        The provenance as a VALUE, beside the sentence built from it. The
        sentence is prose and has been reworded twice; a check that read it
        would be asserting the wording. What matters is that this pane and the
        node agree about where the tip came from, and that two copies of this
        pane agree with each other - which they did not, see 01M0JZ5VM8.
      */}
      <p data-merge-tipfrom={tipFrom} className="px-4 pt-3 text-muted-foreground text-xs">
        {decided ? (
          <>
            judged against <code className="text-xs">{tip}</code>
            {tipFrom === "deployed"
              ? " — the commit this node was built from, not a live read of the target"
              : null}
          </>
        ) : (
          "no target tip, so nothing here is judged — this is the queue, not an answer"
        )}
      </p>
      <ul className="flex flex-col">
        {items.map((m) => (
          // data-merge-row is the row's id, so a check can ask what THIS row is
          // drawn as rather than counting badges across the pane. The three
          // states this component now separates - nobody looked, the gate said
          // no, it may land - are only distinguishable per row.
          <li
            key={m.id}
            data-merge-row={m.id}
            className="flex flex-col gap-1 border-border-soft border-b px-4 py-3"
          >
            <div className="flex flex-wrap items-center gap-2">
              <Link
                className="font-medium text-sm hover:underline"
                to={artifactPath({ project: m.project, type: m.type, id: m.id }) ?? "#"}
              >
                {m.title || m.branch}
              </Link>
              {/* WHAT TO DO FIRST, when somebody has said - the same word the
                  room's todo panel draws, off the same colour map. Drawn only
                  when set, for the same reason the todo panel gives: a chip on
                  every row saying "unjudged" would be a column of the same
                  word, and the field's whole point is that unjudged and
                  unimportant are different facts. */}
              {m.priority ? (
                <Badge
                  variant="outline"
                  data-merge-priority={m.id}
                  data-merge-priority-value={m.priority}
                  className={priorityClass(m.priority)}
                >
                  {m.priority}
                </Badge>
              ) : null}
              <Badge variant="secondary" style={statusStyle(m.status)}>
                {m.status || "todo"}
              </Badge>
              <Verdict item={m} decided={decided} />
              {/* A run is measuring this right now. Landing anything else on the
                  target while this is up invalidates its evidence - which is the
                  thing nobody could see before, and cost two rebuilds in an hour.
                  Drawn in the LIVE tone rather than in the outline grey it used
                  to share with "not judged": a run measuring this branch and
                  nobody having looked at it are opposite facts, and they were the
                  same colour. */}
              {m.gating ? (
                <Badge
                  variant="secondary"
                  data-merge-gating=""
                  data-tone="accent"
                  style={toneStyle(verdictTone("gating"))}
                  title="a run is measuring this branch right now - landing anything else on the target invalidates it"
                >
                  gating
                </Badge>
              ) : null}
              {/* WHERE THE RANKING IS SET, on the row rather than behind a
                  click: a merge row is already four badges wide on a full
                  pane, and a control a click away is one nobody finds. A
                  select rather than a cycling chip, for the same reason the
                  room panel gives: four states - unjudged, now, next, later -
                  and a chip that cycled would make "take it back" three clicks
                  and an accident on the way. The queue ORDER does not follow
                  this word - the operator settled FIFO for the time being -
                  so setting it never moves a row out of its place. */}
              <label className="ml-auto flex items-center gap-1 text-muted-foreground text-xs">
                do first
                <select
                  data-merge-priority-set={m.id}
                  aria-label={`what to do first about ${m.title || m.branch}`}
                  className="rounded border border-border bg-background px-1 py-0.5 text-foreground text-xs"
                  value={m.priority ?? ""}
                  onChange={(event) => void onPriority(m.id, event.target.value)}
                >
                  {/*
                    The empty option is FIRST and is named. "unjudged" rather
                    than "none" or a blank line, because the whole distinction
                    this field keeps is that nobody having looked is a
                    different fact from somebody deciding it can wait.
                  */}
                  <option value="">unjudged</option>
                  <option value="now">now</option>
                  <option value="next">next</option>
                  <option value="later">later</option>
                </select>
              </label>
            </div>
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-muted-foreground text-xs">
              <span>
                <code className="text-xs">{m.branch}</code> →{" "}
                <code className="text-xs">{m.target}</code>
              </span>
              {/*
                The gate's own evidence, beside the claim it supports. A verdict
                with no run behind it is a self-report, and a merge queue is
                exactly where that has to be visible rather than trusted.
              */}
              {m.gated_tip ? (
                <span>
                  gated on <code className="text-xs">{m.gated_tip}</code>
                  {m.gate_run ? (
                    <>
                      {" "}
                      by <code className="text-xs">{m.gate_run}</code>
                    </>
                  ) : null}
                </span>
              ) : (
                <span>no gate has measured it</span>
              )}
              {m.assignee ? <span>carried by {m.assignee}</span> : null}
            </div>
            {/*
              The reason, in full, on the rows that are refused. It names both
              tips - "re-gate it" is not an instruction until the reader knows
              what to re-gate against.
            */}
            {m.admissible === false && m.reason ? (
              <p className="text-muted-foreground text-xs">
                {m.reason}
                {/*
                  The row that explains this refusal, INLINE with the refusal
                  and not as a banner over the panel. A banner is a second
                  announcement, and announcing is what already failed: the row
                  this feature was built for was filed and announced twice, and
                  still cost one agent three gate runs and another forty minutes
                  of re-derivation, because it was not in front of the person
                  looking at the symptom.
                */}
                {m.known_issue ? <KnownIssueLink issue={m.known_issue} /> : null}
              </p>
            ) : null}
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * A stamp that refuses the zero time.
 *
 * `until` and `taken_at` are on the wire even when nothing is held, as
 * `0001-01-01T00:00:00Z`: their Go tags say omitempty and omitempty does not
 * omit a time.Time. That string PARSES, so lib/utils clock() renders it happily
 * as midnight and a reader is shown a deadline from the first century. Anything
 * before 2000 here is a zero value that survived a marshal, not a date.
 */
function stamp(iso?: string) {
  if (!iso) return "";
  const at = new Date(iso);
  if (Number.isNaN(at.getTime()) || at.getFullYear() < 2000) return "";
  // The zero-time refusal above is this function's whole reason to exist; the
  // RENDERING is clock()'s job and is shared, so a queue stamp gains the date
  // on a row from yesterday exactly as every other list does. Keeping a second
  // toLocaleTimeString() here is how the two drift.
  return clock(iso);
}

/** Whole minutes between two instants, or null when either is unusable. */
function minutesSince(iso?: string) {
  if (!iso) return null;
  const at = new Date(iso);
  if (Number.isNaN(at.getTime()) || at.getFullYear() < 2000) return null;
  return Math.max(0, Math.round((Date.now() - at.getTime()) / 60000));
}

/**
 * What the MACHINE is doing, above the rows rather than on any one of them.
 *
 * WHY THIS IS NOT A ROW BADGE. There is already a per-row `gating` badge, and
 * it answers a different question: that one says this branch is being measured,
 * this one says the target is reserved and by whom until when. The second is
 * what a person wants when nothing on the list is landable, because it is the
 * only thing on the page that distinguishes a queue that is working from one
 * that is stopped.
 *
 * THREE STATES, and the middle one is the reason this component is worth
 * writing rather than printing lock.held:
 *
 *   held                a gate is measuring. Everything else waits, correctly,
 *                       and `until` is how long - which is the actionable
 *                       number, not the elapsed one.
 *   expired, still here  nobody gave the target back. ReleaseMergeLock DELETES
 *                       the row, so a lock past its `until` means the holder
 *                       died or overran rather than finished. IT BLOCKS
 *                       NOTHING - WouldTake reads an expired lock as a free
 *                       target - and a pane that showed this as a lock without
 *                       saying so would manufacture the exact "we are stuck"
 *                       reading it exists to prevent.
 *   free                nothing holds it.
 *
 * The holder is named by handle when the node could resolve one and by
 * principal id when it could not. Neither is dropped in favour of a blank: the
 * question this answers is "who do I talk to", and an id is an answer to it.
 */
function GateState({ lock, items }: { lock?: MergeLock; items: MergeRequest[] }) {
  // No lock on the answer at all is an older node or a page that has not read
  // yet. Drawing "the target is free" there would be inventing a measurement,
  // so this draws nothing - the one honest option when you were told nothing.
  if (!lock) return null;

  const holder = lock.holder_name || lock.holder || "";
  // WHICH BRANCH, not merely which row id. The lock records the row, and the
  // row is on the list in front of the reader most of the time - so name what
  // they can see. A lock for a row that has already left the queue keeps the
  // id, which is still enough to ask somebody about.
  const forRow = lock.item ? items.find((m) => m.id === lock.item) : undefined;
  const what = forRow?.branch || (lock.item ? shortId(lock.item, 8) : "");
  const until = stamp(lock.until);
  const took = stamp(lock.taken_at);
  const running = minutesSince(lock.taken_at);

  if (lock.held) {
    return (
      <p data-gate-state="running" className="text-muted-foreground text-xs">
        <span data-tone="accent" style={toneStyle(verdictTone("gating"))} className="font-medium">
          a gate is running
        </span>
        {holder ? <> — {holder} holds the target</> : <> — the target is held</>}
        {what ? (
          <>
            {" "}
            for <code className="text-xs">{what}</code>
          </>
        ) : null}
        {took ? <> since {took}</> : null}
        {running !== null ? <> ({running}m)</> : null}
        {until ? <>, and nothing else can land until {until}</> : null}
      </p>
    );
  }

  // A lock row that is past its window. Present at all means it was never
  // released, because releasing deletes it.
  if (holder) {
    return (
      <p data-gate-state="stale" className="text-muted-foreground text-xs">
        {holder} never gave the target back
        {what ? (
          <>
            {" "}
            after <code className="text-xs">{what}</code>
          </>
        ) : null}
        {until ? <>: the reservation ran out at {until}</> : null}. Nothing is waiting on it — an
        expired lock is a free target, and the next declaration takes it.
      </p>
    );
  }

  return (
    <p data-gate-state="free" className="text-muted-foreground text-xs">
      no gate is running — the target is free, so anything admissible here can land now
    </p>
  );
}

/**
 * The pointer at the end of a refusal: "known: <title>", linked to the row.
 *
 * The title is shown rather than the id because the title is what makes a
 * reader decide to open it - an id asks them to fetch before they can tell
 * whether it is worth fetching, and at this moment they are already annoyed.
 *
 * A row with no ref is one personal to its author: no route reaches it, so the
 * text stands unlinked rather than pointing somewhere that answers 404. That is
 * still the useful half - it says this is known and what it is called.
 */
function KnownIssueLink({ issue }: { issue: KnownIssue }) {
  const label = `known: ${issue.title}`;
  return (
    <>
      {" — "}
      {issue.ref ? (
        <Link className="underline" data-known-issue={issue.id} to={refPath(issue.ref) ?? "#"}>
          {label}
        </Link>
      ) : (
        <span data-known-issue={issue.id}>{label}</span>
      )}
    </>
  );
}

/**
 * Three states, not two, and that is the point of this component existing.
 *
 * undefined means NOT DECIDED - nobody stated a tip, so no question was asked.
 * Drawing "may land" there would be a green light nobody switched on, which is
 * the exact failure the store refuses to commit and would be pointless to
 * prevent server-side and then reintroduce in the browser.
 */
function Verdict({ item, decided }: { item: MergeRequest; decided: boolean }) {
  if (!decided || item.admissible === undefined) {
    return (
      <Badge
        variant="secondary"
        data-merge-verdict="undecided"
        data-tone="mute"
        style={toneStyle(verdictTone("undecided"))}
      >
        not judged
      </Badge>
    );
  }
  // A RED IS NOT AN ABSENCE OF LOOKING, and the code cannot tell you which one
  // this is. `applyRed` never writes gated_tip - deliberately, because a
  // written tip is what MergeAdmissible reads as evidence FOR landing - so a
  // row whose gate FAILED refuses with merge.ungated, the same token as a row
  // nobody has ever measured. mergered_test.go:50-59 asserts that on master.
  //
  // So the arm below, read alone, draws "we looked and it failed" as "waiting
  // for the gate". That is the same collapse this component exists to undo,
  // pointing the other way: caught by @flowy-claude while writing the check for
  // it, before either had landed.
  //
  // THE RED ITSELF IS THE DISCRIMINATOR, and it is already on the wire -
  // api_mergequeue.go:59 sends `red` whenever RedTipOf is set. So this asks the
  // row what happened rather than inferring it from a code that cannot say.
  if (item.red?.tip) {
    return (
      <Badge
        variant="secondary"
        data-merge-verdict="red"
        data-tone="bad"
        style={toneStyle(verdictTone("refused"))}
      >
        the gate said no
      </Badge>
    );
  }
  // NOBODY HAS LOOKED IS NOT A NO, and until 2026-08-21 this component could
  // not tell the difference: the node sends admissible:false for a row it has
  // simply never measured, so an ordinary queue waiting its turn was drawn as a
  // wall of refusals. The operator read "0 may land, 4 refused" off a healthy
  // queue and could not tell it from four rejected branches; of those four, one
  // had a red and three had never been gated against the current master.
  //
  // The node states the cause and this reads it rather than inferring one from
  // red and blocked being absent - which is inferring a fact somebody already
  // holds, and is how "empty" comes to mean "absent".
  if (item.code === "merge.ungated" || item.code === "merge.stale_gate") {
    return (
      <Badge
        variant="secondary"
        data-merge-verdict="waiting"
        data-tone="mute"
        style={toneStyle(verdictTone("undecided"))}
      >
        {item.code === "merge.stale_gate" ? "needs a re-gate" : "waiting for the gate"}
      </Badge>
    );
  }
  if (item.admissible) {
    return (
      <Badge
        variant="secondary"
        data-merge-verdict="admissible"
        data-tone="ok"
        style={toneStyle(verdictTone("admissible"))}
      >
        may land
      </Badge>
    );
  }
  return (
    <Badge
      variant="secondary"
      data-merge-verdict="refused"
      data-tone="bad"
      style={toneStyle(verdictTone("refused"))}
    >
      refused
    </Badge>
  );
}
