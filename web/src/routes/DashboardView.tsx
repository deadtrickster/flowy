import { useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";

import { ApiError, type Artifact } from "@/lib/api";
import { type DashboardTile, dashboards, tilesOf } from "@/lib/dashboards";
import { useSignedIn } from "@/lib/session";

/**
 * A dashboard, rendered from its declaration and the rows producers pushed.
 *
 * The page holds no state of its own: the tiles come from the row, the
 * numbers from the metric rows, and both are fetched on every load - so what
 * changed since the last load is what the page shows. Every number carries
 * its age, computed from the row it reads, and a datum older than its tile's
 * threshold is styled stale rather than silently live: the operator reading
 * prose somebody typed is exactly the failure this exists to fix.
 *
 * The age recomputes on a second tick, not only on load, because a page left
 * open is where a stale reading does its damage - a number that looked fresh
 * at open and has gone quietly stale is a lie with a timestamp.
 */

/** One reading drawn as words: 12s, 4m, 6h, 2d. */
function ageWords(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
  return `${Math.floor(seconds / 86400)}d`;
}

/** The reading of one metric series, off the row it came from. */
function ageSeconds(row: Artifact, now: number): number {
  const at = Date.parse(row.created);
  if (!Number.isFinite(at)) return 0;
  return Math.max(0, Math.floor((now - at) / 1000));
}

/** The value a tile draws, in the words a person reads. */
function readingOf(row: Artifact): string {
  const fields = row.fields as { value?: unknown } | undefined;
  const value = fields?.value;
  if (typeof value === "string") return value;
  if (value === null || value === undefined) return "";
  return String(value);
}

/** One number tile, drawn from the newest row of its series. */
function NumberTile({
  tile,
  row,
  now,
}: {
  tile: DashboardTile;
  row: Artifact | undefined;
  now: number;
}) {
  const age = row ? ageSeconds(row, now) : 0;
  const stale = row && (tile.stale_after_seconds ?? 0) > 0 && age > (tile.stale_after_seconds ?? 0);
  return (
    <div
      data-tile-label={tile.label}
      data-tile-kind="number"
      data-empty={row ? undefined : "true"}
      data-stale={stale ? "true" : undefined}
      data-value={row ? readingOf(row) : undefined}
      data-age={row ? age : undefined}
      className="flex flex-col justify-between rounded-md border border-border p-4"
    >
      <div className="text-muted-foreground text-xs">{tile.label}</div>
      {row ? (
        <>
          <div className="py-1 font-semibold text-2xl tabular-nums">{readingOf(row)}</div>
          <div className="text-muted-foreground text-xs" data-tile-age>
            {ageWords(age)}
            {stale ? ", stale" : ""}
          </div>
        </>
      ) : (
        <div className="py-1 text-muted-foreground text-sm">
          its metric has no rows pushed yet - not zero, just nothing
        </div>
      )}
    </div>
  );
}

/** The grid a grid tile draws: cols as headers, rows as model plus cells.
 * Any reading that is not this shape is null, and the tile says so - a grid
 * drawn from a wrong-shaped reading is a matrix that lies with confidence. */
function gridOf(
  row: Artifact,
): { cols: string[]; rows: { model: string; cells: (string | number)[] }[] } | null {
  const fields = row.fields as { value?: unknown } | undefined;
  const value = fields?.value;
  if (typeof value !== "object" || value === null) return null;
  const v = value as { cols?: unknown; rows?: unknown };
  if (!Array.isArray(v.cols) || !Array.isArray(v.rows)) return null;
  const cols = v.cols.map(String);
  const rows = v.rows.map((r) => {
    const o = r as { model?: unknown; cells?: unknown } | null;
    return {
      model: o?.model === undefined || o?.model === null ? "" : String(o.model),
      cells: Array.isArray(o?.cells) ? o.cells.map(String) : [],
    };
  });
  return { cols, rows };
}

/** One grid tile: the newest reading of its series, drawn as its matrix. It
 * carries its age and staleness exactly like a number tile - a coverage grid
 * frozen at its last pass looks like a sweep that covered everything, which
 * is the most confident-looking wrong answer available on the page. */
function GridTile({
  tile,
  row,
  now,
}: { tile: DashboardTile; row: Artifact | undefined; now: number }) {
  const age = row ? ageSeconds(row, now) : 0;
  const stale = row && (tile.stale_after_seconds ?? 0) > 0 && age > (tile.stale_after_seconds ?? 0);
  const grid = row ? gridOf(row) : null;
  return (
    <div
      data-tile-label={tile.label}
      data-tile-kind="grid"
      data-empty={row ? undefined : "true"}
      data-grid-bad={row && !grid ? "true" : undefined}
      data-stale={stale ? "true" : undefined}
      data-age={row ? age : undefined}
      className="flex flex-col gap-1 rounded-md border border-border p-4 sm:col-span-2 lg:col-span-4"
    >
      <div className="text-muted-foreground text-xs">{tile.label}</div>
      {row && !grid ? (
        <div className="py-1 text-muted-foreground text-sm">
          its newest reading is not a grid of {"{cols, rows}"} - the tile says so rather than
          drawing a wrong-shaped matrix
        </div>
      ) : !row || !grid ? (
        <div className="py-1 text-muted-foreground text-sm">
          its metric has no rows pushed yet - not zero, just nothing
        </div>
      ) : (
        <>
          <table className="mt-1 border-collapse text-sm">
            <thead>
              <tr>
                <th className="border-border border-b px-2 py-1 text-muted-foreground text-xs font-medium" />
                {grid.cols.map((col, i) => (
                  <th
                    // biome-ignore lint/suspicious/noArrayIndexKey: a grid's columns are positional - names can repeat, and the pushed value carries no ids, so the index is the identity
                    key={i}
                    data-grid-col={i}
                    className="border-border border-b px-2 py-1 text-muted-foreground text-xs font-medium"
                  >
                    {col}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {grid.rows.map((r, i) => (
                <tr
                  // biome-ignore lint/suspicious/noArrayIndexKey: a grid's rows are positional - model names can repeat, and the pushed value carries no ids, so the index is the identity
                  key={i}
                  data-grid-model={r.model}
                >
                  <th
                    className="px-2 py-1 text-muted-foreground text-xs font-medium"
                    data-grid-model-cell
                  >
                    {r.model}
                  </th>
                  {r.cells.map((cell, j) => (
                    <td
                      // biome-ignore lint/suspicious/noArrayIndexKey: a row's cells are positional - the pushed value carries no ids, so the index is the identity
                      key={j}
                      data-grid-cell={i * grid.cols.length + j}
                      data-grid-value={cell}
                      className="px-2 py-1 tabular-nums"
                    >
                      {cell}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
          <div className="text-muted-foreground text-xs" data-tile-age>
            {ageWords(age)}
            {stale ? ", stale" : ""}
          </div>
        </>
      )}
    </div>
  );
}

/** One table tile: the series' rows, newest first, each with its age. */
function TableTile({ tile, rows, now }: { tile: DashboardTile; rows: Artifact[]; now: number }) {
  return (
    <div
      data-tile-label={tile.label}
      data-tile-kind="table"
      data-empty={rows.length === 0 ? "true" : undefined}
      className="flex flex-col gap-1 rounded-md border border-border p-4"
    >
      <div className="text-muted-foreground text-xs">{tile.label}</div>
      {rows.length === 0 ? (
        <div className="py-1 text-muted-foreground text-sm">
          its metric has no rows pushed yet - not zero, just nothing
        </div>
      ) : (
        <ul className="mt-1 flex flex-col">
          {rows.map((row) => (
            <li
              key={row.id}
              data-metric-row={row.id}
              data-value={readingOf(row)}
              className="flex items-baseline justify-between gap-3 border-border border-b py-1 last:border-b-0"
            >
              <span className="font-medium tabular-nums">{readingOf(row)}</span>
              <span className="text-muted-foreground text-xs">
                {ageWords(ageSeconds(row, now))}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

export function DashboardView() {
  const { id } = useParams();
  const signedIn = useSignedIn();
  const [dash, setDash] = useState<Artifact | null>(null);
  const [rows, setRows] = useState<Artifact[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [refused, setRefused] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());

  // The age recomputes on a second tick - see the file head.
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);

  useEffect(() => {
    if (!signedIn || !id) {
      setDash(null);
      setRows([]);
      setLoaded(false);
      return;
    }
    let stopped = false;
    setLoaded(false);
    setRefused(false);
    setError(null);
    dashboards
      .read(id)
      .then(async (artifact) => {
        if (stopped) return;
        setDash(artifact);
        const names = [
          ...new Set(
            tilesOf(artifact)
              .map((t) => t.metric)
              .filter(Boolean),
          ),
        ];
        if (names.length === 0) return;
        try {
          const page = await dashboards.metrics(names);
          if (!stopped) setRows(page.metrics ?? []);
        } catch (err) {
          // The tiles render their empty state; the declaration is still the
          // truth of the page even if the read of the data failed.
          if (!stopped) setError(err instanceof Error ? err.message : String(err));
        }
      })
      .catch((err: unknown) => {
        if (stopped) return;
        // Out of scope and missing are told apart here: the node answers a
        // 404 for both, but the reader's page must say refused rather than
        // draw an empty dashboard - an empty page reads as "there is
        // nothing to read", which is not what happened.
        if (err instanceof ApiError && err.status === 404) setRefused(true);
        else setError(err instanceof Error ? err.message : String(err));
      })
      .finally(() => {
        if (!stopped) setLoaded(true);
      });
    return () => {
      stopped = true;
    };
  }, [signedIn, id]);

  const tiles = useMemo(() => (dash ? tilesOf(dash) : []), [dash]);

  /** The rows of one series, newest first - the door's order is the page's. */
  const rowsOf = (name: string): Artifact[] =>
    rows.filter((row) => {
      const fields = row.fields as { name?: unknown } | undefined;
      return fields?.name === name;
    });

  if (!signedIn) {
    return (
      <div className="px-4 py-6 text-muted-foreground text-sm">
        log in, or paste a token, to see this dashboard - signed out this is a locked shelf, not an
        empty one
      </div>
    );
  }
  if (refused) {
    return (
      <div data-dashboard-refused className="px-4 py-6 text-muted-foreground text-sm">
        this dashboard is not readable by you - it lives outside the projects you can read
      </div>
    );
  }
  if (error) {
    return <p className="px-4 py-6 text-destructive text-sm">{error}</p>;
  }
  if (!loaded || !dash) {
    return <p className="px-4 py-6 text-muted-foreground text-sm">reading the dashboard…</p>;
  }

  return (
    <div className="flex h-full flex-col">
      <header className="flex flex-wrap items-center gap-2 border-border border-b px-4 py-3">
        <h1 className="font-semibold text-base">{dash.title || dash.id}</h1>
        <Link className="text-muted-foreground text-xs hover:underline" to="/dashboards">
          all dashboards
        </Link>
        <span className="ml-auto text-muted-foreground text-xs">
          {dash.project ?? "no project"} - drawn from pushed rows, nothing runs here
        </span>
      </header>

      <div
        data-dashboard={dash.id}
        className="grid min-h-0 flex-1 auto-rows-min gap-3 overflow-y-auto p-4 sm:grid-cols-2 lg:grid-cols-4"
      >
        {tiles.map((tile) => {
          if (tile.kind === "table") {
            return <TableTile key={tile.label} tile={tile} rows={rowsOf(tile.metric)} now={now} />;
          }
          if (tile.kind === "grid") {
            return <GridTile key={tile.label} tile={tile} row={rowsOf(tile.metric)[0]} now={now} />;
          }
          if (tile.kind === "number") {
            return (
              <NumberTile key={tile.label} tile={tile} row={rowsOf(tile.metric)[0]} now={now} />
            );
          }
          // A kind outside the vocabulary cannot be written - see
          // checkDashboardRow - so this is a row from a newer node, drawn
          // honestly as unrenderable rather than skipped.
          return (
            <div
              key={tile.label}
              data-tile-label={tile.label}
              data-tile-kind={tile.kind}
              className="rounded-md border border-border p-4 text-muted-foreground text-sm"
            >
              {tile.label}: tile kind {tile.kind} is not in this console's vocabulary
            </div>
          );
        })}
      </div>
    </div>
  );
}
