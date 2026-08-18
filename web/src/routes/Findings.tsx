import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";

import { SeverityBar, SeverityDot, StateChip } from "@/components/StateMarks";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { type Artifact, api } from "@/lib/api";
import {
  EVIDENCE_STATES,
  UNKNOWN_UPSTREAM,
  UPSTREAM_STATES,
  evidenceOf,
  filedUpstream,
  hasRepro,
  knownUpstream,
  reproOf,
  upstreamOf,
} from "@/lib/findings";
import { useSession } from "@/lib/session";
import { evidenceTone, reproTone, severityTone, upstreamTone } from "@/lib/statecolour";

/**
 * The findings: what the fleet's own bug hunting turned up, one artifact of
 * type finding per issue - see lifecycle.go's head comment on why a finding
 * gets no column of its own and rides Kind/Severity/Tags/Related like any
 * other artifact.
 *
 * Shaped exactly like Reports.tsx: a debounced read against the node's search
 * door, narrowed to this type, so the list is permission-filtered by
 * construction rather than by anything drawn here. See api.findings and
 * api.searchFindings, which are that same reports()/searchReports() pattern
 * pointed at a different type.
 *
 * THIS LIST SHOWS THREE AXES AND NEVER ONE STANDING FOR ANOTHER. Our lifecycle
 * (status: open, triaged, done) says how far WE got. The upstream filing
 * (unfiled, filed as #123, accepted, rejected) says what THEIR tracker holds.
 * The evidence (source, reproduced, verified on a sha) says how sure anybody is
 * and on what. A finding that is done and unfiled is the ordinary case - written
 * up, nobody sent it, which is where most of the corpus sits - and a page that
 * drew only `status` would report that finding as finished work with nothing to
 * do. See web/src/lib/findings.ts, which is where the three are read off a row
 * and why they are read there rather than here.
 *
 * The filters this page adds on top are NOT more query params. status/kind/
 * project are columns ArtifactQuery already narrows a read by; severity, tag,
 * upstream and evidence are not - the store has no such column to ask with, and
 * two of them live inside a jsonb blob - so all of them are applied here, over
 * the artifacts the permission-filtered read already returned, the same way
 * Todos.tsx narrows its tag filter over an already-fetched queue rather than
 * asking the node for a tag it cannot answer.
 *
 * MARKS ARE A FILTER, NOT DECORATION. "show me everything written up and not
 * yet filed" is the question this list exists to answer - it is the list
 * somebody works from before filing anything - so the two axes that answer it
 * are selectable and their counts are in the header, rather than being badges a
 * reader has to scan for by eye.
 *
 * Their option lists come from the vocabularies the store defines
 * (UPSTREAM_STATES, EVIDENCE_STATES) rather than from what happens to be on the
 * page, which is the opposite of how severity and tag are built here and
 * deliberately so: those are free labels with no canonical list to copy, while
 * these are closed sets a write is refused against, and an "unfiled" option that
 * disappeared whenever every loaded finding happened to be filed would be a
 * filter that stops offering the question the page is for.
 */
export function Findings() {
  const { token } = useSession();
  const [findings, setFindings] = useState<Artifact[]>([]);
  const [query, setQuery] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);

  const [status, setStatus] = useState("");
  const [kind, setKind] = useState("");
  const [severity, setSeverity] = useState("");
  const [project, setProject] = useState("");
  const [tag, setTag] = useState("");
  const [upstream, setUpstream] = useState("");
  const [evidence, setEvidence] = useState("");

  // The read is the list or the search, decided by the box - same door, same
  // debounce, same "last answer to arrive is not necessarily the last one
  // asked for" guard as Reports.tsx. See that file for why `stopped` exists.
  useEffect(() => {
    if (!token) {
      setFindings([]);
      setLoaded(false);
      return;
    }
    let stopped = false;
    const asked = query.trim();
    const timer = setTimeout(
      () => {
        const read = asked ? api.searchFindings(asked) : api.findings();
        read
          .then((page) => {
            if (stopped) return;
            setFindings(page.artifacts);
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

  const statusOptions = optionsIn(findings, (f) => f.status);
  const kindOptions = optionsIn(findings, (f) => f.kind);
  const severityOptions = optionsIn(findings, (f) => f.severity);
  const projectOptions = optionsIn(findings, (f) => f.project ?? undefined);
  const tagOptions = tagsIn(findings);

  const filters = { status, kind, severity, project, tag, upstream, evidence };
  const shown = narrow(findings, filters);
  const filtered = Object.values(filters).some((value) => value !== "");
  const counts = countAxes(findings);
  const groups = groupByUpstream(shown);
  // WHAT IS TICKED, and it is page state rather than anything the node holds -
  // a selection is a sentence about what somebody is about to do, not a fact
  // about the corpus.
  //
  // Kept as ids rather than as a filtered list for the reason flow 5 on the row
  // names: a selection that quietly changed when a filter did would be one
  // nobody could trust the count of. The ids survive the filter; the header
  // says how many of them are off-screen.
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const [bulk, setBulk] = useState<string | null>(null);
  const [bulkBusy, setBulkBusy] = useState(false);

  const toggle = useCallback((id: string) => {
    setSelected((was) => {
      const next = new Set(was);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  // The runnable half of the selection, and the part of it that is off-screen.
  // Both are counted from the SAME ids the header shows, so the numbers on the
  // control and the number beside it cannot disagree.
  const chosen = useMemo(() => findings.filter((f) => selected.has(f.id)), [findings, selected]);
  const runnableChosen = useMemo(() => chosen.filter((f) => hasRepro(reproOf(f))), [chosen]);
  const offscreen = chosen.length - chosen.filter((f) => shown.includes(f)).length;

  // RUN WHAT WAS ASKED FOR AND SAY WHAT HAPPENED. One POST per finding rather
  // than a bulk door: the runner queues per finding, and a door that took a
  // list would have to invent an answer for "three queued, one refused" that
  // this loop can just report.
  const runSelection = async () => {
    if (bulkBusy || runnableChosen.length === 0) return;
    // A dind run is a container, a database and a compile. Twelve of them by
    // mis-click is somebody's whole evening, so past ten it asks first.
    if (runnableChosen.length > 10 && !window.confirm(`start ${runnableChosen.length} runs?`)) {
      return;
    }
    setBulkBusy(true);
    setBulk(null);
    let queued = 0;
    const refused: string[] = [];
    for (const finding of runnableChosen) {
      try {
        await api.reproRun(finding.id, "latest");
        queued += 1;
      } catch (err) {
        refused.push(`${finding.title || finding.id}: ${(err as Error).message}`);
      }
    }
    setBulkBusy(false);
    setBulk(
      refused.length === 0
        ? `queued ${queued} run${queued === 1 ? "" : "s"}`
        : `queued ${queued}, refused ${refused.length} - ${refused[0]}`,
    );
  };
  return (
    // Same full-height-flex-column shape as Reports.tsx and Todos.tsx: the
    // list is what scrolls, the header and filter row stay put.
    <div className="flex h-full flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-lg font-semibold">findings</h2>
        <Input
          className="max-w-xs"
          aria-label="search findings"
          placeholder="search the findings"
          value={query}
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => setQuery(event.target.value)}
        />
        <span className="text-muted-foreground text-xs">
          {filtered ? `${shown.length} of ${findings.length}` : findings.length} finding
          {findings.length === 1 && !filtered ? "" : "s"}
          {query.trim() ? ` matching ${JSON.stringify(query.trim())}` : ""}
          {filtered ? (
            <button
              type="button"
              className="ml-1 underline hover:text-foreground"
              onClick={() => {
                setStatus("");
                setKind("");
                setSeverity("");
                setProject("");
                setTag("");
                setUpstream("");
                setEvidence("");
              }}
            >
              clear filters
            </button>
          ) : null}
        </span>
      </div>

      {/* The counts, in the header, so the page reports its own state without
          being filtered first - the room-pane tab rule applied to the surface
          this list actually has. The three numbers are the three questions
          asked of this corpus every day: how much is written up and nobody sent
          it, how much has been filed, and how much of it can actually be
          re-run. */}
      {findings.length > 0 ? (
        <div
          aria-label="findings counts"
          data-unfiled={counts.unfiled}
          data-referenced={counts.referenced}
          data-filed={counts.filed}
          data-repro={counts.repro}
          className="flex flex-wrap items-center gap-2 text-muted-foreground text-xs"
        >
          {/* Each number is drawn in the tone of the rows it counts, so the
              header and the list speak ONE language: the colour beside "3
              unfiled" is the colour those three rows are wearing further down.
              A header in its own palette would be a second vocabulary for the
              same three facts. The numerals are mono and tabular so the counts
              line up as they change under a filter. */}
          <StateChip
            axis="upstream"
            state="unfiled"
            tone={upstreamTone("unfiled")}
            title="written up, and nobody has sent them anywhere"
          >
            <span className="font-mono tabular-nums">{counts.unfiled}</span>
            <span className="ml-1">unfiled</span>
          </StateChip>
          <StateChip
            axis="upstream"
            state="referenced"
            tone={upstreamTone("referenced")}
            title="these name something over there and nobody claims to have sent them"
          >
            <span className="font-mono tabular-nums">{counts.referenced}</span>
            <span className="ml-1">referenced</span>
          </StateChip>
          <StateChip
            axis="upstream"
            state="filed"
            tone={upstreamTone("filed")}
            title="sent to somebody else's tracker, and the filing still stands"
          >
            <span className="font-mono tabular-nums">{counts.filed}</span>
            <span className="ml-1">filed upstream</span>
          </StateChip>
          <StateChip
            axis="repro"
            state="yes"
            tone={reproTone(true)}
            title="these ship something that can actually be re-run"
          >
            <span className="font-mono tabular-nums">{counts.repro}</span>
            <span className="ml-1">with a repro tree</span>
          </StateChip>
          {/* The shape of the corpus, which no amount of per-row colour gives:
              forty dots down a page do not add up to a proportion by eye. It
              measures what is ON the page, so it narrows with the filters and
              answers "what did that filter actually select" as well. */}
          <SeverityBar items={shown} label="severity of the findings shown" />
        </div>
      ) : null}

      {/* THE SELECTION BAR, and it only exists when something is selected: an
          always-present "0 selected" is a line of chrome that says nothing on
          the ordinary visit. Twenty-seven findings carry repro trees now, which
          is what makes this a batch rather than a list. */}
      {selected.size > 0 ? (
        <div
          className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-card px-2 py-1.5 text-xs"
          data-finding-selection=""
        >
          <span data-finding-selected-count={selected.size}>
            <span className="font-mono tabular-nums">{selected.size}</span> selected
          </span>
          {offscreen > 0 ? (
            <span className="text-muted-foreground" data-finding-selected-offscreen={offscreen}>
              {offscreen} of them not shown by this filter
            </span>
          ) : null}
          <Button
            type="button"
            size="sm"
            variant="secondary"
            data-finding-run-selected=""
            disabled={bulkBusy || runnableChosen.length === 0}
            onClick={() => {
              void runSelection();
            }}
          >
            {bulkBusy
              ? "starting..."
              : runnableChosen.length === chosen.length
                ? `run ${runnableChosen.length}`
                : `run ${runnableChosen.length} of ${chosen.length}`}
          </Button>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            data-finding-select-none=""
            onClick={() => setSelected(new Set())}
          >
            clear
          </Button>
          {bulk ? (
            <span className="text-muted-foreground" data-finding-bulk-result="">
              {bulk}
            </span>
          ) : null}
        </div>
      ) : null}
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <FilterSelect label="status" value={status} onChange={setStatus} options={statusOptions} />
        <FilterSelect
          label="upstream"
          value={upstream}
          onChange={setUpstream}
          options={UPSTREAM_STATES}
        />
        <FilterSelect
          label="evidence"
          value={evidence}
          onChange={setEvidence}
          options={[...EVIDENCE_STATES, UNSTATED]}
        />
        <FilterSelect label="kind" value={kind} onChange={setKind} options={kindOptions} />
        <FilterSelect
          label="severity"
          value={severity}
          onChange={setSeverity}
          options={severityOptions}
        />
        <FilterSelect
          label="project"
          value={project}
          onChange={setProject}
          options={projectOptions}
        />
        <FilterSelect label="tag" value={tag} onChange={setTag} options={tagOptions} />
      </div>

      {error ? <div className="text-destructive text-sm">{error}</div> : null}

      <ol aria-label="findings" className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto">
        {shown.length === 0 ? (
          <li className="text-muted-foreground text-sm">
            {emptyReads({
              token: Boolean(token),
              loaded,
              failed: Boolean(error),
              query: query.trim(),
              filtered,
            })}
          </li>
        ) : null}
        {/* Grouped by whose code it is about, with the heading sticky so the
            corpus you are reading stays named while you scroll through forty of
            them. Grouping only happens when there is more than one corpus on the
            page: a single sticky heading over the whole list is a label, not a
            grouping, and it would push the first row down for nothing. */}
        {groups.length > 1
          ? groups.map((group) => (
              <li key={group.name} data-finding-group={group.name}>
                <div className="sticky top-0 z-10 flex flex-wrap items-center gap-2 bg-background py-1 font-semibold text-muted-foreground text-xs uppercase tracking-wide">
                  <span>{group.name}</span>
                  <span
                    className="font-mono tabular-nums"
                    data-finding-group-count={group.items.length}
                  >
                    {group.items.length}
                  </span>
                  {/* Each corpus gets its own shape, which is the question the
                      grouping is really for: is ragflow mostly high and serenedb
                      mostly low, or the other way round. */}
                  <SeverityBar items={group.items} label={`severity across ${group.name}`} />
                </div>
                <ol className="flex flex-col gap-3 pt-2">
                  {group.items.map((f) => (
                    <FindingCard
                      key={f.id}
                      finding={f}
                      selected={selected.has(f.id)}
                      onToggle={toggle}
                    />
                  ))}
                </ol>
              </li>
            ))
          : shown.map((f) => (
              <FindingCard key={f.id} finding={f} selected={selected.has(f.id)} onToggle={toggle} />
            ))}
      </ol>
    </div>
  );
}

/** UNSTATED is the evidence filter's extra option: rows where nobody has said
 * how strong the evidence is. It is offered because "which of these has nobody
 * assessed" is a question about the corpus, and a filter that could only select
 * the three stated words could not ask it. */
const UNSTATED = "not stated";

/** optionsIn collects the distinct, non-empty values `pick` reads off `list`,
 * sorted - the option set for a single-valued filter (status/kind/severity/
 * project), built from what is actually on the page rather than a vocabulary
 * kept over here. */
function optionsIn(list: Artifact[], pick: (a: Artifact) => string | undefined): string[] {
  const seen = new Set<string>();
  for (const item of list) {
    const value = pick(item);
    if (value) seen.add(value);
  }
  return [...seen].sort((a, b) => a.localeCompare(b));
}

/** tagsIn is optionsIn for the multi-valued column - every tag on every row,
 * not just the first. */
function tagsIn(list: Artifact[]): string[] {
  const seen = new Set<string>();
  for (const item of list) {
    for (const tag of item.tags ?? []) seen.add(tag);
  }
  return [...seen].sort((a, b) => a.localeCompare(b));
}

/**
 * WHOSE CODE IS THIS ABOUT - the grouping the operator asked for twice: "I
 * still dont see serenedb and ragflow findings".
 *
 * A finding names its upstream project in two places and neither is a column.
 * upstream_tracker is the authoritative one when a filing exists, and the rest
 * of the corpus carries it as a TAG - the importers put `serenedb` and
 * `ragflow` on the rows they wrote. So the vocabulary is derived from the
 * trackers actually present, and then a row is matched to one of THOSE names
 * rather than to any word that happens to be a tag: without that, "flaky" and
 * "parser" would each become a corpus of their own and the page would group by
 * noise.
 *
 * DONE OVER THE ROWS ALREADY FETCHED, on the same terms as the severity and tag
 * filters above. This is GROUPING, not filtering: the page shows every finding
 * the read returned and sorts them under headings, so it needs all of them in
 * hand at once and has nothing to ask the node for.
 *
 * The node CAN narrow by tag now - `?tag=` was ignored and answered with the
 * whole corpus until 2b0fe67 fixed it, and an ignored filter is the worst kind
 * because it answers 200 with more than was asked for and no client can tell.
 * That is why the grouping was built to key off the ROW rather than off the
 * request, and why it needed no change when the door was fixed: a heading here
 * is a fact about the finding under it, not a claim about what was queried.
 */
function trackersIn(list: Artifact[]): Set<string> {
  const seen = new Set<string>();
  for (const item of list) {
    const filing = upstreamOf(item);
    if (filing.tracker) seen.add(filing.tracker.toLowerCase());
    for (const ref of filing.refs) {
      if (ref.tracker) seen.add(ref.tracker.toLowerCase());
    }
  }
  return seen;
}

/** Which upstream project one finding belongs to. Its own tracker first,
 * because that is a filing somebody made; then a tag that names one of the
 * trackers this corpus knows about. A row matching neither is not forced into
 * somebody's project - it goes under its own heading, which is a fact about the
 * import rather than a gap to hide. */
const NO_UPSTREAM_PROJECT = "no upstream project";

function upstreamProjectOf(finding: Artifact, trackers: Set<string>): string {
  const filing = upstreamOf(finding);
  if (filing.tracker) return filing.tracker.toLowerCase();
  for (const tag of finding.tags ?? []) {
    const name = tag.trim().toLowerCase();
    if (trackers.has(name)) return name;
  }
  // THE ROW'S OWN PROJECT, which is where this answer actually lives now. The
  // corpus was re-filed into real projects, so a finding about their code says
  // project=ragflow or project=serenedb and carries NO upstream_tracker at all -
  // a filing is a thing somebody did later, and most of the corpus is unfiled.
  // Without this the two clauses above find nothing on live data, every row
  // falls to the last line, and the grouping silently collapses to one heap,
  // which is the exact complaint it was built to answer.
  if (finding.project) return finding.project.trim().toLowerCase();
  return NO_UPSTREAM_PROJECT;
}

/** The list broken into corpora, biggest first so the page opens on the one
 * with the most in it, and the unattributed rows last because they are the
 * remainder rather than a project. */
function groupByUpstream(list: Artifact[]): { name: string; items: Artifact[] }[] {
  const trackers = trackersIn(list);
  const groups = new Map<string, Artifact[]>();
  for (const finding of list) {
    const name = upstreamProjectOf(finding, trackers);
    const group = groups.get(name);
    if (group) group.push(finding);
    else groups.set(name, [finding]);
  }
  return [...groups]
    .map(([name, items]) => ({ name, items }))
    .sort((a, b) => {
      if (a.name === NO_UPSTREAM_PROJECT) return 1;
      if (b.name === NO_UPSTREAM_PROJECT) return -1;
      return b.items.length - a.items.length || a.name.localeCompare(b.name);
    });
}

/**
 * countAxes is the header's numbers.
 *
 * unfiled and filed are NOT complements, and the gap between them is the point.
 * rejected and withdrawn are filings that happened and no longer stand, so they
 * are in neither. REFERENCED IS IN NEITHER EITHER: it means the finding names
 * something over there and nobody claims to have sent it, which is exactly the
 * state that gets miscounted as filed - seven of sixteen RAGFlow findings are
 * that, and counting them as sent is how one filing was reported as eight. So
 * it gets a count of its own and each number says only what it says.
 */
function countAxes(list: Artifact[]) {
  let unfiled = 0;
  let referenced = 0;
  let filed = 0;
  let repro = 0;
  for (const finding of list) {
    const filing = upstreamOf(finding);
    if (filing.state === "unfiled") unfiled += 1;
    if (filing.state === "referenced") referenced += 1;
    if (filedUpstream(filing)) filed += 1;
    if (hasRepro(reproOf(finding))) repro += 1;
  }
  return { unfiled, referenced, filed, repro };
}

function narrow(
  list: Artifact[],
  filters: {
    status: string;
    kind: string;
    severity: string;
    project: string;
    tag: string;
    upstream: string;
    evidence: string;
  },
): Artifact[] {
  return list.filter((f) => {
    if (filters.status && f.status !== filters.status) return false;
    if (filters.kind && f.kind !== filters.kind) return false;
    if (filters.severity && f.severity !== filters.severity) return false;
    if (filters.project && (f.project ?? "") !== filters.project) return false;
    if (filters.tag && !(f.tags ?? []).includes(filters.tag)) return false;
    if (filters.upstream && upstreamOf(f).state !== filters.upstream) return false;
    if (filters.evidence) {
      const state = evidenceOf(f).state;
      if (filters.evidence === UNSTATED ? state !== undefined : state !== filters.evidence) {
        return false;
      }
    }
    return true;
  });
}

function FilterSelect({
  label,
  value,
  onChange,
  options,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  options: string[];
}) {
  // htmlFor/id rather than wrapping the Select in the label: biome's a11y
  // check only sees through a native <select>, not a component wrapping one,
  // so a wrapped label here reads to it as unassociated.
  const id = `finding-filter-${label}`;
  return (
    <div className="flex items-center gap-1 text-muted-foreground">
      <label htmlFor={id}>{label}</label>
      <Select
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className="h-7 text-xs"
      >
        <option value="">any</option>
        {options.map((name) => (
          <option key={name} value={name}>
            {name}
          </option>
        ))}
      </Select>
    </div>
  );
}

/**
 * What an empty list says, which is never nothing - see Reports.tsx's
 * emptyReads for the fuller argument. `filtered` is the fifth case a findings
 * list has that a reports list does not: a filter that matched nothing is not
 * "no findings" and not "signed out", and saying so is what tells the reader
 * to widen the filter rather than go looking for a bug in the read.
 */
function emptyReads({
  token,
  loaded,
  failed,
  query,
  filtered,
}: {
  token: boolean;
  loaded: boolean;
  failed: boolean;
  query: string;
  filtered: boolean;
}) {
  if (!token) {
    return "paste a token to see the findings - signed out, this is not an empty shelf, it is a locked one";
  }
  if (failed) {
    return "the findings could not be read, so this page is not saying there are none";
  }
  if (!loaded) {
    return "reading the findings…";
  }
  if (filtered) {
    return "nothing matches this filter - widen it or clear it";
  }
  if (query) {
    return `nothing you can read matches ${JSON.stringify(query)} - clear the box for all of them`;
  }
  // Not "no findings": this node's reach is what was read, and a corpus that
  // has not been imported yet reads exactly the same as one that has nothing
  // in it. Saying which two things this could be is the difference between a
  // reader waiting for an import and a reader filing a bug about this page.
  return "no findings you can read - nothing has been raised here, and no corpus has been imported into your reach yet";
}

/**
 * One finding: what it is, how bad, and where it stands ON EVERY AXIS.
 *
 * The badges are ordered ours-then-theirs-then-evidence rather than grouped by
 * colour, because that is the order the questions get asked: how far did we get,
 * did anybody send it, and how much do we actually know. Each axis is also on
 * the row as a data attribute, so a check can assert them separately - two
 * badges reading "done" and "fixed" are one string apart on screen and a page
 * that drew our status twice would look right in a screenshot.
 */
function FindingCard({
  finding,
  selected,
  onToggle,
}: {
  finding: Artifact;
  selected: boolean;
  onToggle: (id: string) => void;
}) {
  const project = finding.project ?? "_";
  const filing = upstreamOf(finding);
  const evidence = evidenceOf(finding);
  const tree = reproOf(finding);
  const runnable = hasRepro(tree);

  // The issue number is what a reader can act on, so it rides in the badge and
  // not in a tooltip. The link is drawn only when the row carries a url: a URL
  // built here out of a tracker name and a number would be a guess, and a guess
  // that 404s is worse than a number somebody can search for themselves.
  // "filed pr #16958" reads differently from "filed #16958", and the difference
  // is whether we reported a defect or sent them a fix. A finding with no
  // filing of its own but citations over there says how many it cites, because
  // referenced-with-nothing-named is not a state anybody can act on.
  const upstreamLabel = filing.id
    ? `${filing.state} ${filing.kind === "pr" ? "pr " : ""}#${filing.id}`
    : filing.refs.length > 0
      ? `${filing.state} · ${filing.refs.length} ref${filing.refs.length === 1 ? "" : "s"}`
      : filing.state;

  return (
    <li
      data-finding={finding.id}
      data-lifecycle={finding.status}
      data-upstream={filing.state}
      data-upstream-id={filing.id ?? ""}
      data-evidence={evidence.state ?? ""}
      data-repro={runnable ? "yes" : "no"}
    >
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            {/* THE TICK COMES FIRST, before the severity dot, because it is the
                one control on the card and a control a reader has to hunt for
                is one they use once. Its label carries the title so that a
                screen reader announcing it says which finding is being
                selected, and so a browser check can tick a NAMED row rather
                than the third box down. */}
            <input
              type="checkbox"
              data-finding-select={finding.id}
              aria-label={`select ${finding.title || finding.id}`}
              checked={selected}
              onChange={() => onToggle(finding.id)}
              className="h-3.5 w-3.5 shrink-0 accent-foreground"
            />
            <SeverityDot severity={finding.severity} />
            <Link className="hover:underline" to={`/p/${project}/finding/${finding.id}`}>
              {finding.title || finding.id}
            </Link>
          </CardTitle>
          <div className="flex flex-wrap gap-1 pt-1">
            {/* Which corpus this came out of. It is the project column, and it
                is first because a finding read without knowing whose code it is
                about is not readable at all. */}
            <Badge variant="secondary">{finding.project ?? "personal"}</Badge>
            {finding.status ? <Badge variant="outline">ours: {finding.status}</Badge> : null}
            <UpstreamBadge
              state={filing.state}
              label={upstreamLabel}
              url={filing.url}
              tracker={filing.tracker}
            />
            <EvidenceBadge
              state={evidence.state}
              verifiedOn={evidence.verified_on}
              runnable={runnable}
              isolation={tree.isolation}
            />
            {finding.kind ? <Badge variant="outline">{finding.kind}</Badge> : null}
            {/* The severity as a word too, in the same tone as its dot. The dot
                is what the eye finds; the word is what a reader can quote, and
                anybody who cannot separate rust from amber reads this one. */}
            {finding.severity ? (
              <StateChip
                axis="severity"
                state={finding.severity}
                tone={severityTone(finding.severity)}
                title="how bad this finding is"
              >
                {finding.severity}
              </StateChip>
            ) : null}
            {(finding.tags ?? []).map((tag) => (
              <Badge key={tag} variant="outline">
                {tag}
              </Badge>
            ))}
          </div>
        </CardHeader>
        <CardContent className="text-muted-foreground text-xs">
          updated {finding.updated} · {finding.visibility}
          {/* When the repro last ran, WHEN THE ROW SAYS SO. A run's verdict is
              an append-only event on the finding (internal/store/findingruns.go)
              and a list read carries no events, so this is what the evidence
              claim on the row states about its own last run - and nothing at
              all when it states none. A list that fetched the run log per row
              would be forty reads to draw one page, and one that printed
              "never run" from their absence would be saying something it did
              not check. */}
          {evidence.verified_at ? ` · last run ${evidence.verified_at}` : ""}
        </CardContent>
      </Card>
    </li>
  );
}

/** The filing badge. A state outside the vocabulary is shown as it was written,
 * with a title saying so - see UNKNOWN_UPSTREAM. Turning it into "unfiled"
 * would be this page miscounting somebody else's tracker quietly. */
function UpstreamBadge({
  state,
  label,
  url,
  tracker,
}: {
  state: string;
  label: string;
  url?: string;
  tracker?: string;
}) {
  const known = knownUpstream(state);
  const title = known
    ? tracker
      ? `filed with ${tracker}`
      : "where this stands on their tracker"
    : UNKNOWN_UPSTREAM;
  // Tinted, not outlined, and the tint is the state's own. Every one of these
  // was `variant="outline"` before, which drew filed, unfiled and referenced in
  // ONE grey - three facts the whole page is built to keep apart, rendered
  // identically. An unknown word draws the warn pair rather than the quiet one:
  // see upstreamTone.
  const body = (
    <StateChip axis="upstream" state={state} tone={upstreamTone(state)} title={title}>
      upstream: {label}
      {known ? null : " (?)"}
    </StateChip>
  );
  if (!url) return body;
  return (
    <a href={url} target="_blank" rel="noreferrer" className="hover:underline">
      {body}
    </a>
  );
}

/**
 * The evidence badge: how strong the claim is, and on what.
 *
 * A row that states nothing gets a badge saying so rather than no badge at all.
 * Absent evidence is the most common state in the corpus and the one that
 * decides whether a finding may be filed, and a missing badge reads as "fine" -
 * which is exactly the wrong reading.
 *
 * Whether a repro tree EXISTS is a separate fact from what running it proved,
 * and both are here: a finding can carry a script nobody has run, and one can
 * be marked reproduced from a run whose tree was never attached.
 */
function EvidenceBadge({
  state,
  verifiedOn,
  runnable,
  isolation,
}: {
  state?: string;
  verifiedOn?: string;
  runnable: boolean;
  isolation?: string;
}) {
  // BOTH OUTCOMES OF A RUN NAME THE COMMIT THEY WERE MEASURED ON, because
  // "refuted" with nothing saying WHERE is how a real defect gets closed - see
  // EvidenceState in lib/findings.ts. verified and refuted are the two ends of
  // this axis rather than two steps along it, and each carries its sha.
  const evidence =
    (state === "verified" || state === "refuted") && verifiedOn
      ? `${state} on ${verifiedOn.slice(0, 12)}`
      : (state ?? "evidence not stated");
  // "not stated" is drawn in the WARN pair rather than the quiet one, which is
  // the single most load-bearing colour choice on this page. Unstated evidence
  // is what stops a finding being filed, it is the commonest state in the
  // corpus, and in grey it reads as "fine, nothing to see" - which is the exact
  // opposite of what it means. See evidenceTone.
  return (
    <>
      <StateChip
        axis="evidence"
        state={state ?? "not stated"}
        tone={evidenceTone(state)}
        title={
          state
            ? "how strong the evidence is, and what it was run against"
            : "nobody has said how strong the evidence for this finding is, so this cannot be filed yet"
        }
      >
        {evidence}
      </StateChip>
      <StateChip
        axis="repro"
        state={runnable ? "yes" : "no"}
        tone={reproTone(runnable)}
        title="whether this finding ships something that can be run"
      >
        {runnable ? `repro: ${isolation || "plain"}` : "no repro tree"}
      </StateChip>
    </>
  );
}
