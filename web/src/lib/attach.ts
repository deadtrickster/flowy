import { api } from "@/lib/api";

/**
 * Writing a file to the node, in one place, because the rules are not obvious
 * and a second copy of them is a second set of answers.
 *
 * IT WAS INLINE IN MessageBox, and moving it out is the point of 01M0GGQ8D4:
 * the operator asked to attach a file to a todo raised from a chat, and the
 * only code that knew how lived inside the message composer. Copying it would
 * have produced two ceilings, two chunk sizes and two refusal sentences that
 * drift apart on the day one of them is fixed.
 *
 * THREE THINGS HERE ARE MEASURED RATHER THAN STYLISTIC:
 *
 *   the ceiling is checked BEFORE the round trip, in the sentence the node
 *   would have used - a four megabyte refusal that arrives after four megabytes
 *   have been uploaded is the same refusal delivered at the worst moment
 *
 *   base64 is built in 32k chunks, because
 *   String.fromCharCode(...spread) on a multi-megabyte array overflows the
 *   argument list and throws. A four megabyte screenshot is the NORMAL case
 *   here, not the edge one
 *
 *   an empty file is refused by name, because 0 bytes uploads without error and
 *   produces an attachment nobody can open
 */
export interface Attached {
  id: string;
  name: string;
  bytes: number;
}

/**
 * writeFile stores one file and answers with what to show for it.
 *
 * It THROWS on refusal rather than returning null: every caller has somewhere
 * to put a sentence, and a null here would be a failure that looks like a file
 * nobody chose. `where` names the room the attachment belongs to, and `titled`
 * is what a list of attachments says a week later - "pasted in #general" is
 * worth more than a uuid, and less than nothing is worth guessing.
 */
export async function writeFile(file: File, where: string, titled?: string): Promise<Attached> {
  if (file.size > api.MAX_ATTACHMENT) {
    throw new Error(
      `${file.name || "that"} is ${file.size} bytes and the ceiling is ${api.MAX_ATTACHMENT}. Attach it as a file the node can keep, or cut it down.`,
    );
  }
  if (file.size === 0) {
    throw new Error(`${file.name || "that"} is empty - there is nothing to attach.`);
  }
  const bytes = new Uint8Array(await file.arrayBuffer());
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  const name = file.name || titled || "attachment";
  const written = await api.writeAttachment({
    content_base64: btoa(binary),
    title: titled || name,
    filename: file.name || undefined,
    content_type: file.type || undefined,
    room: where,
  });
  return { id: written.item.id, name, bytes: written.size_bytes };
}
