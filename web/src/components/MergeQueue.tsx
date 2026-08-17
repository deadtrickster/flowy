import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import type { MergeRequest } from "@/lib/api";
import { statusStyle } from "@/lib/todos";

/**
 * The verdict colours, defined here rather than borrowed from the status scale.
 *
 * The first version of this passed statusStyle("blocked"), which reads as
 * correct and is not: there is no "blocked" rank, so it fell through to the same
 * grey as "waiting" and drew a refusal as a shrug. A colour that silently falls
 * back to the wrong meaning is worse than no colour, and the word in the badge
 * is always the real signal - these only make it faster to scan.
 */
const GREEN = "#4fae7a";
const RED = "#d1585f";

function verdictStyle(colour: string) {
  return { color: colour, backgroundColor: `color-mix(in srgb, ${colour} 18%, transparent)` };
}

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
          <li key={m.id} className="flex flex-col gap-1 border-border border-b px-4 py-3">
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
              <p className="text-muted-foreground text-xs">{m.reason}</p>
            ) : null}
          </li>
        ))}
      </ul>
    </div>
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
      <Badge variant="outline" data-merge-verdict="undecided">
        not judged
      </Badge>
    );
  }
  if (item.admissible) {
    return (
      <Badge variant="secondary" data-merge-verdict="admissible" style={verdictStyle(GREEN)}>
        may land
      </Badge>
    );
  }
  return (
    <Badge variant="secondary" data-merge-verdict="refused" style={verdictStyle(RED)}>
      refused
    </Badge>
  );
}
