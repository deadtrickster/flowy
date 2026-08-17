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
export function splitBody(body: string, meta?: string): BodyRun[] {
  const known = mentionIds(meta);
  if (known.size === 0) return [{ key: "0", text: body }];

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

    if (start > plain) runs.push({ key: `t${plain}`, text: body.slice(plain, start) });
    runs.push({ key: `m${start}`, text: `@${name}`, name, id });
    plain = start + 1 + name.length;
  }
  if (plain < body.length) runs.push({ key: `t${plain}`, text: body.slice(plain) });
  return runs;
}
