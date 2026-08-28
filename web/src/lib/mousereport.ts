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
}

/** SGR button codes. Wheel is 64 and 65 rather than a separate report. */
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
  const tracking = () => term.wasmTerm?.hasMouseTracking() === true;

  const down = (event: MouseEvent) => {
    if (!tracking()) return;
    const at = cellOf(canvas, term, event);
    if (!at) return;
    // The default is a text selection, which is not what the guest asked for
    // when it turned tracking on.
    event.preventDefault();
    send(sgr(event.button + modifiers(event), at.col, at.row, true));
  };

  const up = (event: MouseEvent) => {
    if (!tracking()) return;
    const at = cellOf(canvas, term, event);
    if (!at) return;
    event.preventDefault();
    // A release reports button 3 in SGR - which button came up is carried by
    // the terminating `m`, not by the code.
    send(sgr(BUTTON_RELEASE_UNKNOWN + modifiers(event), at.col, at.row, false));
  };

  const wheel = (event: WheelEvent) => {
    if (!tracking()) return;
    const at = cellOf(canvas, term, event);
    if (!at) return;
    event.preventDefault();
    const button = event.deltaY < 0 ? BUTTON_WHEEL_UP : BUTTON_WHEEL_DOWN;
    send(sgr(button + modifiers(event), at.col, at.row, true));
  };

  canvas.addEventListener("mousedown", down);
  canvas.addEventListener("mouseup", up);
  // Not passive: a scroll inside a tracking terminal belongs to the guest, and
  // preventDefault is how the page stops scrolling underneath it.
  canvas.addEventListener("wheel", wheel, { passive: false });

  return () => {
    canvas.removeEventListener("mousedown", down);
    canvas.removeEventListener("mouseup", up);
    canvas.removeEventListener("wheel", wheel);
  };
}
