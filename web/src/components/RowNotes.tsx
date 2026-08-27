import { useState } from "react";

import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { type Artifact, api } from "@/lib/api";
import { writeFile } from "@/lib/attach";
import { useBodyAttachments } from "@/lib/attachrefs";
import { renderDocument } from "@/lib/markdown";
import { speakerStyle } from "@/lib/speakercolour";
import { clock, shortId } from "@/lib/utils";

interface Props {
  artifact: Artifact;
  /** onAppended hands the updated row back, so the page around this agrees -
   * StatusControl's onMoved, for the same reason: the node answered with the
   * row, and a second copy of it held in here would be the one that drifts. */
  onAppended: (artifact: Artifact) => void;
}

/**
 * WHAT WAS LEARNED ABOUT THIS ROW, under the body somebody filed it with.
 *
 * A row used to be fixed at the moment it was written. Everything found out
 * afterwards - the measurement, the real fix shape, what it turned out to be
 * blocked on - went into a room, scrolled away, and was rediscovered by whoever
 * picked the row up next; one defect was diagnosed four times by four agents in
 * an evening. The node can hold it now (internal/store/todonote.go). This is
 * the half that lets a person read it without knowing a door exists.
 *
 * UNDER THE BODY, NOT BESIDE IT. The order on screen is the order the reader
 * needs: the author's statement of the work, then what was learned about it,
 * oldest first, which is the order it was learned in. A panel off to the side
 * would make the notes a thing to go and look at, and the whole failure this
 * fixes is that they were somewhere you had to know to look.
 *
 * NOTHING HERE EDITS OR DELETES, because the node has no verb for either. There
 * is no pencil on an entry and no cross beside it: a note that turned out to be
 * wrong is answered by a further note saying so, which is what the record should
 * say anyway. The box at the bottom appends and that is the only write on this
 * component.
 *
 * THE NOTES ARE NOT FETCHED HERE. They come on the row from the single-row read
 * the page already did, and back again on the append's own answer, both out of
 * the node's permission-filtered read. A fetch of the log door beside that would
 * be the same entries asked a second time, with a window in which the two
 * answers disagree - see todonote.go's viewNotes, which is the same decision one
 * layer down.
 *
 * A NOTE BODY IS MARKDOWN, the same GFM the artifact body above it renders
 * through renderDocument - one dialect per page. It used to be a <pre> dump,
 * which is how an operator note written in markdown ("we support ghfmd here")
 * showed its asterisks and backticks to the room.
 */
/**
 * One comment's body, with the files it refers to drawn in it.
 *
 * ITS OWN COMPONENT BECAUSE OF THE FETCH. A comment names its screenshot the
 * way any other body does - ![what it shows](01M0...) - and resolving that
 * means a read per referenced file, which is a hook, which cannot be called
 * inside the map over the notes. So each body is a component and each fetches
 * its own.
 *
 * A comment carrying no reference does no work here: attachmentsIn finds
 * nothing, the effect returns early, and this is an ordinary render.
 */
function NoteBody({ note }: { note: string }) {
  const files = useBodyAttachments(note);
  return (
    <div
      data-note-body=""
      className="report-body text-sm"
      // Rendered, not dumped: markdown to HTML, sanitized because a note is
      // agent-written. The sanitizer is why noDangerouslySetInnerHtml is off
      // for this file in biome.json - the rule cannot see through DOMPurify,
      // and the comment cannot sit inside the tag where the rule fires.
      dangerouslySetInnerHTML={{ __html: renderDocument(note, files) }}
    />
  );
}

export function RowNotes({ artifact, onAppended }: Props) {
  const [draft, setDraft] = useState("");
  // How many files are on their way up. A count and not a flag, because two
  // pasted screenshots are two uploads and a flag would clear on the first.
  const [uploading, setUploading] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const notes = artifact.notes ?? [];

  /**
   * A SCREENSHOT PASTED INTO A COMMENT LANDS IN THE COMMENT'S OWN TEXT.
   *
   * The operator's words were "screenshots should be supported in
   * notes/comments... plus listed in attachment, JIRA style". The reference goes
   * into the draft as ordinary markdown - ![name](id) - rather than into a
   * second field beside it, and that is the decision worth stating: a comment is
   * a body, bodies already draw what they refer to, and a parallel list of files
   * per comment would be a second place for the same fact to be wrong.
   *
   * It is put where the cursor is not: appended on its own line. A paste that
   * chopped a half-typed sentence in two would be the box editing prose somebody
   * was in the middle of.
   */
  const attach = async (file: File, pasted: boolean) => {
    setUploading((n) => n + 1);
    setError(null);
    try {
      // Through lib/attach, which is where the ceiling, the 32k base64 chunking
      // and the refusal sentences live. A second copy of them is a second set
      // of answers - see 01M0GGQ8D4.
      const carried = await writeFile(file, "", pasted ? "pasted into a note" : file.name);
      setDraft((text) => `${text.replace(/\s*$/, "")}\n\n![${carried.name}](${carried.id})\n`);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setUploading((n) => n - 1);
    }
  };

  const append = async () => {
    const text = draft.trim();
    if (!text || busy) return;
    setBusy(true);
    setError(null);
    try {
      const written = await api.noteTodo(artifact.id, text);
      onAppended(written.item);
      setDraft("");
    } catch (err) {
      // The node's own words. An empty note and a row with no project are both
      // refused up there with a sentence that says what to do about it, and a
      // console that replaced either with "could not add note" would be hiding
      // the only useful part of the answer.
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      data-row-notes={notes.length}
      className="flex flex-col gap-3 rounded-lg border border-border p-3"
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-muted-foreground text-xs uppercase tracking-wide">
          notes
        </span>
        <span className="text-muted-foreground text-xs">
          what has been learned about this row since it was filed - added to, never rewritten
        </span>
      </div>

      {notes.length > 0 ? (
        <ol className="flex flex-col gap-3">
          {notes.map((entry) => (
            <li key={entry.id} data-row-note={entry.id} className="flex flex-col gap-1">
              {/*
                Who wrote it and when, above the words rather than after them: a
                reader deciding how much weight to give a measurement wants the
                seat before the sentence, and an agent's note and its operator's
                are read differently. The kind is drawn in words - the seat is an
                id either way, so nothing else on the line says which it is.

                The NAME comes first when the node resolved one (NoteEntry
                actor_name, the same rule as Artifact.author): a wall of notes
                attributed by id tails is a wall a reader cannot tell apart.
                Without one the tail of the id is kept - a note carries no
                name for the seat, and a blank would be worse than the tail -
                with the full id in the title, which is what the status
                history beside this does.
              */}
              <div className="flex flex-wrap items-baseline gap-2 text-xs">
                <span
                  data-note-actor={entry.actor}
                  className="font-mono"
                  style={speakerStyle(entry.actor)}
                  title={entry.actor_user ? `${entry.actor}, for ${entry.actor_user}` : entry.actor}
                >
                  {entry.actor_name || shortId(entry.actor, 8)}
                </span>
                {entry.actor_kind ? (
                  <span className="text-muted-foreground">
                    {entry.actor_kind === "agent" ? "an agent" : "a person"}
                  </span>
                ) : null}
                <span className="ml-auto text-muted-foreground">{clock(entry.created)}</span>
              </div>
              <NoteBody note={entry.note} />
            </li>
          ))}
        </ol>
      ) : (
        // Said rather than left blank, and said as an invitation. An empty
        // section that draws nothing reads as a section that failed to load.
        <div className="text-muted-foreground text-xs">
          nothing has been learned about this row yet - a measurement, the fix shape, or what it is
          blocked on all belong here
        </div>
      )}

      {/*
        The box. It is a form so enter-in-the-button and the submit both work,
        and the textarea takes newlines: a note is a paragraph more often than a
        line, which is why enter does NOT send here and does in the message box
        beside a conversation.
      */}
      <form
        className="flex flex-col gap-2"
        autoComplete="off"
        onSubmit={(event) => {
          event.preventDefault();
          void append();
        }}
      >
        <Textarea
          data-note-draft=""
          value={draft}
          disabled={busy}
          onChange={(event) => setDraft(event.target.value)}
          onPaste={(event) => {
            // ONLY FILES. A paste into a comment box is overwhelmingly text and
            // text must land in the box - intercepting that would break the
            // commonest thing this does to serve the rarest.
            const files = Array.from(event.clipboardData.files);
            if (files.length === 0) return;
            event.preventDefault();
            for (const file of files) void attach(file, true);
          }}
          placeholder="what did you learn about this row? paste a screenshot straight in"
          aria-label="add a note"
        />
        <div className="flex items-center gap-3">
          {/* The paperclip is the second way in - the first is paste, because a
              screenshot is in the clipboard and was never a file on disk. */}
          <label className="cursor-pointer text-muted-foreground text-xs hover:underline">
            <input
              data-note-attach=""
              type="file"
              className="hidden"
              disabled={busy}
              onChange={(event) => {
                const file = event.target.files?.[0];
                event.target.value = "";
                if (file) void attach(file, false);
              }}
            />
            {uploading > 0 ? `attaching ${uploading}…` : "attach a file"}
          </label>
          {error ? <span className="text-destructive text-xs">{error}</span> : null}
          <Button
            type="submit"
            size="sm"
            variant="secondary"
            data-note-add=""
            className="ml-auto"
            disabled={busy || draft.trim() === ""}
          >
            {busy ? "adding…" : "add a note"}
          </Button>
        </div>
      </form>
    </div>
  );
}
