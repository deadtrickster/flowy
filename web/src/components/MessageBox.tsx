import { CornerDownLeft, X } from "lucide-react";
import { type KeyboardEvent, useEffect, useRef, useState } from "react";

import { CitedMessage } from "@/components/CitedMessage";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import type { Citation } from "@/lib/api";

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
  onSend: (body: string, to: string) => Promise<void>;
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
}

/**
 * The human message box. A person types here and the console posts it as them -
 * the token decides that, not a field in the form, so a message cannot be put
 * in somebody else's mouth.
 *
 * Enter sends, shift-enter is a newline: the thing being written is usually one
 * line to an agent that is waiting.
 */
export function MessageBox({ citation, clearReply, disabled, onSend, quote }: Props) {
  const [draft, setDraft] = useState("");
  // to is who the message is for, and it is a field of its own rather than a
  // convention inside the body: an @name in prose is a name somebody typed,
  // and this is a column the node checks against a real principal.
  const [to, setTo] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // The box is disabled while a send is in flight, and A DISABLED ELEMENT LOSES
  // FOCUS - the browser blurs it and nothing gives it back, so pressing enter
  // dropped the caret and the next keystroke went nowhere. In a room where the
  // normal rhythm is several short messages, that is a bug per message.
  //
  // Restored when the send finishes rather than inside send(), because the
  // element is still disabled at that point and focus() on a disabled element
  // is a no-op. It waits for the render that re-enables it.
  const box = useRef<HTMLTextAreaElement>(null);
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

  const send = async () => {
    const body = draft.trim();
    if (!body || sending) return;
    setSending(true);
    setError(null);
    try {
      await onSend(body, to.trim());
      setDraft("");
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
        placeholder={disabled ? "paste a token to say something" : "say something…"}
        aria-label="message"
      />

      <div className="flex items-center gap-3">
        <span className="text-muted-foreground text-xs">enter sends, shift-enter is a newline</span>
        {error ? <span className="text-destructive text-xs">{error}</span> : null}
        <Button type="submit" size="sm" className="ml-auto" disabled={disabled || sending}>
          <CornerDownLeft className="h-3.5 w-3.5" />
          {sending ? "sending…" : "send"}
        </Button>
      </div>
    </form>
  );
}
