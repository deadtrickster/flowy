import { useCallback, useEffect, useRef, useState } from "react";

import { cn } from "@/lib/utils";

/**
 * A draggable edge between two panels, and the width it remembers.
 *
 * The operator: "make left/right panels resizable". The two columns this
 * console is made of are a hard w-60 and a hard w-[26rem], chosen by whoever
 * wrote them for a window they happened to have.
 *
 * LOCALSTORAGE, AND THAT IS THE OPPOSITE OF WHAT lib/unread.tsx DECIDED - which
 * is worth stating, because the two look like the same question and are not.
 * A reader mark is a claim about WHAT HAS BEEN READ and must be the same on
 * every device, so it lives on the node; a last-seen in the tab drifts from the
 * mark the rest of the system believes. A column width is a fact about THIS
 * SCREEN. A phone and a desk legitimately disagree about it, and storing it on
 * the node would make one of them wrong on purpose.
 *
 * BOUNDED, because a panel dragged to zero is a panel nobody can get back. The
 * caller gives a floor and a ceiling and the handle refuses to leave them - the
 * same rule the console applies to a control clipped by a scroll container: an
 * element you cannot reach is an element you do not have.
 *
 * A SEPARATOR, NOT A DIV WITH A CURSOR. role="separator" with aria-orientation
 * and the arrow keys, because a drag that only a mouse can perform hands the
 * layout to one kind of reader. Arrow keys move it by a step; Home and End take
 * it to the bounds.
 */
export function ResizeHandle({
  storageKey,
  min,
  max,
  edge,
  onWidth,
  label,
}: {
  /** Where this width is remembered. One key per panel. */
  storageKey: string;
  min: number;
  max: number;
  /**
   * Which side of the handle the panel is on. "left" means dragging right makes
   * the panel wider - the nav; "right" is the mirror - the room's side column.
   */
  edge: "left" | "right";
  onWidth: (px: number) => void;
  label: string;
}) {
  // TWO PIECES OF THE SAME FACT, deliberately. The ref is the one the move
  // handler reads, because it is set SYNCHRONOUSLY inside pointerdown; the
  // state exists only to repaint the handle, and a repaint one render late is
  // invisible. Reading the state in the move handler was the second version of
  // this bug: `dragging` is still false during the moves that arrive in the
  // same tick as the press.
  const active = useRef(false);
  const [dragging, setDragging] = useState(false);
  const width = useRef<number>(min);

  const clamp = useCallback((px: number) => Math.min(max, Math.max(min, px)), [min, max]);

  const set = useCallback(
    (px: number) => {
      const next = clamp(px);
      width.current = next;
      onWidth(next);
      try {
        localStorage.setItem(storageKey, String(next));
      } catch {
        // A browser that refuses storage still gets a working drag; it just
        // forgets. Silent because there is nothing the reader can do about it
        // and a toast about localStorage is noise, not information.
      }
    },
    [clamp, onWidth, storageKey],
  );

  // The remembered width, applied once. Read here rather than by the caller so
  // that the bounds and the key live in one place - a caller that read it
  // itself would be a second opinion about what is valid.
  useEffect(() => {
    let stored: string | null = null;
    try {
      stored = localStorage.getItem(storageKey);
    } catch {
      stored = null;
    }
    const px = Number(stored);
    // NOT `stored ? ... : ...`: "0" is a stored value and Number("") is 0, so a
    // truthiness test would take an empty key as a width of zero - the panel
    // this component exists to prevent.
    if (stored !== null && Number.isFinite(px) && px > 0) {
      width.current = clamp(px);
      onWidth(width.current);
    }
  }, [storageKey, clamp, onWidth]);

  // POINTER CAPTURE, NOT WINDOW LISTENERS. The first version attached
  // pointermove to the window from an effect keyed on `dragging`, and the
  // effect runs one render AFTER the pointerdown that set it - so the opening
  // frames of every drag were dropped. Measured rather than reasoned about: a
  // pointerdown followed immediately by moves changed nothing at all, and the
  // same moves 120ms later moved the panel to the pixel.
  //
  // On a slow render that is the whole gesture. setPointerCapture routes every
  // subsequent event for this pointer to this element, from the moment of the
  // press, however far the cursor travels - which is also what the window
  // listener was there to buy.
  const drag = useCallback(
    (event: React.PointerEvent<HTMLButtonElement>) => {
      set(edge === "left" ? event.clientX : window.innerWidth - event.clientX);
    },
    [edge, set],
  );

  return (
    <button
      type="button"
      // A separator is the right role and HTML has no tag for one, so the
      // element underneath is a button: focusable, key-driven and reachable by
      // TAB without anything being added by hand.
      //
      // The suppression was removed earlier in this same change, on the grounds
      // that biome did not object - and then reshaping the element made the rule
      // fire. A suppression is not dead because it is quiet today; it is quiet
      // because of how the code looks today.
      // biome-ignore lint/a11y/useSemanticElements: there is no <separator>
      role="separator"
      aria-orientation="vertical"
      aria-label={label}
      data-resize-handle={storageKey}
      tabIndex={0}
      className={cn(
        // A REAL SIX PIXELS, not one pixel with a pseudo-element pretending.
        // The first cut was w-px plus after:-left-1/-right-1, and a pointer
        // aimed at the middle of it hit NOTHING - measured by listening on the
        // element itself, which saw zero pointerdowns from a press right on it.
        // A target you can only hit by accident is the same defect as a control
        // clipped by a scroll container, which this console has had twice.
        //
        // The visible line is the inner span; the button is the grab area.
        "group relative flex w-1.5 shrink-0 cursor-col-resize items-stretch justify-center",
        "bg-transparent focus-visible:outline-none",
      )}
      onPointerDown={(event) => {
        event.preventDefault();
        active.current = true;
        setDragging(true);
        // Capture is an optimisation, not the mechanism: it keeps the events
        // coming when the cursor outruns a one-pixel strip. A synthetic pointer
        // with no id to capture must not take the drag down with it.
        try {
          event.currentTarget.setPointerCapture(event.pointerId);
        } catch {
          // no capture available; the drag still works while the pointer is over
          // the handle, and ends on pointerup as usual.
        }
      }}
      onPointerMove={(event) => {
        if (!active.current) return;
        drag(event);
      }}
      onPointerUp={(event) => {
        active.current = false;
        setDragging(false);
        try {
          event.currentTarget.releasePointerCapture(event.pointerId);
        } catch {
          // nothing was captured; nothing to release.
        }
      }}
      onPointerCancel={() => {
        active.current = false;
        setDragging(false);
      }}
      onKeyDown={(event) => {
        const step = event.shiftKey ? 40 : 8;
        if (event.key === "ArrowLeft") {
          event.preventDefault();
          set(width.current + (edge === "left" ? -step : step));
        } else if (event.key === "ArrowRight") {
          event.preventDefault();
          set(width.current + (edge === "left" ? step : -step));
        } else if (event.key === "Home") {
          event.preventDefault();
          set(min);
        } else if (event.key === "End") {
          event.preventDefault();
          set(max);
        }
      }}
    >
      <span
        aria-hidden="true"
        className={cn(
          "w-px transition-colors",
          dragging ? "bg-primary" : "bg-border group-hover:bg-primary/60",
        )}
      />
    </button>
  );
}
