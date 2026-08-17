import DOMPurify from "dompurify";
import { AnimatePresence, motion } from "framer-motion";
import { marked } from "marked";
import { type KeyboardEvent, useCallback, useEffect, useRef, useState } from "react";

import { AttachmentCards } from "@/components/AttachmentCards";
import { CitedMessage } from "@/components/CitedMessage";
import { Badge } from "@/components/ui/badge";
import { type FlowyEvent, isAgent } from "@/lib/api";
import { selectedSpan } from "@/lib/cite";
import { splitBody } from "@/lib/mentions";
import { speakerStyle } from "@/lib/speakercolour";
import { clock, cn, shortId, speaker } from "@/lib/utils";

interface Props {
  events: FlowyEvent[];
  selected: FlowyEvent | null;
  onSelect: (event: FlowyEvent) => void;
  /**
   * onCite is a span of one message somebody selected with the mouse, in BYTES
   * into that message's body. The reply then quotes exactly those bytes,
   * because the node cuts the quote out of the source with the same offsets.
   */
  onCite?: (event: FlowyEvent, start: number, end: number) => void;
  /** me is the principal reading, so a message for them can say so. */
  me?: { user?: string; agent?: string };
  /**
   * onSeen is the message this view has actually reached: the id of the newest
   * one on screen, said only while the transcript is at the bottom. It is the
   * same "at the end" the scrolling rule below is built on, and it is
   * deliberately not "the room was opened" - somebody who scrolled back is
   * reading, not caught up, and the unread mark must not step over what they
   * have not got to yet. The id and not the reading beside it, because a
   * reading does not survive a browser's arithmetic - see lib/unread, which is
   * what does something with this.
   */
  onSeen?: (through: string) => void;
}

/**
 * The room, oldest first. Selecting a message is what a reply attaches to: the
 * next thing said names it as a parent, which is the branch the thread view
 * draws, and cites it, which is what the reply shows above itself.
 *
 * Selecting TEXT inside a message narrows that citation to what was selected,
 * and it is the same one action - there is no button and no form, because the
 * thing this replaces is somebody retyping the sentence they are answering.
 *
 * A row is a div with a button's role and a button's keyboard handling rather
 * than a <button>, which it was. Text inside a button cannot be selected by
 * dragging over it - the browser treats the control as one thing - so the
 * element that made clicking work is the element that made citing a span
 * impossible. The role and the tab stop stay, and so does enter/space, because
 * what a screen reader is told and what a keyboard can reach must not change
 * over a mouse gesture; biome.json turns off useSemanticElements for this file
 * alone, which is the rule that would otherwise ask for the button back.
 */
export function MessageList({ events, selected, onSelect, onCite, me, onSeen }: Props) {
  // Whether an id is the person reading or the agent working for them, which
  // is the pair the node treats as one reader everywhere else.
  const isMe = (id?: string) => !!id && (id === me?.user || id === me?.agent);

  const scroller = useRef<HTMLDivElement>(null);
  const count = events.length;

  // READING WINS OVER FOLLOWING.
  //
  // This scrolled to the bottom on every change in count, whatever the reader
  // was doing - so scrolling back through the room yanked you to the end the
  // moment anybody spoke, which in a room this busy makes the history
  // unreadable. Reported by the user, twice, while trying to read it.
  //
  // So: follow only when you are ALREADY at the bottom. Being scrolled up is
  // the reader saying they are reading, and nothing arriving should overrule
  // that. What arrives instead is a count and a way back down.
  //
  // AND IT IS A REF, NOT STATE. Whether the reader is at the end has to be true
  // the instant they get there and the instant they leave, because the thing
  // that reads it is an effect that runs when messages land - and messages land
  // between a scroll and the render that state would have carried its reading
  // in. `atBottom` is still kept, but only as a render input for the pill;
  // nothing decides anything from it.
  //
  // It starts true, which is what places somebody in a room they have just
  // opened: there is nothing on screen to be at the end of yet, and the first
  // page arriving is the transcript, not an arrival.
  const following = useRef(true);
  const [pending, setPending] = useState(0);
  const [atBottom, setAtBottom] = useState(true);

  // Within a few pixels counts as the bottom: a row's fractional height leaves
  // the end a pixel or so short of an exact comparison, which would decide the
  // reader had scrolled away from a view they never moved.
  const atEnd = useCallback(() => {
    const el = scroller.current;
    if (!el) return true;
    return el.scrollHeight - el.scrollTop - el.clientHeight < 24;
  }, []);

  // THE END AS IT IS NOW, NOT AS IT WAS WHEN THE SCROLL STARTED.
  //
  // This was `bottom.current.scrollIntoView({ behavior: "smooth" })`, and a
  // smooth scroll latches its destination offset at the moment it is called.
  // The room's history does not arrive in one answer - the first GET carries a
  // page of 200 and the long poll delivers the rest - so opening a full room
  // fired this once per batch and left four animations in flight, each aimed at
  // the bottom of a different, shorter transcript. Whichever one Chrome
  // finished last decided where the reader ended up, and the browser then fired
  // scrollend there, so nothing retried. Measured over eight loads of a 718
  // message room: three landed at the end, four at 251527 - the bottom of the
  // room as it stood at 399 messages - and one at 377934, the bottom at 599.
  // That is the "scrolls to a random place on reload" the operator reported: it
  // is not random, it is the end of a stale prefix.
  //
  // Setting scrollTop reads the height at the instant it runs and cannot be
  // raced by an animation. The lost smoothness is not a loss: the animation
  // that was being smoothed is a 450,000 pixel slide nobody watches, and it is
  // the only thing that ever put the reader somewhere they did not ask to be.
  const toEnd = useCallback(() => {
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, []);

  const onScroll = useCallback(() => {
    const bottomNow = atEnd();
    following.current = bottomNow;
    setAtBottom(bottomNow);
    // Arriving under your own steam clears the badge - it is a way back, not
    // a receipt, so it should not need dismissing once you are there.
    if (bottomNow) setPending(0);
  }, [atEnd]);

  const jumpToBottom = () => {
    toEnd();
    setPending(0);
  };

  // The newest message on screen, which is the one a reader at the bottom has
  // got to.
  const newest = events.at(-1)?.id;

  const seen = useRef(count);
  useEffect(() => {
    if (count === 0) return;
    const added = count - seen.current;
    seen.current = count;
    // The ref, not the state: a batch of history landing one frame after a
    // scroll was measured against a reading React had not committed yet, and
    // the room decided the reader had wandered off. That is how a plain reload
    // of a 718 message room offered "319 new messages" to somebody who had not
    // touched it - the history it was itself still drawing, called new.
    if (following.current) {
      toEnd();
      // Read, as opposed to delivered: this is a reader sitting at the end
      // while something arrives, which is the whole of what "seen" means.
      if (newest) onSeen?.(newest);
    } else if (added > 0) {
      setPending((n) => n + added);
    }
  }, [count, newest, onSeen, toEnd]);

  // The other way to reach the newest message: scrolling back down to it. It is
  // its own effect rather than a dependency of the one above, because that one
  // reads the ref and never this state - and an effect carrying a dependency it
  // does not use is a dependency nobody can check.
  useEffect(() => {
    if (atBottom && newest) onSeen?.(newest);
  }, [atBottom, newest, onSeen]);

  // Enter and space are what a button answers to, and this row is not one any
  // more. Keeping them is not politeness: selecting a message is how a reply
  // attaches to it, so a keyboard reader who could not select one could not
  // answer anybody.
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>, message: FlowyEvent) => {
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      onSelect(message);
    }
  };

  return (
    // The pill lives OUTSIDE the scroller, absolutely positioned over it.
    // Inside, it was a flex child - `sticky` still occupies its slot in flow -
    // so the moment it appeared it added its own height plus the gap to the
    // content and pushed the reader down by exactly that much. Measured
    // deterministically at 34px on three consecutive runs: 2988 -> 3022,
    // 5601 -> 5635, 8214 -> 8248. Offering somebody a way back to the bottom
    // must not itself move them, which is the whole complaint this component
    // was fixed for.
    <div className="relative flex min-h-0 flex-1 flex-col">
      <div
        ref={scroller}
        onScroll={onScroll}
        className="flex flex-1 flex-col gap-2 overflow-y-auto p-4"
      >
        <AnimatePresence initial={false}>
          {events.map((event) => {
            const agent = isAgent(event);
            // For you, and not from you. An addressed message is still an
            // ordinary message in the room - the same people read it, it sits in
            // the same place - so this is a ring around it and never a filter.
            const forMe = isMe(event.addressee) && !isMe(event.actor);
            return (
              <motion.div
                key={event.id}
                role="button"
                tabIndex={0}
                layout
                initial={{ opacity: 0, y: 8 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.18, ease: "easeOut" }}
                onClick={(clicked) => {
                  // A click that ended a text selection has already cited that
                  // span - onMouseUp on the body fires before this - so selecting
                  // the message here would widen the citation straight back to
                  // the whole message.
                  const selection = window.getSelection();
                  if (
                    selection &&
                    !selection.isCollapsed &&
                    selection.anchorNode &&
                    clicked.currentTarget.contains(selection.anchorNode)
                  ) {
                    return;
                  }
                  onSelect(event);
                }}
                onKeyDown={(keyed) => onKeyDown(keyed, event)}
                className={cn(
                  "rounded-lg border border-border bg-card p-3 text-left transition-colors hover:border-primary/40",
                  forMe && "border-primary/50 bg-primary/5",
                  // A private message is drawn as a different thing, not as a
                  // room message with a note on it. The dashed edge is what a
                  // reader takes in without reading, and the badge below says
                  // which of the two it is in words - a direct message that
                  // looked identical to a room message would be a trap for
                  // whoever writes the next one.
                  event.private && "border-amber-500/60 border-dashed bg-amber-500/5",
                  selected?.id === event.id && "border-primary/70 ring-1 ring-primary/40",
                )}
              >
                <div className="flex items-center gap-2 pb-1">
                  <Badge variant={agent ? "agent" : "human"}>{agent ? "agent" : "human"}</Badge>
                  {event.private ? (
                    <Badge
                      variant="outline"
                      title="only the sender and the addressee can read this"
                    >
                      private
                    </Badge>
                  ) : null}
                  {/*
                   * Who said it. The name the node recorded when it was said,
                   * and the tail of the actor id when the message has none - a
                   * room where every line was an id is what this replaces, and
                   * the id stays on the title so it is still there to copy.
                   */}
                  {/*
                  In the speaker's own colour, derived from the name so the
                  same person is the same colour here, in the roster and on a
                  todo they own. The name stays: colour is an accelerator for
                  people who see it, never the only thing carrying who spoke.
                */}
                  <span
                    className="rounded px-1.5 py-0.5 font-mono text-xs"
                    style={speakerStyle(speaker(event))}
                    title={event.actor}
                  >
                    {speaker(event)}
                  </span>
                  {/*
                  Whose word this is, which is a different question from whose
                  name is on it. "authored" means this node verified a signature
                  made with the speaker's own key; "attributed" means it could
                  not, and the message rests on the word of whichever node
                  relayed it - honest, possibly, and not the speaker's own word.
                  The distinction is drawn because it was not: a peer whose node
                  key an operator had pinned could write messages under anybody's
                  name and they were rendered here exactly like this person's own.

                  Nearly everything is attributed until principals have keys, so
                  the attributed mark is quiet - muted text, no border - and the
                  verified one is the badge. Neither is hidden: a reader who
                  cannot tell the two apart is the reader this replaces.
                */}
                  {event.authorship === "authored" ? (
                    <Badge
                      variant="outline"
                      title={`${speaker(event)} signed this with their own key, and this node verified it`}
                    >
                      signed
                    </Badge>
                  ) : (
                    <span
                      className="text-[11px] text-muted-foreground"
                      title="attributed: this node holds no signature of the speaker's own for this message, so it rests on the word of the node that relayed it"
                    >
                      attributed
                    </span>
                  )}
                  {event.addressee ? (
                    <Badge variant="outline">
                      to {forMe ? "you" : shortId(event.addressee, 8)}
                    </Badge>
                  ) : null}
                  <span className="ml-auto text-muted-foreground text-xs">
                    {clock(event.created)}
                  </span>
                </div>
                {/*
                What this message is answering, above what it says. The words
                are the node's, cut out of the message being quoted for
                whoever is reading - see CitedMessage - so this is a quotation
                and not the citing speaker's account of one.
              */}
                {event.citation ? <CitedMessage citation={event.citation} /> : null}
                {/*
                The body, with the @names the node resolved drawn in the
                colour of whoever they name - the same colour that person
                speaks in above, because both come from the name. A mention of
                YOU is ringed as well, which is the treatment the row already
                gives a message addressed at you: the ring says "this one is
                yours" whether it is around the message or around your name
                inside it. An @word the node resolved to nobody is left as the
                text it is.

                It is its own element, holding the body and nothing else,
                because a span is measured against this element's text: the
                citation above and the ids below would each shift every offset
                by their own length.
              */}
                <div
                  data-body={event.id}
                  className="select-text whitespace-pre-wrap break-words text-sm"
                  onMouseUp={(released) => {
                    // Markdown-rendered bodies carry rendered text, not the raw
                    // body, so a span measured here would quote the wrong bytes.
                    if (isMarkdown(event.body)) return;
                    const span = selectedSpan(released.currentTarget, event.body);
                    if (span && onCite) onCite(event, span.start, span.end);
                  }}
                >
                  {isMarkdown(event.body) ? (
                    // A body with structure renders as what it is - the code
                    // block a log is, the list a plan is - rather than as a
                    // wall of signs. Sanitized because bodies are agent-written;
                    // the same renderer the report page uses, at chat size.
                    //
                    // Span citations are skipped for these: a cite records byte
                    // offsets into the RAW body, and markdown rendering changes
                    // the visible text, so a span selected against the rendered
                    // DOM would quote the wrong bytes. Whole-message replies
                    // still work - they name the id, not a span.
                    //
                    <div
                      className="report-body text-sm"
                      dangerouslySetInnerHTML={{
                        __html: DOMPurify.sanitize(
                          marked.parse(event.body, { async: false }) as string,
                        ),
                      }}
                    />
                  ) : (
                    splitBody(event.body, event.meta?.mentions).map((run) =>
                      run.name ? (
                        <span
                          key={run.key}
                          data-mention={run.id}
                          className={cn(
                            "rounded px-0.5 font-medium",
                            isMe(run.id) && "ring-1 ring-primary/70",
                          )}
                          style={speakerStyle(run.name)}
                        >
                          {run.text}
                        </span>
                      ) : (
                        run.text
                      ),
                    )
                  )}
                </div>
                {event.meta?.attachments ? (
                  <AttachmentCards ids={event.meta.attachments.split(" ").filter(Boolean)} />
                ) : null}
                <div className="flex gap-2 pt-1 font-mono text-[11px] text-muted-foreground">
                  <span>#{shortId(event.id)}</span>
                  <span>thread {shortId(event.thread)}</span>
                  {event.parents.length > 0 ? (
                    <span>← {event.parents.map((id) => shortId(id)).join(" ")}</span>
                  ) : (
                    <span>opens a thread</span>
                  )}
                </div>
              </motion.div>
            );
          })}
        </AnimatePresence>
      </div>
      {pending > 0 ? (
        <button
          type="button"
          onClick={jumpToBottom}
          className="-translate-x-1/2 absolute bottom-2 left-1/2 z-10 rounded-full border border-border bg-card px-3 py-1 text-xs shadow-sm hover:border-primary/50"
        >
          {pending} new message{pending === 1 ? "" : "s"} - jump to latest
        </button>
      ) : null}
    </div>
  );
}

/**
 * isMarkdown says whether a body carries structure worth rendering: a fenced
 * code block, a list, a heading, or a table. Prose with a bold word stays on
 * the plain path - that path is where mention colours and span citations
 * live, and neither survives markdown rendering, so the upgrade is only for
 * the bodies that need it.
 */
function isMarkdown(body: string): boolean {
  if (body.includes("```")) return true;
  return /^(\s*[-*+]\s+\S|\s*\d+\.\s+\S|#{1,6}\s+\S|\|.*\|)/m.test(body);
}
