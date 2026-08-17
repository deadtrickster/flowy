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
  const claim = typeof fields.content_type === "string" ? fields.content_type : undefined;
  const digest = typeof fields.digest === "string" ? fields.digest : undefined;

  return (
    <div className="flex items-center gap-2 rounded-md border border-border bg-muted/40 px-2 py-1 text-xs">
      {err ? (
        <span className="text-muted-foreground">no such attachment: {shortId(id, 8)}</span>
      ) : (
        <>
          <span className="min-w-0 truncate font-medium">{item?.title || shortId(id, 8)}</span>
          {size !== undefined ? <Badge variant="outline">{human(size)}</Badge> : null}
          {claim ? <Badge variant="outline">claims {claim}</Badge> : null}
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
      {open && content === null && !err ? (
        <span className="text-muted-foreground">not on this node</span>
      ) : null}
      {open && content !== null && claim?.startsWith("image/") ? (
        <img
          src={`data:${claim};base64,${content}`}
          alt={item?.title || "attachment"}
          className="mt-1 max-h-64 rounded"
        />
      ) : null}
      {open && content !== null && !claim?.startsWith("image/") ? (
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
