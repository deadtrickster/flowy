/**
 * The @names inside a message body, for drawing.
 *
 * A mention is decided by the node, not here: it parses the body when the
 * message is said, resolves each name against the registry, addresses the
 * message to the first one and records the pairs it resolved in
 * meta.mentions - see mentions.go. So this module never guesses who somebody
 * meant. It finds the words the node already said were people, and leaves
 * every other @word as the text it is.
 *
 * That is why an unresolved name is drawn plain rather than coloured: a name
 * that means nobody is prose, and colouring it would tell a reader somebody
 * was addressed when nobody was.
 *
 * The scan mirrors mentionNames in mentions.go, and has to: the console must
 * not colour an @ the node did not treat as one. Both are the same two
 * clauses - the @ starts a word, so email@example.com is an address and not a
 * mention of example, and a trailing full stop belongs to the sentence.
 */

/** A name byte: what can appear inside a name, matching isNameByte in Go. */
const NAME_BYTE = /[A-Za-z0-9_.-]/;

/**
 * One run of a message body: text, and who it names when it is a mention.
 *
 * key is the offset it starts at, so a list of these has stable keys without
 * the renderer counting - two identical @names in one message are two runs.
 */
export interface BodyRun {
  key: string;
  text: string;
  /** The name as written, without the @, when this run is a mention. */
  name?: string;
  /** The principal that name resolved to, as the node resolved it. */
  id?: string;
  /** The URL this run links to, when the run is a bare link somebody typed. */
  href?: string;
}

/**
 * mentionIds is meta.mentions parsed back: lowercase name to principal id.
 * The encoding is "name:id" pairs separated by spaces, and a name can hold
 * neither character, so splitting is exact.
 */
export function mentionIds(meta?: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const pair of (meta ?? "").split(" ")) {
    const colon = pair.indexOf(":");
    if (colon <= 0) continue;
    out.set(pair.slice(0, colon).toLowerCase(), pair.slice(colon + 1));
  }
  return out;
}

/**
 * splitBody cuts a message body into runs, marking the @names the node
 * resolved. A body with no mentions comes back as one run, which is exactly
 * what every message written before this feature existed is.
 */
/**
 * LINK is a bare URL somebody typed. Only http and https: a body is text a peer
 * wrote, so anything that could execute - javascript:, data: - must never become
 * something clickable. Trailing punctuation is excluded because a sentence
 * ending in a URL is more common here than a URL ending in a full stop.
 */
const LINK = /https?:\/\/[^\s<>"']+[^\s<>"'.,;:!?)\]]/g;

/**
 * links splits one run of plain text into text and link runs.
 *
 * It runs on the PLAIN path rather than through the markdown renderer, because
 * that path is where mention chips and span citations live - a body rendered as
 * markdown loses both, which is why isMarkdown is deliberately narrow. So a
 * message with a URL in it stays plain, and only the URL becomes a link.
 */
function links(text: string, keyBase: string): BodyRun[] {
  const out: BodyRun[] = [];
  let plain = 0;
  LINK.lastIndex = 0;
  for (let m = LINK.exec(text); m !== null; m = LINK.exec(text)) {
    if (m.index > plain) {
      out.push({ key: `${keyBase}t${plain}`, text: text.slice(plain, m.index) });
    }
    out.push({ key: `${keyBase}l${m.index}`, text: m[0], href: m[0] });
    plain = m.index + m[0].length;
  }
  if (plain === 0) return [{ key: keyBase, text }];
  if (plain < text.length) out.push({ key: `${keyBase}t${plain}`, text: text.slice(plain) });
  return out;
}

export function splitBody(body: string, meta?: string): BodyRun[] {
  const known = mentionIds(meta);
  if (known.size === 0) return links(body, "0");

  const runs: BodyRun[] = [];
  let plain = 0;
  for (let i = 0; i < body.length; i++) {
    if (body[i] !== "@") continue;
    // The @ starts a word, or this is somebody's email address.
    if (i > 0 && NAME_BYTE.test(body[i - 1])) continue;
    let end = i + 1;
    while (end < body.length && NAME_BYTE.test(body[end])) end++;
    const start = i;
    // Resume after what was scanned either way: an @ with nothing usable after
    // it is not a mention, and re-reading those bytes cannot make one appear.
    i = end - 1;

    const name = body.slice(start + 1, end).replace(/\.+$/, "");
    const id = known.get(name.toLowerCase());
    if (!name || !id) continue;

    if (start > plain) runs.push(...links(body.slice(plain, start), `t${plain}`));
    runs.push({ key: `m${start}`, text: `@${name}`, name, id });
    plain = start + 1 + name.length;
  }
  if (plain < body.length) runs.push(...links(body.slice(plain), `t${plain}`));
  return runs;
}
