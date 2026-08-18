import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";

import { DocumentPanes, documentRoom } from "@/components/DocumentPanes";
import { ReproPanel } from "@/components/ReproPanel";
import { StatusControl } from "@/components/StatusControl";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import DOMPurify from "dompurify";
import { marked } from "marked";

import { type Artifact, LIFECYCLE_TYPES, api, refPath } from "@/lib/api";
import { useSession } from "@/lib/session";
import { todoAssignee, todoRaiser } from "@/lib/todos";
import { shortId } from "@/lib/utils";

/**
 * One artifact, at /p/:project/:type/:id.
 *
 * The project and the type are in the path because a link is a thing people
 * send each other: it should say what it points at without being followed. The
 * id is what the node is asked for, and the node decides whether this token may
 * see it - an unreadable artifact is a 404 here exactly as it is on the API.
 *
 * Beside the document, its own conversation and its own plan. The operator
 * asked for it in those words - "all documents should have associated chat and
 * todo window ... I want to discuss it, I also want to cite its parts" - and
 * the reason is that until now a report was read here and argued about in
 * #general, where the argument is a link nobody in the room has followed. See
 * DocumentPanes for what the room is called and why it is an ordinary one.
 */
export function ArtifactView() {
  const { project, type, id = "" } = useParams();
  const { token } = useSession();
  const [artifact, setArtifact] = useState<Artifact | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Where the replacement lives, from ITS OWN ref rather than from the row on
  // screen. Built out of this artifact's project and type it was a guess: a
  // replacement is a different row and may sit in another project or be another
  // type. Without a ref there is no route to offer, so the id shows as text.
  const replacedPath = refPath(artifact?.replaced_by_ref);
  // Who the work came from and who is carrying it, off the same fields the queue
  // reads them off - see web/src/lib/todos.ts, which is the one place this
  // console digs into an artifact's fields for either.
  const raiser = artifact ? todoRaiser(artifact) : "";
  const assignee = artifact ? todoAssignee(artifact) : "";
  // The words somebody has selected in the document, and the ones they have
  // asked to quote. Two states rather than one: selecting is reading, and the
  // draft is only written when somebody says so.
  const [selection, setSelection] = useState("");
  const [quote, setQuote] = useState<{ text: string } | null>(null);

  // Selection is read on mouse-up and key-up, which is every way a browser
  // finishes one. It is read out of the window rather than out of the rendered
  // markdown because what the reader wants to quote is the words as they read
  // them - the node stores no pointer into a document body, so a citation here
  // could only ever be the words themselves, and offsets into HTML that was
  // derived from markdown would point at nothing anybody could check.
  //
  // AN EMPTY READING IS NOT AN UNDONE SELECTION. Pressing anything clears the
  // browser's selection on the way down, and the press's own mouse-up then
  // bubbles into this listener - so the bar took itself down under the pointer
  // in the moment between the press and the click that uses it, and the button
  // quoted an empty string. Measured in a real browser; a build cannot see it.
  // So the last passage somebody highlighted stands until they highlight
  // another one, quote it, or put the bar away.
  const captureSelection = () => {
    const selected = (window.getSelection()?.toString() ?? "").trim();
    if (selected) setSelection(selected);
  };

  useEffect(() => {
    if (!token || !id) return;
    let stopped = false;
    api
      .artifact(id)
      .then((found) => {
        if (!stopped) {
          setArtifact(found);
          setError(null);
        }
      })
      .catch((err: Error) => {
        if (!stopped) {
          setArtifact(null);
          setError(err.message);
        }
      });
    return () => {
      stopped = true;
    };
  }, [token, id]);

  return (
    <div className="flex h-full min-h-0">
      {/*
        The document column listens for the end of a selection - mouse-up and
        key-up are every way a browser finishes one. It is a listener and not a
        control: it reads what the browser already did and adds no behaviour of
        its own, so there is nothing here for a keyboard user to activate that
        selecting text does not already do.
      */}
      <div
        className="min-w-0 flex-1 overflow-y-auto p-6"
        onMouseUp={captureSelection}
        onKeyUp={captureSelection}
      >
        <div className="mx-auto flex max-w-3xl flex-col gap-4">
          <div className="flex items-center gap-2 text-muted-foreground text-xs">
            <Link to="/" className="hover:text-foreground">
              overview
            </Link>
            <span>/</span>
            <span>{project}</span>
            <span>/</span>
            <span>{type}</span>
            <span>/</span>
            <span className="font-mono">{id}</span>
          </div>

          {error ? <div className="text-destructive text-sm">{error}</div> : null}
          {!token ? <div className="text-muted-foreground text-sm">no token</div> : null}

          {/*
           * A report that has been superseded says so where somebody reads it,
           * above the document rather than in a badge beside the title: whoever
           * opened this is about to act on what it says, and the one thing they
           * need before that is that a newer one exists. The node derives
           * replaced_by through the same filter as the row, so the link is only
           * ever offered when it goes somewhere this token can follow.
           */}
          {artifact?.replaced_by ? (
            <div
              data-replaced-by={artifact.replaced_by}
              className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-destructive text-sm"
            >
              this report has been replaced -{" "}
              {replacedPath ? (
                <Link className="font-mono underline" to={replacedPath}>
                  {artifact.replaced_by}
                </Link>
              ) : (
                <span className="font-mono">{artifact.replaced_by}</span>
              )}{" "}
              supersedes it
            </div>
          ) : null}

          {artifact ? (
            <Card>
              <CardHeader>
                <CardTitle className="text-base">{artifact.title || artifact.id}</CardTitle>
                <div className="flex flex-wrap gap-1 pt-1">
                  <Badge variant="secondary">{artifact.type}</Badge>
                  {artifact.kind ? <Badge variant="outline">{artifact.kind}</Badge> : null}
                  <Badge variant="outline">{artifact.visibility}</Badge>
                  {artifact.status ? <Badge variant="outline">{artifact.status}</Badge> : null}
                  {typeof (artifact.fields as Record<string, unknown> | null | undefined)?.as_of ===
                  "string" ? (
                    <Badge variant="outline">
                      as of {(artifact.fields as Record<string, string>).as_of}
                    </Badge>
                  ) : null}
                  {(artifact.tags ?? []).map((tag) => (
                    <Badge key={tag} variant="outline">
                      {tag}
                    </Badge>
                  ))}
                </div>
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                {/* Only the types that have a lifecycle get the control. A
                  transcript has no status to move, and the node would say so. */}
                {LIFECYCLE_TYPES.includes(artifact.type) ? (
                  <StatusControl artifact={artifact} onMoved={setArtifact} />
                ) : null}
                {artifact.type === "report" ? (
                  // A report is a document somebody reads on purpose, so it is
                  // rendered, not dumped: markdown to HTML, sanitized because
                  // the body is agent-written. The sanitizer is why
                  // noDangerouslySetInnerHtml is off for this file in biome.json -
                  // the rule cannot see through DOMPurify, and the comment cannot
                  // sit inside the tag where the rule fires.
                  <div
                    className="report-body text-sm"
                    dangerouslySetInnerHTML={{
                      __html: DOMPurify.sanitize(
                        marked.parse(artifact.body, { async: false }) as string,
                      ),
                    }}
                  />
                ) : (
                  <pre className="whitespace-pre-wrap break-words font-sans text-sm">
                    {artifact.body}
                  </pre>
                )}
                {artifact.discovery ? (
                  <div>
                    <div className="pb-1 font-medium text-muted-foreground text-xs">discovery</div>
                    <pre className="whitespace-pre-wrap break-words font-sans text-sm">
                      {artifact.discovery}
                    </pre>
                  </div>
                ) : null}
                {artifact.type === "finding" ? <FindingSection artifact={artifact} /> : null}

                {/*
                 * WHERE THE WORK CAME FROM AND WHO HAS IT, for the items that
                 * are work. Two facts and neither is the `owner` on the line
                 * below: that is the seat whose token wrote the row, which for
                 * a board four agents file into is the agent that typed it and
                 * not the party that asked for it. A page that showed only the
                 * author is the ambiguity the raiser exists to end, so both are
                 * here, said in words rather than as two bare names.
                 *
                 * Drawn when the row says either one. A note is not in the queue
                 * and carries neither, and it gets no line rather than a line of
                 * blanks.
                 */}
                {raiser || assignee ? (
                  <div className="text-muted-foreground text-xs">
                    <span data-artifact-raiser={raiser}>
                      raised by {raiser ? <strong>{raiser}</strong> : "nobody on the record"}
                    </span>
                    {", "}
                    <span data-artifact-assignee={assignee}>
                      carried by {assignee ? <strong>{assignee}</strong> : "nobody"}
                    </span>
                  </div>
                ) : null}
                <div className="font-mono text-muted-foreground text-xs">
                  hlc {artifact.hlc} · node {artifact.node} · owner {artifact.owner_user} ·{" "}
                  {/*
                  Whose words these are, which the node beside it does not
                  answer: a node signature says which machine relayed the row,
                  not who wrote it. "attributed" is not an accusation - it is
                  most rows, and it says this node is holding somebody's word
                  that the owner wrote them.
                */}
                  <span
                    title={
                      artifact.authorship === "authored"
                        ? "the owner signed these words with their own key, and this node verified it"
                        : "attributed: this node holds no signature of the owner's own for these words, so they are here on the word of the node that relayed the row"
                    }
                  >
                    {artifact.authorship === "authored" ? "authored" : "attributed"}
                  </span>
                </div>
              </CardContent>
            </Card>
          ) : null}

          {/*
          What is about to be quoted, shown before it is: the bar sticks to the
          bottom of the document column so it is under the words somebody just
          dragged over, rather than in the panel their eyes are not on. It is a
          button and not an automatic insert - selecting text is how people read
          a long report, and a console that pasted every selection into a draft
          would fight the reader.
        */}
          {selection ? (
            <div className="sticky bottom-0 flex items-start gap-3 rounded-md border border-border bg-card px-3 py-2 shadow">
              <span className="min-w-0 flex-1 truncate text-muted-foreground text-xs italic">
                “{selection}”
              </span>
              <button
                type="button"
                className="shrink-0 rounded border border-border px-1.5 py-0.5 text-xs hover:bg-accent/60"
                onClick={() => {
                  setQuote({ text: selection });
                  // The passage is in the draft and the bar has said what it
                  // had to say. Leaving it up would offer to quote something
                  // that has already been quoted.
                  window.getSelection()?.removeAllRanges();
                  setSelection("");
                }}
              >
                quote in the discussion
              </button>
              <button
                type="button"
                className="shrink-0 text-muted-foreground text-xs hover:text-foreground"
                aria-label="put the quote bar away"
                onClick={() => {
                  window.getSelection()?.removeAllRanges();
                  setSelection("");
                }}
              >
                ✕
              </button>
            </div>
          ) : null}
        </div>
      </div>

      {/*
        The document's own room, beside the document. Rendered whatever the
        artifact turned out to be, and before it has loaded: the room is named
        after the id in the path, so the conversation is readable even when the
        document itself is a 404 for this token - which is the case where
        somebody most needs to ask about it.
      */}
      <aside className="flex w-[26rem] shrink-0 flex-col border-border border-l">
        {id ? <DocumentPanes room={documentRoom(id)} quote={quote} /> : null}
      </aside>
    </div>
  );
}

/**
 * The finding-specific section: severity, kind and repro-class, tags and
 * related, and ReproPanel's mount point.
 *
 * NOT a file-an-issue control. Forge already works for any artifact - see the
 * generic lifecycle/status machinery above, which a finding rides exactly
 * like a bug - so a second, finding-only "file this" button here would be a
 * second door onto the same write, disagreeing with the first the day one of
 * them changes.
 *
 * severity has nowhere general to go: it is not in the badge row the card
 * header draws for every artifact type above, because only a finding (and a
 * bug) carries one that means anything. kind is already up there too, and is
 * repeated here because a reader looking at this section for "is this worth
 * my time" wants severity and kind together, not one here and one above.
 *
 * repro-class is read off Fields rather than a column of its own, because a
 * finding's repro tree IS a manifest in Fields - see
 * internal/store/findingrepro.go's head comment on why it gets no column,
 * the same reason lifecycle.go's head comment gives for the type itself.
 */
function FindingSection({ artifact }: { artifact: Artifact }) {
  const fields = (artifact.fields ?? {}) as Record<string, unknown>;
  const files = Array.isArray(fields.repro_files) ? fields.repro_files : [];
  const hasRepro = files.length > 0;
  const isolation =
    typeof fields.isolation === "string" && fields.isolation ? fields.isolation : "plain";

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-muted-foreground text-xs uppercase tracking-wide">
          finding
        </span>
        {artifact.severity ? <Badge variant="outline">severity: {artifact.severity}</Badge> : null}
        {artifact.kind ? <Badge variant="outline">{artifact.kind}</Badge> : null}
        <Badge variant="outline">{hasRepro ? `repro: ${isolation}` : "no repro tree"}</Badge>
        {(artifact.tags ?? []).map((tag) => (
          <Badge key={tag} variant="outline">
            {tag}
          </Badge>
        ))}
      </div>

      {artifact.related && artifact.related.length > 0 ? (
        <div>
          <div className="pb-1 font-medium text-muted-foreground text-xs">related</div>
          <div className="flex flex-wrap gap-1">
            {/* No link, unlike replaced_by above: related is a bare id with
                no ref beside it to build one from, and a link guessed out of
                this artifact's own project/type is exactly the mistake
                refPath (lib/api.ts) exists to refuse - it would as often as
                not point at the wrong row. */}
            {artifact.related.map((relatedId) => (
              <Badge key={relatedId} variant="outline" className="font-mono" title={relatedId}>
                {shortId(relatedId)}
              </Badge>
            ))}
          </div>
        </div>
      ) : null}

      <ReproPanel finding={artifact.id} runnable={hasRepro} />
    </div>
  );
}
