/**
 * Spans into a message body, on the console's side of the wire.
 *
 * A citation records a pointer and a span and never the quoted text - see
 * citations.go - so the only thing the console has to get right is the
 * arithmetic, and it has to get it right in the same units the node uses.
 *
 * Those units are BYTES. The body is bytes to the node: it slices them, and the
 * row's signature is over them. A browser counts UTF-16 code units, so a
 * console that sent those would quote the wrong words the first time somebody
 * selected text after an accented character, and would be refused at the door
 * when a span landed inside one - which is the failure the node checks for
 * rather than repairs, because repairing it means quoting something other than
 * what was selected.
 *
 * The state below is here rather than in either view because both views that
 * render a transcript need it - the room and the thread of one handoff - and a
 * second copy of "which message, and which span of it" is how the two would
 * end up disagreeing about what a reply is answering.
 */

import { useCallback, useState } from "react";

import type { Citation, FlowyEvent } from "@/lib/api";

const bytes = new TextEncoder();
const text = new TextDecoder();

/**
 * The byte span of whatever the reader has selected inside `container`, or null
 * when the selection is empty, is outside it, or runs across two of them.
 *
 * The offset is measured against the container's own text, so the container has
 * to hold the body and nothing else: an attribution or a citation drawn inside
 * it would shift every span by its own length.
 */
export function selectedSpan(
  container: HTMLElement,
  body: string,
): { start: number; end: number } | null {
  const selection = window.getSelection();
  if (!selection || selection.isCollapsed || selection.rangeCount === 0) return null;
  const range = selection.getRangeAt(0);
  if (!container.contains(range.startContainer) || !container.contains(range.endContainer)) {
    return null;
  }
  const selected = range.toString();
  if (!selected.trim()) return null;

  const before = range.cloneRange();
  before.selectNodeContents(container);
  before.setEnd(range.startContainer, range.startOffset);

  const start = bytes.encode(body.slice(0, before.toString().length)).length;
  return { start, end: start + bytes.encode(selected).length };
}

/**
 * byteSlice is the same cut the node will make, for the preview above the reply
 * box: the words that WILL be quoted once this is sent, taken out of a message
 * already on screen. It is not a second source of truth - what is rendered
 * afterwards is the node's own derivation, and this exists so that what
 * somebody is about to say is visible before they say it.
 */
export function byteSlice(body: string, start: number, end: number): string {
  return text.decode(bytes.encode(body).slice(start, end));
}

/** What a transcript view holds about the message it is answering. */
export interface Cited {
  /** The message a reply attaches to: its parent, and what it cites. */
  selected: FlowyEvent | null;
  /** That citation as it will be drawn above the box, or null for none. */
  citation: Citation | null;
  /** And as the say takes it. Undefined when nothing is selected. */
  cite?: { message: string; start?: number; end?: number };
  /** Select a message: a citation of the whole of it. */
  select: (event: FlowyEvent) => void;
  /** Narrow that citation to a span somebody selected inside one. */
  citeSpan: (event: FlowyEvent, start: number, end: number) => void;
  clear: () => void;
}

/**
 * The message a reply is answering, and which span of it.
 *
 * The preview citation it builds is cut out of a message already on screen with
 * the offsets the node will use, so what somebody sees before they send is what
 * the node will derive afterwards. It is a preview and not a second source of
 * truth: what travels is the pointer and the span, and what is rendered on the
 * message once it lands is the node's own derivation.
 */
export function useCitation(): Cited {
  const [selected, setSelected] = useState<FlowyEvent | null>(null);
  const [span, setSpan] = useState<{ message: string; start: number; end: number } | null>(null);

  const select = useCallback((event: FlowyEvent) => {
    setSelected(event);
    setSpan(null);
  }, []);

  const citeSpan = useCallback((event: FlowyEvent, start: number, end: number) => {
    setSelected(event);
    setSpan({ message: event.id, start, end });
  }, []);

  const clear = useCallback(() => {
    setSelected(null);
    setSpan(null);
  }, []);

  // A span only means anything about the message it was taken from: a selection
  // made in one message and a click on another are two actions, and the second
  // one wins.
  const of = span && selected && span.message === selected.id ? span : null;

  return {
    selected,
    select,
    citeSpan,
    clear,
    citation: selected
      ? {
          message: selected.id,
          whole: !of,
          ...(of ? { start: of.start, end: of.end } : {}),
          readable: true,
          actor: selected.actor,
          name: selected.meta?.actor_name,
          text: of ? byteSlice(selected.body, of.start, of.end) : selected.body,
        }
      : null,
    cite: selected
      ? { message: selected.id, ...(of ? { start: of.start, end: of.end } : {}) }
      : undefined,
  };
}
