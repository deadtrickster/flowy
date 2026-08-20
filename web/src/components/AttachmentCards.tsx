import { useEffect, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { type Artifact, api } from "@/lib/api";
import { shortId } from "@/lib/utils";

/**
 * The cards for what a message carries: one per attachment id, named from the
 * attachment's own row, fetched lazily so a room full of cards costs the
 * badges and not the bytes.
 *
 * The bytes are fetched on demand, never eagerly: content is base64 and a
 * screenshot is a megabyte. An image claim renders a preview once loaded,
 * everything else names itself and offers the payload behind a click. The
 * content type is a CLAIM the writer made and never what this renders from -
 * the same rule the write made - so the preview says "claims image/png"
 * rather than pretending the node verified it.
 */
function Card({ id }: { id: string }) {
  const [item, setItem] = useState<Artifact | null>(null);
  const [open, setOpen] = useState(false);
  const [content, setContent] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let stopped = false;
    api
      .attachment(id)
      .then((page) => {
        if (!stopped) {
          setItem(page.item);
          if (open) setContent(page.content);
        }
      })
      .catch((e: Error) => {
        if (!stopped) setErr(e.message);
      });
    return () => {
      stopped = true;
    };
  }, [id, open]);

  const fields = (item?.fields ?? {}) as Record<string, unknown>;
  const size = typeof fields.size === "number" ? fields.size : undefined;
  // SNIFFED and CLAIMED are two different fields and the difference is the
  // security property, not decoration - mcp_attachments.go names them apart on
  // purpose. content_type is what the NODE found in the bytes; claimed_type is
  // what the writer said they were. This card had one variable called `claim`
  // holding the sniffed value and a badge reading "claims image/png", so it
  // credited the node's own finding to whoever uploaded the file: the exact
  // reading the naming exists to prevent.
  const sniffed = typeof fields.content_type === "string" ? fields.content_type : undefined;
  const claimed = typeof fields.claimed_type === "string" ? fields.claimed_type : undefined;
  // sha256, which is what the field is called. This read `fields.digest`, which
  // no attachment has ever had, so the digest was silently absent on every card
  // since the day it was written - absent and empty being indistinguishable to
  // a `typeof` test.
  const digest = typeof fields.sha256 === "string" ? fields.sha256 : undefined;

  return (
    // A COLUMN. It was `flex items-center`, a row, and the preview is a sibling
    // of the caption - so an opened image became another item in that row,
    // pushed to the far side of the message with the caption floating at its
    // vertical middle. The operator sent a picture of it captioned "the layout
    // is broken". The caption belongs above the thing it captions.
    <div className="flex flex-col gap-1 rounded-md border border-border bg-muted/40 px-2 py-1 text-xs">
      <div className="flex items-center gap-2">
        {err ? (
          <span className="text-muted-foreground">no such attachment: {shortId(id, 8)}</span>
        ) : (
          <>
            <span className="min-w-0 truncate font-medium">{item?.title || shortId(id, 8)}</span>
            {size !== undefined ? <Badge variant="outline">{human(size)}</Badge> : null}
            {sniffed ? <Badge variant="outline">{sniffed}</Badge> : null}
            {/* Only when the writer's claim disagrees with the bytes, because
              that disagreement is worth a reader's attention and an agreement
              is noise. */}
            {claimed && claimed !== sniffed ? (
              <Badge variant="outline" title="what the writer said these bytes were">
                claimed {claimed}
              </Badge>
            ) : null}
            {digest ? (
              <span className="font-mono text-muted-foreground" title={digest}>
                {digest.slice(0, 12)}
              </span>
            ) : null}
            <button
              type="button"
              className="ml-auto text-primary underline"
              onClick={() => setOpen((on) => !on)}
            >
              {open ? "hide" : "open"}
            </button>
          </>
        )}
      </div>
      {open && content === null && !err ? (
        <span className="text-muted-foreground">not on this node</span>
      ) : null}
      {/* Rendered from the SNIFFED type and never from the claim, which is the
          rule the field naming exists to keep: "image/png" on a payload of
          markup is how a render path becomes an injection surface. */}
      {open && content !== null && sniffed?.startsWith("image/") ? (
        <img
          src={`data:${sniffed};base64,${content}`}
          alt={item?.title || "attachment"}
          className="max-h-64 max-w-full self-start rounded object-contain"
        />
      ) : null}
      {open && content !== null && !sniffed?.startsWith("image/") ? (
        <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap break-words rounded bg-muted p-2 font-mono text-[10px]">
          {atob(content).slice(0, 2048)}
        </pre>
      ) : null}
    </div>
  );
}

/** The row of cards under a message that carries attachments. */
export function AttachmentCards({ ids }: { ids: string[] }) {
  if (ids.length === 0) return null;
  return (
    <div className="flex flex-col gap-1 pt-1">
      {ids.map((id) => (
        <Card key={id} id={id} />
      ))}
    </div>
  );
}

/** human renders a byte count as a person reads it. */
function human(n: number): string {
  if (n < 1024) return `${n}B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}KB`;
  return `${(n / (1024 * 1024)).toFixed(1)}MB`;
}
