import { type Artifact, request } from "@/lib/api";

/**
 * Dashboards: pages agents author and people read.
 *
 * A dashboard is a memory artifact of kind `dashboard` whose fields.tiles is
 * the whole of the declaration - a fixed vocabulary of components, each one a
 * query over a named metric series. It RUNS nothing: the numbers come from
 * metric rows producers push through the ordinary artifact door, and this
 * console renders the declaration. Two loads of the page answer from the rows,
 * so what changed between them is what the page shows, and a tile whose metric
 * was never pushed says so rather than drawing a plausible zero.
 *
 * The store half rules the shape (internal/store/dashboards.go): the tile
 * vocabulary is closed, and a row that is not a dashboard is refused at write.
 * This module reads through the ordinary doors - the artifact read for the
 * declaration, GET /api/metrics/rows for the data - so there is no dashboard
 * door to keep in agreement with the store beyond the two that already exist.
 */

export const DASHBOARD_TYPE = "memory";
export const DASHBOARD_KIND = "dashboard";

/** One declared tile, as the store rules it (store.DashboardTile). The
 * vocabulary is number, table, grid, frame, series, gauge, report - each
 * kind renders over the named metric. A gauge declares metric and kind ONLY:
 * its scale and thresholds travel with the reading, because the producer is
 * the party that knows them. A report is the document style: the reading is
 * the whole page - header plus sections of the closed vocabulary - and the
 * console renders structure, never markup. */
export interface DashboardTile {
  kind: string;
  label: string;
  metric: string;
  /** How old a reading may be before the tile draws it as stale rather than
   * live. Zero means never stale. */
  stale_after_seconds?: number;
  /** The window a series tile draws - the newest N readings, oldest first.
   * Zero means the console's default. */
  points?: number;
}

/** The tiles a dashboard row declares, or none. The row checks make a written
 * row carry them; a read is lenient. */
export function tilesOf(artifact: Artifact): DashboardTile[] {
  const fields = artifact.fields as { tiles?: DashboardTile[] } | undefined;
  return fields?.tiles ?? [];
}

/** One reading of one series, newest first is the door's order. */
export interface MetricPage {
  metrics: Artifact[];
}

/** One point of a series, off GET /api/metrics/series: the row's clock and
 * the reading it carried. */
export interface SeriesPoint {
  at: string;
  value: unknown;
}

/** One series window, as the series door answers it: oldest first, its own
 * truncated flag, absent from the array when its name was never pushed. */
export interface SeriesEntry {
  name: string;
  points: SeriesPoint[];
  truncated: boolean;
}

export interface SeriesPage {
  series: SeriesEntry[];
  asked: string[];
}

/** One line of a log tail, off GET /api/logs/tail. Level may be empty - a
 * line without one is legal and deliberately so, the store's comment says a
 * crash dump is exactly the line most worth having. */
export interface LogLine {
  id: string;
  at: number;
  stream: string;
  level: string;
  type: string;
  message: string;
}

/** One stream's tail, as the log door answers it: the last lines oldest
 * first, with the level and type counts over the whole filtered window - the
 * header says so without the caller counting the page it was given. */
export interface LogTail {
  stream: string;
  lines: LogLine[];
  counts: { levels: Record<string, number>; types: Record<string, number> };
}

export const dashboards = {
  list: () =>
    request<{ artifacts: Artifact[] }>(
      `/api/artifacts?type=${encodeURIComponent(DASHBOARD_TYPE)}&kind=${encodeURIComponent(DASHBOARD_KIND)}&limit=200`,
    ),

  read: (id: string) => request<Artifact>(`/api/artifact/${encodeURIComponent(id)}`),

  /**
   * The rows of the named series, newest first, under this reader's reach.
   * The name is repeatable because a page reads every series its tiles name
   * in one call.
   */
  metrics: (names: string[]) =>
    request<MetricPage>(
      `/api/metrics/rows?${names.map((n) => `name=${encodeURIComponent(n)}`).join("&")}&limit=200`,
    ),

  /**
   * The newest `points` readings of each named series, oldest first. One
   * points value applies to every name - the door's shape - so the page
   * groups its series tiles by the window they declare and asks each window
   * once: a window wider than a tile's own would rob that tile of its
   * truncated flag.
   */
  series: (names: string[], points: number) =>
    request<SeriesPage>(
      `/api/metrics/series?${names.map((n) => `name=${encodeURIComponent(n)}`).join("&")}&points=${points}`,
    ),

  /**
   * The last lines of one log stream, oldest first. The stream is the tile's
   * metric - a log tile declares metric and kind only, like a gauge - and the
   * door is per stream because "every log line on this node" is not a tile
   * anybody declares.
   */
  logTail: (stream: string, limit = 200) =>
    request<LogTail>(`/api/logs/tail?stream=${encodeURIComponent(stream)}&limit=${limit}`),
};
