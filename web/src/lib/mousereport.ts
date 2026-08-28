/**
 * Mouse reporting for a ghostty-web terminal.
 *
 * WHY THIS EXISTS. ghostty-web 0.4.0 has no mouse encoder. It exposes
 * hasMouseTracking() so a host can tell that the guest asked for mouse, and its
 * own mouse handlers do link hover and scrollbar dragging - nothing converts a
 * click into the bytes a program reads. So byobu's click-a-title-to-switch, and
 * every other mouse-driven TUI, does nothing until this does it.
 *
 * SGR 1006 ONLY, which is what everything modern negotiates:
 *
 *   ESC [ < button ; col ; row M     press
 *   ESC [ < button ; col ; row m     release
 *
 * The older X10 encoding packs coordinates into single bytes and breaks past
 * column 223, which is an ordinary width for this panel. Emitting only SGR is
 * the honest choice: a program that never asked for 1006 also never turned
 * tracking on, so nothing is sent to it at all.
 *
 * AND IT IS GATED ON THE GUEST HAVING ASKED. A shell sitting at a bash prompt
 * has not enabled tracking, and sending it mouse bytes would put escape
 * sequences into somebody's command line and break selecting text with the
 * mouse - which is the commoner case by far. Tracking off means these handlers
 * do nothing and the browser's own selection behaves as it always did.
 */

/** What ghostty exposes that this needs. Narrow on purpose. */
export interface MouseTarget {
  cols: number;
  rows: number;
  wasmTerm?: { hasMouseTracking(): boolean };
  /** DEC private modes, read straight off the parser. */
  getMode?(mode: number, isAnsi?: boolean): boolean;
}

/** SGR button codes. Wheel is 64 and 65 rather than a separate report. */
/** The motion bit in an SGR button code. */
const MOTION = 32;
const BUTTON_WHEEL_UP = 64;
const BUTTON_WHEEL_DOWN = 65;
const BUTTON_RELEASE_UNKNOWN = 3;

function modifiers(event: MouseEvent): number {
  // shift 4, alt 8, ctrl 16 - the standard bits, added to the button.
  return (event.shiftKey ? 4 : 0) + (event.altKey ? 8 : 0) + (event.ctrlKey ? 16 : 0);
}

/**
 * cellOf turns a pixel position into a 1-based terminal cell.
 *
 * DERIVED FROM THE CANVAS AND THE GRID rather than from font metrics: the
 * renderer knows its cell size and keeps it private, and box/cols is the same
 * number for any renderer that fills its canvas. It is also robust to the
 * canvas being scaled by the browser, which a metric read from the font is not.
 */
function cellOf(canvas: HTMLElement, term: MouseTarget, event: MouseEvent) {
  const box = canvas.getBoundingClientRect();
  if (box.width <= 0 || box.height <= 0 || term.cols <= 0 || term.rows <= 0) return null;
  const col = Math.floor(((event.clientX - box.left) / box.width) * term.cols) + 1;
  const row = Math.floor(((event.clientY - box.top) / box.height) * term.rows) + 1;
  // Clamped rather than dropped: a click one pixel outside the last cell is a
  // click on the last cell to the person who made it.
  return {
    col: Math.min(Math.max(col, 1), term.cols),
    row: Math.min(Math.max(row, 1), term.rows),
  };
}

function sgr(button: number, col: number, row: number, press: boolean): string {
  return `\x1b[<${button};${col};${row}${press ? "M" : "m"}`;
}

/**
 * attachMouseReporting wires a canvas to a terminal's input and returns the
 * detach.
 *
 * `send` is the same path a keystroke takes, because to the guest this IS a
 * keystroke: mouse reports arrive on stdin like anything else.
 */
export function attachMouseReporting(
  canvas: HTMLElement,
  term: MouseTarget,
  send: (data: string) => void,
): () => void {
  // WHICH TRACKING, NOT WHETHER. hasMouseTracking() is one boolean over four
  // different agreements, and they are not interchangeable:
  //
  //   1000  button events only - a program that asked for this and is sent
  //         motion gets a flood it never requested, on every pixel
  //   1002  button events plus motion WHILE A BUTTON IS DOWN - what tmux and
  //         byobu turn on, and what makes dragging a pane divider work
  //   1003  every motion, button or not
  //   1006  SGR encoding, which is the only one of the formats that can carry
  //         a column past 223
  //
  // So the modes are read individually where the library exposes them, and the
  // boolean is the fallback for a build that does not. Reporting in a format
  // the guest cannot parse is worse than not reporting: X10 encoding puts the
  // coordinate in a byte, and a click in column 300 arrives as garbage the
  // shell then executes.
  const mode = (n: number) => term.getMode?.(n) === true;
  const tracking = () =>
    mode(1000) || mode(1002) || mode(1003) || term.wasmTerm?.hasMouseTracking() === true;
  // Motion is only sent to somebody who asked for it. Without this, 1000 gets a
  // report per pixel.
  const wantsMotion = () => mode(1002) || mode(1003);
  const wantsAnyMotion = () => mode(1003);
  // If the guest never turned SGR on we stay quiet rather than guessing a
  // format. Nothing here can encode X10 safely at these terminal widths, and
  // the honest answer to "cannot report" is silence, not a wrong report.
  const sgrFormat = () => mode(1006) || term.getMode === undefined;

  // WHICH BUTTON IS DOWN, because SGR motion reports carry it and a release
  // does not say. Held here rather than read off event.buttons so that a drag
  // that began outside the canvas does not report a button nobody pressed here.
  let held: number | null = null;
  // The last cell reported, so a drag across one character sends one report
  // rather than one per pixel. tmux redraws on each, and at 60Hz over a socket
  // that is what makes a resize feel like it is fighting back.
  let lastCell = "";

  const reportable = () => tracking() && sgrFormat();

  const down = (event: MouseEvent) => {
    if (!reportable()) return;
    const at = cellOf(canvas, term, event);
    if (!at) return;
    // The default is a text selection, which is not what the guest asked for
    // when it turned tracking on.
    event.preventDefault();
    held = event.button;
    lastCell = `${at.col},${at.row}`;
    send(sgr(event.button + modifiers(event), at.col, at.row, true));
  };

  const up = (event: MouseEvent) => {
    if (!reportable()) return;
    const at = cellOf(canvas, term, event);
    if (!at) return;
    event.preventDefault();
    held = null;
    lastCell = "";
    // A release reports button 3 in SGR - which button came up is carried by
    // the terminating `m`, not by the code.
    send(sgr(BUTTON_RELEASE_UNKNOWN + modifiers(event), at.col, at.row, false));
  };

  // MOTION, ONLY FOR SOMEBODY WHO ASKED. 1002 is motion while a button is
  // down - the dragging case - and 1003 is all of it. A program on 1000 gets
  // none, which is the whole point of reading the modes apart.
  const move = (event: MouseEvent) => {
    if (!reportable() || !wantsMotion()) return;
    if (held === null && !wantsAnyMotion()) return;
    const at = cellOf(canvas, term, event);
    if (!at) return;
    const cell = `${at.col},${at.row}`;
    if (cell === lastCell) return;
    lastCell = cell;
    // 32 is the motion bit. A drag carries the held button; a bare 1003 motion
    // carries 3, which is "no button" in the same place a release puts it.
    const button = (held ?? BUTTON_RELEASE_UNKNOWN) + MOTION + modifiers(event);
    send(sgr(button, at.col, at.row, true));
  };

  const wheel = (event: WheelEvent) => {
    if (!reportable()) return;
    const at = cellOf(canvas, term, event);
    if (!at) return;
    event.preventDefault();
    const button = event.deltaY < 0 ? BUTTON_WHEEL_UP : BUTTON_WHEEL_DOWN;
    send(sgr(button + modifiers(event), at.col, at.row, true));
  };

  canvas.addEventListener("mousedown", down);
  canvas.addEventListener("mouseup", up);
  canvas.addEventListener("mousemove", move);
  // Not passive: a scroll inside a tracking terminal belongs to the guest, and
  // preventDefault is how the page stops scrolling underneath it.
  canvas.addEventListener("wheel", wheel, { passive: false });

  return () => {
    canvas.removeEventListener("mousedown", down);
    canvas.removeEventListener("mouseup", up);
    canvas.removeEventListener("mousemove", move);
    canvas.removeEventListener("wheel", wheel);
  };
}
