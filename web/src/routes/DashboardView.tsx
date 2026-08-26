import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";

import { ApiError, type Artifact } from "@/lib/api";
import {
  type DashboardTile,
  type SeriesEntry,
  type SeriesPage,
  dashboards,
  tilesOf,
} from "@/lib/dashboards";
import { CW, LH, PAD, frameOf, frameSvg, pickFrame } from "@/lib/frame";
import { type CursorTip, describe, place } from "@/lib/frame-cursor";
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
 * Every reading also says what it is - measured, inferred, unknown - in the
 * producer's own words from fields.state, and absent is unknown. A number
 * that does not say what it is must read as unknown, not as measured:
 * measured is a claim, and the claim is the producer's to make. Numbers
 * right-align, per the serenedash finding (01M0XCCQK19G4T03NBJDDDWFW1):
 * digits line up, so a column of readings reads as a column.
 *
 * The age recomputes on a second tick, not only on load, because a page left
 * open is where a stale reading does its damage - a number that looked fresh
 * at open and has gone quietly stale is a lie with a timestamp.
 */

/**
 * One reading drawn as words: <1m, 4m, 6h, 2d. Under a minute is <1m, not a
 * ticking second count - coarse formatting is stable formatting, and a page
 * that recomputes its age every second must not redraw it differently every
 * second.
 */
function ageWords(seconds: number): string {
  if (seconds < 60) return "<1m";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
  return `${Math.floor(seconds / 86400)}d`;
}

/** The age of a clock reading, coarse to the second. */
function ageSecondsAt(at: string, now: number): number {
  const parsed = Date.parse(at);
  if (!Number.isFinite(parsed)) return 0;
  return Math.max(0, Math.floor((now - parsed) / 1000));
}

/** The reading of one metric series, off the row it came from. */
function ageSeconds(row: Artifact, now: number): number {
  return ageSecondsAt(row.created, now);
}

/** Which of measured, inferred, unknown a reading claims to be. The claim is
 * the producer's, carried on the row; absent or unrecognised is unknown. */
function stateOf(row: Artifact | undefined): string {
  const fields = row?.fields as { state?: unknown } | undefined;
  const state = fields?.state;
  if (state === "measured" || state === "inferred" || state === "unknown") return state;
  return "unknown";
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
      data-state={stateOf(row)}
      data-value={row ? readingOf(row) : undefined}
      data-age={row ? age : undefined}
      className="flex flex-col justify-between rounded-md border border-border p-4"
    >
      <div className="text-muted-foreground text-xs">{tile.label}</div>
      {row ? (
        <>
          <div data-tile-value className="py-1 font-semibold text-2xl tabular-nums text-right">
            {readingOf(row)}
          </div>
          <div className="flex items-baseline gap-2 text-muted-foreground text-xs" data-tile-age>
            <span>{ageWords(age)}</span>
            <span data-tile-state={stateOf(row)}>{stateOf(row)}</span>
            {stale ? <span>, stale</span> : null}
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

/** The grid a grid tile draws: cols as headers, rows as label plus cells.
 * `label`, not `model` - the substrate does not learn what a sweep is: a
 * grid of disk usage by host has no models in it. Any reading that is not
 * this shape is null, and the tile says so - a grid drawn from a
 * wrong-shaped reading is a matrix that lies with confidence. */
function gridOf(row: Artifact): {
  cols: string[];
  rows: { label: string; cells: (string | number)[] }[];
} | null {
  const fields = row.fields as { value?: unknown } | undefined;
  const value = fields?.value;
  if (typeof value !== "object" || value === null) return null;
  const v = value as { cols?: unknown; rows?: unknown };
  if (!Array.isArray(v.cols) || !Array.isArray(v.rows)) return null;
  const cols = v.cols.map(String);
  const rows = v.rows.map((r) => {
    const o = r as { label?: unknown; cells?: unknown } | null;
    return {
      label: o?.label === undefined || o?.label === null ? "" : String(o.label),
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
      data-state={stateOf(row)}
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
                  // biome-ignore lint/suspicious/noArrayIndexKey: a grid's rows are positional - labels can repeat, and the pushed value carries no ids, so the index is the identity
                  key={i}
                  data-grid-label={r.label}
                >
                  <th
                    className="px-2 py-1 text-muted-foreground text-xs font-medium"
                    data-grid-label-cell
                  >
                    {r.label}
                  </th>
                  {r.cells.map((cell, j) => (
                    <td
                      // biome-ignore lint/suspicious/noArrayIndexKey: a row's cells are positional - the pushed value carries no ids, so the index is the identity
                      key={j}
                      data-grid-cell={i * grid.cols.length + j}
                      data-grid-value={cell}
                      className="px-2 py-1 tabular-nums text-right"
                    >
                      {cell}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
          <div className="flex items-baseline gap-2 text-muted-foreground text-xs" data-tile-age>
            <span>{ageWords(age)}</span>
            <span data-tile-state={stateOf(row)}>{stateOf(row)}</span>
            {stale ? <span>, stale</span> : null}
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
              data-state={stateOf(row)}
              className="flex items-baseline justify-between gap-3 border-border border-b py-1 last:border-b-0"
            >
              <span className="flex items-baseline gap-2 text-muted-foreground text-xs">
                <span>{ageWords(ageSeconds(row, now))}</span>
                <span data-tile-state={stateOf(row)}>{stateOf(row)}</span>
              </span>
              <span className="ml-auto font-medium tabular-nums text-right">{readingOf(row)}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** One frame tile: the newest reading of its series, drawn as its terminal
 * frame. The SVG is the frameSvg port of export.py - byte-identical to the
 * producer's own export - and the pointer answers from the frame's own prose
 * through describe(), the hover.py port: panel, row, legend term. A
 * keyboard cursor walks the same answers - j/k move a row, pgup/pgdn by
 * ten, home/end to the ends, esc clears - because a cursor key must answer
 * the same question as a pointer at the same cell, or the keyboard is a
 * second vocabulary to keep in agreement with the first.
 *
 * The frame renders at its natural cell size and scrolls: no rescaling,
 * because the whole point of the pinned grid is that a column is a column.
 * A scaled frame would also make the pointer math scale-dependent, and one
 * geometry is enough geometry. */
function FrameTile({
  tile,
  row,
  now,
}: { tile: DashboardTile; row: Artifact | undefined; now: number }) {
  const age = row ? ageSeconds(row, now) : 0;
  const stale = row && (tile.stale_after_seconds ?? 0) > 0 && age > (tile.stale_after_seconds ?? 0);
  const frame = row ? frameOf(row) : null;
  // THE PANEL'S WIDTH DECIDES WHICH RENDERING IS DRAWN. The producer pushes the
  // same frame at several widths; this measures the box and takes the widest
  // that fits, so the frame fills its panel instead of sitting narrow inside it.
  // Measured rather than assumed: a dashboard is one column on a phone and three
  // on a desktop, and the tile cannot know which it is in.
  const [boxPx, setBoxPx] = useState(0);
  const picked = frame ? pickFrame(frame, boxPx || 1e9) : null;
  const svg = picked ? frameSvg(picked.lines, { cols: picked.cols }) : "";
  const [tip, setTip] = useState<{ row: number; col: number; text: CursorTip } | null>(null);
  const [cursor, setCursor] = useState<number | null>(null);
  const [tipPx, setTipPx] = useState<[number, number]>([0, 0]);
  const boxRef = useRef<HTMLDivElement>(null);
  const tipRef = useRef<HTMLDivElement>(null);

  // THE BOX, NOT THE WINDOW. A tile is one column of a grid that reflows on its
  // own, so the window's width says nothing about how much room this frame has.
  // ResizeObserver rather than a resize listener for the same reason: the column
  // changes width when the grid does, which is not always when the window does.
  useEffect(() => {
    const box = boxRef.current;
    if (!box) return;
    const seen = () => setBoxPx(box.clientWidth);
    seen();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(seen);
    ro.observe(box);
    return () => ro.disconnect();
  }, []);

  /** The cell a pixel is in, in the frame's own geometry. The renderer and
   * the pointer share the constants, so the column a pixel lands on is the
   * column a glyph was pinned to. */
  function cellAt(px: number, py: number): [number, number] {
    return [Math.floor((py - PAD) / LH), Math.floor((px - PAD) / CW)];
  }

  function pointAt(e: React.MouseEvent) {
    const box = boxRef.current;
    if (!box || !frame || !picked) return;
    const rect = box.getBoundingClientRect();
    const [r, c] = cellAt(e.clientX - rect.left, e.clientY - rect.top);
    setCursor(null); // the pointer owns the tooltip; the cursor stands down
    const text = describe(picked.lines, r, c, frame);
    setTip(text ? { row: r, col: c, text } : null);
  }

  function onKey(e: React.KeyboardEvent) {
    if (!frame || !picked || picked.lines.length === 0) return;
    const cur = cursor ?? tip?.row ?? Math.floor(picked.lines.length / 2);
    let next: number | null = cur;
    if (e.key === "j" || e.key === "ArrowDown") next = Math.min(cur + 1, picked.lines.length - 1);
    else if (e.key === "k" || e.key === "ArrowUp") next = Math.max(cur - 1, 0);
    else if (e.key === "PageDown") next = Math.min(cur + 10, picked.lines.length - 1);
    else if (e.key === "PageUp") next = Math.max(cur - 10, 0);
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = frame.lines.length - 1;
    else if (e.key === "Escape") next = null;
    else return;
    e.preventDefault();
    if (next === null) {
      setCursor(null);
      setTip(null);
      return;
    }
    setCursor(next);
    // Column one: inside the first cell, so the cursor answers the row's
    // label. Column zero is the border column of a boxed frame - its segment
    // is empty - and the label lookup is the whole point of the cursor.
    const text = describe(frame.lines, next, 1, frame);
    setTip(text ? { row: next, col: 0, text } : null);
  }

  // The tooltip box is sized to its text and placed so it is never
  // clipped: rendered first, measured, then placed with place() against
  // the frame's visible cells - the terminal's edge is the pane's edge.
  useEffect(() => {
    if (!tip || !boxRef.current || !tipRef.current) return;
    const box = boxRef.current;
    const bw = tipRef.current.offsetWidth / CW;
    const bh = tipRef.current.offsetHeight / LH;
    const left = box.scrollLeft / CW;
    const visible = box.clientWidth / CW;
    const visibleH = box.clientHeight / LH;
    const [t, l] = place(bw, bh, tip.col, tip.row, visible, visibleH);
    setTipPx([PAD + (l + left) * CW, PAD + t * LH]);
  }, [tip]);

  return (
    <div
      data-tile-label={tile.label}
      data-tile-kind="frame"
      data-empty={row ? undefined : "true"}
      data-frame-bad={row && !frame ? "true" : undefined}
      data-stale={stale ? "true" : undefined}
      data-state={stateOf(row)}
      data-age={row ? age : undefined}
      className="flex flex-col gap-1 rounded-md border border-border p-4 sm:col-span-2 lg:col-span-4"
    >
      <div className="text-muted-foreground text-xs">{tile.label}</div>
      {row && !frame ? (
        <div className="py-1 text-muted-foreground text-sm">
          its newest reading is not a frame of {"{lines, ...}"} - the tile says so rather than
          drawing a wrong-shaped frame
        </div>
      ) : !row || !frame ? (
        <div className="py-1 text-muted-foreground text-sm">
          its metric has no rows pushed yet - not zero, just nothing
        </div>
      ) : (
        <div
          ref={boxRef}
          // biome-ignore lint/a11y/noNoninteractiveTabindex: the cursor is the point - j/k, pgup/pgdn, home/end and esc are the frame's own keys, so the frame is a keyboard control by design
          tabIndex={0}
          role="application"
          aria-label={`frame ${tile.label} - j/k move the cursor, pgup/pgdn by ten, home/end to the ends, esc clears`}
          data-frame-cursor-row={cursor ?? undefined}
          onMouseMove={pointAt}
          onMouseLeave={() => setTip(null)}
          onKeyDown={onKey}
          className="overflow-x-auto rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <div className="relative inline-block">
            {/* The frame itself. Built by frameSvg from the pushed lines -
							text in, console-built markup out, everything escaped. */}
            <div
              data-frame-svg
              // biome-ignore lint/security/noDangerouslySetInnerHtml: the markup is frameSvg's, and frameSvg escapes every pushed run - the check arm proves angle brackets render as text
              dangerouslySetInnerHTML={{ __html: svg }}
            />
            {cursor !== null && (
              <div
                data-frame-cursor
                style={{
                  top: PAD + cursor * LH,
                  left: PAD,
                  width: `calc(100% - ${PAD * 2}px)`,
                  height: LH,
                }}
                className="pointer-events-none absolute rounded-sm bg-ring/15"
              />
            )}
            {tip && (
              <div
                ref={tipRef}
                data-frame-tip
                data-frame-tip-title={tip.text.title}
                data-frame-tip-body={tip.text.body}
                style={{ left: tipPx[0], top: tipPx[1] }}
                className="absolute z-10 w-80 rounded-md border border-border bg-card px-3 py-2 shadow-lg"
              >
                <div className="font-medium text-xs">{tip.text.title}</div>
                <div className="text-muted-foreground text-xs">{tip.text.body}</div>
              </div>
            )}
          </div>
        </div>
      )}
      <div className="flex items-baseline gap-2 text-muted-foreground text-xs" data-tile-age>
        <span>{ageWords(age)}</span>
        <span data-tile-state={stateOf(row)}>{stateOf(row)}</span>
        {stale ? <span>, stale</span> : null}
      </div>
    </div>
  );
}

/** One series tile: the newest N readings of its metric, drawn as a
 * sparkline - the serenedash look, where a number is a trend with the trend
 * shown. The window comes from the series door, oldest first, and the tile
 * pins the newest point: the leftmost point is the oldest, the dot is the
 * now. The whole window is drawn from numbers only - a point that is not a
 * finite number would draw a trend that is not there, so the tile refuses
 * the window and says so rather than connecting prose.
 *
 * Age and staleness read off the newest point's own clock - the datum IS
 * the newest reading - and state off the newest row, exactly like a number
 * tile: absent is unknown, and a claim of measured is the producer's to
 * make. The door's truncated flag rides with the tile: when the series
 * holds more points than the window, the sparkline says so instead of
 * reading as the whole of it.
 */
const SERIES_W = 240;
const SERIES_H = 48;
const SERIES_PAD = 4;
const SERIES_DEFAULT_WINDOW = 60;

/** The window a series tile draws: the last `wanted` numeric points, or
 * null when any point in it is not a finite number, or none when the name
 * was never pushed (the door omits it). */
function windowOf(entry: SeriesEntry | undefined, wanted: number): number[] | null {
  if (!entry || entry.points.length === 0) return [];
  const nums: number[] = [];
  for (const p of entry.points.slice(-wanted)) {
    if (typeof p.value !== "number" || !Number.isFinite(p.value)) return null;
    nums.push(p.value);
  }
  return nums;
}

/** The polyline's points attribute, in viewBox units. SVG y grows downward,
 * so a rising series reads as a falling line - and the check arm reads THIS
 * attribute and asserts the leftmost point sits above the rightmost for
 * rising values, which is the oldest-first proof in coordinates. */
function sparkPath(nums: number[]): string {
  const n = nums.length;
  if (n === 0) return "";
  if (n === 1) return `${SERIES_W},${SERIES_H / 2}`;
  let min = nums[0];
  let max = nums[0];
  for (const v of nums) {
    if (v < min) min = v;
    if (v > max) max = v;
  }
  const span = max - min;
  const yOf = (v: number) =>
    span === 0
      ? SERIES_H / 2
      : SERIES_H - SERIES_PAD - ((v - min) / span) * (SERIES_H - 2 * SERIES_PAD);
  return nums.map((v, i) => `${(i / (n - 1)) * SERIES_W},${yOf(v)}`).join(" ");
}

/** The y of the newest point - the dot's cy, always at the right edge. */
function sparkLastY(nums: number[]): number {
  const n = nums.length;
  if (n === 0) return SERIES_H / 2;
  let min = nums[0];
  let max = nums[0];
  for (const v of nums) {
    if (v < min) min = v;
    if (v > max) max = v;
  }
  const span = max - min;
  const last = nums[n - 1];
  return span === 0
    ? SERIES_H / 2
    : SERIES_H - SERIES_PAD - ((last - min) / span) * (SERIES_H - 2 * SERIES_PAD);
}

function SeriesTile({
  tile,
  entry,
  row,
  now,
}: {
  tile: DashboardTile;
  entry: SeriesEntry | undefined;
  row: Artifact | undefined;
  now: number;
}) {
  const wanted = tile.points && tile.points > 0 ? tile.points : SERIES_DEFAULT_WINDOW;
  const nums = windowOf(entry, wanted);
  const bad = nums === null;
  const empty = !bad && nums.length === 0;
  const last = !empty && !bad ? nums[nums.length - 1] : 0;
  // The age is the newest ROW's wall clock - the same reading the dot pins,
  // and the same age every other tile shows. The door's `at` is the store's
  // HLC, a logical ordering clock, not wall time.
  const age = row && !empty && !bad ? ageSeconds(row, now) : 0;
  const stale =
    !empty && !bad && (tile.stale_after_seconds ?? 0) > 0 && age > (tile.stale_after_seconds ?? 0);
  return (
    <div
      data-tile-label={tile.label}
      data-tile-kind="series"
      data-empty={empty ? "true" : undefined}
      data-series-bad={bad ? "true" : undefined}
      data-stale={stale ? "true" : undefined}
      data-state={stateOf(row)}
      data-age={!empty && !bad ? age : undefined}
      data-series-points={!empty && !bad ? nums.length : undefined}
      data-series-first={!empty && !bad ? nums[0] : undefined}
      data-series-latest={!empty && !bad ? last : undefined}
      data-series-truncated={!empty && !bad && entry?.truncated ? "true" : undefined}
      className="flex flex-col gap-1 rounded-md border border-border p-4"
    >
      <div className="text-muted-foreground text-xs">{tile.label}</div>
      {bad ? (
        <div className="py-1 text-muted-foreground text-sm">
          its points are not numbers - the tile says so rather than drawing a wrong-shaped sparkline
        </div>
      ) : empty ? (
        <div className="py-1 text-muted-foreground text-sm">
          its metric has no rows pushed yet - not zero, just nothing
        </div>
      ) : (
        <>
          <div
            data-tile-value
            className="flex items-baseline justify-end font-semibold text-2xl tabular-nums"
          >
            {last}
          </div>
          <svg
            data-series-svg
            viewBox={`0 0 ${SERIES_W} ${SERIES_H}`}
            preserveAspectRatio="none"
            className="h-12 w-full"
          >
            <title>{`${tile.label}: ${last}`}</title>
            <polyline
              data-series-path
              points={sparkPath(nums)}
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
            />
            <circle data-series-dot cx={SERIES_W} cy={sparkLastY(nums)} r="3" />
          </svg>
          <div className="flex items-baseline gap-2 text-muted-foreground text-xs" data-tile-age>
            <span>{ageWords(age)}</span>
            <span data-tile-state={stateOf(row)}>{stateOf(row)}</span>
            {stale ? <span>, stale</span> : null}
          </div>
        </>
      )}
    </div>
  );
}

/** The gauge a gauge tile draws: a numeric value WITH ITS BOUNDS - the scale
 * and the thresholds travel beside the reading, not on the tile, because the
 * producer is the party that knows them (the tile-side scale is refused by
 * name at the write). Any reading that is not this shape is null, and the
 * tile says so - a gauge drawn from a wrong-shaped reading is a bar that lies
 * with confidence. The direction is not a field: crit above warn means high
 * is bad, crit below warn means low is bad - the renderer reads the sense of
 * the gauge off the two numbers it already has, so a free-disk gauge works
 * without a flag saying which way round it is. */
type GaugeReading = {
  value: number;
  min?: number;
  max?: number;
  warn?: number;
  crit?: number;
  direction?: "high" | "low";
  severity?: "ok" | "warn" | "crit";
};

function gaugeOf(row: Artifact): GaugeReading | null {
  const fields = row.fields as
    | {
        value?: unknown;
        min?: unknown;
        max?: unknown;
        thresholds?: unknown;
      }
    | undefined;
  const value = fields?.value;
  if (typeof value !== "number" || !Number.isFinite(value)) return null;
  const min = fields?.min;
  const max = fields?.max;
  // A HALF-DECLARED SCALE IS NOT A SCALE: min alone cannot place a value,
  // and max alone cannot either - a bar drawn from one end invents the other.
  if ((min === undefined) !== (max === undefined)) return null;
  const thresholds = fields?.thresholds;
  if (min === undefined || max === undefined) {
    // Thresholds with no scale are marks off nothing - refused at the write
    // too, so an ignored mark cannot read as a rendering bug here.
    return thresholds === undefined || thresholds === null ? { value } : null;
  }
  if (
    typeof min !== "number" ||
    typeof max !== "number" ||
    !Number.isFinite(min) ||
    !Number.isFinite(max) ||
    max <= min
  ) {
    return null;
  }
  // A value its own scale cannot place is a wrong-shaped reading, not a
  // clamped bar: off the scale says the bounds and the reading disagree.
  if (value < min || value > max) return null;
  if (thresholds === undefined || thresholds === null) {
    return { value, min, max };
  }
  if (typeof thresholds !== "object") return null;
  const marks = thresholds as { warn?: unknown; crit?: unknown };
  const warn = marks.warn;
  const crit = marks.crit;
  if (
    typeof warn !== "number" ||
    typeof crit !== "number" ||
    !Number.isFinite(warn) ||
    !Number.isFinite(crit)
  ) {
    return null;
  }
  // One threshold alone cannot say which way is worse, and a pair at the
  // same spot cannot either - the order IS the direction, so an order the
  // tile cannot read is a gauge it cannot draw.
  if (warn === crit) return null;
  const direction = crit > warn ? "high" : "low";
  const severity =
    direction === "high"
      ? value >= crit
        ? "crit"
        : value >= warn
          ? "warn"
          : "ok"
      : value <= crit
        ? "crit"
        : value <= warn
          ? "warn"
          : "ok";
  return { value, min, max, warn, crit, direction, severity };
}

/** The colour a gauge's fill takes, by its severity - off the serenedash
 * palette, the only colour vocabulary a tile may use: ok is the plain blue,
 * warn borrows the amber of inferred, crit the dim red-orange. The gauge's
 * verdict is in its position on the bar; the colour only names it. */
const GAUGE_FILL: Record<"ok" | "warn" | "crit", string> = {
  ok: "var(--color-serenedash-1)",
  warn: "var(--color-serenedash-4)",
  crit: "var(--color-serenedash-3)",
};

/** One gauge tile: the newest reading of its series, drawn as a value with
 * its bar. The scale comes off the reading - no scale, no bar, just the
 * number - and the thresholds draw warn and crit bands on it: above the mark
 * when high is bad, below it when low is bad. The fill runs min to value and
 * takes the severity's colour. It carries its age and staleness exactly like
 * a number tile: a gauge frozen at its last push reads as a live instrument. */
function GaugeTile({
  tile,
  row,
  now,
}: { tile: DashboardTile; row: Artifact | undefined; now: number }) {
  const age = row ? ageSeconds(row, now) : 0;
  const stale = row && (tile.stale_after_seconds ?? 0) > 0 && age > (tile.stale_after_seconds ?? 0);
  const gauge = row ? gaugeOf(row) : null;
  let fill = 0;
  let warnBand: [number, number] | null = null;
  let critBand: [number, number] | null = null;
  if (gauge !== null && gauge.min !== undefined && gauge.max !== undefined) {
    const { min, max } = gauge;
    const span = max - min;
    const pct = (v: number) => ((v - min) / span) * 100;
    fill = pct(gauge.value);
    if (gauge.warn !== undefined && gauge.crit !== undefined && gauge.direction !== undefined) {
      if (gauge.direction === "high") {
        warnBand = [pct(gauge.warn), pct(gauge.crit) - pct(gauge.warn)];
        critBand = [pct(gauge.crit), 100 - pct(gauge.crit)];
      } else {
        warnBand = [pct(gauge.crit), pct(gauge.warn) - pct(gauge.crit)];
        critBand = [0, pct(gauge.crit)];
      }
    }
  }
  const scaled = gauge !== null && gauge.min !== undefined && gauge.max !== undefined;
  return (
    <div
      data-tile-label={tile.label}
      data-tile-kind="gauge"
      data-empty={row ? undefined : "true"}
      data-gauge-bad={row && !gauge ? "true" : undefined}
      data-stale={stale ? "true" : undefined}
      data-state={stateOf(row)}
      data-age={row && gauge ? age : undefined}
      data-gauge-value={gauge ? gauge.value : undefined}
      data-gauge-min={gauge?.min}
      data-gauge-max={gauge?.max}
      data-gauge-warn={gauge?.warn}
      data-gauge-crit={gauge?.crit}
      data-gauge-direction={gauge?.direction}
      data-gauge-severity={gauge?.severity}
      className="flex flex-col gap-1 rounded-md border border-border p-4"
    >
      <div className="text-muted-foreground text-xs">{tile.label}</div>
      {row && !gauge ? (
        <div className="py-1 text-muted-foreground text-sm">
          its newest reading is not a gauge of {"{value, min, max, thresholds}"} - the tile says so
          rather than drawing a bar that lies
        </div>
      ) : !row || !gauge ? (
        <div className="py-1 text-muted-foreground text-sm">
          its metric has no rows pushed yet - not zero, just nothing
        </div>
      ) : (
        <>
          <div
            data-tile-value
            className="flex items-baseline justify-end font-semibold text-2xl tabular-nums"
          >
            {gauge.value}
          </div>
          {scaled && (
            <div data-gauge-track className="relative h-2 w-full rounded bg-border/60">
              <div
                data-gauge-fill
                style={{ width: `${fill}%`, background: GAUGE_FILL[gauge.severity ?? "ok"] }}
                className="absolute top-0 left-0 h-full rounded"
              />
              {critBand && (
                <div
                  data-gauge-band="crit"
                  style={{
                    left: `${critBand[0]}%`,
                    width: `${critBand[1]}%`,
                    background: "var(--color-serenedash-3)",
                  }}
                  className="absolute top-0 h-full opacity-25"
                />
              )}
              {warnBand && (
                <div
                  data-gauge-band="warn"
                  style={{
                    left: `${warnBand[0]}%`,
                    width: `${warnBand[1]}%`,
                    background: "var(--color-serenedash-4)",
                  }}
                  className="absolute top-0 h-full opacity-25"
                />
              )}
            </div>
          )}
          <div className="flex items-baseline gap-2 text-muted-foreground text-xs" data-tile-age>
            <span>{ageWords(age)}</span>
            <span data-tile-state={stateOf(row)}>{stateOf(row)}</span>
            {stale ? <span>, stale</span> : null}
          </div>
        </>
      )}
    </div>
  );
}

export function DashboardView() {
  const { id } = useParams();
  const signedIn = useSignedIn();
  const [dash, setDash] = useState<Artifact | null>(null);
  const [rows, setRows] = useState<Artifact[]>([]);
  const [seriesPages, setSeriesPages] = useState<Map<number, SeriesPage>>(new Map());
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
      setSeriesPages(new Map());
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
        const tiles = tilesOf(artifact);
        const names = [...new Set(tiles.map((t) => t.metric).filter(Boolean))];
        if (names.length === 0) return;
        // The series door takes one points value for every name - and a
        // window wider than a tile's own would rob that tile of its truncated
        // flag, so the page groups its series tiles by the window they
        // declare and asks each window once. A tile reads its own window's
        // answer; the door's shape stays the door's.
        const byWindow = new Map<number, string[]>();
        for (const t of tiles) {
          if (t.kind !== "series" || !t.metric) continue;
          const w = t.points && t.points > 0 ? t.points : SERIES_DEFAULT_WINDOW;
          const ns = byWindow.get(w) ?? [];
          ns.push(t.metric);
          byWindow.set(w, ns);
        }
        try {
          const [page, spages] = await Promise.all([
            dashboards.metrics(names),
            Promise.all(
              [...byWindow.entries()].map(async ([w, ns]) =>
                dashboards.series([...new Set(ns)], w).then((p) => [w, p] as const),
              ),
            ),
          ]);
          if (!stopped) {
            setRows(page.metrics ?? []);
            setSeriesPages(new Map(spages));
          }
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

  /** The series window one tile declares and reads - the door was asked at
   * that width, so the tile's own truncated flag survives. */
  const windowOfTile = (tile: DashboardTile): number =>
    tile.points && tile.points > 0 ? tile.points : SERIES_DEFAULT_WINDOW;

  /** The series window of one metric, off its tile's own series door ask. */
  const seriesOf = (tile: DashboardTile): SeriesEntry | undefined =>
    seriesPages.get(windowOfTile(tile))?.series.find((s) => s.name === tile.metric);

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
          if (tile.kind === "frame") {
            return (
              <FrameTile key={tile.label} tile={tile} row={rowsOf(tile.metric)[0]} now={now} />
            );
          }
          if (tile.kind === "number") {
            return (
              <NumberTile key={tile.label} tile={tile} row={rowsOf(tile.metric)[0]} now={now} />
            );
          }
          if (tile.kind === "series") {
            return (
              <SeriesTile
                key={tile.label}
                tile={tile}
                entry={seriesOf(tile)}
                row={rowsOf(tile.metric)[0]}
                now={now}
              />
            );
          }
          if (tile.kind === "gauge") {
            return (
              <GaugeTile key={tile.label} tile={tile} row={rowsOf(tile.metric)[0]} now={now} />
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
