import { useEffect, useRef, useState } from "react";

import { attachMouseReporting } from "@/lib/mousereport";

import { attachAgent, sendAgentControl, sendAgentInput, stopAgent } from "@/lib/agentsocket";

// GHOSTTY IS NOT IMPORTED AT MODULE SCOPE, and that is measured rather than
// stylistic. Importing it here put it in the main bundle - two megabytes of
// wasm-loading code on every page - and the console then MOUNTED NOTHING AT
// ALL: the gate's dom check said "the app mounted nothing into #root", because
// whatever the library reaches for at import time is not there when the bundle
// is evaluated outside a full browser. One panel took down every page.
//
// So it is a dynamic import inside run(), which is also the moment somebody
// has asked for a terminal and is willing to wait for one.
type GhosttyModule = typeof import("ghostty-web");
type GhosttyTerminal = InstanceType<GhosttyModule["Terminal"]>;
type GhosttyFit = InstanceType<GhosttyModule["FitAddon"]>;

/**
 * A shell running in a firecode microVM, drawn here.
 *
 * ONE SOCKET, SEVERAL STREAMS, AND CONTROL OUTRANKS OUTPUT. The frame's first
 * byte names the stream and the rest is that stream's payload - raw bytes for
 * the terminal, JSON for control. The node's send loop drains control before
 * output, so an exit notice cannot queue behind a megabyte of scrollback. See
 * internal/flowy/agent_ws.go, where the priority actually lives; this end only
 * has to read the tag.
 *
 * GHOSTTY AND NOT A <pre>. Terminal output is escape sequences, not text: a
 * pre that appended bytes would render a shell's cursor movements as garbage
 * and its colours as literal ESC[ - which looks like a relay that is broken
 * rather than a renderer that was never written.
 */

/**
 * What the panel is doing, as one value rather than several booleans.
 *
 * THREE STATES AND NOT TWO. "nothing started" and "starting" and "the VM is
 * gone" are different things to a person looking at an empty black rectangle,
 * and a single `running` flag would draw the first and the third identically.
 */
type ShellState = "idle" | "starting" | "live" | "ended";

/**
 * The session this panel last held, per project.
 *
 * Kept in localStorage because the thing it has to survive is a RELOAD, and it
 * is per project because two projects are two guests and adopting the wrong one
 * would put somebody in a shell on a machine they did not ask for.
 *
 * Every read and write is guarded: localStorage throws in a private window and
 * in some embedded views, and a terminal panel must not fail to open because a
 * browser declined to remember something.
 */
// PER SLOT AS WELL AS PER PROJECT. It was per project alone, which was right
// while a page held one terminal and became wrong the moment a strip of tabs
// landed: every tab would remember the same id, and reattaching on mount would
// point all of them at ONE shell - several terminals drawing the same session
// and racing each other's keystrokes.
const heldKey = (project: string, slot: number) =>
  `flowy.vmshell.session.${project || "-"}.${slot}`;

function readHeldSession(project: string, slot: number): string {
  try {
    return window.localStorage.getItem(heldKey(project, slot)) ?? "";
  } catch {
    return "";
  }
}

function rememberSession(project: string, slot: number, id: string) {
  try {
    window.localStorage.setItem(heldKey(project, slot), id);
  } catch {
    // A browser that will not remember is not a reason to refuse a terminal.
  }
}

function forgetSession(project: string, slot: number) {
  try {
    window.localStorage.removeItem(heldKey(project, slot));
  } catch {
    // As above.
  }
}

export function VmShell({
  project,
  slot = 0,
  mux = "",
}: {
  project: string;
  slot?: number;
  /**
   * A byobu session on the host to join, by name, instead of the project's own.
   *
   * This is how a session flowy never started becomes reachable from the panel:
   * the list hands the name it read off tmux, and the node joins THAT rather
   * than deriving one from the project. Empty is the ordinary case - the
   * project's own session, which is what Run with nothing chosen means.
   */
  mux?: string;
}) {
  const box = useRef<HTMLDivElement | null>(null);
  const term = useRef<GhosttyTerminal | null>(null);
  // The detach for this panel's slot, so unmounting stops carrying the session
  // without ending it.
  const detach = useRef<(() => void) | null>(null);
  // The fit addon, kept so docking or floating can refit at the moment the box
  // changes shape rather than waiting on the observer's debounce.
  const fitter = useRef<GhosttyFit | null>(null);
  const unmouse = useRef<(() => void) | null>(null);
  const floatBox = useRef<HTMLDivElement | null>(null);
  // WHERE THE FLOATING PANEL HAS BEEN PUT, or null for "wherever it opens".
  // Null is not (0,0): a panel that has never been dragged sits bottom-right by
  // its class, and storing a position it never had would move it on first
  // render for no reason anybody asked for.
  const [at, setAt] = useState<{ x: number; y: number } | null>(null);
  // Whether the shell told us how it ended. A ref rather than state: onclose
  // fires outside React's batching and must read the value as it is NOW, not
  // as it was when the handler closed over it.
  const heard = useRef(false);
  const adopting = useRef(false);
  const [state, setState] = useState<ShellState>("idle");
  // WHICH MACHINE. Two values, and neither is a default that could be arrived
  // at by accident - the operator asked for host shells and the difference is
  // whether what you type can reach this machine.
  const [where, setWhere] = useState<"vm" | "host">("vm");
  // DOCKED OR FLOATING. Floating is not a second component: the same panel,
  // the same session, the same socket - only the box around it moves. Making
  // it a second component would be two terminals to keep in step and a
  // reattach every time somebody undocked.
  const [floating, setFloating] = useState(false);
  const [why, setWhy] = useState("");
  const [session, setSession] = useState("");

  // Tearing down on unmount is not tidiness: the node stops the VM when the
  // socket closes, so leaving one open is a microVM running because somebody
  // navigated away.
  // COMING BACK IS NOT STARTING. Navigating to another page unmounts this
  // panel, which detaches; the session keeps running on the node. Without this
  // the panel came back idle and the shell looked lost until somebody pressed
  // Run - which adopted it and made it reappear, so nothing was ever gone.
  //
  // adopt: true, so opening this page never boots a VM. A remembered id that no
  // longer resolves leaves the panel idle.
  useEffect(() => {
    if (!readHeldSession(project, slot)) return;
    void run(true);
    // Mount only: re-running this on every render would reattach in a loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [project, slot]);

  useEffect(() => {
    return () => {
      detach.current?.();
      detach.current = null;
      unmouse.current?.();
      unmouse.current = null;
      term.current?.dispose();
    };
  }, []);

  // adopt: come back to a shell that is already running, and do nothing at all
  // if there is not one. The Run button passes false; the mount effect passes
  // true. They are different questions - see the effect below.
  // DRAGGING THE FLOATING PANEL.
  //
  // POINTER EVENTS, not mouse: one path covers a trackpad, a touchscreen and a
  // pen, and setPointerCapture is what keeps a fast drag from being lost when
  // the pointer outruns the handle - a mousemove listener on the element alone
  // drops the drag the moment the cursor leaves it.
  const startDrag = (event: React.PointerEvent<HTMLDivElement>) => {
    const box = floatBox.current;
    if (!box) return;
    // From what the browser has laid out, so the FIRST drag starts where the
    // panel already is rather than jumping to a computed origin.
    const rect = box.getBoundingClientRect();
    const grabX = event.clientX - rect.left;
    const grabY = event.clientY - rect.top;
    const handle = event.currentTarget;
    handle.setPointerCapture(event.pointerId);
    event.preventDefault();

    const move = (e: PointerEvent) => {
      // KEPT ON SCREEN. A panel dragged past the edge is one that cannot be
      // dragged back, and its handle is the first thing to go.
      const maxX = Math.max(0, window.innerWidth - rect.width);
      const maxY = Math.max(0, window.innerHeight - rect.height);
      setAt({
        x: Math.min(Math.max(0, e.clientX - grabX), maxX),
        y: Math.min(Math.max(0, e.clientY - grabY), maxY),
      });
    };
    const done = () => {
      handle.removeEventListener("pointermove", move);
      handle.removeEventListener("pointerup", done);
      handle.removeEventListener("pointercancel", done);
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", done);
    // pointercancel too: a touch that becomes a scroll gesture, or a pointer
    // the browser takes away, ends the drag without ever sending pointerup.
    handle.addEventListener("pointercancel", done);
  };

  const run = async (adopt = false) => {
    if (state === "starting" || state === "live") return;
    adopting.current = adopt;
    setState("starting");
    setWhy("");
    heard.current = false;

    // The wasm is loaded on demand rather than at page load. It is 2MB, and
    // every console page would pay for it to serve the one panel that draws a
    // terminal.
    // The library AND its wasm are fetched here rather than at page load.
    const { FitAddon, Terminal, init: initGhostty } = await import("ghostty-web");
    await initGhostty();

    const t = new Terminal({ fontSize: 13, fontFamily: "ui-monospace, monospace" });
    const fit = new FitAddon();
    fitter.current = fit;
    t.loadAddon(fit);
    if (box.current) {
      box.current.replaceChildren();
      t.open(box.current);
      fit.fit();
      // AND IT FOLLOWS THE CONTAINER FROM NOW ON. fit() once at open sizes the
      // terminal to whatever the box happened to be at that instant and never
      // again, so dragging the pane or floating the panel left the guest
      // wrapping at the old width. observeResize is the addon's own
      // ResizeObserver and is OPT-IN - not calling it was the whole of "it
      // should fit height/width of its container".
      fit.observeResize();
    }
    term.current = t;

    // ws:// or wss:// to match how this page was served. Hard-coding either is
    // a panel that works on one deployment.
    // THE SESSION THIS PANEL LAST HELD, so a reload comes back to its own
    // shell rather than starting a second VM beside the first. The node adopts
    // a live session named here and mints a new one otherwise, so a stale id
    // after a node restart costs nothing.
    //
    // localStorage rather than component state: surviving a RELOAD is the whole
    // point, and state does not.
    const held = readHeldSession(project, slot);

    // THE SOCKET IS NOT THIS COMPONENT'S. lib/agentsocket owns one connection
    // for the page and routes frames by slot, so several panels are several
    // slots rather than several sockets - which is what the slot byte on the
    // wire is for. See its head comment.
    detach.current = attachAgent(
      slot,
      { session: held, project, where, mux, rows: t.rows, cols: t.cols, adopt },
      {
        out: (bytes) => t.write(bytes),
        control: (c) => {
          if (c.type === "hello") {
            // ADOPTED, SO THIS IS AN ORDINARY SESSION NOW. Any error after this
            // point is the shell's, and gets reported rather than swallowed as
            // a reattach that found nothing.
            adopting.current = false;
            setSession(c.id ?? "");
            setState("live");
            // REMEMBERED ONLY ONCE THE NODE HAS NAMED IT. Writing the id we
            // asked for would remember a session that was never adopted.
            if (c.id) rememberSession(project, slot, c.id);
            // WHAT WAS DROPPED IS SAID. A reader that joins late is told how
            // much it will never see rather than handed a prefix that looks
            // whole.
            if (c.dropped && c.dropped > 0) {
              t.write(
                `\r\n[${c.dropped} bytes of earlier output are no longer held by the node]\r\n`,
              );
            }
          } else if (c.type === "error" && adopting.current) {
            // A REATTACH THAT FOUND NOTHING IS NOT A FAILURE. The remembered
            // shell is gone - node restarted, or somebody stopped it - so the
            // panel goes back to offering Run rather than reporting an error
            // for something nobody asked for.
            forgetSession(project, slot);
            setState("idle");
            detach.current?.();
            detach.current = null;
          } else if (c.type === "exited" || c.type === "error") {
            setState("ended");
            // THE SHELL'S OWN VERDICT, recorded as HEARD so that losing the
            // wire afterwards cannot overwrite it.
            heard.current = true;
            setWhy(c.why || "the shell ended without saying why");
          }
        },
        lost: (why) => {
          setState((current) => (current === "ended" ? current : "ended"));
          // TWO DIFFERENT FACTS, KEPT APART. The exited frame is the SHELL'S
          // verdict; this is THIS BROWSER losing the wire, which says nothing
          // about the guest - the VM may still be up. Collapsing them was the
          // whole of the complaint that shells "randomly exit".
          if (heard.current) return;
          setWhy(why);
        },
      },
    );

    // Keystrokes out. onData is already the encoded bytes for the key,
    // including the escape sequences for arrows and function keys, which is
    // most of the reason to use a terminal emulator rather than read keydown.
    t.onData((data: string) => sendAgentInput(slot, data));

    // MOUSE, WHICH THE LIBRARY DOES NOT ENCODE. ghostty-web knows the guest
    // turned tracking on and has no encoder for it, so clicking a byobu window
    // title did nothing. See lib/mousereport - it is gated on the guest having
    // asked, so a plain bash prompt keeps ordinary text selection.
    if (box.current) {
      unmouse.current?.();
      unmouse.current = attachMouseReporting(box.current, t, (data) => sendAgentInput(slot, data));
    }

    // And the shape, so the guest wraps where this panel wraps. A pty defaults
    // to 0x0 and a shell on a zero-sized terminal draws nothing sensible.
    const tellSize = (cols: number, rows: number) =>
      sendAgentControl(slot, { type: "resize", rows, cols });
    t.onResize(({ cols, rows }: { cols: number; rows: number }) => tellSize(cols, rows));
  };

  const stop = () => {
    // STOPPING IS DELIBERATE AND IS SAID, which closing the socket no longer
    // is: a closed socket now means "this browser went away" and leaves the VM
    // running. Ending it is a message.
    forgetSession(project, slot);
    stopAgent(slot);
    detach.current?.();
    detach.current = null;
    setState("ended");
    setWhy("stopped from the panel");
  };

  // REFIT THE MOMENT THE BOX CHANGES, rather than waiting for the observer.
  // The observer debounces, which is right for a drag and wrong for a switch
  // that changes the shape in one step - the terminal would sit at the old
  // size for a beat with the guest wrapping to match.
  useEffect(() => {
    fitter.current?.fit();
  }, []);

  const panel = (
    <section
      className={
        floating ? "flex h-full min-h-0 flex-col gap-2" : "flex min-h-0 flex-1 flex-col gap-2"
      }
      data-vm-shell=""
      data-vm-shell-state={state}
      // WHICH GUEST THIS PANEL WOULD ADOPT. The remembered session is keyed on
      // it, so a reader that cannot see the project cannot tell a panel that
      // reattached from one that had nothing to reattach to.
      data-vm-shell-project={project}
      // WHICH SESSION THIS PANEL WOULD JOIN, or empty for the project's own.
      // On the element rather than only in the attach message, because a person
      // looking at two tabs needs to know which session each is showing - and
      // because the attach frame is unobservable to a console holding a token
      // rather than a login, the socket being refused before it is sent.
      data-vm-shell-mux={mux}
      data-vm-shell-floating={floating ? "yes" : "no"}
    >
      <div className="flex items-center gap-2">
        <button
          type="button"
          data-vm-shell-run=""
          className="rounded border border-border px-2 py-1 text-xs hover:bg-muted disabled:opacity-50"
          disabled={state === "starting" || state === "live"}
          onClick={() => void run()}
        >
          {state === "starting"
            ? // WHAT IS ACTUALLY STARTING. This said "bringing a VM up" whatever
              // the selector held, so choosing "this host" and pressing Run
              // reported a VM boot that was never happening - and the operator
              // reasonably read it as the selector being ignored.
              where === "host"
              ? "opening a shell on this host…"
              : "bringing a VM up…"
            : "run a shell"}
        </button>
        {state === "live" ? (
          <button
            type="button"
            data-vm-shell-stop=""
            className="rounded border border-border px-2 py-1 text-xs hover:bg-muted"
            onClick={stop}
          >
            stop
          </button>
        ) : null}
        {/* THE SELECTOR, and it says which machine rather than which mode.
            "vm" and "host" are places; a person about to type a command needs
            to know which one they are typing into, and that is why the live
            panel repeats it below rather than only offering it here. */}
        <label className="flex items-center gap-1 text-muted-foreground text-xs">
          on
          <select
            data-vm-shell-where=""
            className="rounded border border-border bg-background px-1 py-0.5 text-foreground text-xs"
            value={where}
            disabled={state === "starting" || state === "live"}
            onChange={(event) => setWhere(event.target.value === "host" ? "host" : "vm")}
          >
            <option value="vm">a microVM</option>
            <option value="host">this host</option>
          </select>
        </label>
        <button
          type="button"
          data-vm-shell-float-toggle=""
          className="rounded border border-border px-2 py-1 text-xs hover:bg-muted"
          onClick={() => {
            setFloating((on) => !on);
            // The box changes shape in the same tick; refit on the next one,
            // once the browser has laid it out.
            window.requestAnimationFrame(() => fitter.current?.fit());
          }}
        >
          {floating ? "dock" : "float"}
        </button>
        {/*
          A REAL BROWSER WINDOW, which is a different thing from floating. The
          float control moves the panel around this page; this puts the terminal
          on a monitor of its own, outside the console, where the window manager
          can tile it.

          IT OPENS A URL RATHER THAN MOVING THE TERMINAL. A ghostty instance
          cannot be handed to another document - its canvas, its wasm and its
          listeners belong to this one - so the new window mounts its own panel,
          which then adopts the SAME SESSION: the id is in localStorage, which is
          per origin, and the node fans a session to every reader. Two windows,
          one shell, both able to type.

          noopener, so the opened window cannot reach back into this one through
          window.opener. It has no need to and it is a console with an operator
          session in it.
        */}
        <button
          type="button"
          data-vm-shell-window=""
          className="rounded border border-border px-2 py-1 text-xs hover:bg-muted"
          onClick={() => {
            const where = new URL("/shell", window.location.origin);
            where.searchParams.set("project", project);
            where.searchParams.set("slot", String(slot));
            window.open(
              where.toString(),
              // A NAME PER SLOT, so pressing it twice raises the window that is
              // already open instead of stacking a second one on the same shell.
              `flowy-shell-${project}-${slot}`,
              "noopener,width=1100,height=700",
            );
          }}
        >
          window
        </button>
        {session ? (
          <span data-vm-shell-session={session} className="text-muted-foreground text-xs">
            {session.slice(0, 10)}
          </span>
        ) : null}
        {why ? (
          <span data-vm-shell-why="" className="text-muted-foreground text-xs">
            {why}
          </span>
        ) : null}
      </div>
      {/* SAID RATHER THAN LEFT BLANK. An empty black rectangle before anything
          is started reads as a terminal that failed to connect. */}
      {state === "idle" ? (
        <p data-vm-shell-empty="" className="text-muted-foreground text-xs">
          {where === "host"
            ? "nothing is running. Run a shell opens a shell ON THIS HOST - the machine serving this console, with its files and its user. There is no VM between you and it."
            : "nothing is running. Run a shell brings up a microVM and puts its shell here - what you type goes to the guest, not to this machine."}
        </p>
      ) : null}
      {/* SAID WHILE IT IS RUNNING TOO. A prompt looks the same on either
          machine, and "which machine am I on" is not a question a person should
          have to answer from memory of what they picked a while ago. */}
      {state === "live" ? (
        <p data-vm-shell-on={where} className="text-muted-foreground text-xs">
          {where === "host"
            ? "this shell is on THIS HOST - no VM between you and the machine"
            : "this shell is in a microVM - nothing here reaches the host"}
        </p>
      ) : null}
      {/*
        HOW TALL, AND IT IS THE SPACE THAT IS ACTUALLY LEFT.

        The shell hands a real height down: html, body and #root are 100%, the
        frame is `flex h-full`, and main is a flex column with overflow-hidden
        wrapping its children in `min-h-0 flex-1`. So there IS a definite height
        to divide - the chain from /vms down to here simply stopped passing it
        on, and flex-1 with no definite ancestor resolved to the content height,
        which is this terminal. Circular, and it collapsed to 2px of border.

        60vh was the first fix and it was a guess: right shape, wrong number,
        and it ignored the space to either side of it. Now every link in the
        chain carries min-h-0 flex-1 and the terminal takes what the header and
        the form above it leave, which is what "fit its container" meant.

        min-h-0 on every one of them, because a flex item's default min-height
        is auto - it refuses to shrink below its content - and one missing
        min-h-0 anywhere up the chain puts the whole thing back. No pixel floor
        here for the same reason: a floor and min-h-0 contradict each other, and
        a container with a real height does not need one.
      */}
      <div
        ref={box}
        data-vm-shell-screen=""
        className="min-h-0 flex-1 rounded border border-border bg-black"
      />
    </section>
  );

  if (!floating) return panel;

  // FLOATING IS A BOX, NOT A WINDOW MANAGER. Fixed, resizable by the browser's
  // own corner, and bounded so it cannot be dragged or grown off screen. It is
  // deliberately not draggable-by-header with saved coordinates: that is a lot
  // of state to get wrong, and the thing asked for was to get the terminal out
  // of the column, which this does.
  return (
    <>
      {/* WHAT THE PANEL LEAVES BEHIND, so the page does not silently lose a
          terminal. A floating panel with nothing in its place reads as a
          terminal that vanished. */}
      <section className="flex flex-col gap-2" data-vm-shell-docked-slot="">
        <p className="text-muted-foreground text-xs">
          this shell is floating.{" "}
          <button
            type="button"
            data-vm-shell-dock=""
            className="underline hover:text-foreground"
            onClick={() => setFloating(false)}
          >
            dock it
          </button>
        </p>
      </section>
      {/*
        DRAGGABLE, because a floating panel you cannot move is a panel parked on
        whatever it happens to be covering. The operator: "floating shells can
        not be dragged."

        POSITIONED FROM THE TOP LEFT once it has been moved, and from the bottom
        right until then. `right-4 bottom-4` is the right place to appear and
        the wrong thing to drag: moving it would mean subtracting from the right
        edge, which inverts every sign and breaks the moment the window is
        resized. So the first drag converts to left/top, once, from the box the
        browser has already laid out.
      */}
      <div
        ref={floatBox}
        data-vm-shell-float=""
        style={at ? { left: at.x, top: at.y, right: "auto", bottom: "auto" } : undefined}
        className="fixed right-4 bottom-4 z-50 flex h-[min(70vh,40rem)] w-[min(90vw,64rem)] resize flex-col overflow-auto rounded-lg border border-border bg-background p-3 shadow-lg"
      >
        {/*
          A HANDLE, NOT THE WHOLE PANEL. Dragging from anywhere would mean the
          terminal cannot be selected with the mouse and every control inside is
          a failed drag. This bar is the only thing that moves it, which is also
          where a person reaches for a window.
        */}
        <div
          data-vm-shell-drag=""
          onPointerDown={startDrag}
          className="-mt-1 mb-1 flex cursor-move items-center justify-center rounded py-1 text-muted-foreground text-xs hover:bg-muted"
        >
          ⠿ drag
        </div>
        {panel}
      </div>
    </>
  );
}
