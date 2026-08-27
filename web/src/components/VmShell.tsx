import { useEffect, useRef, useState } from "react";

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

/** The streams, matching the constants in internal/flowy/agent_ws.go. */
const STREAM_OUT = 0x01;
const STREAM_IN = 0x02;
const STREAM_CONTROL = 0x03;

interface Hello {
  type: string;
  id?: string;
  project?: string;
  started?: string;
  dropped?: number;
  why?: string;
}

/**
 * What the panel is doing, as one value rather than several booleans.
 *
 * THREE STATES AND NOT TWO. "nothing started" and "starting" and "the VM is
 * gone" are different things to a person looking at an empty black rectangle,
 * and a single `running` flag would draw the first and the third identically.
 */
type ShellState = "idle" | "starting" | "live" | "ended";

export function VmShell({ project }: { project: string }) {
  const box = useRef<HTMLDivElement | null>(null);
  const term = useRef<GhosttyTerminal | null>(null);
  const sock = useRef<WebSocket | null>(null);
  const [state, setState] = useState<ShellState>("idle");
  const [why, setWhy] = useState("");
  const [session, setSession] = useState("");

  // Tearing down on unmount is not tidiness: the node stops the VM when the
  // socket closes, so leaving one open is a microVM running because somebody
  // navigated away.
  useEffect(() => {
    return () => {
      sock.current?.close();
      term.current?.dispose();
    };
  }, []);

  const run = async () => {
    if (state === "starting" || state === "live") return;
    setState("starting");
    setWhy("");

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
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    const url = new URL(`${proto}//${window.location.host}/api/agent/socket`);
    if (project) url.searchParams.set("project", project);
    const ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";
    sock.current = ws;

    ws.onmessage = (event) => {
      const frame = new Uint8Array(event.data as ArrayBuffer);
      if (frame.length === 0) return;
      const body = frame.subarray(1);
      switch (frame[0]) {
        case STREAM_OUT:
          // Straight in as bytes. Decoding to a string first would mangle any
          // sequence that is not valid UTF-8, and a terminal's output is not
          // valid UTF-8 in general.
          t.write(body);
          break;
        case STREAM_CONTROL: {
          let c: Hello;
          try {
            c = JSON.parse(new TextDecoder().decode(body)) as Hello;
          } catch {
            return;
          }
          if (c.type === "hello") {
            setSession(c.id ?? "");
            setState("live");
            // WHAT WAS DROPPED IS SAID. A reader that joins late is told how
            // much it will never see rather than handed a prefix that looks
            // whole.
            if (c.dropped && c.dropped > 0) {
              t.write(
                `\r\n[${c.dropped} bytes of earlier output are no longer held by the node]\r\n`,
              );
            }
          } else if (c.type === "exited") {
            setState("ended");
            setWhy(c.why ?? "the shell ended");
          }
          break;
        }
        default:
          break;
      }
    };
    ws.onclose = () => {
      setState((s) => (s === "ended" ? s : "ended"));
      setWhy((w) => w || "the connection closed");
    };
    ws.onerror = () => {
      setState("ended");
      setWhy("the socket could not be opened - this door is operator-only");
    };

    // Keystrokes out. onData is already the encoded bytes for the key,
    // including the escape sequences for arrows and function keys, which is
    // most of the reason to use a terminal emulator rather than read
    // keydown.
    t.onData((data: string) => {
      if (ws.readyState !== WebSocket.OPEN) return;
      const bytes = new TextEncoder().encode(data);
      const frame = new Uint8Array(bytes.length + 1);
      frame[0] = STREAM_IN;
      frame.set(bytes, 1);
      ws.send(frame);
    });

    // And the shape, so the guest wraps where this panel wraps. A pty defaults
    // to 0x0 and a shell on a zero-sized terminal draws nothing sensible.
    const tellSize = (cols: number, rows: number) => {
      if (ws.readyState !== WebSocket.OPEN) return;
      const body = new TextEncoder().encode(JSON.stringify({ type: "resize", rows, cols }));
      const frame = new Uint8Array(body.length + 1);
      frame[0] = STREAM_CONTROL;
      frame.set(body, 1);
      ws.send(frame);
    };
    t.onResize(({ cols, rows }: { cols: number; rows: number }) => tellSize(cols, rows));
    ws.onopen = () => tellSize(t.cols, t.rows);
  };

  const stop = () => {
    sock.current?.close();
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
          nothing is running. Run a shell brings up a microVM and puts its shell here - what you
          type goes to the guest, not to this machine.
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
