import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import type { KnownIssue, MergeRequest } from "@/lib/api";
import { toneStyle, verdictTone } from "@/lib/statecolour";
import { statusStyle } from "@/lib/todos";

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
}: {
  items: MergeRequest[];
  tip: string;
  tipFrom: "stated" | "deployed" | "none";
  decided: boolean;
  loaded: boolean;
}) {
  if (!loaded) {
    return <p className="px-4 py-6 text-muted-foreground text-sm">reading the queue…</p>;
  }
  if (items.length === 0) {
    return (
      <div className="px-4 py-6">
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
      {/*
        What the verdicts were measured against, said above the rows rather than
        left to be assumed. "deployed" is the honest hedge: nobody passed a tip,
        so these answer "may this land on what is running here" - a real
        question, and not the same as "may this land on master right now".
      */}
      <p className="px-4 pt-3 text-muted-foreground text-xs">
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
          <li key={m.id} className="flex flex-col gap-1 border-border-soft border-b px-4 py-3">
            <div className="flex flex-wrap items-center gap-2">
              <Link
                className="font-medium text-sm hover:underline"
                to={`/p/${m.project ?? "_"}/memory/${m.id}`}
              >
                {m.title || m.branch}
              </Link>
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
        <Link className="underline" data-known-issue={issue.id} to={`/p/${issue.ref}`}>
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
