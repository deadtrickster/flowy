import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { ApiError, type Artifact, api } from "@/lib/api";
import { todoAssignee, todoRaiser } from "@/lib/todos";
import { shortId } from "@/lib/utils";

/**
 * The row behind a message, without leaving the room.
 *
 * A raise says "raised a todo" in prose and the transcript knew nothing else
 * about it: `event.artifact` has carried the id all along and nothing read it,
 * so the one thing a reader wants at that moment - what IS that row now - cost
 * a navigation away from the conversation they were reading.
 *
 * A PREVIEW, NOT A SECOND EDITOR. It shows what the row is and links to the
 * page that owns it. Two places that both edit a row is how the console gets a
 * stale copy of the truth, and the full page already exists.
 *
 * WHY THE FAILURES ARE DRAWN AND NOT SWALLOWED. The three ways this ends
 * badly - the row is gone, this reader may not see it, the node did not answer
 * - are indistinguishable from a dead button if the card renders empty or
 * never opens. The rule this was built under is that done means the flow
 * passes, and "click it and nothing happens" is the exact defect that rule
 * exists to stop, so each of them says which one it is.
 */
export function RowCard({ id, onClose }: { id: string; onClose: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [row, setRow] = useState<Artifact | null>(null);
  const [problem, setProblem] = useState("");

  useEffect(() => {
    // Cancelled rather than raced: opening two cards quickly must not let the
    // first answer land in the second's card.
    let live = true;
    setRow(null);
    setProblem("");
    api
      .artifact(id)
      .then((got) => {
        if (live) setRow(got);
      })
      .catch((err) => {
        if (!live) return;
        setProblem(reasonFor(err));
      });
    return () => {
      live = false;
    };
  }, [id]);

  // Opened as a MODAL rather than rendered open: showModal() is what puts it
  // in the top layer, lights ::backdrop, traps focus and makes Escape work.
  // A <dialog open> attribute does none of those - it is a visible dialog
  // sitting in the flow, which is the div this replaces wearing a better tag.
  useEffect(() => {
    dialog.current?.showModal();
  }, []);

  const project = row?.project ?? "_";
  const assignee = row ? todoAssignee(row) : "";
  const raiser = row ? todoRaiser(row) : "";

  return (
    /*
      A REAL <dialog>, opened with showModal(), rather than a div painted to
      look like one. The browser then owns the things a hand-rolled overlay
      gets wrong: Escape closes it, focus is trapped inside it while it is
      open and restored to the chip when it shuts, and everything behind it is
      inert to a screen reader. role="dialog" on a div claims all of that and
      delivers none of it.

      The backdrop is ::backdrop, and a click on it arrives here with the
      dialog itself as the target - the card inside is a different target - so
      "click outside to dismiss" is one comparison rather than a wrapper with
      its own click handler that a keyboard could never reach.
    */
    // biome-ignore lint/a11y/useKeyWithClickEvents: a modal dialog's keyboard dismissal is Escape, which the browser fires as `close` and onClose below handles; a keydown here would be a second, worse copy of it
    <dialog
      ref={dialog}
      onClose={onClose}
      onClick={(e) => {
        if (e.target === dialog.current) onClose();
      }}
      aria-label={`row ${shortId(id)}`}
      className="max-h-[80vh] w-full max-w-lg rounded-lg border border-border bg-card p-4 text-foreground shadow-lg backdrop:bg-background/70"
      data-row-card={id}
    >
      {problem ? (
        <p className="text-muted-foreground text-sm" data-row-card-problem="">
          {problem}
        </p>
      ) : !row ? (
        <p className="text-muted-foreground text-sm" data-row-card-loading="">
          reading the row...
        </p>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2 pb-2">
            <Badge variant="outline">{row.status || "open"}</Badge>
            {row.kind ? <Badge variant="outline">{row.kind}</Badge> : null}
            <span className="font-mono text-muted-foreground text-xs">{shortId(id)}</span>
          </div>
          <h2 className="font-medium text-base" data-row-card-title="">
            {row.title || "(untitled)"}
          </h2>
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 pt-2 text-sm">
            <dt className="text-muted-foreground">holder</dt>
            <dd data-row-card-assignee="">{assignee || "unowned"}</dd>
            <dt className="text-muted-foreground">raised by</dt>
            <dd>{raiser || "unrecorded"}</dd>
          </dl>
          {/*
            The opening of the body and not the whole of it: this is a card
            over a conversation, and a row whose body runs a page long would
            bury the link to the page built for reading it.
          */}
          {row.body ? (
            <p className="whitespace-pre-wrap pt-2 text-muted-foreground text-sm">
              {row.body.slice(0, 400)}
              {row.body.length > 400 ? "..." : ""}
            </p>
          ) : null}
          <Link
            className="mt-3 inline-block text-primary text-sm hover:underline"
            to={`/p/${encodeURIComponent(project)}/${encodeURIComponent(row.type)}/${row.id}`}
            data-row-card-open=""
            onClick={onClose}
          >
            open the full row
          </Link>
        </>
      )}
    </dialog>
  );
}

/**
 * WHICH failure it was, in the words a reader can act on.
 *
 * 410 is the one worth separating: a withdrawn row is not a missing one, and
 * saying "no such row" about something somebody deliberately withdrew sends
 * the reader looking for a bug in the link.
 */
function reasonFor(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 410) return "this row was withdrawn, so there is nothing to show.";
    if (err.status === 404) return "there is no row with this id on this node.";
    if (err.status === 403) return "this row exists and your token may not read it.";
    return `the node refused: ${err.message}`;
  }
  return "the node did not answer, so the row could not be read.";
}
