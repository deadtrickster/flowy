import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import type { Span, Trace, TraceSummary } from "@/lib/api";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { cn, shortId } from "@/lib/utils";

/**
 * The traces tab: what this node did, one operation at a time.
 *
 * A trace is the shape of a request - the permission check, the queries under
 * it, the ingest, the leg of replication - and the waterfall is the only view
 * that makes "which part of this took the time" readable at a glance. The
 * spans are scope-filtered by the node, so what is drawn here is what this
 * token may see: a trace whose spans belong to somebody else comes back empty
 * rather than partly hidden.
 *
 * The bars are one hue, and the kind is a word beside each one rather than a
 * colour: identity that is only colour is identity a colourblind reader does
 * not have. Red is kept for the one thing it should mean - a span that failed.
 */
export function Traces() {
  const { token, whoami } = useSession();
  const [params, setParams] = useSearchParams();
  const [list, setList] = useState<TraceSummary[]>([]);
  const [trace, setTrace] = useState<Trace | null>(null);
  const [error, setError] = useState<string | null>(null);
  const selected = params.get("trace") ?? "";
  const asNode = params.get("scope") === "all" && Boolean(whoami?.operator);

  useEffect(() => {
    if (!token) return;
    let stopped = false;
    api
      .traces(asNode)
      .then((found) => {
        if (!stopped) setList(found.traces ?? []);
      })
      .catch((err: Error) => {
        if (!stopped) setError(err.message);
      });
    return () => {
      stopped = true;
    };
  }, [token, asNode]);

  useEffect(() => {
    if (!token || !selected) {
      setTrace(null);
      return;
    }
    let stopped = false;
    api
      .trace(selected, asNode)
      .then((found) => {
        if (!stopped) setTrace(found.trace);
      })
      .catch((err: Error) => {
        if (!stopped) setError(err.message);
      });
    return () => {
      stopped = true;
    };
  }, [token, selected, asNode]);

  return (
    <div className="flex h-full">
      <section className="flex w-96 shrink-0 flex-col border-border border-r">
        <header className="flex items-center gap-2 border-border border-b px-4 py-3">
          <h1 className="font-semibold text-base">traces</h1>
          <span className="ml-auto text-muted-foreground text-xs">{list.length} recent</span>
        </header>
        {!token ? (
          <p className="p-4 text-muted-foreground text-sm">paste a token to see traces</p>
        ) : null}
        {error ? <p className="p-4 text-destructive text-sm">{error}</p> : null}
        <ul className="min-h-0 flex-1 overflow-y-auto">
          {list.map((one) => (
            <li key={one.trace_id}>
              <button
                type="button"
                onClick={() =>
                  setParams(
                    asNode ? { trace: one.trace_id, scope: "all" } : { trace: one.trace_id },
                  )
                }
                className={cn(
                  "flex w-full flex-col items-start gap-1 border-border border-b px-4 py-2 text-left text-sm hover:bg-accent/50",
                  one.trace_id === selected && "bg-accent",
                )}
              >
                <span className="truncate font-medium">{one.root || "(no root span)"}</span>
                <span className="text-muted-foreground text-xs tabular-nums">
                  {shortId(one.trace_id, 8)} · {one.spans} spans ·{" "}
                  {(one.duration_us / 1000).toFixed(1)}ms
                  {one.nodes.length > 1 ? ` · ${one.nodes.length} nodes` : ""}
                  {one.errors > 0 ? ` · ${one.errors} failed` : ""}
                </span>
              </button>
            </li>
          ))}
        </ul>
      </section>

      <section className="flex min-w-0 flex-1 flex-col">
        {trace ? (
          <Waterfall trace={trace} />
        ) : (
          <p className="p-6 text-muted-foreground text-sm">
            pick a trace to see its waterfall - one bar per span, laid out on the trace's own clock
          </p>
        )}
      </section>
    </div>
  );
}

/** Waterfall lays the spans out on the trace's own clock. */
function Waterfall({ trace }: { trace: Trace }) {
  const start = new Date(trace.started).getTime();
  const span = Math.max(1, new Date(trace.ended).getTime() - start);

  return (
    <>
      <header className="flex flex-wrap items-center gap-2 border-border border-b px-4 py-3">
        <h2 className="font-semibold text-sm">{trace.root || "trace"}</h2>
        <code className="text-muted-foreground text-xs">{trace.trace_id}</code>
        <Badge variant="outline">{(trace.duration_us / 1000).toFixed(1)}ms</Badge>
        {trace.nodes.map((node) => (
          <Badge key={node} variant="secondary">
            {node}
          </Badge>
        ))}
        {trace.errors > 0 ? <Badge variant="default">{trace.errors} failed</Badge> : null}
        <span className="ml-auto text-muted-foreground text-xs">{trace.spans.length} spans</span>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto p-4">
        {trace.spans.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            no span of this trace is yours to read - it exists, and it is somebody else's work
          </p>
        ) : (
          <ol className="flex flex-col gap-1">
            {trace.spans.map((one) => (
              <SpanRow key={one.span_id} span={one} start={start} window={span} />
            ))}
          </ol>
        )}
      </div>
    </>
  );
}

function SpanRow({ span, start, window }: { span: Span; start: number; window: number }) {
  const from = new Date(span.started).getTime();
  const to = new Date(span.ended).getTime();
  const left = Math.min(100, Math.max(0, ((from - start) / window) * 100));
  const width = Math.min(100 - left, Math.max(0.75, ((to - from) / window) * 100));
  const failed = span.status === "error";

  return (
    <li className="flex items-center gap-2 text-sm">
      <div className="w-64 shrink-0 truncate" title={span.name}>
        <span className="font-medium">{span.name}</span>{" "}
        <span className="text-muted-foreground text-xs">{span.kind}</span>
      </div>
      <div className="relative h-4 min-w-0 flex-1 rounded bg-muted/40">
        <div
          className={cn("absolute h-4 rounded", failed ? "bg-destructive" : "bg-primary/70")}
          style={{ left: `${left}%`, width: `${width}%` }}
          title={`${span.name} · ${(span.duration_us / 1000).toFixed(2)}ms · ${span.node}${
            failed ? " · failed" : ""
          }`}
        />
      </div>
      <div className="w-40 shrink-0 text-right text-muted-foreground text-xs tabular-nums">
        {(span.duration_us / 1000).toFixed(2)}ms · {span.node}
      </div>
    </li>
  );
}
