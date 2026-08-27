import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";

import { ApiError, type Artifact } from "@/lib/api";
import {
  type DashboardTile,
  type LogTail,
  type SeriesEntry,
  type SeriesPage,
  type Trace,
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
  const picked = frame ? pickFrame(frame, boxPx > 0 ? boxPx : null) : null;
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
  //
  // A CALLBACK REF, NOT AN EFFECT ON MOUNT. The frame box does not exist until
  // the row has loaded, so an effect with an empty dependency list runs while
  // boxRef.current is still null, attaches nothing, and leaves the width
  // permanently unmeasured. The check caught exactly that: the frame drew its
  // widest rendering and never narrowed. A callback ref fires whenever the node
  // attaches or detaches, which is the question being asked.
  const roRef = useRef<ResizeObserver | null>(null);
  const measure = useCallback((node: HTMLDivElement | null) => {
    boxRef.current = node;
    roRef.current?.disconnect();
    roRef.current = null;
    if (!node) return;
    setBoxPx(node.clientWidth);
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => setBoxPx(node.clientWidth));
    ro.observe(node);
    roRef.current = ro;
  }, []);
  useEffect(() => () => roRef.current?.disconnect(), []);

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
      className="flex min-w-0 flex-col gap-1 rounded-md border border-border p-4 sm:col-span-2 lg:col-span-4"
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
          ref={measure}
          // biome-ignore lint/a11y/noNoninteractiveTabindex: the cursor is the point - j/k, pgup/pgdn, home/end and esc are the frame's own keys, so the frame is a keyboard control by design
          tabIndex={0}
          role="application"
          aria-label={`frame ${tile.label} - j/k move the cursor, pgup/pgdn by ten, home/end to the ends, esc clears`}
          data-frame-cursor-row={cursor ?? undefined}
          onMouseMove={pointAt}
          onMouseLeave={() => setTip(null)}
          onKeyDown={onKey}
          // min-w-0 is what makes the measurement mean anything. A flex or grid
          // item defaults to min-width:auto, so a 1099px frame made this box
          // 1099px wide whatever the viewport - and measuring it then measured
          // the FRAME, not the room, which picks the widest rendering and holds
          // it there. With min-width:0 the box shrinks to its column and
          // overflow-x-auto does the clipping it was always meant to do.
          className="min-w-0 overflow-x-auto rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring"
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

/** One cell of a report's columns or squares sections: text plus optional
 * tone word and hover title. A plain pushed string is a cell of text with no
 * tone - the tone is optional because most cells are just words. */
type ReportCell = {
  text: string;
  tone: string | undefined;
  title: string | undefined;
};

type ReportProgress = {
  kind: "progress";
  tone: string | undefined;
  total: number | undefined;
  value: number | undefined;
  caption: string | undefined;
  segments: { label: string; value: number; tone: string | undefined }[];
};

type ReportCard = {
  title: string;
  pill: string | undefined;
  blurb: string | undefined;
  stats: { label: string; value: string }[];
  spark: { metric: string; points: number } | undefined;
  note: string | undefined;
};

type ReportCards = { kind: "cards"; tone: string | undefined; cards: ReportCard[] };

type ReportColumns = {
  kind: "columns";
  tone: string | undefined;
  columns: { label: string; align: "left" | "right" | "center" }[];
  rows: { cells: ReportCell[]; tone: string | undefined }[];
};

type ReportSquares = {
  kind: "squares";
  tone: string | undefined;
  groups: { label: string; rows: { label: string; cells: ReportCell[] }[] }[];
};

type ReportSection =
  | ReportProgress
  | ReportCards
  | ReportColumns
  | ReportSquares
  | { kind: "unknown"; name: string; tone: undefined };

type ReportDoc = {
  eyebrow: string | undefined;
  title: string | undefined;
  lede: string | undefined;
  sections: ReportSection[];
};

type ReportRead = { ok: true; doc: ReportDoc } | { ok: false; why: "shape" | "empty" };

/** The palette a tone word maps to - the word is the producer's, the colour
 * is the reader's theme. An unknown word draws as no tone at all: refusing
 * it outright would punish a producer for a word this console has not met,
 * and a colour the console invented would be a colour nobody chose. */
const REPORT_TONE: Record<string, string | undefined> = {
  "": undefined,
  good: "var(--color-serenedash-2)",
  warn: "var(--color-serenedash-4)",
  bad: "var(--color-serenedash-3)",
  dim: "var(--color-serenedash-5)",
  accent: "var(--color-serenedash-6)",
};

function toneColour(word: string | undefined): string | undefined {
  if (word === undefined) return undefined;
  return REPORT_TONE[word];
}

function strOf(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

function numOf(v: unknown): number | undefined {
  return typeof v === "number" && Number.isFinite(v) ? v : undefined;
}

function cellOf(v: unknown): ReportCell {
  if (typeof v === "string") return { text: v, tone: undefined, title: undefined };
  if (typeof v !== "object" || v === null) {
    return { text: String(v ?? ""), tone: undefined, title: undefined };
  }
  const o = v as { text?: unknown; tone?: unknown; title?: unknown };
  return {
    text: o.text === undefined || o.text === null ? "" : String(o.text),
    tone: strOf(o.tone),
    title: strOf(o.title),
  };
}

/** The document a report tile draws, parsed from the newest reading. Any
 * reading that is not a document with a non-empty sections array is not a
 * report: prose is a number or a frame's business, not this tile's - and an
 * empty document is a page that shows nothing. Section shapes are parsed
 * leniently field by field, so a producer's extra key is never this tile's
 * problem; an unknown section kind is kept and said so rather than skipped. */
function reportOf(row: Artifact): ReportRead {
  const fields = row.fields as { value?: unknown } | undefined;
  const value = fields?.value;
  if (typeof value !== "object" || value === null) return { ok: false, why: "shape" };
  const v = value as { eyebrow?: unknown; title?: unknown; lede?: unknown; sections?: unknown };
  if (!Array.isArray(v.sections)) return { ok: false, why: "shape" };
  if (v.sections.length === 0) return { ok: false, why: "empty" };
  const sections: ReportSection[] = [];
  for (const s of v.sections) {
    if (typeof s !== "object" || s === null) {
      sections.push({ kind: "unknown", name: String(s ?? ""), tone: undefined });
      continue;
    }
    const sec = s as { kind?: unknown; tone?: unknown };
    const tone = strOf(sec.tone);
    if (sec.kind === "progress") {
      const p = s as { total?: unknown; value?: unknown; caption?: unknown; segments?: unknown };
      const segments: ReportProgress["segments"] = [];
      if (Array.isArray(p.segments)) {
        for (const seg of p.segments) {
          if (typeof seg !== "object" || seg === null) continue;
          const so = seg as { label?: unknown; value?: unknown; tone?: unknown };
          const val = numOf(so.value);
          if (val === undefined) continue;
          segments.push({
            label: so.label === undefined || so.label === null ? "" : String(so.label),
            value: val,
            tone: strOf(so.tone),
          });
        }
      }
      sections.push({
        kind: "progress",
        tone,
        total: numOf(p.total),
        value: numOf(p.value),
        caption: strOf(p.caption),
        segments,
      });
    } else if (sec.kind === "cards") {
      const c = s as { cards?: unknown };
      const cards: ReportCard[] = [];
      if (Array.isArray(c.cards)) {
        for (const card of c.cards) {
          if (typeof card !== "object" || card === null) continue;
          const co = card as {
            title?: unknown;
            pill?: unknown;
            blurb?: unknown;
            stats?: unknown;
            spark?: unknown;
            note?: unknown;
          };
          const stats: ReportCard["stats"] = [];
          if (Array.isArray(co.stats)) {
            for (const st of co.stats) {
              if (typeof st !== "object" || st === null) continue;
              const so = st as { label?: unknown; value?: unknown };
              stats.push({
                label: so.label === undefined || so.label === null ? "" : String(so.label),
                value: so.value === undefined || so.value === null ? "" : String(so.value),
              });
            }
          }
          let spark: ReportCard["spark"];
          if (typeof co.spark === "object" && co.spark !== null) {
            const sp = co.spark as { metric?: unknown; points?: unknown };
            const metric = strOf(sp.metric);
            const points = numOf(sp.points);
            if (metric !== undefined && metric !== "" && points !== undefined && points > 0) {
              spark = { metric, points };
            }
          }
          cards.push({
            title: co.title === undefined || co.title === null ? "" : String(co.title),
            pill: strOf(co.pill),
            blurb: strOf(co.blurb),
            stats,
            spark,
            note: strOf(co.note),
          });
        }
      }
      sections.push({ kind: "cards", tone, cards });
    } else if (sec.kind === "columns") {
      const c = s as { columns?: unknown; rows?: unknown };
      const columns: ReportColumns["columns"] = [];
      if (Array.isArray(c.columns)) {
        for (const col of c.columns) {
          if (typeof col !== "object" || col === null) continue;
          const colo = col as { label?: unknown; align?: unknown };
          const alignRaw = strOf(colo.align);
          const align = alignRaw === "right" || alignRaw === "center" ? alignRaw : "left";
          columns.push({
            label: colo.label === undefined || colo.label === null ? "" : String(colo.label),
            align,
          });
        }
      }
      const rows: ReportColumns["rows"] = [];
      if (Array.isArray(c.rows)) {
        for (const r of c.rows) {
          if (typeof r !== "object" || r === null) continue;
          const ro = r as { cells?: unknown; tone?: unknown };
          rows.push({
            cells: Array.isArray(ro.cells) ? ro.cells.map(cellOf) : [],
            tone: strOf(ro.tone),
          });
        }
      }
      sections.push({ kind: "columns", tone, columns, rows });
    } else if (sec.kind === "squares") {
      const sq = s as { groups?: unknown };
      const groups: ReportSquares["groups"] = [];
      if (Array.isArray(sq.groups)) {
        for (const g of sq.groups) {
          if (typeof g !== "object" || g === null) continue;
          const go = g as { label?: unknown; rows?: unknown };
          const rows: ReportSquares["groups"][number]["rows"] = [];
          if (Array.isArray(go.rows)) {
            for (const r of go.rows) {
              if (typeof r !== "object" || r === null) continue;
              const ro = r as { label?: unknown; cells?: unknown };
              rows.push({
                label: ro.label === undefined || ro.label === null ? "" : String(ro.label),
                cells: Array.isArray(ro.cells) ? ro.cells.map(cellOf) : [],
              });
            }
          }
          groups.push({
            label: go.label === undefined || go.label === null ? "" : String(go.label),
            rows,
          });
        }
      }
      sections.push({ kind: "squares", tone, groups });
    } else {
      sections.push({
        kind: "unknown",
        name: sec.kind === undefined || sec.kind === null ? "" : String(sec.kind),
        tone: undefined,
      });
    }
  }
  return {
    ok: true,
    doc: { eyebrow: strOf(v.eyebrow), title: strOf(v.title), lede: strOf(v.lede), sections },
  };
}

const SPARK_W = 80;
const SPARK_H = 20;
const SPARK_PAD = 2;

/** The card spark: the metric's window off the series door, drawn as the
 * same sparkline shape the series tile draws - oldest first, newest pinned.
 * Drawn only when the door answered with numbers: a spark over prose would
 * draw a trend that is not there, so no points means no spark, not an empty
 * axis. */
function CardSpark({
  entry,
  points,
  name,
}: { entry: SeriesEntry | undefined; points: number; name: string }) {
  const nums = windowOf(entry, points);
  if (nums === null || nums.length === 0) return null;
  let min = nums[0];
  let max = nums[0];
  for (const v of nums) {
    if (v < min) min = v;
    if (v > max) max = v;
  }
  const span = max - min;
  const yOf = (v: number) =>
    span === 0 ? SPARK_H / 2 : SPARK_H - SPARK_PAD - ((v - min) / span) * (SPARK_H - 2 * SPARK_PAD);
  const path =
    nums.length === 1
      ? `${SPARK_W},${SPARK_H / 2}`
      : nums.map((v, i) => `${(i / (nums.length - 1)) * SPARK_W},${yOf(v)}`).join(" ");
  const lastY = yOf(nums[nums.length - 1]);
  return (
    <svg
      data-spark-svg
      data-spark-name={name}
      viewBox={`0 0 ${SPARK_W} ${SPARK_H}`}
      className="h-5 w-20"
    >
      <title>{`${name}: ${nums[nums.length - 1]}`}</title>
      <polyline data-spark-path points={path} fill="none" stroke="currentColor" strokeWidth="2" />
      <circle data-spark-dot cx={SPARK_W} cy={lastY} r="2" />
    </svg>
  );
}

/** The one alignment class a columns section uses for its header and cells.
 * A column aligns its own cells; anything else reads left. */
function alignClassOf(col: ReportColumns["columns"][number] | undefined): string {
  if (col?.align === "right") return "text-right";
  if (col?.align === "center") return "text-center";
  return "text-left";
}

function ReportSectionView({
  section,
  sparkOf,
}: {
  section: ReportSection;
  sparkOf: (metric: string, points: number) => SeriesEntry | undefined;
}) {
  if (section.kind === "unknown") {
    return (
      <div data-report-section-bad className="text-muted-foreground text-sm">
        a section declares kind {section.name || "(none)"} - not in this console's vocabulary, drawn
        as a gap rather than a guess
      </div>
    );
  }
  if (section.kind === "progress") {
    const total = section.total;
    const bar = total !== undefined && total > 0;
    return (
      <div className="flex flex-col gap-1">
        {section.value !== undefined && (
          <div data-progress-value className="flex justify-end font-semibold text-2xl tabular-nums">
            {section.value}
          </div>
        )}
        {bar && (
          <div
            data-progress-track
            data-progress-total={total}
            className="flex h-2 w-full gap-px overflow-hidden rounded bg-border/60"
          >
            {section.segments.length > 0 ? (
              section.segments.map((seg, j) => (
                <div
                  // biome-ignore lint/suspicious/noArrayIndexKey: a progress's segments are positional - the pushed reading carries no ids, so the index is the identity
                  key={j}
                  data-progress-segment={j}
                  data-progress-segment-value={seg.value}
                  data-progress-segment-label={seg.label}
                  data-progress-segment-tone={seg.tone}
                  title={seg.label ? `${seg.label}: ${seg.value}` : String(seg.value)}
                  style={{
                    width: `${Math.min(100, (seg.value / total) * 100)}%`,
                    background: toneColour(seg.tone) ?? `var(--color-serenedash-${(j % 8) + 1})`,
                  }}
                />
              ))
            ) : section.value !== undefined ? (
              <div
                data-progress-fill
                style={{
                  width: `${Math.min(100, (section.value / total) * 100)}%`,
                  background: "var(--color-serenedash-1)",
                }}
              />
            ) : null}
          </div>
        )}
        {section.caption && (
          <div data-progress-caption className="text-muted-foreground text-xs">
            {section.caption}
          </div>
        )}
      </div>
    );
  }
  if (section.kind === "cards") {
    return (
      <div className="grid gap-2 sm:grid-cols-2">
        {section.cards.map((card, j) => (
          <div
            // biome-ignore lint/suspicious/noArrayIndexKey: a report's cards are positional - the pushed document carries no ids, so the index is the identity
            key={j}
            data-report-card={j}
            data-report-card-title={card.title}
            data-report-card-pill={card.pill}
            data-report-card-blurb={card.blurb}
            data-report-card-note={card.note}
            data-report-card-stats={card.stats.length}
            data-report-spark={card.spark?.metric}
            data-report-spark-points={card.spark?.points}
            className="flex flex-col gap-1 rounded-md border border-border p-3"
          >
            <div className="flex items-center gap-2">
              <span className="font-medium text-sm">{card.title}</span>
              {card.pill && (
                <span className="rounded-full bg-border/60 px-2 py-0.5 text-muted-foreground text-xs">
                  {card.pill}
                </span>
              )}
            </div>
            {card.blurb && <div className="text-muted-foreground text-xs">{card.blurb}</div>}
            {card.stats.length > 0 && (
              <ul className="flex flex-col">
                {card.stats.map((stat, k) => (
                  <li
                    // biome-ignore lint/suspicious/noArrayIndexKey: a card's stats are positional - the pushed document carries no ids, so the index is the identity
                    key={k}
                    className="flex items-baseline justify-between gap-3 border-border border-b py-0.5 last:border-b-0"
                  >
                    <span className="text-muted-foreground text-xs">{stat.label}</span>
                    <span className="tabular-nums text-sm text-right">{stat.value}</span>
                  </li>
                ))}
              </ul>
            )}
            {card.spark && (
              <CardSpark
                entry={sparkOf(card.spark.metric, card.spark.points)}
                points={card.spark.points}
                name={card.spark.metric}
              />
            )}
            {card.note && <div className="text-muted-foreground text-xs">{card.note}</div>}
          </div>
        ))}
      </div>
    );
  }
  if (section.kind === "columns") {
    return (
      <table className="mt-1 border-collapse text-sm">
        <thead>
          <tr>
            {section.columns.map((col, j) => (
              <th
                // biome-ignore lint/suspicious/noArrayIndexKey: a report's columns are positional - the pushed document carries no ids, so the index is the identity
                key={j}
                data-report-col={j}
                data-report-col-label={col.label}
                data-report-col-align={col.align}
                className={`border-border border-b px-2 py-1 text-muted-foreground text-xs font-medium ${alignClassOf(col)}`}
              >
                {col.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {section.rows.map((r, i) => (
            <tr
              // biome-ignore lint/suspicious/noArrayIndexKey: a report's rows are positional - the pushed document carries no ids, so the index is the identity
              key={i}
              data-report-row={i}
              data-report-row-tone={r.tone}
            >
              {r.cells.map((cell, j) => (
                <td
                  // biome-ignore lint/suspicious/noArrayIndexKey: a row's cells are positional - the pushed document carries no ids, so the index is the identity
                  key={j}
                  data-report-cell={i * section.columns.length + j}
                  data-report-cell-text={cell.text}
                  data-report-cell-tone={cell.tone}
                  style={{ color: toneColour(cell.tone) }}
                  className={`px-2 py-1 tabular-nums ${alignClassOf(section.columns[j])}`}
                >
                  {cell.text}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    );
  }
  return (
    <div className="flex flex-col gap-1">
      {section.groups.map((group, i) => (
        <div
          // biome-ignore lint/suspicious/noArrayIndexKey: a report's square groups are positional - the pushed document carries no ids, so the index is the identity
          key={i}
          data-report-group={i}
          data-report-group-label={group.label}
          className="flex flex-col gap-0.5"
        >
          {group.label && <div className="text-muted-foreground text-xs">{group.label}</div>}
          {group.rows.map((r, j) => (
            <div
              // biome-ignore lint/suspicious/noArrayIndexKey: a squares row is positional - the pushed document carries no ids, so the index is the identity
              key={j}
              data-square-row={j}
              data-square-row-label={r.label}
              className="flex items-center gap-1"
            >
              {r.label && (
                <span className="w-16 shrink-0 text-muted-foreground text-xs">{r.label}</span>
              )}
              {r.cells.map((cell, k) => (
                <span
                  // biome-ignore lint/suspicious/noArrayIndexKey: a square cell is positional - the pushed document carries no ids, so the index is the identity
                  key={k}
                  data-square-cell={k}
                  data-square-tone={cell.tone}
                  data-square-title={cell.title}
                  data-square-text={cell.text}
                  title={cell.title ?? cell.text}
                  style={{ background: toneColour(cell.tone) }}
                  className="inline-flex h-5 w-5 items-center justify-center rounded-sm text-[10px] leading-none"
                >
                  {cell.text}
                </span>
              ))}
            </div>
          ))}
        </div>
      ))}
    </div>
  );
}

/** One report tile: the newest reading of its series, drawn as its document.
 * The document carries its own header - eyebrow, title, lede - and sections
 * of the closed vocabulary: progress, cards, columns, squares. A tone is a
 * WORD the console maps to the palette; a word outside the set draws as no
 * tone at all. Sparks are metric refs with their own window, asked off the
 * series door at that window - a card spark and a series tile of the same
 * window share one ask. The age reads off the newest row's wall clock
 * exactly like every other tile. */
function ReportTile({
  tile,
  row,
  now,
  sparkOf,
}: {
  tile: DashboardTile;
  row: Artifact | undefined;
  now: number;
  sparkOf: (metric: string, points: number) => SeriesEntry | undefined;
}) {
  const age = row ? ageSeconds(row, now) : 0;
  const stale = row && (tile.stale_after_seconds ?? 0) > 0 && age > (tile.stale_after_seconds ?? 0);
  const read = row ? reportOf(row) : null;
  return (
    <div
      data-tile-label={tile.label}
      data-tile-kind="report"
      data-empty={row ? undefined : "true"}
      data-report-bad={row && !read?.ok ? "true" : undefined}
      data-stale={stale ? "true" : undefined}
      data-state={stateOf(row)}
      data-age={row && read?.ok ? age : undefined}
      className="flex flex-col gap-1 rounded-md border border-border p-4 sm:col-span-2 lg:col-span-4"
    >
      <div className="text-muted-foreground text-xs">{tile.label}</div>
      {!row ? (
        <div className="py-1 text-muted-foreground text-sm">
          its metric has no rows pushed yet - not zero, just nothing
        </div>
      ) : !read?.ok ? (
        <div className="py-1 text-muted-foreground text-sm">
          {read?.why === "empty"
            ? "a report with no sections is a page that shows nothing - the tile says so rather than drawing a blank card"
            : "its newest reading is not a report of {eyebrow, title, lede, sections} - the tile says so rather than drawing a page that lies"}
        </div>
      ) : (
        <>
          {read.doc.eyebrow && (
            <div
              data-report-eyebrow
              className="text-muted-foreground text-xs uppercase tracking-wide"
            >
              {read.doc.eyebrow}
            </div>
          )}
          {read.doc.title && (
            <div data-report-title className="font-semibold text-lg">
              {read.doc.title}
            </div>
          )}
          {read.doc.lede && (
            <div data-report-lede className="text-muted-foreground text-sm">
              {read.doc.lede}
            </div>
          )}
          {read.doc.sections.map((section, i) => (
            <div
              // biome-ignore lint/suspicious/noArrayIndexKey: a report's sections are positional - the pushed document carries no ids, so the index is the identity
              key={i}
              data-report-section={i}
              data-report-section-kind={section.kind}
              data-report-tone={section.tone}
              className="mt-2"
            >
              <ReportSectionView section={section} sparkOf={sparkOf} />
            </div>
          ))}
        </>
      )}
      <div className="flex items-baseline gap-2 text-muted-foreground text-xs" data-tile-age>
        <span>{ageWords(age)}</span>
        <span data-tile-state={stateOf(row)}>{stateOf(row)}</span>
        {stale ? <span>, stale</span> : null}
      </div>
    </div>
  );
}

/** The colour a log level tag takes, off the serenedash palette - fatal and
 * error the dim red-orange of crit, warn and warning the amber, info the blue
 * of ok, and the quiet levels the dim violet. A line with NO level draws no
 * tag: the store lets it through because a crash dump is exactly the line
 * most worth having, and a colour it did not earn would lie about it. */
const LOG_LEVEL_COLOUR: Record<string, string> = {
  FATAL: "var(--color-serenedash-3)",
  ERROR: "var(--color-serenedash-3)",
  WARN: "var(--color-serenedash-4)",
  WARNING: "var(--color-serenedash-4)",
  INFO: "var(--color-serenedash-1)",
  DEBUG: "var(--color-serenedash-8)",
  TRACE: "var(--color-serenedash-8)",
};

/** One log tile: the last lines of its stream, oldest first, with the level
 * counts the door already computed over the window. A log is prose, so the
 * tile draws the lines, never a trend - and a stream with nothing pushed says
 * so rather than drawing an empty list that reads as silence. */
function LogTile({ tile, tail }: { tile: DashboardTile; tail: LogTail | undefined }) {
  const counts = tail?.counts.levels ?? {};
  const empty = !tail || tail.lines.length === 0;
  return (
    <div
      data-tile-label={tile.label}
      data-tile-kind="log"
      data-log-empty={empty ? "true" : undefined}
      className="flex flex-col gap-1 rounded-md border border-border p-4"
    >
      <div className="text-muted-foreground text-xs">{tile.label}</div>
      {empty ? (
        <div className="py-1 text-muted-foreground text-sm">
          its stream has no lines pushed yet - not zero, just nothing
        </div>
      ) : (
        <>
          {Object.keys(counts).length > 0 && (
            <div data-log-counts className="flex flex-wrap gap-x-2 text-[10px]">
              {Object.entries(counts).map(([level, n]) => (
                <span
                  key={level}
                  data-log-count={level}
                  data-log-count-value={n}
                  style={{ color: LOG_LEVEL_COLOUR[level] ?? "var(--color-serenedash-8)" }}
                >
                  {level} {n}
                </span>
              ))}
            </div>
          )}
          <div className="flex flex-col gap-0.5 font-mono text-xs" data-log-lines>
            {tail.lines.map((line) => (
              <div key={line.id} data-log-line={line.message} className="truncate">
                {line.level ? (
                  <span
                    data-log-level={line.level}
                    style={{ color: LOG_LEVEL_COLOUR[line.level] ?? "var(--color-serenedash-8)" }}
                    className="mr-1"
                  >
                    {line.level}
                  </span>
                ) : null}
                {line.type ? <span data-log-type={line.type}>{`[${line.type}] `}</span> : null}
                <span>{line.message}</span>
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

const TRACE_ERROR_COLOUR = "var(--color-serenedash-3)";

/** One trace tile: the spans of the trace it names, in start order - the
 * console's waterfall. The only tile whose declaration is not a series name:
 * the tile carries the id and the id is the query. A span that failed is
 * drawn in the palette's severity colour; a trace that holds no spans
 * readable by this reader says so - the spans may exist and be somebody
 * else's, and that is not a broken page. */
function TraceTile({
  tile,
  trace,
  now,
}: { tile: DashboardTile; trace: Trace | undefined; now: number }) {
  const spans = trace?.spans ?? [];
  const empty = spans.length === 0;
  const errors = trace?.errors ?? 0;
  const nodes = trace?.nodes ?? [];
  const age = trace?.started ? ageSecondsAt(trace.started, now) : 0;
  return (
    <div
      data-tile-label={tile.label}
      data-tile-kind="trace"
      data-trace-empty={empty ? "true" : undefined}
      className="flex flex-col gap-1 rounded-md border border-border p-4"
    >
      <div className="text-muted-foreground text-xs">{tile.label}</div>
      {empty ? (
        <div className="py-1 text-muted-foreground text-sm">
          this trace holds no spans readable by you - they may exist and belong to somebody else
        </div>
      ) : (
        <>
          <div className="flex flex-wrap gap-x-2 text-[10px] text-muted-foreground" data-trace-head>
            <span data-trace-age={age}>{`traced ${ageWords(age)} ago`}</span>
            {errors > 0 && (
              <span data-trace-errors style={{ color: TRACE_ERROR_COLOUR }}>
                {`${errors} span(s) failed`}
              </span>
            )}
            {nodes.length > 0 && <span data-trace-nodes={nodes.join(",")}>{nodes.join(", ")}</span>}
          </div>
          <div className="flex flex-col gap-0.5 font-mono text-xs" data-trace-spans>
            {spans.map((s) => (
              <div key={s.span_id} data-trace-span={s.name} className="flex items-baseline gap-2">
                <span
                  data-trace-span-status={s.status ?? "ok"}
                  style={s.status === "error" ? { color: TRACE_ERROR_COLOUR } : undefined}
                  className="w-10 shrink-0"
                >
                  {s.status ?? "ok"}
                </span>
                <span className="flex-1 truncate">{s.name}</span>
                <span className="shrink-0 text-right tabular-nums text-muted-foreground">
                  {`${Math.max(1, Math.round(s.duration_us / 1000))}ms`}
                </span>
              </div>
            ))}
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
  const [logTails, setLogTails] = useState<Map<string, LogTail>>(new Map());
  const [traces, setTraces] = useState<Map<string, Trace>>(new Map());
  const [loaded, setLoaded] = useState(false);
  const [refused, setRefused] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());

  // The age recomputes on a second tick - see the file head.
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);

  // The data re-fetches on a slower beat than the age - every 30s. A page
  // that never asks again is a screenshot with a clock on it: the age ticks
  // up convincingly while the rows are frozen from page load.

  // One load: read the dashboard, then the metrics, then the series windows
  // it declares. A background refresh keeps the current drawing up until the
  // new one arrives, and a failed one leaves it - a transient refusal must
  // not blank a page that was drawing fine.
  const load = useCallback(
    (background: boolean) => {
      if (!signedIn || !id) {
        setDash(null);
        setRows([]);
        setSeriesPages(new Map());
        setLogTails(new Map());
        setLoaded(false);
        return;
      }
      let stopped = false;
      if (!background) {
        setLoaded(false);
        setRefused(false);
        setError(null);
      }
      dashboards
        .read(id)
        .then(async (artifact) => {
          if (stopped) return;
          setDash(artifact);
          const tiles = tilesOf(artifact);
          const names = [...new Set(tiles.map((t) => t.metric).filter(Boolean))];
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
            // A page may declare no series at all - a dashboard of only trace
            // tiles names no metric - so the metrics ask is skipped rather
            // than asked with an empty name list it would refuse. The log and
            // trace asks below still run: they do not read the metrics door.
            const page = names.length > 0 ? await dashboards.metrics(names) : { metrics: [] };
            // A report's cards may declare sparks - metric refs with their own
            // window. Those windows are only known once the metrics are read,
            // so they are asked after, not beside, the tiles' own series asks.
            for (const row of page.metrics ?? []) {
              const read = reportOf(row);
              if (!read.ok) continue;
              for (const section of read.doc.sections) {
                if (section.kind !== "cards") continue;
                for (const card of section.cards) {
                  if (!card.spark) continue;
                  const ns = byWindow.get(card.spark.points) ?? [];
                  ns.push(card.spark.metric);
                  byWindow.set(card.spark.points, ns);
                }
              }
            }
            const spages = await Promise.all(
              [...byWindow.entries()].map(async ([w, ns]) =>
                dashboards.series([...new Set(ns)], w).then((p) => [w, p] as const),
              ),
            );
            // The log tiles read their streams through the log door, one ask
            // per stream - like the series door, the door's shape is per
            // stream, so the page dedupes what the tiles name.
            const tails = await Promise.all(
              [
                ...new Set(tiles.filter((t) => t.kind === "log" && t.metric).map((t) => t.metric)),
              ].map(async (n) => [n, await dashboards.logTail(n)] as const),
            );
            // The trace tiles read the trace store by id - the one tile whose
            // declaration is its query. One ask per id, deduped like the log
            // door: two tiles naming the same trace read it once.
            const trs = await Promise.all(
              [
                ...new Set(
                  tiles.filter((t) => t.kind === "trace" && t.trace).map((t) => t.trace ?? ""),
                ),
              ].map(async (id) => [id, (await dashboards.traceById(id)).trace] as const),
            );
            if (!stopped) {
              setRows(page.metrics ?? []);
              setSeriesPages(new Map(spages));
              setLogTails(new Map(tails));
              setTraces(new Map(trs));
            }
          } catch (err) {
            // The tiles render their empty state; the declaration is still the
            // truth of the page even if the read of the data failed. A
            // background refresh keeps the last good drawing instead.
            if (!stopped && !background) setError(err instanceof Error ? err.message : String(err));
          }
        })
        .catch((err: unknown) => {
          if (stopped || background) return;
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
    },
    [signedIn, id],
  );

  // The first load for an identity is a full one (clears, loading line,
  // refusals); every beat after that refreshes in the background. The beat
  // lives here, on the load itself, so a re-arm on identity change is free.
  const foregroundKey = useRef("");
  useEffect(() => {
    const key = `${signedIn}:${id}`;
    const background = foregroundKey.current === key;
    void load(background);
    foregroundKey.current = key;
    const timer = setInterval(() => {
      void load(true);
      foregroundKey.current = key;
    }, 30000);
    return () => clearInterval(timer);
  }, [load, signedIn, id]);

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

  /** The series window one card spark declares - the page asked the door at
   * the sparks' windows too, after the metrics named them. */
  const sparkOf = (metric: string, points: number): SeriesEntry | undefined =>
    seriesPages.get(points)?.series.find((s) => s.name === metric);

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
          if (tile.kind === "log") {
            return <LogTile key={tile.label} tile={tile} tail={logTails.get(tile.metric)} />;
          }
          if (tile.kind === "trace") {
            return (
              <TraceTile
                key={tile.label}
                tile={tile}
                trace={traces.get(tile.trace ?? "")}
                now={now}
              />
            );
          }
          if (tile.kind === "report") {
            return (
              <ReportTile
                key={tile.label}
                tile={tile}
                row={rowsOf(tile.metric)[0]}
                now={now}
                sparkOf={sparkOf}
              />
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
