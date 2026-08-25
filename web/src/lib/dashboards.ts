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

/** One declared tile, as the store rules it (store.DashboardTile). */
export interface DashboardTile {
  kind: string;
  label: string;
  metric: string;
  /** How old a reading may be before the tile draws it as stale rather than
   * live. Zero means never stale. */
  stale_after_seconds?: number;
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
};
