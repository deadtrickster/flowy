import { CornerDownLeft, X } from "lucide-react";
import { type KeyboardEvent, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import type { FlowyEvent } from "@/lib/api";
import { shortId } from "@/lib/utils";

interface Props {
  /** replyTo is the message this one will name as its parent, if any. */
  replyTo: FlowyEvent | null;
  clearReply: () => void;
  disabled: boolean;
  onSend: (body: string, to: string) => Promise<void>;
}

/**
 * The human message box. A person types here and the console posts it as them -
 * the token decides that, not a field in the form, so a message cannot be put
 * in somebody else's mouth.
 *
 * Enter sends, shift-enter is a newline: the thing being written is usually one
 * line to an agent that is waiting.
 */
export function MessageBox({ replyTo, clearReply, disabled, onSend }: Props) {
  const [draft, setDraft] = useState("");
  // to is who the message is for, and it is a field of its own rather than a
  // convention inside the body: an @name in prose is a name somebody typed,
  // and this is a column the node checks against a real principal.
  const [to, setTo] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);

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
      onSubmit={(event) => {
        event.preventDefault();
        void send();
      }}
    >
      {replyTo ? (
        <div className="flex items-center gap-2 text-xs">
          <Badge variant="outline">replying to #{shortId(replyTo.id)}</Badge>
          <span className="truncate text-muted-foreground">{replyTo.body}</span>
          <Button
            type="button"
            size="icon"
            variant="ghost"
            className="ml-auto h-6 w-6"
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
