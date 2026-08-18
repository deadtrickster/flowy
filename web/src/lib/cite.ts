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
 * What a drag across a message body means: a span of it, the whole of it, or
 * nothing at all. Three answers rather than two, because "the reader selected
 * nothing" and "the reader selected something this cannot place" are different
 * events and the room does different things with them - a click must arm no
 * reply, and a drag across a body must always arm one.
 */
export type Selected = { start: number; end: number } | { whole: true } | null;

/**
 * The byte span of whatever the reader has selected inside `container`.
 *
 * EVERY BODY IS RENDERED MARKDOWN NOW, which is what this function had to be
 * rewritten for. It used to measure the offset as the LENGTH OF THE RENDERED
 * TEXT before the selection, which is only the same number as an offset into
 * the raw body when the two strings are the same string. Under markdown they
 * are not: backticks, list markers and emphasis are in the body and not on the
 * screen, so an offset counted on screen lands earlier and earlier through the
 * message and the citation quotes the wrong words. The old code was correct
 * because the old plain path guaranteed rendered text == raw body; that
 * guarantee is what this change removes.
 *
 * So the span is found rather than counted: locate the selected text IN THE RAW
 * BODY, at the same occurrence the reader dragged over, and use those offsets.
 * The result is verified by construction - the offsets come from an index of
 * the selected string in the body, so the bytes between them ARE the selected
 * text and a misquote is not expressible.
 *
 * When the selection cannot be found in the raw body at all - it crossed a code
 * span, so what is on screen has no backticks in it, or it crossed an escape or
 * an entity - the honest answer is the whole message. A citation of the whole
 * is true; a span quoting bytes nobody selected is not.
 *
 * Why not map every rendered character back to a source offset through the
 * token stream: measured first. 2073 messages in the busiest room carry three
 * citations, two of them spans. A per-token offset table is a second renderer
 * to keep in step with marked for 0.1 percent of messages, and it fails in the
 * one direction that matters - quietly, quoting somebody as saying something
 * they did not.
 */
export function selectedSpan(container: HTMLElement, body: string): Selected {
  const selection = window.getSelection();
  if (!selection || selection.isCollapsed || selection.rangeCount === 0) return null;
  const range = selection.getRangeAt(0);
  if (!container.contains(range.startContainer) || !container.contains(range.endContainer)) {
    return null;
  }
  const selected = range.toString();
  if (!selected.trim()) return null;

  // WHICH occurrence this is, counted in the rendered text before it. A word
  // said twice in one message is two different spans, and dragging over the
  // second one has to cite the second one.
  const before = range.cloneRange();
  before.selectNodeContents(container);
  before.setEnd(range.startContainer, range.startOffset);
  const rendered = before.toString();
  let nth = 0;
  for (let at = rendered.indexOf(selected); at >= 0; at = rendered.indexOf(selected, at + 1)) {
    nth++;
  }

  // The same occurrence in the raw body. Counting in the rendered text and
  // seeking in the raw one can disagree when the markup itself repeats the
  // words - a link whose text is its own URL - and the disagreement costs a
  // citation of an IDENTICAL string somewhere else in the message, never a
  // citation of different words.
  let at = body.indexOf(selected);
  for (let skipped = 0; at >= 0 && skipped < nth; skipped++) {
    at = body.indexOf(selected, at + 1);
  }
  if (at < 0) at = body.indexOf(selected);
  if (at < 0) return { whole: true };

  const start = bytes.encode(body.slice(0, at)).length;
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
