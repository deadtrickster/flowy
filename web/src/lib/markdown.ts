/**
 * ONE markdown dialect for this console, in one place.
 *
 * The operator asked for "full gh flavored markdown everywhere" after typing a
 * message with `backticks` in it and watching the backticks render as
 * backticks. They were right about what they saw and the cause was not what it
 * looked like: chat already rendered markdown, and only for bodies that a
 * heuristic - a fence, a list, a heading, a table pipe - recognised as
 * structured. A body with nothing but an inline code span was prose to it and
 * took the plain path, backticks included.
 *
 * So this file exists to delete that fork rather than widen it. EVERY body
 * renders through here, and the two places that render one - the room and a
 * report - differ in exactly one option, named below.
 *
 * WHAT GFM MEANS HERE, said out loud because "gfm: true" is marked's default
 * and a default is not a decision anybody made: tables, strikethrough, task
 * lists and autolinked bare URLs are all on, and are asserted in the gate by
 * web/scripts/gfm-check.mjs rather than assumed from the version of marked in
 * package-lock.json.
 *
 * The one difference between the two callers is `breaks`, and it is the
 * difference GitHub itself draws: a single newline inside a paragraph is a line
 * break in a COMMENT and is not one in a FILE. A room message is a comment -
 * three short lines is the house style, and collapsing them into one sentence
 * would lose the shape somebody typed. A report is a file: its prose is hard
 * wrapped, and turning every wrap into a <br> would make it ragged. So chat
 * renders with breaks and a document does not, and that is the whole of the
 * divergence.
 */

import DOMPurify from "dompurify";
import { Marked, type Renderer, type Tokens } from "marked";

import { splitBody } from "@/lib/mentions";
import { speakerStyle } from "@/lib/speakercolour";

/**
 * The dialect. Spelled out rather than inherited from marked's defaults,
 * because what this console renders should not change when a dependency
 * changes its mind - and because a reader asking "are tables on?" deserves an
 * answer in this file.
 *
 * - gfm: tables, strikethrough, task lists, and bare URLs as links.
 * - pedantic: off, which is what lets gfm be on at all.
 */
const DIALECT = { gfm: true, pedantic: false, async: false } as const;

/**
 * Anchors out of a body are anchors out of TEXT A PEER WROTE, so they must not
 * hand the target a window handle it can navigate back, and the referrer is
 * nobody else's business. This was already true of the plain path's typed
 * links; now that every link comes out of the renderer it has to be true of
 * the renderer's, which is what this hook is for.
 *
 * On the sanitizer rather than on marked's renderer because DOMPurify runs
 * last: an anchor arriving in raw HTML inside a body goes through here too,
 * and one that came out of marked cannot be stripped of its rel afterwards.
 */
DOMPurify.addHook("afterSanitizeAttributes", (node) => {
  if (node.tagName === "A" && node.hasAttribute("href")) {
    node.setAttribute("target", "_blank");
    node.setAttribute("rel", "noreferrer noopener");
  }
});

/**
 * sanitize is not optional and never becomes optional: bodies are written by
 * agents, replicated from other nodes, and rendered with dangerouslySetInnerHTML.
 *
 * The allowances are DOMPurify's html profile, which already covers everything
 * GFM emits - table/thead/tbody/tr/th/td, del, pre, code, and the disabled
 * checkbox a task list renders - plus data attributes, which is how the mention
 * chips below survive the sanitizer with the id the node resolved on them.
 */
export function sanitize(html: string): string {
  return DOMPurify.sanitize(html, { ALLOW_DATA_ATTR: true });
}

/** escaped is for text this file puts into HTML itself, around the mention chips. */
function escaped(text: string): string {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

/** Who is reading, for the ring a mention of them wears. */
export interface Reader {
  user?: string;
  agent?: string;
}

/**
 * The mention chips, as a renderer override rather than as a marked extension.
 *
 * A marked inline extension sees the source from the candidate character
 * onwards and cannot see the character BEFORE it, which is the one thing the
 * node's rule turns on: an @ that follows a name byte is an email address and
 * not a mention. A text-token renderer gets the whole run, so the same
 * splitBody the plain path used decides this - the console must never colour an
 * @word the node did not resolve, and the surest way to keep the two agreeing
 * is to keep them one function.
 *
 * Inline code is untouched by this: a codespan is its own token and never
 * reaches here, so `@nobody` inside backticks stays text, which is right.
 */
function mentionsRenderer(meta: string | undefined, me: Reader | undefined) {
  const isMe = (id?: string) => !!id && (id === me?.user || id === me?.agent);
  return function (this: Renderer, token: Tokens.Text | Tokens.Escape): string {
    // The two cases the default renderer answers before it escapes anything: a
    // text token carrying inline children is a container and belongs to the
    // parser, and an already-escaped token is HTML this file must not touch.
    // Dropping either turns a list item into its own markup.
    if ("tokens" in token && token.tokens) return this.parser.parseInline(token.tokens);
    if ("escaped" in token && token.escaped) return token.text;

    const runs = splitBody(token.text, meta);
    return runs
      .map((run) => {
        if (run.name) {
          const style = speakerStyle(run.name);
          // The same two classes the plain path drew, so a mention of the
          // reader is ringed exactly as it was and the colour is the one that
          // person speaks in.
          const ring = isMe(run.id) ? " ring-1 ring-primary/70" : "";
          return (
            `<span data-mention="${escaped(run.id ?? "")}"` +
            ` class="rounded px-0.5 font-medium${ring}"` +
            ` style="color:${style.color};background-color:${style.backgroundColor}">` +
            `${escaped(run.text)}</span>`
          );
        }
        // splitBody also finds bare URLs, and those runs are rendered as TEXT
        // here rather than as anchors: GFM autolinks them one level up, in the
        // tokenizer, so the words reaching this function are already inside a
        // link token when they are a link. Emitting a second <a> around them
        // nests one anchor in another, which no browser will parse - it closes
        // the outer one first and leaves an EMPTY anchor in front of the real
        // one. Measured: link-check timed out waiting for a[href] to become
        // visible, on a page where the link was rendered and clickable.
        return escaped(run.text);
      })
      .join("");
  };
}

/**
 * renderChat is a room message: GFM, with mention chips, and with `breaks` on
 * because a message is a comment and not a file.
 *
 * meta is the node's resolved mentions - meta.mentions, "name:id" pairs - and
 * me is whoever is reading, for the ring.
 */
export function renderChat(body: string, meta?: string, me?: Reader): string {
  const marked = new Marked({ ...DIALECT, breaks: true });
  marked.use({ renderer: { text: mentionsRenderer(meta, me) } });
  return sanitize(marked.parse(body, { async: false }) as string);
}

/**
 * renderDocument is a report, a finding or an artifact body: the same GFM, no
 * mention chips - a document is not addressed at anybody - and no `breaks`,
 * because its prose is hard wrapped.
 */
export function renderDocument(body: string): string {
  const marked = new Marked({ ...DIALECT, breaks: false });
  return sanitize(marked.parse(body, { async: false }) as string);
}
