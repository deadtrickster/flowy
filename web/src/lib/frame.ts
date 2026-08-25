/**
 * Frames: a pushed reading of ANSI lines, rendered exactly as the producer's
 * terminal drew them.
 *
 * The frame model (row 01M0XFJPBC68WT9FQY8C2PCVR6): a producer renders its
 * dashboard into ANSI lines and pushes them; this console draws the lines.
 * A frame is text, not markup - the producer cannot send a <rect> or a
 * <script>, so the only content that reaches the DOM here is the output of
 * `escape` below. That is the ANSI-not-SVG judgement: bounded text in, no
 * markup in.
 *
 * This module is a port of serenedash's export.py runs() + svg() - the pure
 * half, one frame in, one SVG string out. The page() half of export.py is not
 * ported: the console has its own chrome, so the SVG is embedded rather than
 * wrapped in an HTML document. The geometry is the export's own, because the
 * export is the "exact serenedash" the operator asked for.
 *
 * ## Why SVG and not a <pre>
 *
 * A terminal guarantees one glyph is one cell. A browser does not. Box-drawing
 * and block glyphs (┌ ─ █ ░ ▇) routinely resolve to a different font from the
 * ASCII beside them, with a different advance width, so a run of ─ stops being
 * exactly N cells and the right border walks a little further out on every
 * line. Pinning fixes it instead of hiding it: every styled run carries its
 * true column as `x` and an explicit `textLength`, so the glyphs are fitted
 * back onto the grid whatever font supplied them.
 */

/** One SGR colour the renderer understands. The producer's own palette is
 * deliberately fixed here rather than inherited from the terminal, because a
 * frame is read somewhere the terminal's theme does not exist. One Dark hues,
 * lifted in value for a browser's contrast (export.py's PAL). */
const PAL: Record<string, string> = {
  "30": "#6b7280",
  "31": "#ff8b96",
  "32": "#b5e08d",
  "33": "#f5d08a",
  "34": "#7cc4ff",
  "35": "#dc94ee",
  "36": "#6fd7e0",
  "37": "#e6eaf2",
};

/** Dim is a de-emphasis, not an erasure. At .55 the labels were close to
 * unreadable on a bright display, which is where a browser usually is. */
const DIM = 0.72;

/** Cell advance and line height in px. Any pair works - the pinning makes the
 * glyphs fit the cell rather than the cell follow the glyphs - so these only
 * set how large the frame renders. */
const CW = 7.22;
const LH = 15.0;

/** Glyphs that are not really text: a bar is a filled area that happens to be
 * spelled with characters. Drawn as text they inherit a font's glyph box,
 * which is not exactly the line pitch, so after the page scales the SVG by a
 * fractional factor the seams between rows land on different sub-pixels and
 * the bars look unevenly spaced. As rectangles they tile exactly at any
 * scale. The sparkline glyphs carry a height, which the terminal can only
 * express in eighths; a rect can draw the real fraction. */
const FILL: Record<string, [number, number]> = {
  "█": [1.0, 1.0],
  "▓": [1.0, 0.75],
  "▒": [1.0, 0.5],
  "░": [1.0, 0.22],
  "▁": [0.125, 1.0],
  "▂": [0.25, 1.0],
  "▃": [0.375, 1.0],
  "▄": [0.5, 1.0],
  "▅": [0.625, 1.0],
  "▆": [0.75, 1.0],
  "▇": [0.875, 1.0],
};

/** One styled run of a line. `col` is the count of visible characters before
 * the run, so a run knows where it belongs independently of how wide anything
 * renders - the column is what makes the render exact. */
export interface FrameRun {
  col: number;
  text: string;
  fg: string | null;
  bold: boolean;
  dim: boolean;
}

// biome-ignore lint/suspicious/noControlCharactersInRegex: the ESC byte is the thing being matched - an SGR sequence is control characters, that is its definition
const SGR = /\x1b\[([0-9;]*)m/g;

export function runs(line: string): FrameRun[] {
  const out: FrameRun[] = [];
  let col = 0;
  let fg: string | null = null;
  let bold = false;
  let dim = false;
  let pos = 0;
  for (const m of line.matchAll(SGR)) {
    const text = line.slice(pos, m.index);
    if (text) {
      out.push({ col, text, fg, bold, dim });
      col += text.length;
    }
    for (const code of m[1].split(";")) {
      if (code === "0" || code === "") {
        fg = null;
        bold = false;
        dim = false;
      } else if (code === "1") {
        bold = true;
      } else if (code === "2") {
        dim = true;
      } else if (code in PAL) {
        fg = PAL[code];
      }
    }
    pos = m.index + m[0].length;
  }
  const rest = line.slice(pos);
  if (rest) out.push({ col, text: rest, fg, bold, dim });
  return out;
}

/** Python's format spec, because the port must be byte-identical to
 * export.py: JS toFixed rounds half AWAY from zero, python's %.2f rounds half
 * to EVEN, and the cell geometry lands on exact .005 boundaries (LH * 0.375,
 * CW * 25). The export's own output is the reference, so its rounding is the
 * rounding. */
function fmt(v: number, d: number): string {
  const p = 10 ** d;
  const x = v * p;
  const r = Math.round(x);
  if (Math.abs(x - r) === 0.5 && r % 2 !== 0) return ((r - 1) / p).toFixed(d);
  return (r / p).toFixed(d);
}

/** `html.escape` for the one place pushed content reaches the DOM. Every
 * glyph the producer sends goes through here; nothing else the producer sends
 * exists to the DOM. */
function escapeXml(text: string): string {
  return text
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#x27;");
}

export interface FrameSvgOptions {
  /** Outer padding in px (export.py's default). */
  pad?: number;
  /** The frame's own background - a terminal is a dark room. */
  background?: string;
  /** Fixes the grid width instead of taking it from the widest line. It
   * matters whenever more than one frame is shown at the same scale: a page
   * that stretches each SVG to its container scales a narrow panel up more
   * than a wide one, so the same dashboard renders two frames in two
   * different font sizes. One grid, one scale. */
  cols?: number;
}

/** One SVG string for a list of ANSI lines, every run pinned to its column. */
export function frameSvg(lines: string[], opts: FrameSvgOptions = {}): string {
  const pad = opts.pad ?? 8;
  const background = opts.background ?? "#101318";
  const cols =
    opts.cols ??
    lines.reduce(
      (m, ln) =>
        Math.max(
          m,
          runs(ln).reduce((s, r) => s + r.text.length, 0),
        ),
      0,
    );
  const w = cols * CW + pad * 2;
  const h = lines.length * LH + pad * 2;
  const parts = [
    `<svg xmlns="http://www.w3.org/2000/svg" width="${fmt(w, 0)}" height="${fmt(h, 0)}" viewBox="0 0 ${fmt(w, 0)} ${fmt(h, 0)}" font-size="12" font-family="DejaVu Sans Mono,Liberation Mono,Menlo,Consolas,monospace">`,
    `<rect width="100%" height="100%" fill="${background}" rx="6"/>`,
  ];
  lines.forEach((line, row) => {
    const y = pad + (row + 1) * LH - 4;
    for (const { col, text, fg, bold, dim } of runs(line)) {
      if (!text.trim()) continue; // whitespace needs no element; the grid places the rest
      const colour = fg ?? PAL["37"];
      if ([...text].every((ch) => ch in FILL)) {
        // A bar or a sparkline. One rect per run of identical glyphs, so a
        // solid bar is a single rectangle and a trace is one per step.
        const top = pad + row * LH;
        let i = 0;
        while (i < text.length) {
          let j = i;
          while (j < text.length && text[j] === text[i]) j++;
          const [hfrac, alpha] = FILL[text[i]];
          const a = dim ? alpha * DIM : alpha;
          parts.push(
            `<rect x="${fmt(pad + (col + i) * CW, 2)}" y="${fmt(top + LH * (1 - hfrac), 2)}" width="${fmt((j - i) * CW, 2)}" height="${fmt(LH * hfrac, 2)}" fill="${colour}"${a < 1 ? ` opacity="${fmt(a, 2)}"` : ""}/>`,
          );
          i = j;
        }
        continue;
      }
      let attrs = ` fill="${colour}"`;
      if (bold) attrs += ' font-weight="700"';
      if (dim) attrs += ` opacity="${DIM}"`;
      parts.push(
        `<text x="${fmt(pad + col * CW, 2)}" y="${fmt(y, 0)}" ` +
          `textLength="${fmt(text.length * CW, 2)}" lengthAdjust="spacingAndGlyphs" ` +
          `xml:space="preserve"${attrs}>${escapeXml(text)}</text>`,
      );
    }
  });
  parts.push("</svg>");
  return parts.join("");
}
