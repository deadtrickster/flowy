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
 *
 * `eager` is the exception, and it is a narrow one: a card on the ATTACHMENT
 * ROW'S OWN PAGE is the thing that page is about, so hiding it behind a click
 * is hiding the answer to the question that was asked. In a transcript the
 * click stays - a room of cards must not cost a room of megabytes.
 *
 * AND THE BYTES CAN BE TAKEN AWAY. Until this was written there was no
 * download anywhere in the console: a picture could be seen full size and
 * nothing else could be reached at all, so a pdf, a log or a tarball was
 * readable only by a seat with a token and a shell. The operator, on five
 * attachments posted to a room: "the attachment rows they did do not render
 * images nor do they have a download button".
 */
function Card({ id, eager = false }: { id: string; eager?: boolean }) {
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
  // Open from the start only where the page IS this file - see `eager` above.
  const [open, setOpen] = useState(eager);
  // THREE STATES, NOT TWO, AND THE THIRD IS WHY THIS IS undefined AND NOT null.
  //
  // The operator, on five renders another seat had just posted: "all attachments
  // \"not on this node\"". Nothing was wrong with them - 1.5MB and 425KB of
  // image/png, on the node, readable. The card said that WHILE IT WAS STILL
  // FETCHING, because content was null before the answer arrived and null is
  // also what the node sends when it genuinely holds no bytes.
  //
  //   undefined   not answered yet
  //   null        answered, and there is no payload - store.ErrNoBytes, the
  //               real "not on this node"
  //   string      answered, with bytes
  //
  // A small file hides this: 77 bytes fills before anybody reads the words, so
  // the bug is invisible exactly where it would be convenient to test. That is
  // the same shape as the panel that was asked whether it had drawn while it was
  // still loading and reported "drew no panel at all" - wait for a RESOLVED
  // state, and give the unresolved one its own name.
  const [content, setContent] = useState<string | null | undefined>(undefined);
  const [err, setErr] = useState<string | null>(null);
  // WHAT THE DOWNLOAD SAID, and it is a third state again rather than a flag.
  // null is "nothing to report", a string is "asked and cannot hand it over" -
  // a row whose bytes are not on this node, or a read the node refused. A
  // download control that silently does nothing is the failure this whole file
  // has been bitten by twice; if it cannot save, it says which.
  const [why, setWhy] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

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
  // THE FILE'S OWN NAME, which is not its title. fields.filename is what the
  // writer called the file - "snake.jpg" - and the title is a sentence a person
  // wrote about it - "snake for Nikita". Saving the title puts a file called
  // "snake for Nikita" with no extension in somebody's Downloads, which no
  // viewer will open. The id is the last resort and is at least unambiguous.
  const saveAs = (typeof fields.filename === "string" && fields.filename) || id;

  /**
   * Hand the bytes to the browser's own download, which is the only way a page
   * can put a file where a person can find it.
   *
   * IT FETCHES RATHER THAN LINKING, and that is forced: GET /api/attachment/{id}
   * answers JSON with base64, so there is no URL that serves the file - an <a
   * href> would download a document full of JSON. It also carries the token,
   * which a plain link would not for a seat authenticating on a bearer rather
   * than the operator's cookie.
   *
   * It re-asks even when the preview is already open. One extra read of a file
   * a person has just chosen to save is cheaper than a stale copy: the state
   * here is a snapshot from whenever the card was opened.
   */
  const save = async () => {
    setSaving(true);
    setWhy(null);
    try {
      const page = await api.attachment(id);
      if (page.content === null) {
        // The node can see the row and holds no payload - store.ErrNoBytes,
        // which attachments_http.go keeps apart from a 404 on purpose. Say it,
        // rather than saving an empty file that looks like the real one.
        setWhy("not on this node, so there is nothing to save");
        return;
      }
      const raw = atob(page.content);
      const bytes = new Uint8Array(raw.length);
      for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
      // The SNIFFED type, as everything else here does; octet-stream when the
      // node could not name one, so the browser saves rather than guesses.
      const blob = new Blob([bytes], { type: sniffed || "application/octet-stream" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = saveAs;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (e) {
      setWhy(e instanceof Error ? e.message : "the node refused to hand it over");
    } finally {
      setSaving(false);
    }
  };

  return (
    // A COLUMN. It was `flex items-center`, a row, and the preview is a sibling
    // of the caption - so an opened image became another item in that row,
    // pushed to the far side of the message with the caption floating at its
    // vertical middle. The operator sent a picture of it captioned "the layout
    // is broken". The caption belongs above the thing it captions.
    // data-attachment is the id, so a check can ask WHICH card is drawn rather
    // than how many. Counting was enough while one list drew these; two lists
    // draw them now and "the room has a card" and "the thread has THAT card"
    // are different questions.
    <div
      data-attachment={id}
      className="flex flex-col gap-1 rounded-md border border-border bg-muted/40 px-2 py-1 text-xs"
    >
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
            <span className="ml-auto flex shrink-0 items-center gap-2">
              {/* SAVE IT, which until now the console could not do at all. It
                sits beside open rather than inside it: a reader wanting the
                file does not want to look at it first, and a tarball has
                nothing to look at. */}
              <button
                type="button"
                data-attachment-save={id}
                className="text-primary underline disabled:opacity-60"
                disabled={saving}
                title={`save ${saveAs}`}
                onClick={() => void save()}
              >
                {saving ? "saving…" : "download"}
              </button>
              <button
                type="button"
                // Named for the same reason data-attachment is: a check should
                // ask for the control by name rather than by its label, which is
                // "open" or "hide" depending on the state it is trying to change.
                data-attachment-toggle={id}
                className="text-primary underline"
                onClick={() => setOpen((on) => !on)}
              >
                {open ? "hide" : "open"}
              </button>
            </span>
          </>
        )}
      </div>
      {/* Why the download handed nothing over. Its own line and its own
          attribute, so a check can ask what the control SAID rather than
          watching for a file that was never going to arrive. */}
      {why ? (
        <span className="text-muted-foreground" data-attachment-save-why={id}>
          {why}
        </span>
      ) : null}
      {open && content === undefined && !err ? (
        <span className="text-muted-foreground" data-attachment-loading={id}>
          fetching…
        </span>
      ) : null}
      {open && content === null && !err ? (
        <span className="text-muted-foreground" data-attachment-absent={id}>
          not on this node
        </span>
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
      {open && typeof content === "string" && sniffed?.startsWith("image/") ? (
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
      {whole && typeof content === "string" && sniffed?.startsWith("image/") ? (
        // A BACKDROP THAT CLOSES IS A CONTROL, so it is a button.
        //
        // It was a div with role="presentation" and an onClick, which biome
        // refuses under a11y/useKeyWithClickEvents and is right to: a person on
        // a keyboard had Escape and nothing else, and a person on a screen
        // reader had an element announced as decoration that was the only way
        // out. A button is the element that already means "press this and
        // something happens" - it takes focus, it fires on Enter and Space, and
        // the label says which image it closes.
        //
        // Escape stays, bound while the overlay is open: it is what a reader
        // reaches for first and it does not require finding the backdrop.
        <button
          type="button"
          data-attachment-whole={id}
          aria-label={`close ${item?.title || "the image"}`}
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4"
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
        </button>
      ) : null}
      {open && typeof content === "string" && !sniffed?.startsWith("image/") ? (
        <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap break-words rounded bg-muted p-2 font-mono text-[10px]">
          {atob(content).slice(0, 2048)}
        </pre>
      ) : null}
    </div>
  );
}

/**
 * The row of cards under a message that carries attachments.
 *
 * `eager` opens every card in the list without a click, and belongs only where
 * the page is ABOUT the files - an attachment row's own page. A transcript
 * passes nothing and keeps the click.
 */
export function AttachmentCards({ ids, eager = false }: { ids: string[]; eager?: boolean }) {
  if (ids.length === 0) return null;
  return (
    <div className="flex flex-col gap-1 pt-1">
      {ids.map((id) => (
        <Card key={id} id={id} eager={eager} />
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
