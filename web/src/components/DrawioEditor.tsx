import { useCallback, useEffect, useRef, useState } from "react";

/**
 * The draw.io editor, embedded.
 *
 * draw.io is vendored into web/public/drawio (see web/scripts/vendor-drawio.sh)
 * rather than loaded from a CDN, because the console has to build and run with
 * no network at all. What is vendored is the 24.7 MB the editor actually asks
 * for, measured by web/scripts/drawio-probe.mjs, not the 152 MB release.
 *
 * It runs in an iframe and talks the drawio "embed" JSON protocol over
 * postMessage. That is drawio's supported integration seam and the reason this
 * is an iframe rather than a component: the editor is a whole application with
 * its own keyboard handling, its own undo stack and its own stylesheet, and
 * putting it in this document would mean both of those fighting. The frame is
 * same-origin, so nothing here needs to relax a sandbox.
 *
 * The protocol, in the order it happens:
 *
 *   editor -> init                 it is ready, and NOT before
 *   host   -> load {xml, autosave} the document, and please tell me about edits
 *   editor -> load {xml}           it parsed and re-serialised that
 *   editor -> autosave {xml}       on every change, debounced by drawio
 *
 * There is no "save" request in this direction on purpose. autosave is what the
 * editor volunteers, it carries the full document, and taking it means a
 * diagram cannot be lost by someone closing a tab without pressing anything.
 */

/**
 * The editor's url.
 *
 * embed=1&proto=json is the embed protocol. offline=1 is drawio's own switch
 * for "make no network requests", which matters because several of its
 * conveniences (templates, shape search, the plugin registry) would otherwise
 * reach for diagrams.net and quietly fail. The rest turn off chrome that
 * belongs to a standalone app and not to a pane inside another one.
 *
 * dark=1 is the theme, and it is a url parameter rather than a message because
 * that is the only seam this build honours: app.min.js reads `urlParams.dark`
 * FIRST and falls back to mxSettings only when it is absent, while
 * Editor.configure - the obvious place to put it - never looks at darkMode at
 * all. Measured in the vendored file rather than assumed from drawio's docs.
 *
 * It is fixed rather than followed, because the console has one theme: see
 * index.css, "the console is dark, once". A white editor inside a dark console
 * is the mismatch the operator raised, and a switch here would be a second
 * palette to keep in step with a first one that never moves.
 */
const EDITOR_URL =
  "/drawio/index.html?embed=1&proto=json&offline=1&spin=0&libraries=1" +
  "&noExitBtn=1&noSaveBtn=1&saveAndExit=0&modified=unsavedChanges&dark=1";

interface Message {
  event?: string;
  xml?: string;
  message?: unknown;
}

export function DrawioEditor({
  xml,
  revision = 0,
  onChange,
  onReady,
  className,
}: {
  /** The document to open. Read when the editor announces itself. */
  xml: string;
  /**
   * Bump to push `xml` into the editor again, replacing what is on the canvas.
   *
   * This is how the console inserts a shape - it edits the document and asks
   * the editor to take the new one. It is deliberately not "whenever xml
   * changes": the editor is the owner of the document while it is open, and
   * reloading on every autosave echo would fight the user's cursor.
   */
  revision?: number;
  /** Called with the full mxfile on every edit. */
  onChange: (xml: string) => void;
  onReady?: () => void;
  className?: string;
}) {
  const frame = useRef<HTMLIFrameElement | null>(null);
  const [ready, setReady] = useState(false);

  // The document and the callback are held in refs, not closed over, so that a
  // re-render does not tear down the message listener and lose the editor's
  // init - which fires exactly once and is not repeated on request.
  const doc = useRef(xml);
  const change = useRef(onChange);
  const ready$ = useRef(onReady);
  useEffect(() => {
    doc.current = xml;
    change.current = onChange;
    ready$.current = onReady;
  }, [xml, onChange, onReady]);

  const send = useCallback((msg: unknown) => {
    frame.current?.contentWindow?.postMessage(JSON.stringify(msg), "*");
  }, []);

  useEffect(() => {
    function onMessage(event: MessageEvent) {
      // Only the frame we mounted. The editor is same-origin, so this is an
      // identity check on the window rather than a guess from the origin
      // string, and it is what keeps another frame on the page from driving
      // this one.
      if (!frame.current || event.source !== frame.current.contentWindow) return;
      if (typeof event.data !== "string" || event.data.length === 0) return;

      let msg: Message;
      try {
        msg = JSON.parse(event.data);
      } catch {
        return;
      }

      if (msg.event === "init") {
        send({ action: "load", autosave: 1, xml: doc.current });
        setReady(true);
        ready$.current?.();
        return;
      }
      // load is the editor's echo of what it parsed; autosave is an edit. Only
      // the second is a change worth writing - treating the echo as one would
      // save the document back over itself on open, for every diagram anybody
      // so much as looked at.
      if (msg.event === "autosave" && typeof msg.xml === "string") {
        change.current(msg.xml);
      }
    }

    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [send]);

  // A push of the document into an editor that is already up. The first load
  // is the init handler's; this one is every later one, and it is skipped
  // before the editor is ready because a load sent before init is dropped.
  useEffect(() => {
    // revision 0 is the document the editor opened with, and the init handler
    // above already loaded that one. Pushing it again here would be a second
    // load of the same xml on every mount.
    if (revision === 0) return;
    // A load sent before init is dropped by the editor, so a push that arrives
    // early has to wait for ready rather than be lost.
    if (!ready) return;
    send({ action: "load", autosave: 1, xml: doc.current });
  }, [revision, ready, send]);

  return (
    <div className={className} data-drawio-ready={ready ? "yes" : "no"}>
      <iframe
        ref={frame}
        title="draw.io editor"
        src={EDITOR_URL}
        className="h-full w-full border-0"
        data-testid="drawio-frame"
      />
    </div>
  );
}
