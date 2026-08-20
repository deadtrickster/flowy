import { CornerDownLeft, Paperclip, X } from "lucide-react";
import { type ClipboardEvent, type KeyboardEvent, useEffect, useRef, useState } from "react";

import { CitedMessage } from "@/components/CitedMessage";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { type Citation, api } from "@/lib/api";

interface Props {
  /**
   * citation is what this message will be about: the selected message, whole,
   * or the span of it somebody selected with the mouse. It is drawn here before
   * it is sent for the reason it is drawn on the message afterwards - "replying
   * to #3f9a1c" tells the writer nothing about which half of a long message
   * they are about to answer.
   *
   * It is built from a message already on screen, so the words are as good as
   * the node's; what is stored is still the pointer and the span, and what is
   * rendered after the send is the node's own derivation.
   */
  citation: Citation | null;
  clearReply: () => void;
  disabled: boolean;
  onSend: (body: string, to: string, attachments: string[]) => Promise<void>;
  /**
   * quote is words from OUTSIDE the transcript to drop into the draft as a
   * blockquote - a passage of the document this box sits beside. It is not a
   * citation and must not be one: a citation is a pointer into a message the
   * node can slice and check, and a document is not a message, so the words
   * travel as words in the body where the reader can see they are a quotation.
   *
   * It is an object rather than a string because quoting the same sentence
   * twice is two actions, and a string would be the same value both times - the
   * identity of the object is what says somebody asked again.
   */
  quote?: { text: string } | null;
  /**
   * room is where an attachment written from this box belongs. It is a filter
   * and not a permission - the node says so - so it is here to make a capture
   * findable next to the conversation it came out of, not to keep anyone out.
   */
  room: string;
}

/**
 * The human message box. A person types here and the console posts it as them -
 * the token decides that, not a field in the form, so a message cannot be put
 * in somebody else's mouth.
 *
 * Enter sends, shift-enter is a newline: the thing being written is usually one
 * line to an agent that is waiting.
 */
export function MessageBox({ citation, clearReply, disabled, onSend, quote, room }: Props) {
  const [draft, setDraft] = useState("");
  // to is who the message is for, and it is a field of its own rather than a
  // convention inside the body: an @name in prose is a name somebody typed,
  // and this is a column the node checks against a real principal.
  const [to, setTo] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // What is going up with this message. Each is written to the node AS IT IS
  // ATTACHED rather than when the message is sent, so a refusal - too big, or
  // the node unreachable - arrives while the person is still looking at the
  // thing they attached, instead of after they have typed a paragraph and hit
  // enter. The cost is an attachment written for a message that is never sent,
  // which is a row in the project and not a leak.
  const [carrying, setCarrying] = useState<{ id: string; name: string; bytes: number }[]>([]);
  const [uploading, setUploading] = useState(0);

  // The box is disabled while a send is in flight, and A DISABLED ELEMENT LOSES
  // FOCUS - the browser blurs it and nothing gives it back, so pressing enter
  // dropped the caret and the next keystroke went nowhere. In a room where the
  // normal rhythm is several short messages, that is a bug per message.
  //
  // Restored when the send finishes rather than inside send(), because the
  // element is still disabled at that point and focus() on a disabled element
  // is a no-op. It waits for the render that re-enables it.
  const box = useRef<HTMLTextAreaElement>(null);
  const picker = useRef<HTMLInputElement>(null);
  const wasSending = useRef(false);
  useEffect(() => {
    if (wasSending.current && !sending && !disabled) box.current?.focus();
    wasSending.current = sending;
  }, [sending, disabled]);

  // A passage of the document, appended rather than substituted: somebody who
  // has typed half a sentence and then reaches for the words they are answering
  // has not asked for that sentence to be thrown away. The caret goes back into
  // the box afterwards, because the next thing to happen is always typing.
  useEffect(() => {
    if (!quote) return;
    const quoted = quote.text
      .split("\n")
      .map((line) => `> ${line}`)
      .join("\n");
    setDraft((current) =>
      current.trim() ? `${current.trimEnd()}\n\n${quoted}\n\n` : `${quoted}\n\n`,
    );
    box.current?.focus();
  }, [quote]);

  /**
   * THE OPERATOR'S WORDS WERE "no way to post a screenshot". A screenshot is in
   * the CLIPBOARD, not on disk - the whole point of the print-screen key is that
   * you never named a file - so paste is the gesture this has to answer, and the
   * paperclip is the second way in rather than the first.
   *
   * A pasted image has no filename. It is named from the room and the fact that
   * it was pasted, because "pasted image" in a list of attachments a week later
   * is worth more than a uuid, and less than nothing is worth guessing.
   */
  const attach = async (file: File, pasted: boolean) => {
    // The ceiling BEFORE the round trip, in the same sentence the node would
    // have used. A four megabyte refusal that arrives after four megabytes have
    // been uploaded is the same refusal, delivered at the worst moment.
    if (file.size > api.MAX_ATTACHMENT) {
      setError(
        `${file.name || "that"} is ${file.size} bytes and the ceiling is ${api.MAX_ATTACHMENT}. Attach it as a file the node can keep, or cut it down.`,
      );
      return;
    }
    if (file.size === 0) {
      setError(`${file.name || "that"} is empty - there is nothing to attach.`);
      return;
    }
    setUploading((n) => n + 1);
    setError(null);
    try {
      const bytes = new Uint8Array(await file.arrayBuffer());
      // Chunked because String.fromCharCode(...spread) on a multi-megabyte array
      // overflows the argument list and throws - a four megabyte paste is the
      // NORMAL case here, not the edge one.
      let binary = "";
      for (let i = 0; i < bytes.length; i += 0x8000) {
        binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
      }
      const name = file.name || (pasted ? "pasted" : "attachment");
      const written = await api.writeAttachment({
        content_base64: btoa(binary),
        title: pasted ? `pasted in #${room}` : name,
        filename: file.name || undefined,
        content_type: file.type || undefined,
        room,
      });
      setCarrying((current) => [
        ...current,
        { id: written.item.id, name, bytes: written.size_bytes },
      ]);
      box.current?.focus();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setUploading((n) => n - 1);
    }
  };

  const onPaste = (event: ClipboardEvent<HTMLTextAreaElement>) => {
    // Only files. A paste is overwhelmingly TEXT, and text pasted into a message
    // box must land in the message box - intercepting that would break the most
    // common thing this box does to serve the rarest.
    const files = Array.from(event.clipboardData.files);
    if (files.length === 0) return;
    event.preventDefault();
    for (const file of files) void attach(file, true);
  };

  const send = async () => {
    const body = draft.trim();
    // An attachment with no words is a message: "here, look at this" is the
    // whole point of pasting a screenshot, and demanding a caption for it would
    // be the console asking a person to explain a picture to a room that can
    // see it.
    if ((!body && carrying.length === 0) || sending) return;
    setSending(true);
    setError(null);
    try {
      await onSend(
        body,
        to.trim(),
        carrying.map((c) => c.id),
      );
      setDraft("");
      setCarrying([]);
      clearReply();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSending(false);
    }
  };

  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      void send();
    }
  };

  return (
    <form
      className="flex flex-col gap-2 border-border border-t bg-card/40 p-3"
      autoComplete="off"
      onSubmit={(event) => {
        event.preventDefault();
        void send();
      }}
    >
      {citation ? (
        <div className="flex items-start gap-2">
          <div className="min-w-0 flex-1">
            <CitedMessage citation={citation} />
          </div>
          <Button
            type="button"
            size="icon"
            variant="ghost"
            className="h-6 w-6 shrink-0"
            onClick={clearReply}
            aria-label="stop replying"
          >
            <X className="h-3 w-3" />
          </Button>
        </div>
      ) : null}

      <Input
        value={to}
        disabled={disabled || sending}
        onChange={(event) => setTo(event.target.value)}
        placeholder="to (a user or agent id) - leave empty for the room"
        aria-label="addressee"
        className="h-8 text-xs"
      />

      <Textarea
        ref={box}
        value={draft}
        disabled={disabled || sending}
        onChange={(event) => setDraft(event.target.value)}
        onKeyDown={onKeyDown}
        onPaste={onPaste}
        placeholder={disabled ? "paste a token to say something" : "say something…"}
        aria-label="message"
      />

      {carrying.length > 0 ? (
        <div className="flex flex-wrap gap-2" data-carrying>
          {carrying.map((c) => (
            <span
              key={c.id}
              className="flex items-center gap-1 rounded bg-muted px-2 py-1 text-xs"
              data-carried={c.id}
            >
              <Paperclip className="h-3 w-3 shrink-0" />
              <span className="max-w-40 truncate">{c.name}</span>
              <span className="text-muted-foreground">{Math.ceil(c.bytes / 1024)}k</span>
              {/* Taken off the MESSAGE, not deleted from the node: the bytes are
                  written and the log is append-only, so this is honestly "not
                  this message" rather than "gone", and saying otherwise would
                  be the console lying about what it can do. */}
              <Button
                type="button"
                size="icon"
                variant="ghost"
                className="h-4 w-4 shrink-0"
                onClick={() => setCarrying((current) => current.filter((x) => x.id !== c.id))}
                aria-label={`do not send ${c.name}`}
              >
                <X className="h-3 w-3" />
              </Button>
            </span>
          ))}
        </div>
      ) : null}

      <div className="flex items-center gap-3">
        {/* The second way in. The label says PASTE first because that is the
            gesture a screenshot arrives by and the one that needed no control
            at all - the paperclip is for the file that is already on disk. */}
        <input
          ref={picker}
          type="file"
          multiple
          className="hidden"
          onChange={(event) => {
            for (const file of Array.from(event.target.files ?? [])) void attach(file, false);
            // Cleared so attaching the same file twice in a row fires onChange
            // the second time - the input reports a CHANGE of value, and the
            // same file is not one.
            event.target.value = "";
          }}
          aria-label="attach a file"
        />
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className="h-7 w-7 shrink-0"
          disabled={disabled || sending}
          onClick={() => picker.current?.click()}
          aria-label="attach a file"
          data-attach
        >
          <Paperclip className="h-3.5 w-3.5" />
        </Button>
        <span className="text-muted-foreground text-xs">
          {uploading > 0
            ? `attaching ${uploading}…`
            : "enter sends, shift-enter is a newline, paste an image to attach it"}
        </span>
        {error ? <span className="text-destructive text-xs">{error}</span> : null}
        <Button
          type="submit"
          size="sm"
          className="ml-auto"
          disabled={disabled || sending || uploading > 0}
        >
          <CornerDownLeft className="h-3.5 w-3.5" />
          {sending ? "sending…" : "send"}
        </Button>
      </div>
    </form>
  );
}
