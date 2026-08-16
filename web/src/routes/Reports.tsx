import { useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { type Artifact, api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { shortId } from "@/lib/utils";

/**
 * The reports: published documents - research, designs, reviews - that the
 * project reads on purpose. What is listed here is what the permission filter
 * allows, like every other list, and each card says what the report is true of
 * (as_of) so nobody has to guess whether it is current.
 */
export function Reports() {
  const { token } = useSession();
  const [reports, setReports] = useState<Artifact[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!token) {
      setReports([]);
      return;
    }
    let stopped = false;
    api
      .reports()
      .then((page) => {
        if (!stopped) {
          setReports(page.artifacts);
          setError(null);
        }
      })
      .catch((e: Error) => {
        if (!stopped) setError(e.message);
      });
    return () => {
      stopped = true;
    };
  }, [token]);

  return (
    <div className="flex flex-col gap-3">
      <h2 className="text-lg font-semibold">reports</h2>
      {error ? <div className="text-destructive text-sm">{error}</div> : null}
      {!token ? <div className="text-muted-foreground text-sm">no token</div> : null}
      {token && !error && reports.length === 0 ? (
        <div className="text-muted-foreground text-sm">
          no reports yet - publish one with report_write over MCP
        </div>
      ) : null}
      {reports.map((r) => {
        const fields = (r.fields ?? {}) as Record<string, unknown>;
        const asOf = typeof fields.as_of === "string" ? fields.as_of : undefined;
        const supersedes = typeof fields.supersedes === "string" ? fields.supersedes : undefined;
        return (
          <Card key={r.id}>
            <CardHeader>
              <CardTitle className="text-base">
                <Link className="hover:underline" to={`/p/${r.project ?? "_"}/report/${r.id}`}>
                  {r.title || r.id}
                </Link>
              </CardTitle>
              <div className="flex flex-wrap gap-1 pt-1">
                <Badge variant="secondary">report</Badge>
                {asOf ? <Badge variant="outline">as of {asOf}</Badge> : null}
                {supersedes ? (
                  <Badge variant="outline">supersedes {shortId(supersedes)}</Badge>
                ) : null}
                {(r.tags ?? []).map((tag) => (
                  <Badge key={tag} variant="outline">
                    {tag}
                  </Badge>
                ))}
              </div>
            </CardHeader>
            <CardContent className="text-muted-foreground text-xs">
              updated {r.updated} · {r.visibility}
            </CardContent>
          </Card>
        );
      })}
    </div>
  );
}
