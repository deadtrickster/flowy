import { X } from "lucide-react";
import { Link } from "react-router-dom";

import { ReproPanel } from "@/components/ReproPanel";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { type Artifact, artifactPath } from "@/lib/api";
import { hasRepro, reproOf } from "@/lib/findings";
import { renderChat } from "@/lib/markdown";

/**
 * The finding, open beside the list it was chosen from.
 *
 * THE OPERATOR SENT A SCREENSHOT OF THE OLD PYTHON CONSOLE: list on the left,
 * the finding open on the right, the run controls on top, all on one screen.
 * Flowy had every one of those parts and made a reader LEAVE the list to see
 * one, then come back to run the next - so the parts existed and the work did
 * not. "Old server parity" was reported four or five times and read as missing
 * widgets each time; it was the shape.
 *
 * It takes the artifact the list already holds rather than fetching by id. The
 * list read it a moment ago, and a second read would put a spinner between the
 * click and the words for a row that is already in memory.
 */
export function FindingPane({ finding, onClose }: { finding: Artifact; onClose: () => void }) {
  const to = artifactPath(finding);
  const runnable = hasRepro(reproOf(finding));

  return (
    <aside
      data-finding-pane={finding.id}
      className="flex min-h-0 w-[46%] shrink-0 flex-col gap-3 overflow-y-auto rounded-lg border border-border bg-card/40 p-3"
    >
      <div className="flex items-start gap-2">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2 pb-1">
            <Badge variant="outline">finding</Badge>
            {finding.severity ? <Badge variant="outline">{finding.severity}</Badge> : null}
            {finding.project ? <Badge variant="outline">{finding.project}</Badge> : null}
            {runnable ? <Badge variant="outline">repro</Badge> : null}
          </div>
          <h3 className="font-semibold text-sm leading-snug">{finding.title}</h3>
        </div>
        {/* The full page stays reachable: this pane is the reading surface, not
            a replacement for the row's own address. Somebody who wants to send
            a link to a colleague needs the address, and a pane has none. */}
        {to ? (
          <Link
            to={to}
            className="shrink-0 text-primary text-xs underline"
            data-finding-pane-open=""
          >
            open
          </Link>
        ) : null}
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className="h-6 w-6 shrink-0"
          aria-label="close the finding"
          data-finding-pane-close=""
          onClick={onClose}
        >
          <X className="h-3 w-3" />
        </Button>
      </div>

      {finding.body?.trim() ? (
        <div
          className="report-body break-words text-sm"
          // Sanitized in lib/markdown, which is why noDangerouslySetInnerHtml is
          // off for this file in biome.json - the rule cannot see through
          // DOMPurify and the comment cannot sit inside the tag where it fires.
          dangerouslySetInnerHTML={{ __html: renderChat(finding.body, "", {}) }}
        />
      ) : (
        <div className="text-muted-foreground text-xs">no body on this finding</div>
      )}

      {/* The run controls, the per-version table and the log viewer, which are
          the reason a reader opened this at all. The project goes with it for
          the same reason ArtifactView passes it: the runner holds several and
          cannot resolve a version without being told whose source to resolve
          it against. */}
      <ReproPanel finding={finding.id} project={finding.project} runnable={runnable} />
    </aside>
  );
}
