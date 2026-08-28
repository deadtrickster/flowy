/**
 * One websocket to the node's shell door, shared by every terminal on the page.
 *
 * WHY IT IS NOT INSIDE THE PANEL. VmShell used to own its socket, which was
 * right while there was one terminal and wrong the moment there are tabs: N
 * panels would open N sockets and the slot byte the wire carries would stay 0
 * forever - the multiplexing built into the node would go unused by the only
 * client it has.
 *
 * So the connection lives here and panels are consumers. A tab strip renders
 * several <VmShell slot={i}> and needs no socket code of its own, which is also
 * what keeps the wire in one file rather than in whichever component happened
 * to be written first.
 *
 * ONE PER DOCUMENT, opened on the first attach and kept while anything is
 * attached. Sessions outlive the socket on the node's side, so closing it costs
 * nothing but a reattach - see internal/flowy/agent_ws.go.
 */

/** The streams, matching the constants in internal/flowy/agent_ws.go. */
export const STREAM_OUT = 0x01;
export const STREAM_IN = 0x02;
export const STREAM_CONTROL = 0x03;

/** What the node says back. Fields it omits are simply absent. */
export interface AgentControlMessage {
  type: string;
  slot?: number;
  id?: string;
  project?: string;
  started?: string;
  dropped?: number;
  where?: string;
  why?: string;
}

export interface AgentHandlers {
  /** Raw guest bytes for this slot, in order. */
  out: (bytes: Uint8Array) => void;
  /** hello, exited, error - already parsed. */
  control: (message: AgentControlMessage) => void;
  /** The socket went away. Says nothing about the shell, which may still run. */
  lost: (why: string) => void;
}

interface Attachment {
  handlers: AgentHandlers;
  /** Sent on open, and again on a reconnect, so a slot re-adopts its session. */
  attach: Record<string, unknown>;
}

const attachments = new Map<number, Attachment>();
let socket: WebSocket | null = null;

function frame(tag: number, slot: number, body: Uint8Array): Uint8Array {
  const out = new Uint8Array(body.length + 2);
  out[0] = tag;
  out[1] = slot;
  out.set(body, 2);
  return out;
}

function encode(message: unknown): Uint8Array {
  return new TextEncoder().encode(JSON.stringify(message));
}

function open() {
  if (
    socket &&
    (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)
  ) {
    return;
  }
  // ws:// or wss:// to match how this page was served. Hard-coding either is a
  // panel that works on one deployment.
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  // NO QUERY PARAMETERS. Which terminal, which machine and which session are
  // all said in the attach message, because one socket carries several
  // terminals and a URL can describe only one.
  const ws = new WebSocket(`${proto}//${window.location.host}/api/agent/socket`);
  ws.binaryType = "arraybuffer";
  socket = ws;

  ws.onopen = () => {
    // Every attached slot announces itself, so a reconnect re-adopts every
    // session rather than only the one that happened to open the socket.
    for (const [slot, held] of attachments) {
      ws.send(frame(STREAM_CONTROL, slot, encode({ ...held.attach, type: "attach" })));
    }
  };

  ws.onmessage = (event) => {
    const bytes = new Uint8Array(event.data as ArrayBuffer);
    if (bytes.length < 2) return;
    const slot = bytes[1];
    const held = attachments.get(slot);
    if (!held) return;
    const body = bytes.subarray(2);
    if (bytes[0] === STREAM_OUT) {
      // Straight in as bytes. Decoding to a string first would mangle any
      // sequence that is not valid UTF-8, and a terminal's output is not valid
      // UTF-8 in general.
      held.handlers.out(body);
      return;
    }
    if (bytes[0] === STREAM_CONTROL) {
      try {
        held.handlers.control(JSON.parse(new TextDecoder().decode(body)) as AgentControlMessage);
      } catch {
        // A control frame this client cannot parse is the node speaking a
        // dialect this build does not know. Dropping it is better than tearing
        // the terminal down over a field that was added.
      }
    }
  };

  const lost = (why: string) => {
    if (socket === ws) socket = null;
    for (const held of attachments.values()) held.handlers.lost(why);
  };
  ws.onclose = () => lost("the connection to this node closed - the VM may still be running");
  ws.onerror = () => lost("the socket could not be opened - this door is operator-only");
}

/**
 * attachAgent puts one terminal on the shared socket and returns the detach.
 *
 * DETACHING IS NOT STOPPING: it tells the node this socket has stopped carrying
 * the session, and the shell keeps running. Closing a tab must not end
 * somebody's build - only `stopAgent` does that.
 */
export function attachAgent(
  slot: number,
  request: Record<string, unknown>,
  handlers: AgentHandlers,
): () => void {
  attachments.set(slot, { handlers, attach: request });
  open();
  if (socket?.readyState === WebSocket.OPEN) {
    socket.send(frame(STREAM_CONTROL, slot, encode({ ...request, type: "attach" })));
  }
  return () => {
    if (socket?.readyState === WebSocket.OPEN) {
      socket.send(frame(STREAM_CONTROL, slot, encode({ type: "detach" })));
    }
    attachments.delete(slot);
    // The last one out closes the door. Sessions outlive the socket, so this
    // costs a reattach and nothing else.
    if (attachments.size === 0 && socket) {
      socket.close();
      socket = null;
    }
  };
}

/** Keystrokes for one terminal. */
export function sendAgentInput(slot: number, data: string) {
  if (socket?.readyState !== WebSocket.OPEN) return;
  socket.send(frame(STREAM_IN, slot, new TextEncoder().encode(data)));
}

/** A control message for one terminal - resize, and anything added later. */
export function sendAgentControl(slot: number, message: Record<string, unknown>) {
  if (socket?.readyState !== WebSocket.OPEN) return;
  socket.send(frame(STREAM_CONTROL, slot, encode(message)));
}

/** Ends the session behind a slot. The one message that stops a shell. */
export function stopAgent(slot: number) {
  sendAgentControl(slot, { type: "stop" });
}
