import { useEffect, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import type {
  Anomaly,
  Availability,
  CollabGroup,
  CorpusGroup,
  Metrics as MetricsPayload,
  NodeGroup,
  PermGroup,
  SyncGroup,
} from "@/lib/api";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";

/**
 * The metrics tab: the six groups the node measures about itself, as the token
 * holding this console may see them.
 *
 * The rule the whole view is built on is that a group which could not be read
 * renders its reason and no numbers. Everything here goes through Unmeasured
 * for that: a card that showed 0 where the answer was "this is the operator's
 * view and you are not the operator" would be a console telling a person their
 * node is empty.
 */
export function Metrics() {
  const { token, whoami } = useSession();
  const [metrics, setMetrics] = useState<MetricsPayload | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [asNode, setAsNode] = useState(false);

  useEffect(() => {
    if (!token) {
      setMetrics(null);
      return;
    }
    let stopped = false;
    setError(null);
    api
      .metrics(asNode)
      .then((found) => {
        if (!stopped) setMetrics(found);
      })
      .catch((err: Error) => {
        if (!stopped) setError(err.message);
      });
    return () => {
      stopped = true;
    };
  }, [token, asNode]);

  const groups = metrics?.groups;

  return (
    <div className="h-full overflow-y-auto p-6">
      <div className="mx-auto flex max-w-5xl flex-col gap-4">
        <div className="flex items-center gap-3">
          <div>
            <h1 className="font-semibold text-xl tracking-tight">metrics</h1>
            <p className="text-muted-foreground text-sm">
              what this node measured, for what you may read - a number that could not be measured
              says so instead of reading as zero
            </p>
          </div>
          {whoami?.operator ? (
            <label className="ml-auto flex items-center gap-2 text-xs">
              <input
                type="checkbox"
                checked={asNode}
                onChange={(event) => setAsNode(event.target.checked)}
              />
              the whole node
            </label>
          ) : null}
        </div>

        {!token ? (
          <p className="text-muted-foreground text-sm">paste a token to see your numbers</p>
        ) : null}
        {error ? <div className="text-destructive text-sm">{error}</div> : null}

        {metrics ? (
          <p className="text-muted-foreground text-xs">
            {metrics.node} · {metrics.version} · scope <code>{metrics.scope.key}</code> ·{" "}
            {new Date(metrics.generated).toLocaleTimeString()}
          </p>
        ) : null}

        {groups?.node ? <NodeCard group={groups.node} /> : null}
        {groups?.corpus ? <CorpusCard group={groups.corpus} /> : null}
        {groups?.collaboration ? <CollabCard group={groups.collaboration} /> : null}
        {groups?.sync ? <SyncCard group={groups.sync} /> : null}
        {groups?.permissions ? <PermsCard group={groups.permissions} /> : null}
        {groups?.anomalies ? (
          <AnomaliesCard
            series={groups.anomalies.series}
            minSamples={groups.anomalies.min_samples}
            basis={groups.anomalies.basis}
            available={groups.anomalies.available}
            reason={groups.anomalies.reason}
          />
        ) : null}
      </div>
    </div>
  );
}

/**
 * Unmeasured is what a group renders instead of its numbers.
 *
 * It is one component so that "could not be read" always looks the same and
 * never looks like data. The reason comes from the node - it is the node that
 * knows whether this was a permission, a platform that does not report the
 * value, or a peer that was never asked.
 */
function Unmeasured({ reason }: { reason?: string }) {
  return (
    <p className="text-muted-foreground text-sm">
      <span className="font-medium">not measured</span>
      {reason ? ` - ${reason}` : " - the node gave no reason"}
    </p>
  );
}

/** Tile is one number with what it is a number of. */
function Tile({ label, value, of }: { label: string; value: string | number; of?: string }) {
  return (
    <div className="rounded-md border border-border p-2">
      <div className="text-muted-foreground text-xs">{label}</div>
      <div className="font-semibold text-lg tabular-nums">{value}</div>
      {of ? <div className="text-muted-foreground text-xs">{of}</div> : null}
    </div>
  );
}

function GroupCard({
  title,
  description,
  group,
  children,
}: {
  title: string;
  description?: string;
  group: Availability;
  children: React.ReactNode;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
        <CardDescription>
          {group.available ? (group.measured ?? description) : description}
        </CardDescription>
      </CardHeader>
      <CardContent>{group.available ? children : <Unmeasured reason={group.reason} />}</CardContent>
    </Card>
  );
}

function NodeCard({ group }: { group: NodeGroup }) {
  return (
    <GroupCard title="node" description="this machine, and it is the operator's view" group={group}>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <Tile label="uptime" value={`${Math.round(group.uptime_s ?? 0)}s`} of={group.build} />
        {group.db?.available ? (
          <Tile
            label="store"
            value={group.db.up ? "up" : "down"}
            of={`${group.db.engine} · ${group.db.latency_ms}ms ping`}
          />
        ) : (
          <Tile label="store" value="?" of={group.db?.reason} />
        )}
        {group.cpu?.available ? (
          <Tile
            label="cpu"
            value={group.cpu.core_share.toFixed(3)}
            of={`of one core, over ${Math.round(group.cpu.window_s)}s (${group.cpu.cores} cores here)`}
          />
        ) : (
          <Tile label="cpu" value="?" of={group.cpu?.reason} />
        )}
        {group.memory?.available ? (
          <Tile
            label="rss"
            value={`${(group.memory.rss_bytes / (1024 * 1024)).toFixed(1)} MB`}
            of={group.memory.source}
          />
        ) : (
          <Tile label="rss" value="?" of={group.memory?.reason} />
        )}
        {group.pool ? (
          <Tile
            label="db pool"
            value={`${group.pool.in_use}/${group.pool.max_open}`}
            of="in use of max_open"
          />
        ) : null}
        {group.traces ? (
          <Tile
            label="spans"
            value={group.traces.kept}
            of={
              group.traces.dropped > 0
                ? `${group.traces.dropped} could not be recorded`
                : (group.traces.exporter ?? "recorded here, exported nowhere")
            }
          />
        ) : null}
      </div>
    </GroupCard>
  );
}

function CorpusCard({ group }: { group: CorpusGroup }) {
  const types = Object.entries(group.by_type);
  const projects = Object.entries(group.by_project);
  return (
    <GroupCard title="corpus" description="what you may read" group={group}>
      <div className="flex flex-col gap-3">
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <Tile label="artifacts" value={group.artifacts} of="you may read" />
          <Tile label="events" value={group.events} of="you may read" />
          <Tile label="new today" value={group.growth.artifacts_24h} of={group.growth.of} />
          <Tile label="new this week" value={group.growth.artifacts_7d} of={group.growth.of} />
        </div>

        <div>
          <h3 className="mb-1 font-medium text-sm">search coverage</h3>
          {group.embedding.available ? (
            <p className="text-muted-foreground text-sm">
              {group.embedding.bm25_only} of {group.embedding.denominator} text-indexed,{" "}
              {group.embedding.embedded} embedded {group.embedding.of}
            </p>
          ) : (
            <Unmeasured reason={group.embedding.reason} />
          )}
        </div>

        <div>
          <h3 className="mb-1 font-medium text-sm">by type</h3>
          <Counts entries={types} />
        </div>
        <div>
          <h3 className="mb-1 font-medium text-sm">by project</h3>
          <Counts entries={projects} />
        </div>
        <div>
          <h3 className="mb-1 font-medium text-sm">on disk</h3>
          {group.storage.available ? (
            <p className="text-muted-foreground text-sm tabular-nums">
              {(group.storage.total_bytes / (1024 * 1024)).toFixed(1)} MB over{" "}
              {Object.keys(group.storage.tables_bytes).length} tables
            </p>
          ) : (
            <Unmeasured reason={group.storage.reason} />
          )}
        </div>
      </div>
    </GroupCard>
  );
}

/** Counts is a labelled list of numbers, which is not a chart and does not need to be. */
function Counts({ entries }: { entries: [string, number][] }) {
  if (entries.length === 0) {
    return <p className="text-muted-foreground text-sm">nothing you may read is in this bucket</p>;
  }
  return (
    <div className="flex flex-wrap gap-2">
      {entries.map(([label, n]) => (
        <Badge key={label} variant="outline">
          {label} <span className="ml-1 font-semibold tabular-nums">{n}</span>
        </Badge>
      ))}
    </div>
  );
}

function CollabCard({ group }: { group: CollabGroup }) {
  const days = group.messages_by_day;
  const most = Math.max(1, ...days.map((day) => day.count));
  return (
    <GroupCard title="collaboration" description={group.window} group={group}>
      <div className="flex flex-col gap-3">
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <Tile label="messages" value={group.messages_24h} of="in the last 24 hours" />
          <Tile label="open todos" value={group.open_todos} of="todo, feature and handoff" />
          <Tile label="handoffs" value={group.handoffs_in_flight} of="open or delegated" />
          <Tile
            label="active"
            value={`${group.active_users_24h}/${group.active_agents_24h}`}
            of="people / agents in 24h"
          />
        </div>

        {/* One series, so no legend: the heading names it. The bars are one hue
            with the count on hover, rather than a number over every column. */}
        <div>
          <h3 className="mb-1 font-medium text-sm">messages a day, last 7</h3>
          <div className="flex h-24 items-end gap-2">
            {days.map((day) => (
              <div key={day.day} className="flex min-w-0 flex-1 flex-col items-center gap-1">
                <div
                  className="w-full rounded-t bg-primary/70"
                  style={{ height: `${Math.round((day.count / most) * 72)}px` }}
                  title={`${day.count} on ${day.day}`}
                />
                <span className="truncate text-muted-foreground text-xs">{day.day.slice(5)}</span>
              </div>
            ))}
          </div>
        </div>

        <div>
          <h3 className="mb-1 font-medium text-sm">tasks</h3>
          <Counts entries={Object.entries(group.tasks_by_state)} />
        </div>
      </div>
    </GroupCard>
  );
}

function SyncCard({ group }: { group: SyncGroup }) {
  return (
    <GroupCard title="sync" description="federation, and it is the operator's view" group={group}>
      <div className="flex flex-col gap-3">
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <Tile label="peers" value={group.peers.length} of="bookmarked here" />
          <Tile label="local hwm" value={group.local_hwm} of="highest reading held" />
          <Tile label="offline queue" value={group.offline_queue} of="rows owed to a reader" />
          <Tile label="conflicts" value={group.conflicts_total} of="rows that lost a merge" />
        </div>
        {group.peers.length === 0 ? (
          <p className="text-muted-foreground text-sm">no peer has ever synced with this node</p>
        ) : (
          <ul className="flex flex-col gap-1 text-sm">
            {group.peers.map((peer) => (
              <li key={peer.peer} className="rounded-md border border-border p-2">
                <div className="font-mono text-xs">{peer.peer}</div>
                <div className="text-muted-foreground text-xs tabular-nums">
                  pull {peer.pull_cursor} · pushed {peer.pushed_cursor} · {peer.pending_push} to
                  push ·{" "}
                  {peer.last_seen_age_s === undefined
                    ? "never seen"
                    : `seen ${peer.last_seen_age_s}s ago`}{" "}
                  · {peer.conflicts} conflicts
                </div>
              </li>
            ))}
          </ul>
        )}
        <div>
          <h3 className="mb-1 font-medium text-sm">pending pull</h3>
          <Unmeasured reason={group.pending_pull.reason} />
        </div>
      </div>
    </GroupCard>
  );
}

function PermsCard({ group }: { group: PermGroup }) {
  return (
    <GroupCard title="permissions" description={group.window} group={group}>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <Tile label="grants" value={group.grants} of="you are party to" />
        <Tile label="shares" value={group.artifact_shares} of="of a single artifact" />
        <Tile label="cross-project" value={group.cross_project_grants} of="grants across an edge" />
        <Tile label="refused" value={group.denied_24h} of="requests in the last 24 hours" />
      </div>
    </GroupCard>
  );
}

/**
 * The anomaly card, and the reason this whole view exists.
 *
 * A series with too little history renders "insufficient samples" and the count
 * it has - not a verdict, not a zero, and not a green tick. The comparison is
 * against this node's own recorded readings, which is what the basis line says.
 */
function AnomaliesCard({
  series,
  minSamples,
  basis,
  available,
  reason,
}: {
  series: Anomaly[];
  minSamples: number;
  basis: string;
  available: boolean;
  reason?: string;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>anomalies</CardTitle>
        <CardDescription>{basis}</CardDescription>
      </CardHeader>
      <CardContent>
        {!available ? (
          <Unmeasured reason={reason} />
        ) : (
          <ul className="flex flex-col gap-1">
            {series.map((one) => (
              <li
                key={one.series}
                className="flex flex-wrap items-center gap-2 rounded-md border border-border p-2 text-sm"
              >
                <code className="text-xs">{one.series}</code>
                <span className="tabular-nums">{one.latest}</span>
                {one.verdict === "unusual" ? (
                  <Badge variant="default">unusual</Badge>
                ) : one.verdict === "normal" ? (
                  <Badge variant="outline">normal</Badge>
                ) : (
                  <Badge variant="outline">insufficient samples</Badge>
                )}
                <span className="text-muted-foreground text-xs">
                  {one.reason ??
                    `${one.samples} of ${minSamples} readings, baseline ${one.baseline ?? "?"}`}
                </span>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
