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
/**
 * A WORD IN ANGLE BRACKETS IS A WORD, NOT A TAG.
 *
 * The operator's report was "some markdown rendering is broken on some times",
 * and this is it, measured: `use <id> for the row` renders as "use  for the
 * row". marked hands `<id>` through as raw HTML, the sanitizer drops the
 * unknown element, and because an unknown empty element has no children there
 * is nothing left to keep. The word is GONE - not mangled, not escaped, gone -
 * and the sentence still reads as a sentence, which is what makes it worse than
 * a visible glitch. `run flowy inbox --as <you>` loses the only part that told
 * anybody what to type.
 *
 * This house writes that way constantly: <file>, <name>, <id>, <you> appear
 * unbackticked all through the repo's own prose and every agent copies the
 * style. So the console silently eats words from the documents it exists to
 * show, and nobody can tell from the reading that anything was removed.
 *
 * The fix is to put the literal back rather than to widen what HTML a body may
 * contain. An element the sanitizer will not allow becomes the text somebody
 * typed - `<id>` - and its children are kept as DOMPurify would have kept them
 * anyway. Deliberate HTML in a body still works exactly as before, because an
 * ALLOWED tag never reaches this branch.
 *
 * `a < b` was never affected and is not touched here: marked escapes a bare
 * less-than that does not begin something tag-shaped, so it arrives as &lt; and
 * is not an element at all.
 */
DOMPurify.addHook("uponSanitizeElement", (node, data) => {
  const tag = data.tagName;
  if (!tag || data.allowedTags[tag]) return;
  // Only element nodes, and only ones the parser made from something that
  // looked like a tag. #text, #comment and the document fragment itself all
  // arrive here too and none of them is a word somebody lost.
  const owner = node.ownerDocument;
  if (node.nodeType !== 1 || !node.parentNode || !owner) return;
  node.parentNode.insertBefore(owner.createTextNode(`<${tag}>`), node);
});

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

/**
 * A bare row id is a link to the row it names, through the resolver route
 * /a/<ulid> - the renderer cannot look a row up synchronously, so the link
 * names the resolver and the resolver 302s to wherever the row lives.
 *
 * The pattern is strict on purpose, for the reason the row 01M0XDPFSA7M73
 * spells out: "a pattern that is nearly right will turn arbitrary tokens into
 * dead links, which is worse than no links at all". A ULID is 26 characters
 * starting 01 - that prefix plus 24 Crockford base32 characters, which
 * excludes I, L, O and U - and the boundaries are the byte classes a human
 * pastes an id between: start of message or a non-alphanumeric character
 * before it, and a non-alphanumeric character or end of message after it. A
 * ULID inside a longer word is a longer word, and a ULID inside a URL is the
 * URL's business - GFM autolinked it already, and an anchor inside an anchor
 * closes the outer one first (measured, see the URL-run comment below).
 */
const ROW_ID = /(^|[^0-9A-Za-z])(01[0-9A-HJKMNP-TV-Z]{24})(?![0-9A-Za-z])/g;

/** rowLinks wraps the row ids in a run of plain text in resolver anchors. */
function rowLinks(text: string): string {
  let out = "";
  let last = 0;
  ROW_ID.lastIndex = 0;
  for (let m = ROW_ID.exec(text); m !== null; m = ROW_ID.exec(text)) {
    const start = m.index + m[1].length;
    out += escaped(text.slice(last, start));
    out += `<a href="/a/${m[2]}">${m[2]}</a>`;
    last = start + m[2].length;
  }
  return out + escaped(text.slice(last));
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
        if (run.href) return escaped(run.text);
        // And a bare row id is a link of its own - the resolver takes it from
        // here.
        return rowLinks(run.text);
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
 * documentTextRenderer is the text renderer for a document: no mention chips -
 * a document is not addressed at anybody - but the same URL-run respect as
 * chat (a typed link is the tokenizer's to wrap, never a second anchor) and
 * the same row-id links. It reuses splitBody for the same reason chat does:
 * one function decides what is a URL, so the two renderers cannot disagree
 * about which text an id sits inside.
 */
function documentTextRenderer() {
  return function (this: Renderer, token: Tokens.Text | Tokens.Escape): string {
    // The same two cases the chat renderer answers first: a text token with
    // inline children belongs to the parser, and an already-escaped token is
    // HTML this file must not touch.
    if ("tokens" in token && token.tokens) return this.parser.parseInline(token.tokens);
    if ("escaped" in token && token.escaped) return token.text;

    return splitBody(token.text)
      .map((run) => (run.href ? escaped(run.text) : rowLinks(run.text)))
      .join("");
  };
}

/**
 * renderDocument is a report, a finding or an artifact body: the same GFM, no
 * mention chips - a document is not addressed at anybody - and no `breaks`,
 * because its prose is hard wrapped. Bare row ids are links here too: a
 * document quotes ids constantly, and the resolver fixes them everywhere.
 */
export function renderDocument(
  body: string,
  files: Map<string, ResolvedAttachment> = new Map(),
): string {
  const marked = new Marked({ ...DIALECT, breaks: false });
  marked.use({
    renderer: { text: documentTextRenderer(), image: documentImageRenderer(files) },
  });
  return sanitize(marked.parse(body, { async: false }) as string);
}

/**
 * A body refers to a file it carries by the file's own id, as an ordinary
 * markdown image: `![what it shows](01M0...)`.
 *
 * NO NEW SYNTAX, which is the whole argument for it. An attachment is an
 * artifact and its id is what everything else here already names it by - the
 * bare-id links above, the `attachments` field, the cards. A body written this
 * way still reads as markdown anywhere that has never heard of flowy, which is
 * what the operator asked for when they said the file should be "right into the
 * body as <img>, plus listed in attachment, JIRA style".
 *
 * `attachment:01M0...` is taken too, because a person who has just read the
 * word "attachment" in a UI will type it, and refusing a spelling nobody was
 * told about is a refusal the reader has to guess at.
 */
const ATTACHMENT_HREF = /^(?:attachment:)?(01[0-9A-HJKMNP-TV-Z]{24})$/;

/** The id in an image href, or "" when the href is an ordinary URL. */
export function attachmentHref(href: string): string {
  const m = ATTACHMENT_HREF.exec(href.trim());
  return m ? m[1] : "";
}

/**
 * attachmentsIn is every file a body refers to, in the order it refers to them.
 *
 * It exists because the render is SYNCHRONOUS and the bytes are not: a caller
 * has to know what to fetch before it can draw, and the alternative - render a
 * placeholder and swap the src in afterwards - puts a network read inside a
 * dangerouslySetInnerHTML subtree, where React does not own the nodes and
 * nothing tells it when they are replaced.
 *
 * Duplicates collapse and order is kept, so a body naming one file three times
 * is one fetch and the list at the foot draws it once.
 */
export function attachmentsIn(body: string): string[] {
  const found: string[] = [];
  const seen = new Set<string>();
  const marked = new Marked({ ...DIALECT });
  marked.use({
    renderer: {
      image(this: Renderer, token: Tokens.Image) {
        const id = attachmentHref(token.href);
        if (id && !seen.has(id)) {
          seen.add(id);
          found.push(id);
        }
        return "";
      },
    },
  });
  marked.parse(body, { async: false });
  return found;
}

/**
 * What a caller knows about one file by the time it draws: the bytes as a data
 * URI, or nothing at all.
 *
 * ABSENT IS NOT EMPTY, and this type is where that is kept. `src` empty with
 * `why` set is "asked and cannot show it" - the caller may not read it, or the
 * artifact replicated here and its bytes did not, which is what store.ErrNoBytes
 * is for and why it is deliberately not ErrNotFound. A file still being fetched
 * is neither, and is simply not in the map yet.
 */
export interface ResolvedAttachment {
  src: string;
  title: string;
  why?: string;
}

/**
 * The image renderer for a document: an attachment id becomes the picture, and
 * anything else is left to marked.
 *
 * A REFERENCE THE READER CANNOT SEE SAYS SO, BY NAME. The alternative is an
 * <img> with a dead src, which every browser draws as the same small broken
 * glyph whether the file is gone, forbidden, or never existed - three states
 * with one appearance, in a document whose whole job is to be evidence. So an
 * unresolved reference renders as text naming the id and the reason, and it is
 * a data attribute rather than a class so a check can assert it without
 * asserting a stylesheet.
 */
function documentImageRenderer(files: Map<string, ResolvedAttachment>) {
  return function image(this: Renderer, token: Tokens.Image): string {
    const id = attachmentHref(token.href);
    if (!id) {
      const title = token.title ? ` title="${escaped(token.title)}"` : "";
      return `<img src="${escaped(token.href)}" alt="${escaped(token.text)}"${title}>`;
    }
    const got = files.get(id);
    const caption = token.text || got?.title || id;
    if (!got || !got.src) {
      const why = got?.why || "it is not on this node, or you may not read it";
      return `<span data-attachment-missing="${id}">${escaped(caption)} - this file is referred to here and cannot be shown: ${escaped(why)}</span>`;
    }
    return `<img data-attachment="${id}" src="${escaped(got.src)}" alt="${escaped(caption)}" title="${escaped(got.title || caption)}">`;
  };
}
