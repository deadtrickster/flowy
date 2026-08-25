/**
 * The cursor over a frame: what is under the pointer, answered the way
 * serenedash's hover.py answers it.
 *
 * Every answer comes out of the frame's own prose - its legend terms, its
 * panel one-liners - looked up by where the pointer is. Nothing is inferred
 * about a value: the tooltip names the panel and the row and repeats what
 * the producer wrote about that term, it never invents a reading of the
 * number. The alternative - a second table of hover text - is a second
 * place for the same prose to go stale, and the one that goes stale is
 * always the one nobody opens.
 *
 * Port of hover.py's pure half: segment_at, panel_at, the phrase lookup and
 * describe. Deliberately not ported: the anomaly hit (live producer state,
 * dropped from the frame contract by agreement) and the config hazards
 * branch (a frame that wants reasons carries them in its own legend
 * section). The grid constants default to serenedash's own (fmt.py) - the
 * console's default grid is the renderer's grid - and a frame's grid
 * overrides them.
 */

import type { FrameReading } from "@/lib/frame";

/** fmt.py:20 - COL_LABEL, COL_VALUE, COL_BAR = 22, 10, 18. The serenedash
 * column grid the pointer reads fields off; a frame that carries its own
 * grid overrides this. */
export const DEFAULT_GRID = { label: 22, value: 10, bar: 18 };

/** Placeholders in a legend term: `wal  N.NNx`, `N% of X on disk`. They
 * stand for the number, so they are not what anyone points at. */
const NOISE = /^[+-]?[NXT][%.\w/]*$|^(of|on|the|a|per|and)$/;

const WORD = /[A-Za-z_][A-Za-z_0-9]*/g;

// biome-ignore lint/suspicious/noControlCharactersInRegex: the ESC byte is the thing being matched - an SGR sequence is control characters, that is its definition
const SGR = /\x1b\[([0-9;]*)m/g;

/** A line without its SGR escapes - what the eye sees is what the pointer
 * geometry reads, so column math never counts an invisible escape. */
function strip(text: string): string {
  return text.replace(SGR, "");
}

/** (start column, text) of the box cell the column falls in. In wide mode
 * two panels share a line - `│ left │ │ right │` - so a row's label is not
 * at the start of the line, it is at the start of whichever cell was
 * pointed at. */
export function segmentAt(text: string, col: number): [number, string] {
  const starts: number[] = [];
  for (let i = 0; i < text.length; i++) if (text[i] === "│") starts.push(i + 1);
  if (starts.length === 0) return [0, text];
  const lo = starts.filter((p) => p <= col).at(-1) ?? 0;
  const hi = Math.min(...starts.filter((p) => p > col), text.length) - 1;
  return [lo, text.slice(lo, hi)];
}

/** Which panel the pointer is over, scanning upward from the pointer row.
 * Titles sit immediately after the box corner, and a line can carry two of
 * them. The view's own name outside the main frame. */
export function panelAt(lines: string[], row: number, col: number, view = "main"): string | null {
  if (view !== "main") return view;
  const spans: [number, string][] = [];
  for (let i = Math.min(row, lines.length - 1); i >= 0; i--) {
    const text = strip(lines[i]);
    spans.length = 0;
    for (const m of text.matchAll(/┌─(\S+)/g)) spans.push([m.index, m[1]]);
    if (spans.length === 0) continue;
    const hit = [...spans].reverse().find(([s]) => s <= col);
    return hit ? hit[1] : spans[0][1];
  }
  return null;
}

/** Words under the column, longest phrase first: `io wait` before `wait`.
 * Two-word labels are most of the vocabulary that needs explaining at all,
 * so matching a single word first would answer the wrong question every
 * time. */
export function phrases(text: string, col: number): string[] {
  const hits = [...text.matchAll(WORD)];
  const here = hits.findIndex((m) => m.index <= col && col < m.index + m[0].length);
  if (here === -1) return [];
  const out: string[] = [];
  for (const [lo, hi] of [
    [here - 1, here + 1],
    [here, here + 2],
    [here, here + 1],
  ]) {
    if (lo < 0 || hi > hits.length) continue;
    const span = text.slice(hits[lo].index, hits[hi - 1].index + hits[hi - 1][0].length);
    // Only if they really are adjacent words; a phrase spanning a column
    // gap is two labels that happen to sit next to each other, not a term.
    if (!span.includes("  ")) out.push(span.toLowerCase());
  }
  return out;
}

/** The legend index: {(section, key): (term, meaning)} and
 * {key: (section, term, meaning)}, built from the frame's legend. A
 * `columnar / search idx / spill` entry documents three labels in one, and
 * each of them is a row you can point at. */
function indexLegend(legend: Record<string, [string, string][]>) {
  // Nested rather than a composite key: the (section, key) pair is the
  // key, and no separator string can be guaranteed absent from a term.
  const bysec = new Map<string, Map<string, [string, string]>>();
  const anywhere = new Map<string, [string, string, string]>();
  for (const [section, items] of Object.entries(legend)) {
    const sec = bysec.get(section) ?? new Map<string, [string, string]>();
    bysec.set(section, sec);
    for (const [term, meaning] of items) {
      for (const alt of term.split("/")) {
        const words = alt
          .trim()
          .split(/\s+/)
          .filter((w) => !NOISE.test(w));
        for (const key of new Set([words.join(" "), ...words])) {
          if (!key) continue;
          const lk = key.toLowerCase();
          if (!sec.has(lk)) sec.set(lk, [term, meaning]);
          if (!anywhere.has(lk)) anywhere.set(lk, [section, term, meaning]);
        }
      }
    }
  }
  return { bysec, anywhere };
}

export interface CursorTip {
  title: string;
  body: string;
}

/** (title, body) for the point, or null. Title is `panel · term`. */
export function describe(
  lines: string[],
  row: number,
  col: number,
  frame: FrameReading,
  view = "main",
): CursorTip | null {
  if (!(row >= 0 && row < lines.length)) return null;
  const grid = { ...DEFAULT_GRID, ...(frame.grid ?? {}) };
  const legend = frame.legend ?? {};
  const panels = frame.panels ?? {};
  const { bysec, anywhere } = indexLegend(legend);
  const barAt = grid.label + grid.value + 2;

  const text = strip(lines[row]);
  let c = col;
  if (c >= text.trimEnd().length && view === "main") {
    c = Math.min(c, Math.max(0, text.length - 1));
  }
  const panel = panelAt(lines, row, c, view);
  const sec = panel !== null && panel in panels ? panel : null;
  const [start, seg] = segmentAt(text, c);
  const off = c - start;
  const ch = c >= 0 && c < text.length ? text[c] : " ";

  // The label is the first field of the cell, so it identifies the row
  // wherever in the row you are pointing - which is the whole point of
  // hovering a bar rather than its label.
  const label = seg.length > 2 ? seg.slice(0, grid.label).trim() : "";

  // A border or a title is the panel, not a row in it - a heading is the
  // one place you have asked about the panel itself.
  if (sec && /[┌└─┐┘]/.test(seg || text)) return { title: sec, body: panels[sec] };

  const lookups = [...phrases(seg, off), ...(label ? [label.toLowerCase()] : [])];
  for (const key of lookups) {
    const hit = sec ? bysec.get(sec)?.get(key) : undefined;
    if (hit) return { title: `${sec} · ${hit[0]}`, body: hit[1] };
  }
  // Only from the label field. In a tail the words are the producer's, not
  // the dashboard's - a term in the legend must not be answered from a
  // free-form tail that happens to spell it.
  if (off < grid.label || !sec) {
    for (const key of lookups) {
      const hit = anywhere.get(key);
      if (hit) {
        // Named in another panel's section. Say which, so a term that means
        // one thing under storage and another under memory is never quietly
        // answered from the wrong one.
        const where = hit[0] === sec ? "" : `  (legend: ${hit[0]})`;
        return { title: `${sec ?? hit[0]} · ${hit[1]}${where}`, body: hit[2] };
      }
    }
  }

  // Nothing named. The grid still knows what KIND of thing it is, and for a
  // bar or a trace that is the useful half of the answer: what it is a
  // share of.
  if (barAt <= off && off < barAt + grid.bar && (ch === "█" || ch === "░")) {
    return {
      title: `${sec ?? "panel"} · bar`,
      body: `\`${label}\` as a share of this row's own denominator, which the row's tail names. Every bar and its trace divide by the same thing.`,
    };
  }
  if ("·▁▂▃▄▅▆▇█".includes(ch) && off >= barAt + grid.bar) {
    return {
      title: `${sec ?? "panel"} · history`,
      body: `\`${label}\` over the recent past, oldest on the left, drawn against the same denominator as the bar on this row.`,
    };
  }
  if (sec) return { title: sec, body: panels[sec] };
  return null;
}

/** Where to put the tooltip box: under the pointer, flipped at an edge so it
 * is never clipped. Cell coordinates, same space as the frame. */
export function place(
  boxW: number,
  boxH: number,
  mx: number,
  my: number,
  width: number,
  height: number,
): [number, number] {
  const top = my + 1 + boxH <= height ? my + 1 : my - boxH;
  const left = mx + 1 + boxW <= width ? mx + 1 : width - boxW;
  return [Math.max(0, Math.min(top, height - boxH)), Math.max(0, left)];
}
