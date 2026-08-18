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
export function ReproPanel({
  finding,
  project,
  runnable,
}: {
  finding: string;
  project?: string | null;
  runnable: boolean;
}) {
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

  return (
    <ReproPanelBody
      finding={finding}
      project={project}
      onBaseCleared={() => setBase(getReproBase())}
    />
  );
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
      autoComplete="off"
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

const ACTIVE: ReproStatus[] = ["queued", "building", "running"];

/** What a runner that cannot run says about itself, in one sentence a reader
 * can act on. Packaging and version resolution need nothing from the run queue,
 * so they keep working and this says so rather than reading as "the runner is
 * down". */
const PACKAGES_ONLY =
  "this runner packages and resolves versions but cannot run: its run queue is not linked in " +
  "(cmd/handoff-runner/wiring.go). Download the package and run it where Docker is.";

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
    case "building":
      // A cold build of a system under test is measured in hours, so it gets
      // the same in-flight colour rather than looking like a hung run.
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
  project,
  onBaseCleared,
}: {
  finding: string;
  project?: string | null;
  onBaseCleared: () => void;
}) {
  const [version, setVersion] = useState("latest");
  const [versionInfo, setVersionInfo] = useState<ReproVersion | null>(null);
  const [versionError, setVersionError] = useState<string | null>(null);

  const [runs, setRuns] = useState<ReproRun[]>([]);
  const [runsError, setRunsError] = useState<string | null>(null);
  // Whether the runner behind this base can run anything at all, as IT says -
  // null until the first answer arrives, which is not the same as false. A
  // panel that assumed either way before asking would spend its first seconds
  // either offering a button that refuses or hiding one that works.
  const [linked, setLinked] = useState<boolean | null>(null);

  const [runBusy, setRunBusy] = useState(false);
  const [runError, setRunError] = useState<string | null>(null);

  const [pkgBusy, setPkgBusy] = useState(false);
  const [pkgError, setPkgError] = useState<string | null>(null);

  const [openLog, setOpenLog] = useState<string | null>(null);

  const downloadPackage = async () => {
    const asked = version.trim() || "latest";
    if (pkgBusy) return;
    setPkgBusy(true);
    setPkgError(null);
    try {
      await savePackage(finding, asked);
    } catch (err) {
      setPkgError(packageError(err));
    } finally {
      setPkgBusy(false);
    }
  };

  // Runs, on the same 2.5s beat console.html's pollRuns used. Stops on
  // unmount - the interval outliving the component is what leaves a poll
  // running against a finding nobody is looking at anymore.
  const loadRuns = useCallback(() => {
    api
      .reproRuns()
      .then((page) => {
        // Narrowed HERE, because the door does not narrow: GET /runs answers
        // with every run whose finding the caller may read. See api.reproRuns.
        setRuns((page.runs ?? []).filter((run) => run.finding === finding));
        setLinked(page.linked);
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
        .reproVersion(project, asked)
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
  }, [version, project]);

  /**
   * Ask for a run, and say what came back.
   *
   * POST /run answers with what it QUEUED and what it REFUSED, per finding -
   * so a call that was turned down comes back 400 with a reason on the row
   * rather than as a run that never appears. Both halves are handled: without
   * the refused one, a finding whose project this runner does not hold would
   * leave the panel looking like it had started something.
   */
  const run = async () => {
    const asked = version.trim();
    if (!asked || runBusy) return;
    setRunBusy(true);
    setRunError(null);
    try {
      const started = await api.reproRun(finding, asked);
      loadRuns();
      const queued = started.queued?.[0];
      if (queued) {
        setOpenLog(queued.run);
      } else {
        const refusal = started.refused?.[0];
        setRunError(refusal ? refusal.error : "the runner queued nothing and said why not");
      }
    } catch (err) {
      setRunError(err instanceof Error ? err.message : String(err));
    } finally {
      setRunBusy(false);
    }
  };

  const versions = byVersion(runs);
  const activeCount = runs.filter((r) => ACTIVE.includes(r.status)).length;

  // THE RUN DOOR REFUSES BY NAME UNTIL ITS QUEUE IS WIRED IN, and a button that
  // can only produce that refusal is worse than no button: the reader clicks
  // it, gets a sentence about a build of a binary they do not run, and learns
  // nothing about their finding. Both doors report the same fact - /runs as
  // `linked`, /version as `runnable` - and either saying no is enough to
  // withhold the control. Undecided (nothing answered yet) is not no: the
  // button stays until something says otherwise, which is also what a runner
  // too old to report either word gets.
  const cannotRun = linked === false || versionInfo?.runnable === false;

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
          {/* PACKAGING IS NOT RUNNING, and it is offered on its own here rather
              than only beside a confirmed run: /package needs nothing from the
              run queue, so it is the one thing that still works on a runner
              that cannot run - which is exactly when somebody needs the package
              to take elsewhere. */}
          <Button
            size="sm"
            variant="outline"
            disabled={pkgBusy}
            onClick={() => void downloadPackage()}
          >
            {pkgBusy ? "packaging…" : "⤓ package"}
          </Button>
          <Button
            size="sm"
            variant="secondary"
            disabled={runBusy || cannotRun}
            title={cannotRun ? PACKAGES_ONLY : undefined}
            onClick={() => void run()}
          >
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
              {versionInfo.binary_ready ? "builds from source (cached)" : "builds from source"}
            </Badge>
          ) : null}
          {!versionInfo.buildable ? <Badge variant="outline">not buildable</Badge> : null}
          <span className="font-sans">{versionInfo.note}</span>
        </div>
      ) : null}
      {/* Said in the panel and not only in a tooltip: this is a property of the
          deployment, not of this finding, and the reader's next move - package
          it and run it somewhere that can - depends on knowing which. */}
      {cannotRun ? (
        <div data-repro-runnable="no" className="text-muted-foreground text-xs">
          {PACKAGES_ONLY}
        </div>
      ) : null}
      {versionError ? <div className="text-destructive text-xs">{versionError}</div> : null}
      {runError ? <div className="text-destructive text-xs">{runError}</div> : null}
      {pkgError ? <div className="text-destructive text-xs">{pkgError}</div> : null}

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

/** savePackage fetches the tgz and hands it to the browser's own download,
 * which is the only way a page can put a file where a person can find it. One
 * copy for the two buttons that do it - the header's, for a finding nobody has
 * run, and the per-version one beside a confirmed verdict. */
async function savePackage(finding: string, version: string) {
  const { blob, filename } = await api.reproPackage(finding, version);
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/** packageError keeps the runner's status on the message: a 404 from /package
 * means this runner does not hold that project, and a 409 means the tree's
 * isolation is one it cannot build. Dropping the number would make those two
 * read alike. */
function packageError(err: unknown): string {
  if (err instanceof ApiError) return `${err.status} ${err.message}`;
  return err instanceof Error ? err.message : String(err);
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
    const list = [...(groups.get(version) ?? [])].sort((a, b) => runAt(b) - runAt(a));
    return { version, sha: list.find((r) => r.sha)?.sha, runs: list };
  });
}

/**
 * When a run last did something, as one number to order by.
 *
 * The runner stamps three moments and a run has reached however many of them it
 * has reached, so the latest one that exists is the one that says where this
 * run is in time - a queued run has only queued_at, and ordering it by an
 * absent ended_at would file every waiting run at the beginning of time,
 * underneath verdicts from last week.
 *
 * They are unix SECONDS on the wire (cmd/handoff-runner's Run: int64), which
 * matters only here and in whenRan below.
 */
function runAt(run: ReproRun): number {
  return run.ended_at ?? run.started_at ?? run.queued_at ?? 0;
}

/** whenRan is that moment as something to read, and "not yet" when the runner
 * stamped none - which is what a run has just been accepted looks like. */
function whenRan(run: ReproRun): string {
  const at = runAt(run);
  if (!at) return "not yet stamped";
  return new Date(at * 1000).toISOString().replace("T", " ").slice(0, 19);
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
      await savePackage(finding, entry.version);
    } catch (err) {
      setPkgError(packageError(err));
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
              title={`${r.status} · ${whenRan(r)}`}
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
