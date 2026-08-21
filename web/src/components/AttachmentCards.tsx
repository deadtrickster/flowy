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
  // Whether the full-size view is up. Per card, so two images in one message
  // cannot both be open and fight over the overlay.
  const [whole, setWhole] = useState(false);

  // ESCAPE CLOSES IT, which is the first thing a person tries and the only
  // thing a keyboard user has: the backdrop is a pointer target and nothing
  // else dismisses this. Bound only while the overlay is up, so the transcript
  // keeps its own key handling the rest of the time.
  useEffect(() => {
    if (!whole) return;
    const shut = (event: KeyboardEvent) => {
      if (event.key === "Escape") setWhole(false);
    };
    window.addEventListener("keydown", shut);
    return () => window.removeEventListener("keydown", shut);
  }, [whole]);
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
      {/*
        THE PREVIEW IS A DOOR, not a picture.
        
        The operator, having just asked us to put screenshots in the room rather
        than in a terminal only one of us reads: "hmm when i tap on the image it
        stays small preview". It was capped at max-h-64 - 256 pixels - and
        nothing made it bigger: the image was not clickable, and
        GET /api/attachment/{id} answers JSON with base64 rather than bytes, so
        a plain link would not open it in a tab either.
        
        A console screenshot is 1500 wide. At 256 tall the thing it was taken to
        show is not in it, which made the agreement about posting them worthless.
      */}
      {open && content !== null && sniffed?.startsWith("image/") ? (
        <button
          type="button"
          data-attachment-open={id}
          aria-label={`see ${item?.title || "the attachment"} full size`}
          className="cursor-zoom-in self-start"
          onClick={() => setWhole(true)}
        >
          <img
            src={`data:${sniffed};base64,${content}`}
            alt={item?.title || "attachment"}
            className="max-h-64 max-w-full rounded object-contain"
          />
        </button>
      ) : null}
      {/*
        FULL SIZE, over the page, and dismissed by anything a person would try:
        the backdrop, Escape, or the button that opened it. It is a fixed
        overlay rather than an in-place expansion because the card lives in a
        scrolling transcript - growing it in place moves every message under the
        reader, which is the scrolling defect this console has already paid for
        once.
      */}
      {whole && content !== null && sniffed?.startsWith("image/") ? (
        <div
          data-attachment-whole={id}
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4"
          role="presentation"
          onClick={() => setWhole(false)}
        >
          {/* object-contain and max dimensions rather than natural size: an
              image larger than the window would otherwise overflow it and the
              reader would be looking at its top left corner with no way to
              reach the rest. */}
          <img
            src={`data:${sniffed};base64,${content}`}
            alt={item?.title || "attachment"}
            className="max-h-full max-w-full object-contain"
          />
        </div>
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
