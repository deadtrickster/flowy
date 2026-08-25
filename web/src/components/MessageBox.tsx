import { CornerDownLeft, Paperclip, X } from "lucide-react";
import { type ClipboardEvent, type KeyboardEvent, useEffect, useRef, useState } from "react";

import { CitedMessage } from "@/components/CitedMessage";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import type { Citation } from "@/lib/api";
import { atFragment, matchNames, useRoster } from "@/lib/atname";
import { writeFile } from "@/lib/attach";
import { useSession } from "@/lib/session";

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
  /**
   * WHY the box is dead, in the placeholder, as words the reader can act on.
   *
   * It read "paste a token to say something" for every disabled state, which is
   * one cause named for what turned out to be several. On 2026-08-25 the
   * operator hit the one where that sentence is actively wrong: they had
   * cleared their token so the project switcher would let them in, and the
   * console then asked a signed-in person to paste back the credential that
   * takes the switcher away. Left empty this falls back to the old sentence, so
   * a caller that says nothing loses nothing.
   */
  disabledReason?: string;
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
export function MessageBox({
  citation,
  clearReply,
  disabled,
  disabledReason,
  onSend,
  quote,
  room,
}: Props) {
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

  // THE NAMES AN @ CAN MEAN, from the node's own roster. See lib/atname: a
  // mention that does not resolve is prose, so this list is not decoration, it
  // is the difference between addressing somebody and appearing to.
  const { whoami } = useSession();
  const { names } = useRoster(whoami != null);
  // The @word the caret is inside, recomputed on every draft or caret move.
  // Held rather than derived at render because the CARET is not state React
  // knows about - a selection change with no text change still moves it.
  const [at, setAt] = useState<{ fragment: string; from: number; to: number } | null>(null);
  // Which suggestion the arrow keys are on. Reset whenever the fragment moves,
  // so a list that reorders under the cursor cannot leave it pointing at a name
  // the reader is no longer looking at.
  const [pick, setPick] = useState(0);

  const shown = at ? matchNames(names, at.fragment).slice(0, 6) : [];
  const offering = shown.length > 0 && at !== null;

  /** Where the caret is now, asked of the element rather than remembered. */
  const lookAt = (el: HTMLTextAreaElement | null) => {
    if (!el) return;
    const found = atFragment(el.value, el.selectionStart ?? 0);
    setAt(found);
    setPick(0);
  };

  /**
   * Put the chosen name in, replacing the fragment, and leave the caret after a
   * trailing space so the next word is a word.
   *
   * SPLICED BY THE OFFSETS CAPTURED WITH THE FRAGMENT, not by searching the
   * draft for the @ again: by the time this runs the text may have moved, and
   * a second search would find a different @ in a message with two of them.
   */
  const choose = (name: string) => {
    if (!at) return;
    const next = `${draft.slice(0, at.from)}@${name} ${draft.slice(at.to)}`;
    const caret = at.from + name.length + 2;
    setDraft(next);
    setAt(null);
    setPick(0);
    // After React writes the value, put the caret back where the writer is.
    requestAnimationFrame(() => {
      const el = box.current;
      if (!el) return;
      el.focus();
      el.setSelectionRange(caret, caret);
    });
  };
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
    setUploading((n) => n + 1);
    setError(null);
    try {
      // The ceiling, the chunking and the refusal sentences live in lib/attach
      // now - the todo panel needs the same three and a second copy of them is
      // a second set of answers. See 01M0GGQ8D4.
      const carried = await writeFile(
        file,
        room,
        pasted ? `pasted in #${room}` : file.name || undefined,
      );
      setCarrying((current) => [...current, carried]);
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
    // THE LIST TAKES THE KEYS FIRST, and Enter is the one that matters: this
    // box SENDS on Enter, so an open suggestion list that let Enter through
    // would send a half-typed name the moment somebody tried to accept one -
    // and a half-typed name resolves to nobody, which is exactly the failure
    // the suggestions exist to prevent. Escape closes without choosing, so the
    // list can never trap a writer who wants a literal @word.
    if (offering) {
      if (event.key === "ArrowDown") {
        event.preventDefault();
        setPick((n) => (n + 1) % shown.length);
        return;
      }
      if (event.key === "ArrowUp") {
        event.preventDefault();
        setPick((n) => (n - 1 + shown.length) % shown.length);
        return;
      }
      if (event.key === "Enter" || event.key === "Tab") {
        event.preventDefault();
        choose(shown[pick]?.name ?? "");
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        setAt(null);
        return;
      }
    }
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

      {/*
        THE LIST SITS ABOVE THE BOX, not below it: the composer is already at
        the bottom of the room, so a list underneath would open off-screen or
        push the box down under the writer's hands mid-sentence.

        Rendered only while there is something to offer - an @ with no matches
        shows nothing rather than an empty panel, because "no such person" is
        already said by the name staying plain when the message lands.
      */}
      {offering ? (
        <div
          data-at-suggestions
          className="flex flex-col gap-0.5 rounded-md border border-border bg-popover p-1"
        >
          {shown.map((n, i) => (
            <button
              key={n.name}
              type="button"
              data-at-name={n.name}
              data-at-picked={i === pick ? "" : undefined}
              // onMouseDown, not onClick: the textarea's onBlur clears the list,
              // and blur fires before click - so a click would land on a list
              // that had already gone. mousedown runs first and preventDefault
              // keeps the focus in the box where the caret is.
              onMouseDown={(event) => {
                event.preventDefault();
                choose(n.name);
              }}
              className={`flex items-center gap-2 rounded px-2 py-1 text-left text-sm ${
                i === pick ? "bg-accent text-accent-foreground" : "hover:bg-accent/60"
              }`}
            >
              <span className="font-medium">@{n.name}</span>
              <span className="text-muted-foreground text-xs">{n.kind}</span>
            </button>
          ))}
        </div>
      ) : null}

      <Textarea
        ref={box}
        value={draft}
        disabled={disabled || sending}
        onChange={(event) => {
          setDraft(event.target.value);
          lookAt(event.target);
        }}
        // A CARET MOVE IS NOT A TEXT CHANGE. Clicking into the middle of an
        // @word, or arrowing back into one, changes what is being typed without
        // changing the string - onChange never fires and the list would go on
        // offering names for a fragment the caret has left.
        onSelect={(event) => lookAt(event.currentTarget)}
        onBlur={() => setAt(null)}
        onKeyDown={onKeyDown}
        onPaste={onPaste}
        placeholder={
          disabled ? disabledReason || "paste a token to say something" : "say something…"
        }
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
