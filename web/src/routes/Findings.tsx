import { useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { type Artifact, api } from "@/lib/api";
import { useSession } from "@/lib/session";

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
 * The five filters this page adds on top are NOT five more query params.
 * status/kind/project are columns ArtifactQuery already narrows a read by,
 * but severity and tag are not - the store has no such column to ask with -
 * so all five are applied here, over the artifacts the permission-filtered
 * read already returned, the same way Todos.tsx narrows its tag filter over
 * an already-fetched queue rather than asking the node for a tag it cannot
 * answer. Their option lists are drawn from that same fetched set for the
 * same reason Todos.tsx draws its tag list that way: a hardcoded vocabulary
 * here would be a second copy of severity and tag, which have none to copy -
 * these are free labels, and this page's list is whatever is actually
 * written on the rows in front of it.
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

  const shown = narrow(findings, { status, kind, severity, project, tag });
  const filtered = status !== "" || kind !== "" || severity !== "" || project !== "" || tag !== "";

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
              }}
            >
              clear filters
            </button>
          ) : null}
        </span>
      </div>

      <div className="flex flex-wrap items-center gap-2 text-xs">
        <FilterSelect label="status" value={status} onChange={setStatus} options={statusOptions} />
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
        {shown.map((f) => (
          <FindingCard key={f.id} finding={f} />
        ))}
      </ol>
    </div>
  );
}

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

function narrow(
  list: Artifact[],
  filters: { status: string; kind: string; severity: string; project: string; tag: string },
): Artifact[] {
  return list.filter((f) => {
    if (filters.status && f.status !== filters.status) return false;
    if (filters.kind && f.kind !== filters.kind) return false;
    if (filters.severity && f.severity !== filters.severity) return false;
    if (filters.project && (f.project ?? "") !== filters.project) return false;
    if (filters.tag && !(f.tags ?? []).includes(filters.tag)) return false;
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
  return "no findings yet";
}

/** One finding: what it is, how bad, and where it stands. */
function FindingCard({ finding }: { finding: Artifact }) {
  const project = finding.project ?? "_";
  return (
    <li data-finding={finding.id}>
      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            <Link className="hover:underline" to={`/p/${project}/finding/${finding.id}`}>
              {finding.title || finding.id}
            </Link>
          </CardTitle>
          <div className="flex flex-wrap gap-1 pt-1">
            <Badge variant="secondary">finding</Badge>
            {finding.kind ? <Badge variant="outline">{finding.kind}</Badge> : null}
            {finding.severity ? <Badge variant="outline">{finding.severity}</Badge> : null}
            {finding.status ? <Badge variant="outline">{finding.status}</Badge> : null}
            {(finding.tags ?? []).map((tag) => (
              <Badge key={tag} variant="outline">
                {tag}
              </Badge>
            ))}
          </div>
        </CardHeader>
        <CardContent className="text-muted-foreground text-xs">
          updated {finding.updated} · {finding.visibility}
        </CardContent>
      </Card>
    </li>
  );
}
