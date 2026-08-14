import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";

import { StatusControl } from "@/components/StatusControl";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { type Artifact, LIFECYCLE_TYPES, api } from "@/lib/api";
import { useSession } from "@/lib/session";

/**
 * One artifact, at /p/:project/:type/:id.
 *
 * The project and the type are in the path because a link is a thing people
 * send each other: it should say what it points at without being followed. The
 * id is what the node is asked for, and the node decides whether this token may
 * see it - an unreadable artifact is a 404 here exactly as it is on the API.
 */
export function ArtifactView() {
  const { project, type, id = "" } = useParams();
  const { token } = useSession();
  const [artifact, setArtifact] = useState<Artifact | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!token || !id) return;
    let stopped = false;
    api
      .artifact(id)
      .then((found) => {
        if (!stopped) {
          setArtifact(found);
          setError(null);
        }
      })
      .catch((err: Error) => {
        if (!stopped) {
          setArtifact(null);
          setError(err.message);
        }
      });
    return () => {
      stopped = true;
    };
  }, [token, id]);

  return (
    <div className="h-full overflow-y-auto p-6">
      <div className="mx-auto flex max-w-3xl flex-col gap-4">
        <div className="flex items-center gap-2 text-muted-foreground text-xs">
          <Link to="/" className="hover:text-foreground">
            overview
          </Link>
          <span>/</span>
          <span>{project}</span>
          <span>/</span>
          <span>{type}</span>
          <span>/</span>
          <span className="font-mono">{id}</span>
        </div>

        {error ? <div className="text-destructive text-sm">{error}</div> : null}
        {!token ? <div className="text-muted-foreground text-sm">no token</div> : null}

        {artifact ? (
          <Card>
            <CardHeader>
              <CardTitle className="text-base">{artifact.title || artifact.id}</CardTitle>
              <div className="flex flex-wrap gap-1 pt-1">
                <Badge variant="secondary">{artifact.type}</Badge>
                {artifact.kind ? <Badge variant="outline">{artifact.kind}</Badge> : null}
                <Badge variant="outline">{artifact.visibility}</Badge>
                {artifact.status ? <Badge variant="outline">{artifact.status}</Badge> : null}
                {(artifact.tags ?? []).map((tag) => (
                  <Badge key={tag} variant="outline">
                    {tag}
                  </Badge>
                ))}
              </div>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              {/* Only the types that have a lifecycle get the control. A
                  transcript has no status to move, and the node would say so. */}
              {LIFECYCLE_TYPES.includes(artifact.type) ? (
                <StatusControl artifact={artifact} onMoved={setArtifact} />
              ) : null}
              <pre className="whitespace-pre-wrap break-words font-sans text-sm">
                {artifact.body}
              </pre>
              {artifact.discovery ? (
                <div>
                  <div className="pb-1 font-medium text-muted-foreground text-xs">discovery</div>
                  <pre className="whitespace-pre-wrap break-words font-sans text-sm">
                    {artifact.discovery}
                  </pre>
                </div>
              ) : null}
              <div className="font-mono text-muted-foreground text-xs">
                hlc {artifact.hlc} · node {artifact.node} · owner {artifact.owner_user}
              </div>
            </CardContent>
          </Card>
        ) : null}
      </div>
    </div>
  );
}
