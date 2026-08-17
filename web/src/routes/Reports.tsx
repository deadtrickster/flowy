import { useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { type Artifact, api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { shortId } from "@/lib/utils";

/**
 * The reports: published documents - research, designs, reviews - that the
 * project reads on purpose. What is listed here is what the permission filter
 * allows, like every other list, and each card says what the report is true of
 * (as_of) so nobody has to guess whether it is current.
 *
 * Two things a list of titles cannot do on its own, and both are here:
 *
 *   - search. A corpus of documents is not browsable past the first screenful,
 *     and what somebody remembers about a report is a phrase from inside it,
 *     not its title. So the box asks the NODE - the same ranked full-text
 *     search report_search rides, narrowed to reports - rather than filtering
 *     the titles already on the page, which would never find a word that is
 *     only in a body.
 *   - which of these have been replaced. supersedes points backwards from the
 *     newer document, so nothing on an old report says it has been overtaken;
 *     the node turns that round for whoever is reading. A stale report that
 *     looks exactly like a current one is the failure this type was invented
 *     to prevent, and a list is where it happens.
 */
export function Reports() {
  const { token } = useSession();
  const [reports, setReports] = useState<Artifact[]>([]);
  const [query, setQuery] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);

  // The read is the list or the search, decided by the box. Both are the same
  // permission-filtered door, so what changes between them is the ranking and
  // nothing about what may be seen.
  //
  // The query is debounced because every keystroke is otherwise a request, and
  // the last answer to arrive is not necessarily the last one asked for - the
  // stopped flag is what keeps a slow early response from painting over a fast
  // later one.
  useEffect(() => {
    if (!token) {
      setReports([]);
      setLoaded(false);
      return;
    }
    let stopped = false;
    const asked = query.trim();
    const timer = setTimeout(
      () => {
        const read = asked ? api.searchReports(asked) : api.reports();
        read
          .then((page) => {
            if (stopped) return;
            setReports(page.artifacts);
            setError(null);
          })
          .catch((e: Error) => {
            if (!stopped) setError(e.message);
          })
          .finally(() => {
            if (!stopped) setLoaded(true);
          });
      },
      asked ? 150 : 0,
    );
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [token, query]);

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-lg font-semibold">reports</h2>
        <Input
          className="max-w-xs"
          aria-label="search reports"
          placeholder="search the reports"
          value={query}
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => setQuery(event.target.value)}
        />
        <span className="text-muted-foreground text-xs">
          {reports.length} report{reports.length === 1 ? "" : "s"}
          {query.trim() ? ` matching ${JSON.stringify(query.trim())}` : ""}
        </span>
      </div>
      {error ? <div className="text-destructive text-sm">{error}</div> : null}

      <ol aria-label="reports" className="flex flex-col gap-3">
        {/* An empty list says which empty it is - see emptyReads below. */}
        {reports.length === 0 ? (
          <li className="text-muted-foreground text-sm">
            {emptyReads({
              token: Boolean(token),
              loaded,
              failed: Boolean(error),
              query: query.trim(),
            })}
          </li>
        ) : null}
        {reports.map((r) => (
          <ReportCard key={r.id} report={r} />
        ))}
      </ol>
    </div>
  );
}

/**
 * What an empty list says, which is never nothing.
 *
 * Signed out, read but empty, a search that matched nothing and a read that
 * failed are four different facts and all four look like a blank page. The
 * signed-out one is the one this page kept getting wrong: it said "no token",
 * which is true and reads, under a heading that says reports, as "there are
 * none". Nobody signed out has been told there are no reports - they have been
 * told nothing at all, and the sentence has to say so and say what to do.
 */
function emptyReads({
  token,
  loaded,
  failed,
  query,
}: {
  token: boolean;
  loaded: boolean;
  failed: boolean;
  query: string;
}) {
  if (!token) {
    return "paste a token to see the reports - signed out, this is not an empty shelf, it is a locked one";
  }
  if (failed) {
    return "the reports could not be read, so this page is not saying there are none";
  }
  if (!loaded) {
    return "reading the reports…";
  }
  if (query) {
    return `nothing you can read matches ${JSON.stringify(query)} - clear the box for all of them`;
  }
  return "no reports yet - publish one with report_write over MCP";
}

/**
 * One report: what it claims, what it was true of, and whether it still
 * stands.
 *
 * replaced_by is drawn as a link rather than as a note, because being told a
 * document is stale without being told where the current one is leaves the
 * reader exactly where they were. The node only fills it in when the
 * replacement is one this token may read, so a link here always goes
 * somewhere.
 */
function ReportCard({ report }: { report: Artifact }) {
  const fields = (report.fields ?? {}) as Record<string, unknown>;
  const asOf = typeof fields.as_of === "string" ? fields.as_of : undefined;
  const supersedes = typeof fields.supersedes === "string" ? fields.supersedes : undefined;
  const replacedBy = report.replaced_by;
  const project = report.project ?? "_";
  return (
    <li data-report={report.id} data-replaced-by={replacedBy ?? ""}>
      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            <Link className="hover:underline" to={`/p/${project}/report/${report.id}`}>
              {report.title || report.id}
            </Link>
          </CardTitle>
          <div className="flex flex-wrap gap-1 pt-1">
            <Badge variant="secondary">report</Badge>
            {asOf ? <Badge variant="outline">as of {asOf}</Badge> : null}
            {supersedes ? <Badge variant="outline">supersedes {shortId(supersedes)}</Badge> : null}
            {replacedBy ? (
              <Link to={`/p/${project}/report/${replacedBy}`} title={`read ${replacedBy} instead`}>
                <Badge
                  variant="outline"
                  className="border-destructive/40 bg-destructive/10 text-destructive"
                >
                  replaced by {shortId(replacedBy)}
                </Badge>
              </Link>
            ) : null}
            {(report.tags ?? []).map((tag) => (
              <Badge key={tag} variant="outline">
                {tag}
              </Badge>
            ))}
          </div>
        </CardHeader>
        <CardContent className="text-muted-foreground text-xs">
          updated {report.updated} · {report.visibility}
        </CardContent>
      </Card>
    </li>
  );
}
