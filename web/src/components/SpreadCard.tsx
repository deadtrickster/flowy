import { useEffect, useState } from "react";

import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { type NagView, api } from "@/lib/api";
import { speakerStyle } from "@/lib/speakercolour";

/**
 * The board and how the work is spread across it.
 *
 * The probe has existed on the node since it moved off board-nag.sh - shares
 * per seat, both thresholds, a verdict - and no surface a person sits in front
 * of drew any of it. So the operator's own rule was reachable from a terminal
 * and nowhere else, and four seats each recomputed it from /api/artifacts
 * instead: twenty times in one session, measured on the commands issued.
 *
 * EVERY COUNT IS THE CALLER'S, because the door has no name parameter. This is
 * the board as this token can see it, which is the only board it should act on.
 *
 * The verdict is the node's word and is never recomputed here. A console that
 * derived its own would be the fifth copy of a rule that has already drifted
 * once between two bash scripts.
 */
export function SpreadCard() {
  const [nag, setNag] = useState<NagView | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let stopped = false;
    api
      .nag()
      .then((view) => {
        if (!stopped) {
          setNag(view);
          setError(null);
        }
      })
      .catch((err: Error) => {
        if (!stopped) setError(err.message);
      });
    return () => {
      stopped = true;
    };
  }, []);

  if (error) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>the board</CardTitle>
        </CardHeader>
        <CardContent className="text-destructive text-sm">{error}</CardContent>
      </Card>
    );
  }
  if (!nag) return null;

  const w = nag.workload;
  return (
    <Card>
      <CardHeader>
        {/* WHICH BOARD. Every count below is computed for one project, and the
         * node now says which - so the title says it too rather than leaving
         * a reader with two tabs on two tokens to work out whose rows these
         * are. `all` when an operator is reading across every project, which
         * is a different answer from any one of them and must not look like
         * a project called nothing. */}
        <CardTitle data-nag-project={nag.all_projects ? "all" : (nag.project ?? "")}>
          the board{nag.all_projects ? " (all projects)" : nag.project ? ` · ${nag.project}` : ""}
        </CardTitle>
        <CardDescription>{verdictLine(nag)}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap gap-4 text-sm" data-nag-counts={nag.open}>
          <Count label="open" value={nag.open} />
          {/* Unowned first among the rest: it is the one number an idle reader
              can act on without asking anybody. */}
          <Count label="unowned" value={nag.unowned} />
          <Count label="yours" value={nag.mine} />
          <Count label="yours, not started" value={nag.mine_todo} />
          <Count label="stale" value={nag.stale} />
        </div>
        {w.shares.length === 0 ? (
          <p className="text-muted-foreground text-sm">nobody is carrying anything</p>
        ) : (
          <ul className="flex flex-col gap-1">
            {w.shares.map((s) => (
              <li
                key={s.assignee}
                data-spread-share={s.assignee}
                data-spread-open={s.open}
                className="flex items-center gap-2 text-sm"
              >
                <span className="w-36 shrink-0 truncate" style={speakerStyle(s.assignee)}>
                  {s.assignee}
                </span>
                {/* The bar is drawn against the REBALANCE line rather than
                    against the largest share, so two boards a week apart are
                    comparable and a bar that reaches the end means the same
                    thing every time. */}
                <span className="h-2 min-w-0 flex-1 rounded bg-muted">
                  <span
                    className="block h-2 rounded bg-primary/60"
                    style={{ width: `${Math.min(100, (s.share / w.rebalance) * 100)}%` }}
                  />
                </span>
                <span className="w-20 shrink-0 text-right font-mono text-muted-foreground text-xs">
                  {s.open} · {Math.round(s.share * 100)}%
                </span>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

function Count({ label, value }: { label: string; value: number }) {
  return (
    <span className="flex items-baseline gap-1">
      <span className="font-mono text-base">{value}</span>
      <span className="text-muted-foreground text-xs">{label}</span>
    </span>
  );
}

/**
 * The sentence under the title: which line was crossed, and what to do.
 *
 * Named rather than left as a verdict word, for the reason the CLI names it:
 * `check` and `rebalance` mean different things to do, and a reader given the
 * word alone is left doing the arithmetic.
 */
function verdictLine(nag: NagView): string {
  const w = nag.workload;
  const pct = (n: number) => `${Math.round(n * 100)}%`;
  switch (w.verdict) {
    case "rebalance":
      return `${w.top} holds ${pct(w.top_share)}, over the ${pct(w.rebalance)} line - hand some back`;
    case "check":
      return `${w.top} holds ${pct(w.top_share)}, over the ${pct(w.check)} line`;
    case "alone":
      return "one carrier and nothing unclaimed, so the share says nothing";
    case "empty":
      return "nothing open";
    default:
      return "the work is spread";
  }
}
