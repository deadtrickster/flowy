import { useEffect, useRef, useState } from "react";

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
const heldKey = (project: string) => `flowy.vmshell.session.${project || "-"}`;

function readHeldSession(project: string): string {
  try {
    return window.localStorage.getItem(heldKey(project)) ?? "";
  } catch {
    return "";
  }
}

function rememberSession(project: string, id: string) {
  try {
    window.localStorage.setItem(heldKey(project), id);
  } catch {
    // A browser that will not remember is not a reason to refuse a terminal.
  }
}

function forgetSession(project: string) {
  try {
    window.localStorage.removeItem(heldKey(project));
  } catch {
    // As above.
  }
}

export function VmShell({ project, slot = 0 }: { project: string; slot?: number }) {
  const box = useRef<HTMLDivElement | null>(null);
  const term = useRef<GhosttyTerminal | null>(null);
  // The detach for this panel's slot, so unmounting stops carrying the session
  // without ending it.
  const detach = useRef<(() => void) | null>(null);
  // Whether the shell told us how it ended. A ref rather than state: onclose
  // fires outside React's batching and must read the value as it is NOW, not
  // as it was when the handler closed over it.
  const heard = useRef(false);
  const [state, setState] = useState<ShellState>("idle");
  // WHICH MACHINE. Two values, and neither is a default that could be arrived
  // at by accident - the operator asked for host shells and the difference is
  // whether what you type can reach this machine.
  const [where, setWhere] = useState<"vm" | "host">("vm");
  const [why, setWhy] = useState("");
  const [session, setSession] = useState("");

  // Tearing down on unmount is not tidiness: the node stops the VM when the
  // socket closes, so leaving one open is a microVM running because somebody
  // navigated away.
  useEffect(() => {
    return () => {
      detach.current?.();
      detach.current = null;
      term.current?.dispose();
    };
  }, []);

  const run = async () => {
    if (state === "starting" || state === "live") return;
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
    t.loadAddon(fit);
    if (box.current) {
      box.current.replaceChildren();
      t.open(box.current);
      fit.fit();
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
    const held = readHeldSession(project);

    // THE SOCKET IS NOT THIS COMPONENT'S. lib/agentsocket owns one connection
    // for the page and routes frames by slot, so several panels are several
    // slots rather than several sockets - which is what the slot byte on the
    // wire is for. See its head comment.
    detach.current = attachAgent(
      slot,
      { session: held, project, where, rows: t.rows, cols: t.cols },
      {
        out: (bytes) => t.write(bytes),
        control: (c) => {
          if (c.type === "hello") {
            setSession(c.id ?? "");
            setState("live");
            // REMEMBERED ONLY ONCE THE NODE HAS NAMED IT. Writing the id we
            // asked for would remember a session that was never adopted.
            if (c.id) rememberSession(project, c.id);
            // WHAT WAS DROPPED IS SAID. A reader that joins late is told how
            // much it will never see rather than handed a prefix that looks
            // whole.
            if (c.dropped && c.dropped > 0) {
              t.write(
                `\r\n[${c.dropped} bytes of earlier output are no longer held by the node]\r\n`,
              );
            }
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
    forgetSession(project);
    stopAgent(slot);
    detach.current?.();
    detach.current = null;
    setState("ended");
    setWhy("stopped from the panel");
  };

  return (
    <section className="flex flex-col gap-2" data-vm-shell="" data-vm-shell-state={state}>
      <div className="flex items-center gap-2">
        <button
          type="button"
          data-vm-shell-run=""
          className="rounded border border-border px-2 py-1 text-xs hover:bg-muted disabled:opacity-50"
          disabled={state === "starting" || state === "live"}
          onClick={() => void run()}
        >
          {state === "starting" ? "bringing a VM up…" : "run a shell"}
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
      <div
        ref={box}
        data-vm-shell-screen=""
        className="min-h-[320px] rounded border border-border bg-black"
      />
    </section>
  );
}
