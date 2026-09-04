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
  collapsed,
  onToggleCollapsed,
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
  /**
   * Whether the panel beside this handle is collapsed, and how to flip it.
   *
   * BOTH OR NEITHER. Passing one without the other would give a handle that
   * knows the panel is collapsed and cannot say so, or a control that toggles
   * something nothing renders. The pair is optional together: a caller with no
   * collapse simply does not draw the button.
   */
  collapsed?: boolean;
  onToggleCollapsed?: () => void;
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

  const separator = (
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
        // touch-none IS THE FIX FOR THE DRAG THAT STOPPED PART WAY, and it is
        // not the same thing as pointer capture. The operator, from a Fold 8:
        // "i try to drag the separator and it does follow for some pixels and
        // then stops, so i have to do it multiple times".
        //
        // With the default touch-action a touch screen browser spends the first
        // few pixels of a gesture deciding whether it is a scroll. When it
        // decides yes it takes the gesture, fires pointercancel, and every
        // later move goes to the scroller - which is exactly "follows for some
        // pixels and then stops", and why onPointerCancel below then clears
        // `active`. setPointerCapture does NOT prevent that: capture routes the
        // events this element would get, and the browser has stopped sending
        // any. Only touch-action tells it not to arbitrate in the first place.
        //
        // A MOUSE NEVER SAW IT, which is why the handle read as correct: there
        // is no scroll to lose the gesture to, so every desktop drag worked and
        // the bug lived only where nobody was testing.
        "group relative flex shrink-0 cursor-col-resize touch-none items-stretch justify-center",
        // SIX PIXELS IS A MOUSE TARGET AND A FINGER IS NOT A MOUSE. The comment
        // below argues six against one, which was the right argument against a
        // one-pixel strip and still leaves a target a finger hits by luck. The
        // grab area widens on a coarse pointer; the line it draws does not, so
        // the layout is unchanged on a desk.
        "w-1.5 pointer-coarse:w-6",
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

  if (!onToggleCollapsed) return separator;

  // A COLLAPSE THAT CAN BE UNDONE FROM WHERE IT HAPPENED. The operator asked
  // for a collapsible nav; the trap the bounds above already guard against is
  // a panel with no way back, so the control stays put when the panel goes -
  // it sits on the separator, which is still on screen at width zero.
  //
  // A SIBLING, NOT A CHILD. The separator is a <button> and a button inside a
  // button is invalid HTML that browsers silently reparent, which would have
  // moved the control out of the strip it is positioned against.
  return (
    <div className="relative hidden shrink-0 items-stretch md:flex">
      {collapsed ? null : separator}
      <button
        type="button"
        onClick={onToggleCollapsed}
        aria-label={collapsed ? `show ${label}` : `hide ${label}`}
        aria-expanded={!collapsed}
        className={cn(
          // Centred on the seam and pulled half its own width across it, so it
          // reads as belonging to the edge rather than to either panel.
          "-translate-y-1/2 absolute top-1/2 z-10 flex h-10 w-4 items-center justify-center",
          "rounded-sm border border-border bg-card text-muted-foreground",
          "hover:text-foreground focus-visible:outline-none focus-visible:ring-1",
          // VISIBLE WHEN COLLAPSED, QUIET WHEN NOT. Once the panel is gone this
          // is the only way back, so it cannot be hover-only - a touch screen
          // has no hover and the operator is on one.
          collapsed
            ? "left-0 opacity-100"
            : "-left-2 opacity-0 focus-visible:opacity-100 group-hover/nav:opacity-100 pointer-coarse:opacity-100",
        )}
      >
        <span aria-hidden="true" className="text-xs leading-none">
          {collapsed ? "\u203a" : "\u2039"}
        </span>
      </button>
    </div>
  );
}
