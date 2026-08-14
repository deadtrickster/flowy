import { useEffect, useState } from "react";

import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { type NodeCounts, api } from "@/lib/api";

/**
 * A stub, deliberately: the numbers here are the ones /healthz already reports,
 * because a metrics page that invents its own is a second source of truth. What
 * it proves today is that the route exists at its own path and survives a
 * reload.
 */
export function Metrics() {
  const [health, setHealth] = useState<NodeCounts | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let stopped = false;
    api
      .health()
      .then((found) => {
        if (!stopped) setHealth(found);
      })
      .catch((err: Error) => {
        if (!stopped) setError(err.message);
      });
    return () => {
      stopped = true;
    };
  }, []);

  const counts = Object.entries(health?.counts ?? {});

  return (
    <div className="h-full overflow-y-auto p-6">
      <div className="mx-auto flex max-w-3xl flex-col gap-4">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">metrics</h1>
          <p className="text-muted-foreground text-sm">
            what the node reports about itself - a stub until there is something worth graphing
          </p>
        </div>

        {error ? <div className="text-destructive text-sm">{error}</div> : null}

        <Card>
          <CardHeader>
            <CardTitle>node</CardTitle>
            <CardDescription>
              {health ? `${health.node} · ${health.version} · db ${health.db}` : "asking…"}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <dl className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              {counts.map(([table, n]) => (
                <div key={table} className="rounded-md border border-border p-2">
                  <dt className="text-muted-foreground text-xs">{table}</dt>
                  <dd className="font-semibold text-lg tabular-nums">{n}</dd>
                </div>
              ))}
            </dl>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
