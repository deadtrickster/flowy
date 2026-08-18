import { useCallback, useEffect, useRef, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  ApiError,
  type ReproRun,
  type ReproStatus,
  ReproUnconfigured,
  type ReproVersion,
  api,
  getReproBase,
  setReproBase,
} from "@/lib/api";
import { cn } from "@/lib/utils";

/**
 * The repro panel: run a finding's repro tree against a version, watch it,
 * and see what every version tried so far came back as.
 *
 * Ported from hands-off/tools/handoff-service/console.html's run module -
 * doRun/pollRuns/openLog/refreshLog/pkg - onto the CONTRACT this migration's
 * Go side is building against instead of that Python service's actual routes
 * (POST /api/run, GET /api/runs, ... vs this file's POST /run, GET /runs?
 * finding=, ...). Poll-based throughout, on the same intervals the original
 * used (runs every 2.5s, an open log every 1.2s): neither console.html nor
 * flowy's own console uses websockets, and a repro run is a slow enough thing
 * that a two-second lag learning it moved from queued to running costs
 * nothing.
 *
 * CONFIRMED, NOT-CONFIRMED AND ERROR ARE THREE DIFFERENT THINGS, everywhere in
 * this file. error means the sandbox broke - a docker build failed, the
 * runner's own timeout fired - and is drawn as a distinct, muted-amber state
 * rather than folded into the red "not confirmed" one. Collapsing them would
 * read as the runner having tried and failed to reproduce a live bug, when
 * what actually happened is nobody managed to ask the question.
 */
export function ReproPanel({ finding, runnable }: { finding: string; runnable: boolean }) {
  // Read once per mount rather than subscribed: this is the only place the
  // setting is edited, so a change here is a call to setReproBase followed
  // by re-reading it, not a value anything else could move out from under.
  const [base, setBase] = useState(() => getReproBase());

  if (!runnable) {
    return (
      <div className="rounded-md border border-border bg-card p-3 text-muted-foreground text-xs">
        this finding has no repro tree - nothing here to run
      </div>
    );
  }

  if (!base) {
    return (
      <RunnerBaseSetup
        onSave={(next) => {
          setReproBase(next);
          setBase(getReproBase());
        }}
      />
    );
  }

  return <ReproPanelBody finding={finding} onBaseCleared={() => setBase(getReproBase())} />;
}

/**
 * What shows when no runner base is configured. THE PANEL MUST SAY SO
 * PLAINLY rather than let a call fall through to a relative fetch, which
 * would silently hit flowy's own origin - a 404 there is indistinguishable
 * from "not confirmed" to anything that does not check, and this panel is
 * the one place that has to.
 */
function RunnerBaseSetup({ onSave }: { onSave: (base: string) => void }) {
  const [draft, setDraft] = useState("");
  return (
    <form
      className="flex flex-col gap-2 rounded-md border border-dashed border-border p-3 text-xs"
      onSubmit={(event) => {
        event.preventDefault();
        if (draft.trim()) onSave(draft);
      }}
    >
      <div className="text-muted-foreground">
        no repro runner configured - repro runs are served by cmd/handoff-runner, a separate binary
        on a trusted host with Docker access, not by this node. Paste its base URL to enable runs,
        logs and package downloads for this finding.
      </div>
      <div className="flex items-center gap-2">
        <Input
          aria-label="repro runner base URL"
          placeholder="http://runner-host:8801"
          value={draft}
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => setDraft(event.target.value)}
        />
        <Button type="submit" size="sm" variant="secondary">
          use
        </Button>
      </div>
    </form>
  );
}

const ACTIVE: ReproStatus[] = ["queued", "running"];

/** The colour a run's status is drawn in. Deliberately not shared with any
 * queue/lifecycle status style already in the console: those are about work
 * items, this is about a verdict, and "confirmed" here has nothing to do
 * with a todo's "done". */
function statusClasses(status: ReproStatus): string {
  switch (status) {
    case "confirmed":
      return "border-destructive/40 bg-destructive/10 text-destructive";
    case "not-confirmed":
      return "border-emerald-500/40 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400";
    case "error":
      return "border-amber-500/40 bg-amber-500/10 text-amber-600 dark:text-amber-400";
    case "running":
      return "border-sky-500/40 bg-sky-500/10 text-sky-600 dark:text-sky-400";
    default:
      return "";
  }
}

function StatusBadge({ status }: { status: ReproStatus }) {
  return (
    <Badge variant="outline" className={statusClasses(status)}>
      {status}
    </Badge>
  );
}

function ReproPanelBody({
  finding,
  onBaseCleared,
}: {
  finding: string;
  onBaseCleared: () => void;
}) {
  const [version, setVersion] = useState("latest");
  const [versionInfo, setVersionInfo] = useState<ReproVersion | null>(null);
  const [versionError, setVersionError] = useState<string | null>(null);

  const [runs, setRuns] = useState<ReproRun[]>([]);
  const [runsError, setRunsError] = useState<string | null>(null);

  const [runBusy, setRunBusy] = useState(false);
  const [runError, setRunError] = useState<string | null>(null);

  const [openLog, setOpenLog] = useState<string | null>(null);

  // Runs, on the same 2.5s beat console.html's pollRuns used. Stops on
  // unmount - the interval outliving the component is what leaves a poll
  // running against a finding nobody is looking at anymore.
  const loadRuns = useCallback(() => {
    api
      .reproRuns(finding)
      .then((page) => {
        setRuns(page);
        setRunsError(null);
      })
      .catch((err: Error) => {
        // A misconfigured base looks like every other failure to fetch, so
        // hand it back up rather than showing "could not read runs" for
        // something the setup screen already explains better.
        if (err instanceof ReproUnconfigured) {
          onBaseCleared();
          return;
        }
        setRunsError(err.message);
      });
  }, [finding, onBaseCleared]);

  useEffect(() => {
    loadRuns();
    const timer = setInterval(loadRuns, 2500);
    return () => clearInterval(timer);
  }, [loadRuns]);

  // The version preview: what "latest"/a tag/a sha actually resolves to right
  // now, debounced the same way Reports.tsx debounces search - a keystroke is
  // otherwise a request, and the field is typed into on every keystroke.
  useEffect(() => {
    const asked = version.trim();
    if (!asked) {
      setVersionInfo(null);
      setVersionError(null);
      return;
    }
    let stopped = false;
    const timer = setTimeout(() => {
      api
        .reproVersion(asked)
        .then((info) => {
          if (stopped) return;
          setVersionInfo(info);
          setVersionError(null);
        })
        .catch((err: Error) => {
          if (stopped) return;
          setVersionInfo(null);
          setVersionError(err.message);
        });
    }, 250);
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [version]);

  const run = async () => {
    const asked = version.trim();
    if (!asked || runBusy) return;
    setRunBusy(true);
    setRunError(null);
    try {
      const started = await api.reproRun(finding, asked);
      loadRuns();
      setOpenLog(started.id);
    } catch (err) {
      setRunError(err instanceof Error ? err.message : String(err));
    } finally {
      setRunBusy(false);
    }
  };

  const versions = byVersion(runs);
  const activeCount = runs.filter((r) => ACTIVE.includes(r.status)).length;

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-muted-foreground text-xs uppercase tracking-wide">
          repro
        </span>
        {activeCount > 0 ? (
          <Badge variant="outline" className={statusClasses("running")}>
            {activeCount} running
          </Badge>
        ) : null}
        <div className="ml-auto flex items-center gap-2">
          <Input
            aria-label="version"
            className="h-8 w-40 font-mono text-xs"
            placeholder="latest | tag | sha"
            value={version}
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => setVersion(event.target.value)}
          />
          <Button size="sm" variant="secondary" disabled={runBusy} onClick={() => void run()}>
            {runBusy ? "starting…" : "run"}
          </Button>
        </div>
      </div>

      {/* What the runner can say about the version right now, without
          running anything - GET /version. buildable/source_build say a run
          would build before it can start, which is worth knowing before
          asking for one and waiting. */}
      {versionInfo ? (
        <div className="flex flex-wrap items-center gap-2 font-mono text-muted-foreground text-xs">
          <span>{versionInfo.sha.slice(0, 12)}</span>
          {versionInfo.source_build ? (
            <Badge variant="outline">
              {versionInfo.binary ? "builds from source (cached)" : "builds from source"}
            </Badge>
          ) : null}
          {!versionInfo.buildable ? <Badge variant="outline">not buildable</Badge> : null}
          <span className="font-sans">{versionInfo.note}</span>
        </div>
      ) : null}
      {versionError ? <div className="text-destructive text-xs">{versionError}</div> : null}
      {runError ? <div className="text-destructive text-xs">{runError}</div> : null}

      {/* The per-version table: every version this finding has been run
          against, its latest verdict, and the history behind it - a version
          going red after it was once green is the fact the append-only log
          in findingruns.go exists to keep, and a table that only ever shows
          the latest run would erase it. */}
      {versions.length > 0 ? (
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-xs">
            <tbody>
              {versions.map((v) => (
                <VersionRow key={v.version} finding={finding} entry={v} onOpenLog={setOpenLog} />
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="text-muted-foreground text-xs">{runsError ? runsError : "no runs yet"}</div>
      )}

      {openLog ? <LogViewer runId={openLog} runs={runs} onClose={() => setOpenLog(null)} /> : null}
    </div>
  );
}

interface VersionEntry {
  version: string;
  sha?: string;
  runs: ReproRun[];
}

/** byVersion groups a finding's run history by the version it ran against,
 * each group newest-attempt-first - the latest entry is the verdict a reader
 * cares about most, the ones behind it are why a rerun was worth doing. */
function byVersion(runs: ReproRun[]): VersionEntry[] {
  const order: string[] = [];
  const groups = new Map<string, ReproRun[]>();
  for (const r of runs) {
    if (!groups.has(r.version)) {
      order.push(r.version);
      groups.set(r.version, []);
    }
    groups.get(r.version)?.push(r);
  }
  return order.map((version) => {
    const list = [...(groups.get(version) ?? [])].sort((a, b) => b.at.localeCompare(a.at));
    return { version, sha: list.find((r) => r.sha)?.sha, runs: list };
  });
}

function VersionRow({
  finding,
  entry,
  onOpenLog,
}: {
  finding: string;
  entry: VersionEntry;
  onOpenLog: (id: string) => void;
}) {
  const [pkgBusy, setPkgBusy] = useState(false);
  const [pkgError, setPkgError] = useState<string | null>(null);
  const latest = entry.runs[0];

  const download = async () => {
    if (pkgBusy) return;
    setPkgBusy(true);
    setPkgError(null);
    try {
      const { blob, filename } = await api.reproPackage(finding, entry.version);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (err) {
      setPkgError(
        err instanceof ApiError ? `${err.status} ${err.message}` : String((err as Error).message),
      );
    } finally {
      setPkgBusy(false);
    }
  };

  return (
    <tr className="border-border border-t">
      <td className="py-1.5 pr-3 font-mono">
        {entry.version}
        {entry.sha ? (
          <span className="ml-1 text-muted-foreground">{entry.sha.slice(0, 12)}</span>
        ) : null}
      </td>
      <td className="py-1.5 pr-3">{latest ? <StatusBadge status={latest.status} /> : null}</td>
      {/* History behind the latest verdict, oldest to newest, so a reader
          reads it left-to-right the same direction time ran. Each dot is a
          run: hovering names the version's actual verdict and when. */}
      <td className="py-1.5 pr-3">
        <div className="flex items-center gap-1">
          {[...entry.runs].reverse().map((r) => (
            <span
              key={r.id}
              title={`${r.status} · ${r.at}`}
              className={cn("h-2.5 w-2.5 rounded-full border", statusClasses(r.status))}
            />
          ))}
        </div>
      </td>
      <td className="py-1.5 pr-3">
        {latest ? (
          <button
            type="button"
            className="text-muted-foreground underline hover:text-foreground"
            onClick={() => onOpenLog(latest.id)}
          >
            log
          </button>
        ) : null}
      </td>
      <td className="py-1.5">
        {latest?.status === "confirmed" ? (
          <Button size="sm" variant="outline" disabled={pkgBusy} onClick={() => void download()}>
            {pkgBusy ? "building…" : "⤓ package"}
          </Button>
        ) : null}
        {pkgError ? <span className="ml-2 text-destructive">{pkgError}</span> : null}
      </td>
    </tr>
  );
}

/**
 * A pausable log viewer, inline rather than a floating modal: the kit here
 * has no Dialog primitive (see components/ui), and one run's log read as a
 * panel below the table needs nothing else on the page moved out of the way.
 *
 * Pause freezes the view with no fetch and no scroll, matching console.html's
 * refreshLog exactly - a reader who paused to read a stack trace is not
 * fighting new lines arriving under their cursor every 1.2s. Resuming catches
 * up immediately rather than waiting for the next tick.
 */
function LogViewer({
  runId,
  runs,
  onClose,
}: {
  runId: string;
  runs: ReproRun[];
  onClose: () => void;
}) {
  const [text, setText] = useState("loading…");
  const [paused, setPaused] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const pre = useRef<HTMLPreElement>(null);

  const active = ACTIVE.includes(runs.find((r) => r.id === runId)?.status ?? "queued");

  const refresh = useCallback(() => {
    api
      .reproLog(runId)
      .then((next) => {
        setError(null);
        setText((prev) => {
          if (next === prev) return prev;
          const node = pre.current;
          const atBottom = node
            ? node.scrollTop + node.clientHeight >= node.scrollHeight - 40
            : true;
          const savedTop = node?.scrollTop ?? 0;
          // The scroll restore has to happen after the text lands in the DOM,
          // not before - queued for the next tick the same way console.html's
          // synchronous DOM write let the browser lay it out first.
          requestAnimationFrame(() => {
            if (!node) return;
            node.scrollTop = atBottom ? node.scrollHeight : savedTop;
          });
          return next;
        });
      })
      .catch((err: Error) => setError(err.message));
  }, [runId]);

  useEffect(() => {
    setText("loading…");
    refresh();
  }, [refresh]);

  useEffect(() => {
    if (paused || !active) return;
    const timer = setInterval(refresh, 1200);
    return () => clearInterval(timer);
  }, [paused, active, refresh]);

  return (
    <div className="flex flex-col gap-1 rounded-md border border-border">
      <div className="flex items-center gap-2 border-border border-b px-2 py-1 font-mono text-xs">
        <span className="flex-1">
          run {runId} log{active ? " · live" : ""}
          {paused ? " · paused" : ""}
        </span>
        <button
          type="button"
          className={cn(
            "rounded border border-border px-1.5 py-0.5",
            paused && "border-primary text-primary",
          )}
          onClick={() => {
            const next = !paused;
            setPaused(next);
            if (!next) refresh(); // resuming catches up immediately
          }}
        >
          {paused ? "▶ resume" : "⏸ pause"}
        </button>
        <button type="button" className="px-1.5 py-0.5 text-muted-foreground" onClick={onClose}>
          ✕
        </button>
      </div>
      {error ? <div className="px-2 pt-1 text-destructive text-xs">{error}</div> : null}
      <pre
        ref={pre}
        className="max-h-64 overflow-auto whitespace-pre-wrap break-words px-2 py-1.5 font-mono text-xs"
      >
        {text}
      </pre>
    </div>
  );
}
