/**
 * What a selected range of terminal reads like in a room.
 *
 * 01M1558DPM1HRGZNJGMVW24DHF item 4, which that row calls the highest-value
 * small feature on its list: telling another agent what happened currently
 * means retyping the screen or describing it, and both lose the thing that
 * matters - the exact bytes and where they were.
 *
 * A PURE FUNCTION, SEPARATE FROM THE PANEL, because this is the part with
 * decisions in it and the panel part is a button. A browser check cannot reach
 * this: the shell socket is operator-only and refused for a token-only console,
 * so the terminal has no output to select and there is nothing to drag over.
 * Leaving the numbering and the fencing inline would have meant shipping the
 * only real logic here untested.
 */

/** How a selection is written into a message. */
export interface SelectionMessage {
  /** The whole message body, header and fenced block. */
  body: string;
  /** How many lines it carries - the number the control shows before sending. */
  lines: number;
}

/**
 * selectionMessage renders a terminal selection as a message.
 *
 * `from` is the terminal's own first row for the selection when it knows one.
 * When it does not, the block is numbered from 1 and says nothing about screen
 * position - a number that pretends to be a row it is not would be worse than
 * no number, because the reader would go looking at that row.
 */
export function selectionMessage(
  text: string,
  opts: { from?: number; where?: string; project?: string } = {},
): SelectionMessage {
  // Trailing blank lines are an artefact of dragging past the last row, never
  // something somebody meant to send. Leading ones are kept: they are inside
  // what was selected.
  const lines = text.replace(/\s+$/, "").split("\n");
  const first = typeof opts.from === "number" ? opts.from + 1 : null;
  const width = String((first ?? 1) + lines.length - 1).length;
  const numbered = lines
    .map((l, i) => `${String(first === null ? i + 1 : first + i).padStart(width)}  ${l}`)
    .join("\n");
  const machine = opts.where === "host" ? "host" : "microVM";
  const where = opts.project ? ` in ${opts.project}` : "";
  const head = `${lines.length} line${lines.length === 1 ? "" : "s"} from the ${machine} shell${where}`;
  return { body: `${head}\n\n\`\`\`\n${numbered}\n\`\`\``, lines: lines.length };
}
