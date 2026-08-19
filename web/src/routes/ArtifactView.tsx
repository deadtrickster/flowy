import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";

import { DocumentPanes, documentRoom } from "@/components/DocumentPanes";
import { ReproPanel } from "@/components/ReproPanel";
import { RowNotes } from "@/components/RowNotes";
import { SeverityDot, StateChip } from "@/components/StateMarks";
import { StatusControl } from "@/components/StatusControl";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { type Artifact, LIFECYCLE_TYPES, type OriginRef, api, refPath } from "@/lib/api";
import {
  UNKNOWN_UPSTREAM,
  evidenceOf,
  hasRepro,
  knownUpstream,
  refLabel,
  reportDraftOf,
  reproOf,
  upstreamOf,
} from "@/lib/findings";
import { renderDocument } from "@/lib/markdown";
import { useSession } from "@/lib/session";
import { evidenceTone, reproTone, severityTone, upstreamTone } from "@/lib/statecolour";
import { isQueueItem, todoAssignee, todoRaiser } from "@/lib/todos";
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
  const { token, whoami } = useSession();
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
  // WHERE THIS ROW CAME FROM, which is not what blocks it - see
  // internal/store/origin.go, where the two are different verbs because an edge
  // the ready query never reads must not share a name with one it does.
  //
  // Read beside the row rather than folded into it: provenance is a log of
  // entries and the row is a row, and a field on the artifact would be a second
  // copy that disagrees the moment somebody takes a relation back.
  const [origins, setOrigins] = useState<OriginRef[]>([]);
  // FIXING YOUR OWN WORDS. Two rules live on this page and the store decides
  // both: an item's title and body are its AUTHOR'S - a stranger rewriting them
  // is refused in one sentence - while its queue metadata moves for anybody who
  // can read it. So the editor is offered on the words and only to the owner,
  // and the status control beside it stays open to every reader.
  //
  // Offered rather than assumed: the check is whoami's user against the row's
  // owner, which is what the node will judge. A page that showed the editor to
  // everybody would be handing people a refusal instead of a control.
  const [editing, setEditing] = useState(false);
  const [draftTitle, setDraftTitle] = useState("");
  const [draftBody, setDraftBody] = useState("");
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
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
      .origins(id)
      .then((seen) => {
        if (!stopped) setOrigins(seen.origins ?? []);
      })
      .catch(() => {
        // A row with no provenance and a node that could not answer are
        // different, and neither is worth an error banner on a page about
        // something else: the section simply does not draw.
        if (!stopped) setOrigins([]);
      });
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
    /*
      STACKS WHEN THERE IS NO ROOM FOR TWO COLUMNS. This was a flex ROW with a
      fixed 26rem aside that could not shrink, so below about 900px the room
      ran off the right edge: measured at 600px the composer sat at x=301..692
      in a 600px viewport, which is a text area you cannot reach beside a send
      button you can - reported as "send button but no text area".

      The document reads first and the room follows it, because a narrow window
      is somebody reading rather than somebody chatting beside what they read.
    */
    <div className="flex h-full min-h-0 flex-col lg:flex-row">
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
            <span>{artifact?.project ?? project}</span>
            <span>/</span>
            {/* The ROW's type, not the path's, once there is a row.
             *
             * The segment is a claim the link makes, and the page is where it
             * gets checked: a link built before artifactPath existed may carry
             * `artifact`, which is not a type at all, and one built without a
             * type carries `_`. Rendering the segment here put that claim in a
             * breadcrumb two lines above a badge showing the real type, so the
             * page contradicted itself and the breadcrumb was the half a
             * reader believed. Until the row arrives the segment is all there
             * is, so it stands in - and it is replaced rather than corrected,
             * because a correction nobody sees is the same as being wrong. */}
            <span data-artifact-type={artifact?.type ?? type}>{artifact?.type ?? type}</span>
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
                {/*
                 * NAMED SO A CHECK CAN ASK FOR IT. A browser check that reads
                 * the page's text cannot tell "the row did not render" from
                 * "it rendered without a title" - both answer no - and the
                 * failure that follows is a slice of document somebody has to
                 * interpret. With the title as an element the question has
                 * three answers, and they are three different bugs.
                 */}
                {editing ? (
                  <div className="flex flex-col gap-2">
                    <Input
                      aria-label="title"
                      data-edit-title=""
                      value={draftTitle}
                      onChange={(e) => setDraftTitle(e.target.value)}
                    />
                    <textarea
                      aria-label="body"
                      data-edit-body=""
                      rows={12}
                      value={draftBody}
                      onChange={(e) => setDraftBody(e.target.value)}
                      className="rounded border border-border bg-background px-2 py-1 font-mono text-foreground text-xs"
                    />
                    {saveError ? (
                      <p data-edit-refused="" className="text-destructive text-xs">
                        {saveError}
                      </p>
                    ) : null}
                    <div className="flex items-center gap-2">
                      <Button
                        size="sm"
                        data-edit-save=""
                        disabled={saving || !draftTitle.trim()}
                        onClick={async () => {
                          setSaving(true);
                          setSaveError(null);
                          try {
                            const saved = await api.editWords({
                              id: artifact.id,
                              type: artifact.type,
                              kind: artifact.kind,
                              title: draftTitle.trim(),
                              body: draftBody,
                            });
                            setArtifact(saved);
                            setEditing(false);
                          } catch (err) {
                            // The node's own sentence. It says which rule was
                            // broken - words versus queue metadata - and this
                            // page must not improve on it.
                            setSaveError((err as Error).message);
                          } finally {
                            setSaving(false);
                          }
                        }}
                      >
                        {saving ? "saving…" : "save"}
                      </Button>
                      <Button
                        size="sm"
                        variant="secondary"
                        data-edit-cancel=""
                        onClick={() => {
                          setEditing(false);
                          setSaveError(null);
                        }}
                      >
                        cancel
                      </Button>
                    </div>
                  </div>
                ) : (
                  <div className="flex items-start gap-2">
                    <CardTitle className="text-base" data-artifact-title={artifact.id}>
                      {artifact.title || artifact.id}
                    </CardTitle>
                    {whoami && artifact.owner_user && whoami.user === artifact.owner_user ? (
                      <button
                        type="button"
                        data-edit-open=""
                        className="ml-auto shrink-0 text-muted-foreground text-xs hover:underline"
                        onClick={() => {
                          setDraftTitle(artifact.title || "");
                          setDraftBody(artifact.body || "");
                          setSaveError(null);
                          setEditing(true);
                        }}
                      >
                        edit
                      </button>
                    ) : null}
                  </div>
                )}
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
                  //
                  // Through lib/markdown, which is the same GFM the room
                  // renders. Two call sites parsing markdown with two sets of
                  // options is how a console ends up with two dialects and
                  // nobody able to say which one a body is written in.
                  <div
                    data-artifact-body=""
                    className="report-body text-sm"
                    dangerouslySetInnerHTML={{ __html: renderDocument(artifact.body) }}
                  />
                ) : (
                  <pre
                    data-artifact-body=""
                    className="whitespace-pre-wrap break-words font-sans text-sm"
                  >
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
                 * WHAT WAS LEARNED ABOUT THE ROW, under the words it was filed
                 * with, which is the order somebody reading it needs: the
                 * author's statement of the work first, then what everybody
                 * else found out about it.
                 *
                 * Only for the rows that have a note door - see isQueueItem,
                 * which mirrors the node's own list. A report or a proposal
                 * gets no section rather than a box whose write comes back a
                 * 404.
                 *
                 * The notes ride the artifact this page already read; nothing
                 * here fetches them. See RowNotes for why there is no second
                 * read of the log door beside it.
                 */}
                {isQueueItem(artifact) ? (
                  <RowNotes artifact={artifact} onAppended={setArtifact} />
                ) : null}

                {origins.length > 0 ? (
                  <div>
                    <div className="pb-1 font-medium text-muted-foreground text-xs">
                      came out of
                    </div>
                    <div className="flex flex-col gap-1">
                      {/*
                    A LINK WHERE THE READER HAS EARNED ONE, and the bare id where they
                    have not. The entry behind this carries only an id, because it is
                    readable by principals who cannot read the origin - so the node
                    resolves what this token could have read anyway and leaves the
                    rest alone. An unresolved origin is drawn rather than dropped:
                    "this came out of something you cannot see" is a fact, and a
                    section that hid it would be answering a different question.
                  */}
                      {origins.map((origin) => {
                        const path = refPath(origin.ref);
                        return (
                          <div key={origin.id} className="flex items-center gap-2 text-xs">
                            {path ? (
                              <Link className="hover:underline" data-origin={origin.id} to={path}>
                                {origin.title || origin.id}
                              </Link>
                            ) : (
                              <span
                                data-origin={origin.id}
                                className="font-mono text-muted-foreground"
                              >
                                {shortId(origin.id)}
                              </span>
                            )}
                            {path ? (
                              <span className="font-mono text-muted-foreground">
                                {shortId(origin.id)}
                              </span>
                            ) : (
                              <span className="text-muted-foreground">
                                you cannot read this one
                              </span>
                            )}
                          </div>
                        );
                      })}
                    </div>
                  </div>
                ) : null}

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
                  {/*
                  AND WHETHER ITS AUTHOR HAS TAKEN IT BACK, which is a third
                  reading beside the two above and replaces neither: the
                  signature really did verify, and the person whose key made it
                  says the key was not theirs at the time. Drawn only when there
                  is one - a page that said "not disowned" on every row would be
                  telling everybody something about nobody.

                  The id is a LINK because a mark with no route is a rumour with
                  a nicer font: whoever reads this can open the repudiation and
                  see the window and the words its subject signed.
                */}
                  {artifact.disowned ? (
                    <>
                      {" · "}
                      <span
                        data-disowned={artifact.id}
                        className="font-medium text-[var(--danger,#b91c1c)]"
                        title={
                          artifact.disowned.reason ||
                          "the author of this row has disowned the window it falls in"
                        }
                      >
                        disowned by {artifact.disowned.subject}
                      </span>{" "}
                      <Link
                        to={`/artifact/${artifact.disowned.by}`}
                        data-disowned-by={artifact.disowned.by}
                        className="underline"
                      >
                        see the repudiation
                      </Link>
                    </>
                  ) : null}
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
      <aside className="flex h-[28rem] w-full shrink-0 flex-col border-border border-t lg:h-auto lg:w-[26rem] lg:border-t-0 lg:border-l">
        {id ? <DocumentPanes room={documentRoom(id)} quote={quote} /> : null}
      </aside>
    </div>
  );
}

/**
 * The finding-specific section: the three axes, the two documents that are not
 * the body, the repro tree, and ReproPanel's mount point.
 *
 * A FINDING HAS THREE FACES AND THEY ARE DIFFERENT DOCUMENTS, which is why this
 * page draws them one under another rather than folding them into one body:
 *
 *   the FINDING    what is wrong, for somebody who has to fix it. Rendered
 *                  above this section, like every artifact's body.
 *   the DISCOVERY  how it was found, what was tried, what the evidence shows.
 *                  Also above - and it never leaves this node: the packager
 *                  keeps it out of every package it builds.
 *   the REPORT     the text that goes upstream, written for a maintainer who
 *                  has never seen this setup.
 *
 * The third is the one that gets forgotten, and the reason it is a separate
 * document rather than the body reused is that writing a defect up for your own
 * record and writing it for a stranger's tracker are different jobs. Kept on the
 * row (internal/store/findingreport.go) so it can be drafted, read here before
 * anybody sends it, and still be here afterwards to compare against what their
 * tracker holds.
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
  const tree = reproOf(artifact);
  const runnable = hasRepro(tree);
  const isolation = tree.isolation || "plain";
  const filing = upstreamOf(artifact);
  const evidence = evidenceOf(artifact);
  const draft = reportDraftOf(artifact);

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border-soft p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-muted-foreground text-xs uppercase tracking-wide">
          finding
        </span>
        {/* The same dot the list draws, so the row somebody clicked and the page
            they land on agree about how bad this is at a glance. */}
        <SeverityDot severity={artifact.severity} />
        {artifact.severity ? (
          <StateChip
            axis="severity"
            state={artifact.severity}
            tone={severityTone(artifact.severity)}
            title="how bad this finding is"
          >
            severity: {artifact.severity}
          </StateChip>
        ) : null}
        {artifact.kind ? <Badge variant="outline">{artifact.kind}</Badge> : null}
        <StateChip
          axis="repro"
          state={runnable ? "yes" : "no"}
          tone={reproTone(runnable)}
          title="whether this finding ships something that can be run"
        >
          {runnable ? `repro: ${isolation}` : "no repro tree"}
        </StateChip>
        {(artifact.tags ?? []).map((tag) => (
          <Badge key={tag} variant="outline">
            {tag}
          </Badge>
        ))}
      </div>

      {/* The other two axes, on the page that decides what to do about this
          finding. The status control above is OUR lifecycle and says nothing
          about either of these - see web/src/lib/findings.ts. */}
      <div
        className="flex flex-wrap items-center gap-2 text-xs"
        data-finding-upstream={filing.state}
        data-finding-evidence={evidence.state ?? ""}
      >
        <span className="text-muted-foreground">upstream</span>
        {/* Drawn through the same StateChip and the same upstreamTone the list
            uses, rather than being re-picked here. That is what makes "filed is
            teal" one fact about this console instead of two coincidences, and it
            is why a check can compare the colour on the row to the colour on the
            page and expect them to be equal. */}
        {filing.url ? (
          <a href={filing.url} target="_blank" rel="noreferrer" className="hover:underline">
            <StateChip
              axis="upstream"
              state={filing.state}
              tone={upstreamTone(filing.state)}
              title={filing.tracker ? `filed with ${filing.tracker}` : undefined}
            >
              {filing.state}
              {filing.id ? ` #${filing.id}` : ""}
              {filing.tracker ? ` · ${filing.tracker}` : ""}
            </StateChip>
          </a>
        ) : (
          <StateChip
            axis="upstream"
            state={filing.state}
            tone={upstreamTone(filing.state)}
            title={knownUpstream(filing.state) ? undefined : UNKNOWN_UPSTREAM}
          >
            {filing.state}
            {filing.id ? ` #${filing.id}` : ""}
            {filing.tracker ? ` · ${filing.tracker}` : ""}
          </StateChip>
        )}
        {filing.filed_at ? (
          <span className="text-muted-foreground">
            filed {filing.filed_at}
            {filing.filed_by ? ` by ${filing.filed_by}` : ""}
          </span>
        ) : null}
        {/* WHAT THIS FINDING CITES, which is not what was filed. A reference is
            something over there this finding touches - an issue somebody
            mentioned, a pull request that covers three findings at once - and
            it asserts nothing about whether we sent anything. Drawn under its
            own word rather than beside the filing badge, because reading one as
            the other is what turned one filing into eight. */}
        {filing.refs.length > 0 ? (
          <>
            <span className="text-muted-foreground">cites</span>
            {filing.refs.map((ref) =>
              ref.url ? (
                <a
                  key={`${ref.tracker}/${ref.kind}/${ref.id}`}
                  href={ref.url}
                  target="_blank"
                  rel="noreferrer"
                  className="hover:underline"
                >
                  <Badge variant="outline">{refLabel(ref)}</Badge>
                </a>
              ) : (
                <Badge key={`${ref.tracker}/${ref.kind}/${ref.id}`} variant="outline">
                  {refLabel(ref)}
                </Badge>
              ),
            )}
          </>
        ) : null}
        <span className="text-muted-foreground">evidence</span>
        <StateChip
          axis="evidence"
          state={evidence.state ?? "not stated"}
          tone={evidenceTone(evidence.state)}
          title={
            evidence.state
              ? "how strong the evidence is, and what it was run against"
              : "nobody has said how strong the evidence for this finding is, so this cannot be filed yet"
          }
        >
          {evidence.state ?? "not stated"}
        </StateChip>
        {evidence.verified_on ? (
          <span className="font-mono text-muted-foreground">
            on {evidence.verified_on.slice(0, 12)}
            {evidence.verified_at ? ` · ${evidence.verified_at}` : ""}
          </span>
        ) : null}
      </div>

      {/* THE REPORT DRAFT, and its absence said out loud. A finding page that
          simply omitted this pane when there is no draft would read as a finding
          ready to send: the missing upstream write-up is the single most common
          reason a finding sits unfiled, so the empty state names the verb that
          fills it. */}
      <div data-finding-report={draft ? "yes" : "no"}>
        <div className="pb-1 font-medium text-muted-foreground text-xs">
          report draft · what goes upstream
        </div>
        {draft ? (
          <pre className="whitespace-pre-wrap break-words rounded-md border border-border p-2 font-sans text-sm">
            {draft}
          </pre>
        ) : (
          <div className="rounded-md border border-dashed border-border p-2 text-muted-foreground text-xs">
            no upstream draft yet - the body above is written for us, and this is the one written
            for their maintainers. finding_write's <span className="font-mono">report</span> is what
            fills it.
          </div>
        )}
      </div>

      {/* THE REPRO TREE ITSELF, not just whether there is one. A reader
          deciding whether a finding can be filed needs to see what would run:
          which files, which one is the entrypoint, and under what isolation.
          The paths come off the manifest in the order WriteFindingRepro wrote
          them, and each file is an ordinary attachment - readable through the
          same door any other attachment is, which is why the id is shown. */}
      <div data-finding-repro={runnable ? "yes" : "no"}>
        <div className="pb-1 font-medium text-muted-foreground text-xs">repro tree</div>
        {runnable ? (
          <div className="flex flex-col gap-1">
            <div className="flex flex-wrap items-center gap-2 text-muted-foreground text-xs">
              <Badge variant="outline">isolation: {isolation}</Badge>
              {tree.cmd_override ? (
                <span className="font-mono">{tree.cmd_override}</span>
              ) : tree.entrypoint ? (
                <span className="font-mono">
                  {tree.interp ? `${tree.interp} ` : ""}
                  {tree.entrypoint}
                </span>
              ) : (
                <span>no entrypoint recorded - a runner would not know what to execute</span>
              )}
            </div>
            <ul className="flex flex-col gap-0.5 font-mono text-xs">
              {tree.files.map((file) => (
                <li key={file.attachment_id || file.path} className="flex flex-wrap gap-2">
                  <span>{file.path}</span>
                  <span className="text-muted-foreground" title={file.attachment_id}>
                    {file.attachment_id ? shortId(file.attachment_id) : ""}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        ) : (
          <div className="rounded-md border border-dashed border-border p-2 text-muted-foreground text-xs">
            nothing attached to run - finding_write's <span className="font-mono">repro</span> takes
            the tree, and until there is one no run can say anything about this finding.
          </div>
        )}
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

      {/* The project goes with it: the runner holds several, and asking it to
          resolve a version without saying whose source to resolve it against is
          a question it can only answer when it happens to hold exactly one. */}
      <ReproPanel finding={artifact.id} project={artifact.project} runnable={runnable} />
    </div>
  );
}
