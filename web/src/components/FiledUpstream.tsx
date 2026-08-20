import { useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { type Artifact, api } from "@/lib/api";
import { upstreamOf } from "@/lib/findings";

/**
 * Saying a finding has been sent upstream, and taking it back.
 *
 * THE VERB HAD NO DOOR A PERSON COULD OPEN. finding_upstream is an MCP tool, so
 * any seat with an MCP connection could file a finding and the operator, in the
 * console, could only read "upstream: unfiled" on every row. The old python
 * console has this as a button with the issue number beside it and "unmark to
 * reopen"; this is that, against POST /api/finding/{id}/upstream.
 *
 * TAKING IT BACK IS ITS OWN ACT, and the store is stricter about this than the
 * python console was - rightly. "unmark to reopen" over there meant unfiled, and
 * this node refuses that for a filed row, in a sentence worth quoting:
 *
 *   is filed as pa #16958 and calling it "unfiled" would erase that number,
 *   after which somebody files it there a second time. Use "withdrawn" for a
 *   filing we took back, or "rejected" for one they turned down
 *
 * So the control offers WITHDRAWN, which keeps the number and says what
 * happened to it. Rejected is theirs to tell us and is not a button here: a
 * console cannot know that a maintainer closed something.
 */
export function FiledUpstream({
  finding,
  onFiled,
}: {
  finding: Artifact;
  onFiled: (updated: Artifact) => void;
}) {
  const filing = upstreamOf(finding);
  // WHICH TRACKER, because the store refuses a filing without one and says why:
  // "a filing in state filed names which tracker it is in and which number they
  // gave it - without those it is a status word". It is right to refuse: #16958
  // means nothing without saying whose #16958.
  //
  // The default is the filing's own tracker if it has ever had one, then the
  // finding's project, which is what the corpus actually uses - the importers
  // put `ragflow` and `serenedb` on the rows and upstreamProjectOf reads the
  // same names. It stays EDITABLE because a finding filed against a different
  // tracker than its project is ordinary, and a guess the reader cannot correct
  // is worse than a blank.
  const [tracker, setTracker] = useState(filing.tracker || finding.project || "");
  const [id, setId] = useState(filing.id ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const send = async (state: string, issue: string) => {
    setBusy(true);
    setError(null);
    try {
      const answer = await api.fileUpstream(finding.id, {
        state,
        // The number and the tracker ride together with the state, because the
        // store refuses a filing that has one and not the other - a number with
        // no tracker is a claim nobody can look up.
        ...(issue.trim()
          ? { id: issue.trim(), tracker: tracker.trim(), kind: filing.kind || "issue" }
          : {}),
      });
      onFiled(answer.item);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-wrap items-center gap-2" data-upstream-control={finding.id}>
      <Badge variant="outline" data-upstream-state={filing.state}>
        {filing.state}
      </Badge>
      {filing.state === "filed" ? (
        <>
          {filing.id ? (
            <span className="font-mono text-xs" data-upstream-id={filing.id}>
              #{filing.id}
            </span>
          ) : null}
          <Button
            type="button"
            size="sm"
            variant="ghost"
            disabled={busy}
            data-upstream-withdraw=""
            title="we took this filing back - the number is kept, so nobody files it there twice"
            onClick={() => void send("withdrawn", filing.id ?? "")}
          >
            {busy ? "…" : "withdraw"}
          </Button>
        </>
      ) : (
        <>
          <Input
            value={tracker}
            onChange={(event) => setTracker(event.target.value)}
            disabled={busy}
            placeholder="tracker"
            aria-label="which tracker it was filed in"
            className="h-7 w-28 text-xs"
            data-upstream-tracker=""
          />
          <Input
            value={id}
            onChange={(event) => setId(event.target.value)}
            disabled={busy}
            placeholder="issue number"
            aria-label="issue number upstream"
            className="h-7 w-32 text-xs"
            data-upstream-input=""
          />
          <Button
            type="button"
            size="sm"
            variant="secondary"
            disabled={busy}
            data-upstream-file=""
            onClick={() => void send("filed", id)}
          >
            {busy ? "filing…" : "mark filed"}
          </Button>
        </>
      )}
      {error ? (
        <span className="text-destructive text-xs" data-upstream-error="">
          {error}
        </span>
      ) : null}
    </div>
  );
}
