import { AnimatePresence, motion } from "framer-motion";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

import { AttachmentCards } from "@/components/AttachmentCards";
import { CitedMessage } from "@/components/CitedMessage";
import { MessageBody } from "@/components/MessageBody";
import { RowCard } from "@/components/RowCard";
import { Badge } from "@/components/ui/badge";
import { type FlowyEvent, type Reaction, isAgent } from "@/lib/api";
import { byteSlice, selectedSpan } from "@/lib/cite";
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
  /**
   * onOpenThread shows the thread a message is in. It is NOT onSelect, and the
   * difference is the whole of 01M0HGRFN5.
   *
   * Selecting a message is what arms a citation - it is how "reply" quotes what
   * it is answering - and the two thread controls below called it, so opening a
   * thread quoted the message you opened it from. The operator had complained
   * about exactly that collapse for message clicks two days earlier; it came
   * back in a new control because the control was wired to the handler that was
   * already there.
   *
   * Falls back to onSelect when nothing is passed, so a view that only wants
   * the old behaviour keeps it rather than losing the control.
   */
  onOpenThread?: (event: FlowyEvent) => void;
  /**
   * kept is the message ids this reader has bookmarked, and onKeep toggles one.
   *
   * The SET rather than a flag per message, because the control has to be able
   * to say "kept" as well as "keep": a button that only ever adds is a list
   * somebody can fill and not empty, and they would find that out on the page
   * where it matters least.
   */
  kept?: string[];
  onKeep?: (event: FlowyEvent, on: boolean) => void;
  /**
   * onThreadReply is "answer this, in its thread" - one gesture that opens the
   * thread pane on this message and puts the caret in the pane's composer.
   *
   * SEPARATE FROM onSelect ON PURPOSE. Selecting a message and opening its
   * thread have been the same call since this list was written, so `reply`,
   * `thread <id>` and `N replies` all did the same thing and none of them said
   * where the words would go. The operator asked for the pair spelled out:
   * "like we have now reply - this is cited reply in the room. and then reply in
   * thread is well in thread". A control that does not say where it puts your
   * words is a control you have to try to understand.
   *
   * Optional, so a list without a thread pane beside it - TaskView, the
   * document panes - simply does not draw the button rather than drawing one
   * that goes nowhere.
   */
  onThreadReply?: (event: FlowyEvent) => void;
  /**
   * onTodo raises a todo out of a message, carrying the words rather than a
   * pointer to them: `quoted` is what was selected inside the message, or the
   * whole body when nothing was.
   *
   * The TEXT and not the offsets, unlike onCite above, because the two answer
   * different questions. A citation is a reference and is redrawn from the
   * source every time it is read; a todo is a row somebody picks up tomorrow,
   * and a row that says "see the span 40-96 of a message in #general" is a row
   * whose reader has to go and look. The message id travels with it as
   * provenance either way.
   */
  onTodo?: (event: FlowyEvent, quoted: string) => void;
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
  /**
   * onOlder asks for the page before the one on screen. The room opens on a
   * bounded window, so scrolling up is a request for history that is not here
   * yet - see ChatRoom.loadOlder, which is safe to call on every scroll.
   */
  onOlder?: () => void;
  /** Whether there is anything older than the oldest message on screen. */
  moreOlder?: boolean;
  /** Whether a page of older messages is in flight. */
  loadingOlder?: boolean;
  /** The room, named at the top of the transcript when its beginning is on it. */
  room?: string;
  /**
   * What is on each message, keyed by message id, as the room read answered.
   * Optional: an older node sends none, and a room that draws nothing is the
   * right answer to that rather than a crash.
   */
  reactions?: Record<string, Reaction[]>;
  /**
   * Put an emoji on a message or take this reader's own off. Absent in the
   * read-only surfaces that reuse this list, and every control below is drawn
   * only when it is here - a button that cannot do anything is worse than no
   * button.
   */
  onReact?: (message: string, emoji: string, on: boolean) => void;
  /**
   * How many messages each thread on this page holds, keyed by thread id, as
   * the NODE counted them - see ChatPage.threads. Not derived from `events`,
   * and that is the point: this list holds a window of the room, so a thread
   * older than the window would be undercounted by exactly the part a reader
   * cannot see.
   *
   * A thread with no entry is drawn as no entry. Absent is "not counted", not
   * "no replies".
   */
  threads?: Record<string, number>;
}

/**
 * The room, oldest first. Selecting a message is what a reply attaches to: the
 * next thing said names it as a parent, which is the branch the thread view
 * draws, and cites it, which is what the reply shows above itself.
 *
 * Selecting TEXT inside a message narrows that citation to what was selected,
 * and it is the same one action - dragging over the words is how you quote
 * exactly those words, because the thing this replaces is somebody retyping the
 * sentence they are answering.
 *
 * A ROW IS NOT A CONTROL. It was: a div with a button's role, a tab stop and a
 * click that selected the message. So merely clicking a line - to read it, to
 * put the caret somewhere, to dismiss something - silently made it the message
 * you were about to answer. Reported by the operator: "dont cite automatically
 * when message clicked. add reply to button, as other messages have". Selecting
 * is a real button on the row now, and the row announces nothing to a screen
 * reader that it no longer does.
 *
 * Text inside a button cannot be selected by dragging over it - the browser
 * treats the control as one thing - which is why the row could not be a
 * <button> and had to borrow one's role. The reply control can be a real one
 * because it holds no body text, so the role, the tab stop and the enter/space
 * handling all go back to the browser.
 *
 * The old row click carried a guard: a click that ended a text selection had
 * already cited that span - onMouseUp on the body fires before onClick - so
 * selecting the message would have widened the citation straight back to the
 * whole message. That cannot happen through the button. A click event fires on
 * the common ancestor of where the pointer went down and came up, so a drag
 * that starts in the body and ends over the button clicks the ROW, and the row
 * listens to nothing.
 */
export function MessageList({
  events,
  selected,
  onSelect,
  onCite,
  onOpenThread,
  kept,
  onKeep,
  onThreadReply,
  onTodo,
  me,
  onSeen,
  onOlder,
  moreOlder,
  loadingOlder,
  room,
  reactions,
  threads,
  onReact,
}: Props) {
  // A SET, built once per render rather than an .includes() per message: the
  // room draws a bounded window, but the window is not small and the list is
  // not either once somebody has been using it for a week.
  const keeping = useMemo(() => new Set(kept ?? []), [kept]);

  // Whether an id is the person reading or the agent working for them, which
  // is the pair the node treats as one reader everywhere else.
  const isMe = (id?: string) => !!id && (id === me?.user || id === me?.agent);

  /**
   * The row a reader has opened a card on, or nothing.
   *
   * Held here rather than in each message so only one card is ever open: two
   * cards over one transcript is a state nobody asked for, and Escape closing
   * "the" card would then be ambiguous.
   */
  const [cardFor, setCardFor] = useState<string | null>(null);

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

  // WHERE THE READER IS, AS A DISTANCE FROM THE BOTTOM. It is the one measure
  // that survives content being added ABOVE, which is what a page of history
  // is - see the layout effect below, which restores it.
  //
  // Kept up to date from two places, and it needs both. The effect covers
  // messages arriving; the scroll handler covers the reader moving, because a
  // scroll only re-renders this component the FIRST time it leaves the bottom -
  // `setAtBottom(false)` over an `atBottom` that is already false is a no-op
  // React bails out of, so scrolling from halfway up to the top runs no effect
  // at all and a reading taken only there would be a screen and a half stale.
  const fromBottom = useRef(0);
  // The oldest message this view has drawn, which is how a prepend is told from
  // an arrival: history changes the START of the transcript and nothing else.
  const heldOldest = useRef<string | undefined>(undefined);

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

  // ONLY THE READER MAY GIVE UP FOLLOWING, and "not at the end" is not the
  // reader.
  //
  // toEnd() writes scrollTop, and that write fires a scroll event. If a batch
  // grew scrollHeight between the write and the event - which is the ordinary
  // case while a room is arriving - atEnd() reads false in that handler and
  // following.current latched false. Nothing sets it back: every later page
  // then counted as new messages instead of pinning, and the view sat exactly
  // one late batch short of the end. Stable, so it survives a settle loop that
  // waits for the height to stop changing, which is how the gate read it:
  // 5151px short of 45474 with everything quiet.
  //
  // Direction is the signal that cannot be faked by growth. Content arriving
  // never DECREASES scrollTop; a reader going back up always does. So an
  // upward move is the only thing that stops the follow, and reaching the end
  // by any means resumes it.
  const lastTop = useRef(0);
  const onScroll = useCallback(() => {
    const el = scroller.current;
    const top = el ? el.scrollTop : 0;
    // A few pixels of slack for fractional row heights and for a browser that
    // settles a scroll a pixel below where it was asked to.
    const wentUp = top < lastTop.current - 8;
    lastTop.current = top;
    const bottomNow = atEnd();
    // DIRECTION, not position. `following.current = bottomNow` is the line the
    // latch fix exists to remove: toEnd() writes scrollTop, that write fires a
    // scroll event, and if the event lands while the transcript is still growing
    // then atEnd() is false for a moment and the view stops following the end
    // for good. Only a reader moving UP stops it now; reaching the end by any
    // means resumes it.
    if (bottomNow) following.current = true;
    else if (wentUp) following.current = false;
    // And the distance from the bottom, which the lazy-load cutoff keeps so it
    // can restore the reader's place after prepending older messages. It is a
    // measurement, not a decision, so it survives the change above unchanged.
    if (el) fromBottom.current = el.scrollHeight - el.scrollTop;
    setAtBottom(bottomNow);
    // Arriving under your own steam clears the badge - it is a way back, not
    // a receipt, so it should not need dismissing once you are there.
    if (bottomNow) setPending(0);
    // NEAR THE TOP IS A REQUEST FOR HISTORY. The room opens on a window, so the
    // reader running out of transcript is them asking for the page before it.
    //
    // Only from a real scroll, and that is the guard rather than an accident: a
    // freshly opened room renders at scrollTop 0 for one frame before it pins
    // itself to the end, and a check on the reading alone would fire there and
    // fetch a page nobody asked for. Setting scrollTop programmatically does
    // fire this, but by then the reading is the bottom of the transcript.
    //
    // It cannot run away with itself either. A page that arrives leaves the
    // reader a window's worth of messages below the top, because the effect
    // below puts them back where they were looking, so the next fetch needs
    // another scroll. ChatRoom holds one read at a time and stops asking at the
    // beginning of the room.
    if (onOlder && (scroller.current?.scrollTop ?? Number.POSITIVE_INFINITY) < 240) onOlder();
  }, [atEnd, onOlder]);

  const jumpToBottom = () => {
    toEnd();
    setPending(0);
  };

  // The two ends of what is on screen. Which of them moved says what happened:
  // a new end is somebody speaking, a new start is history arriving because the
  // reader asked for it, and the two want opposite treatment.
  const newest = events.at(-1)?.id;
  const oldest = events[0]?.id;

  // HOLD THE READER ON THE MESSAGE THEY WERE LOOKING AT.
  //
  // Prepending a page pushes everything on screen down by the height of what
  // arrived, so a reader who was mid-sentence at the top of the viewport is
  // suddenly a window further down the room. The fix is the invariant that
  // survives content being added ABOVE: the distance from the top of the
  // viewport to the BOTTOM of the transcript does not change.
  //
  // A layout effect, not an effect: it runs before the browser paints, so the
  // reader never sees the displaced frame. The distance it restores is the one
  // taken before this page landed - the last thing this effect does on every
  // render, and again on every scroll - so the reading is always of the DOM as
  // the reader last saw it.
  //
  // IT DOES NOT FIGHT THE PIN, it applies it first. Following the room wins
  // outright - a reader at the end stays at the end, and the effect below
  // re-pins them there on every arrival - so a prepend that lands while they
  // are at the bottom is pinned here rather than left to that effect, which
  // runs AFTER the browser paints and would show one frame of the transcript
  // shoved down by the height of what arrived.
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    if (heldOldest.current && oldest !== heldOldest.current) {
      if (following.current) toEnd();
      else el.scrollTop = el.scrollHeight - fromBottom.current;
    }
    heldOldest.current = oldest;
    fromBottom.current = el.scrollHeight - el.scrollTop;
  });

  const seen = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (count === 0) return;
    const was = seen.current;
    seen.current = newest;
    // RE-PIN ON EVERY ARRIVAL, NOT ONCE. A room does not land in one answer -
    // the poll brings the next batch a moment later - and a view that scrolled
    // itself only on the first one is left short by the height of everything
    // after it. Measured at 5,151px short on a room seeded past one page.
    //
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
      return;
    }
    // NOTHING NEW IS AT THE NEW END, so nothing arrived. This is a page of
    // history the reader asked for, and counting it as unread is the "319 new
    // messages" bug wearing the other hat: the badge would offer to jump you to
    // the latest message over messages that are older than everything on
    // screen. The layout effect above has already held their place.
    if (!was || was === newest) return;
    const at = events.findIndex((event) => event.id === was);
    const added = at >= 0 ? count - at - 1 : count;
    if (added > 0) setPending((n) => n + added);
  }, [count, newest, events, onSeen, toEnd]);

  // The other way to reach the newest message: scrolling back down to it. It is
  // its own effect rather than a dependency of the one above, because that one
  // reads the ref and never this state - and an effect carrying a dependency it
  // does not use is a dependency nobody can check.
  useEffect(() => {
    if (atBottom && newest) onSeen?.(newest);
  }, [atBottom, newest, onSeen]);

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
        {/*
          The top of the window, and it is ALWAYS DRAWN once this transcript can
          page - either "there is more" or "this is where the room starts".
          Both, and never neither, because the row has a height: one that
          appeared when history ran out would shrink the transcript by its own
          height at the exact moment the reader is sitting at the top of it, and
          moving somebody who is reading is the complaint this whole view has
          been fixed for twice - see the pill below, which lives outside the
          scroller for the same reason.

          It is also the way back for a transcript that does not scroll. The
          scroll handler is what normally asks for the page before this one, and
          a room whose window fits inside the viewport fires no scroll events at
          all - so without a control here its history would be unreachable.
        */}
        {onOlder ? (
          <div className="pb-1 text-center text-muted-foreground text-xs">
            {moreOlder ? (
              <button
                type="button"
                onClick={onOlder}
                disabled={loadingOlder}
                className="rounded border border-border px-2 py-0.5 hover:border-primary/50 hover:text-foreground disabled:opacity-60"
              >
                {loadingOlder ? "loading older messages" : "older messages"}
              </button>
            ) : (
              <span>{room ? `the beginning of #${room}` : "the beginning of the room"}</span>
            )}
          </div>
        ) : null}
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
                /*
                  The message as one addressable thing. Every mark a reader is
                  meant to take in together - who spoke, whether the signature
                  verified, whether they have disowned it - lives inside this
                  element, so a check that asks "does this message say both"
                  has an element to ask it of. Without it a check reaches for
                  the nearest attribute it can find, which was the reply
                  BUTTON, and answers a question about a control instead.
                */
                data-message={event.id}
                layout
                initial={{ opacity: 0, y: 8 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.18, ease: "easeOut" }}
                className={cn(
                  "group rounded-lg border border-border bg-card p-3 text-left transition-colors hover:border-primary/40",
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
                  {/*
                    AND WHETHER ITS SPEAKER HAS TAKEN IT BACK. This is where the
                    mark matters most: the actor is the whole of what a message
                    means, so a line somebody has disowned and a line nobody has
                    must not read the same. It sits BESIDE the signed/attributed
                    mark rather than replacing it - the signature verified, and
                    the person whose key made it says the key was not theirs.
                  */}
                  {event.disowned ? (
                    <Badge
                      variant="outline"
                      data-disowned={event.id}
                      className="border-[var(--danger,#b91c1c)] text-[var(--danger,#b91c1c)]"
                      title={
                        event.disowned.reason ||
                        `${event.disowned.subject} has disowned the window this message falls in`
                      }
                    >
                      disowned
                    </Badge>
                  ) : null}
                  {event.addressee ? (
                    <Badge variant="outline">
                      to {forMe ? "you" : shortId(event.addressee, 8)}
                    </Badge>
                  ) : null}
                  <span className="ml-auto text-muted-foreground text-xs">
                    {clock(event.created)}
                  </span>
                  {/*
                    How a message becomes the one you are answering, now that
                    touching the row does not. It is quiet until the pointer is
                    on the row or a keyboard has reached it, which is the
                    density a chat line can carry - but it is ALWAYS in the
                    document and always focusable, because a control that only
                    exists on hover is a control a keyboard user does not have.
                    A real <button>, so enter and space are the browser's job.

                    The name says which message it answers: a screen reader
                    moving down the room hears "reply" against every line
                    otherwise, which names none of them.
                  */}
                  {/*
                    THE MESSAGE IS ABOUT A ROW, and until now the transcript
                    did not say so. event.artifact has carried the id on every
                    raise since raises existed and nothing here read it, so a
                    tap on the message landed on prose and did nothing - which
                    reads as a broken control rather than as a missing one.

                    A real button beside reply, always in the document and
                    focusable for the same reason reply is: a control that
                    exists only on hover is a control a keyboard does not have.
                    It names the row it opens, because a screen reader moving
                    down a busy room hears "row" against every raise otherwise.
                  */}
                  {event.artifact ? (
                    <button
                      type="button"
                      data-row-chip={event.artifact}
                      onClick={() => setCardFor(event.artifact)}
                      aria-label={`open the row ${shortId(event.artifact)} this message raised`}
                      className="rounded border border-border px-1.5 py-0.5 text-[11px] text-muted-foreground transition hover:border-primary/50 hover:text-foreground"
                    >
                      row {shortId(event.artifact)}
                    </button>
                  ) : null}
                  {/* The acks on this message, and the one control that adds
                    one. Beside `reply` because they answer the same question -
                    "what do I do about this line" - and a reaction is the
                    cheaper of the two answers. */}
                  {onReact ? (
                    <ReactionPicker
                      message={event.id}
                      on={reactions?.[event.id] ?? []}
                      onReact={onReact}
                    />
                  ) : null}
                  <button
                    type="button"
                    data-reply={event.id}
                    onClick={() => onSelect(event)}
                    aria-label={`reply to ${speaker(event)}, message ${shortId(event.id)}`}
                    className="rounded border border-border px-1.5 py-0.5 text-[11px] text-muted-foreground opacity-60 transition hover:border-primary/50 hover:text-foreground focus-visible:opacity-100 group-hover:opacity-100"
                  >
                    reply
                  </button>
                  {/*
                    AND THE OTHER PLACE THE WORDS CAN GO. `reply` cites into the
                    ROOM; this one answers inside the thread and leaves the pane
                    where it put you - see ChatRoom, where the pane holds what a
                    reader opened rather than following the newest message.

                    Drawn only when there is a pane to open. A button that says
                    "in thread" on a surface with no thread pane would be a
                    promise nothing keeps.
                  */}
                  {onThreadReply ? (
                    <button
                      type="button"
                      data-thread-reply={event.id}
                      onClick={() => onThreadReply(event)}
                      aria-label={`reply in the thread ${shortId(event.thread)}, message ${shortId(event.id)}`}
                      className="rounded border border-border px-1.5 py-0.5 text-[11px] text-muted-foreground opacity-60 transition hover:border-primary/50 hover:text-foreground focus-visible:opacity-100 group-hover:opacity-100"
                    >
                      reply in thread
                    </button>
                  ) : null}
                </div>
                {/*
                What this message is answering, above what it says. The words
                are the node's, cut out of the message being quoted for
                whoever is reading - see CitedMessage - so this is a quotation
                and not the citing speaker's account of one.
              */}
                {event.citation ? <CitedMessage citation={event.citation} /> : null}
                {/* What is already on this message. Drawn whether or not this
                  reader may add one, because reading who acked is not the same
                  permission as acking. */}
                <ReactionStrip
                  message={event.id}
                  on={reactions?.[event.id] ?? []}
                  onReact={onReact}
                />
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
                {/*
                  EVERY body is markdown now. There used to be a heuristic here
                  - a fence, a list, a heading or a table pipe took the markdown
                  path and everything else took a plain one - and it is deleted
                  rather than widened. It could not be widened: the operator
                  typed a message with `backticks` in it, saw backticks, and
                  said so, and any rule that decides "is this markdown" from the
                  body has the same class of answer for the next construct.
                  Reported as "just go full gh flavored markdown everywhere".

                  What the plain path was carrying, both halves, moved rather
                  than went: mention chips are rendered by lib/markdown, in the
                  colour of whoever they name and ringed when they name YOU, and
                  span citations are found in the raw body rather than counted
                  off the screen - see lib/cite.
                */}
                <MessageBody
                  id={event.id}
                  body={event.body}
                  mentions={event.meta?.mentions}
                  user={me?.user}
                  agent={me?.agent}
                />
                {event.meta?.attachments ? (
                  <AttachmentCards ids={event.meta.attachments.split(" ").filter(Boolean)} />
                ) : null}
                <div className="flex gap-2 pt-1 font-mono text-[11px] text-muted-foreground">
                  {/*
                    CITING IS A CHOICE, and it used to be a side effect of
                    selecting. The operator: "why whenever i select message text
                    here it automatically becomes a citation? I just wanted to
                    copy it."

                    onMouseUp on the body armed the citation, so copying and
                    citing were ONE GESTURE and the common one got the rare
                    one's behaviour.

                    THIS BUTTON IS ALWAYS RENDERED AND HOLDS NO STATE, which is
                    the whole design. Storing the selection when it is made
                    would re-render the message, and this file's own comment on
                    MessageBody records what that does: the innerHTML is written
                    again and the reader's highlight is destroyed under their
                    pointer. That would have broken copying while fixing citing.
                    So nothing happens until the button is pressed, and the
                    selection is read from the browser at that moment.
                  */}
                  <button
                    type="button"
                    data-cite={event.id}
                    title="cite what you have selected in this message, or the whole message"
                    className="cursor-pointer text-muted-foreground underline decoration-dotted hover:text-foreground"
                    onClick={() => {
                      const container = document.querySelector<HTMLElement>(
                        `[data-body="${CSS.escape(event.id)}"]`,
                      );
                      const span = container ? selectedSpan(container, event.body) : null;
                      // NOTHING SELECTED CITES THE WHOLE MESSAGE, which is what
                      // pressing "cite" on a message plainly means, and is the
                      // behaviour reply has always had.
                      if (!span || "whole" in span) onSelect(event);
                      else if (onCite) onCite(event, span.start, span.end);
                    }}
                  >
                    cite
                  </button>
                  {/*
                    AND THE SAME GESTURE INTO THE QUEUE. The operator,
                    01M0HGVPFN: "I should be able to quickly turn a message or a
                    selected part into a rodo".

                    Beside cite and built the same way - always rendered,
                    holding no state, reading the selection from the browser at
                    the moment it is pressed. The reason is in cite's comment
                    above and it applies here unchanged: storing a selection
                    re-renders the message, the body's innerHTML is written
                    again, and the reader's highlight is destroyed under their
                    pointer. A control that broke copying to offer a todo would
                    be a bad trade.

                    Nothing selected raises the WHOLE message, which is what
                    pressing "todo" on a message plainly means - cite and reply
                    both already read a bare press that way.
                  */}
                  {onTodo ? (
                    <button
                      type="button"
                      data-todo-from={event.id}
                      title="raise a todo out of what you have selected, or out of the whole message"
                      className="cursor-pointer text-muted-foreground underline decoration-dotted hover:text-foreground"
                      onClick={() => {
                        const container = document.querySelector<HTMLElement>(
                          `[data-body="${CSS.escape(event.id)}"]`,
                        );
                        const span = container ? selectedSpan(container, event.body) : null;
                        const quoted =
                          span && !("whole" in span)
                            ? byteSlice(event.body, span.start, span.end)
                            : event.body;
                        onTodo(event, quoted);
                      }}
                    >
                      todo
                    </button>
                  ) : null}
                  {/*
                    KEEPING ONE FOR YOURSELF, which is the pin's private twin.
                    01M0HGTV9B: "I should be able to bookmark messages".

                    Beside cite and todo because it is the third thing a reader
                    does with a message they care about, and deliberately NOT
                    beside the pin: a pin rearranges what everybody in the room
                    sees, and somebody who wants to find their own way back
                    tomorrow should not have to make a claim about the
                    conversation to do it.
                  */}
                  {onKeep ? (
                    <button
                      type="button"
                      data-bookmark={event.id}
                      data-kept={String(keeping.has(event.id))}
                      title={
                        keeping.has(event.id)
                          ? "you are keeping this - drop it from your list"
                          : "keep this message in your own list"
                      }
                      className={`cursor-pointer underline decoration-dotted hover:text-foreground ${
                        keeping.has(event.id) ? "text-primary" : "text-muted-foreground"
                      }`}
                      onClick={() => onKeep(event, !keeping.has(event.id))}
                    >
                      {keeping.has(event.id) ? "kept" : "keep"}
                    </button>
                  ) : null}
                  <span>#{shortId(event.id)}</span>
                  {/*
                    THE THREAD, AS A DOOR RATHER THAN A LABEL.

                    It was a plain span, and the operator's report is what a
                    label costs: "i also miss normal slack/mattermost style
                    threads. like why didnt you reply to my plans proposal in a
                    thread. impossible to track things here." Measured before
                    building any of this: threads WORK - events carry one, `flowy
                    say --thread` continues one, and the pane draws one - and in
                    the last 40 messages of #general there were 40 messages, 40
                    distinct threads and not one with more than a single message
                    in it. Four seats talked past each other all day in a room
                    that already supported the thing they were missing.

                    So this is the same id it always printed, wired to the
                    control the reader wanted it to be: it opens the thread pane
                    on this message's thread and arms the composer into it,
                    which is what `reply` beside it does, reached from the place
                    a reader is already looking when they wonder what a thread
                    is.
                  */}
                  <button
                    type="button"
                    data-thread-open={event.id}
                    onClick={() => (onOpenThread ?? onSelect)(event)}
                    aria-label={`open the thread ${shortId(event.thread)} this message is in`}
                    className="cursor-pointer text-muted-foreground underline decoration-dotted hover:text-foreground"
                  >
                    thread {shortId(event.thread)}
                  </button>
                  {/*
                    AND HOW MUCH IS IN IT, when the node has counted it and
                    there is more than this message.

                    The count is the node's, never a fold of what is on screen:
                    this list is a window and a thread is not. `>1` rather than
                    `>0` because every thread contains at least the message
                    drawing it, so "1" is the number that says nothing - and it
                    is drawn from a key being ABSENT versus present, so a thread
                    the node could not count says nothing at all rather than
                    claiming zero.
                  */}
                  {(threads?.[event.thread] ?? 0) > 1 ? (
                    <button
                      type="button"
                      data-thread-replies={event.thread}
                      data-thread-count={threads?.[event.thread]}
                      onClick={() => (onOpenThread ?? onSelect)(event)}
                      aria-label={`open this thread, ${(threads?.[event.thread] ?? 1) - 1} replies`}
                      className="cursor-pointer rounded border border-border px-1.5 text-[11px] text-primary transition hover:border-primary/50 hover:text-foreground"
                    >
                      {(threads?.[event.thread] ?? 1) - 1} repl
                      {(threads?.[event.thread] ?? 1) - 1 === 1 ? "y" : "ies"}
                    </button>
                  ) : null}
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
      {/*
        One card, over the room, outside the scroller - inside it the card
        would scroll away from the reader who opened it, and the transcript
        keeps moving while a card is open because messages keep arriving.
      */}
      {cardFor ? <RowCard id={cardFor} onClose={() => setCardFor(null)} /> : null}
    </div>
  );
}

/**
 * One message body, rendered once.
 *
 * IT IS ITS OWN MEMOISED COMPONENT BECAUSE A SELECTION DID NOT SURVIVE
 * OTHERWISE, and that is a browser fact rather than a tidiness one. React
 * compares dangerouslySetInnerHTML by the identity of the object it is handed,
 * so a fresh `{ __html: render(...) }` on every render writes innerHTML again
 * with the same string - which destroys and rebuilds the text nodes the
 * reader's selection is anchored in. The room re-renders on every poll, and it
 * re-renders the instant a citation is armed, so dragging over a body armed the
 * span and then dropped the highlight under the pointer. Measured in a real
 * browser: the composer held the right span and getSelection() was empty.
 *
 * useMemo on the RENDERED OBJECT fixes it at the root - same body, same object,
 * no write - and it stops re-parsing every message's markdown on every poll,
 * which the transcript was doing for a room of a hundred.
 *
 * The reader is taken apart into `user` and `agent` rather than passed as an
 * object for the same reason: the caller builds that object inline, so it is a
 * new identity every render and would defeat the memo it is a dependency of.
 */

/**
 * The emoji a reader can put on a message without typing one.
 *
 * A fixed short set rather than a picker, and that is the design and not a
 * shortcut. This is an ACK CHANNEL: the value is that answering costs nothing,
 * and a grid of two thousand emoji costs a decision. Four answers cover what
 * this room actually says to each other - seen, agreed, no, and something is
 * wrong here - and anything that needs a fifth needs a sentence.
 */
const ACKS = [
  { emoji: "👀", label: "seen" },
  { emoji: "👍", label: "agreed" },
  { emoji: "👎", label: "no" },
  { emoji: "🔥", label: "this is the problem" },
] as const;

/**
 * The control that adds one, folded away until it is wanted.
 *
 * It draws only when the surface passed onReact. A button that cannot do
 * anything is worse than no button: it says the reader may ack and then does
 * nothing when they try, which reads as the feature being broken rather than
 * absent.
 */
function ReactionPicker({
  message,
  on,
  onReact,
}: {
  message: string;
  on: Reaction[];
  onReact: (message: string, emoji: string, added: boolean) => void;
}) {
  const [open, setOpen] = useState(false);
  const mine = new Set(on.filter((r) => r.mine).map((r) => r.emoji));
  return (
    <span className="relative inline-flex">
      <button
        type="button"
        data-react-open={message}
        onClick={() => setOpen((was) => !was)}
        aria-label={`react to message ${shortId(message)}`}
        aria-expanded={open}
        className="rounded border border-border px-1.5 py-0.5 text-[11px] text-muted-foreground opacity-60 transition hover:border-primary/50 hover:text-foreground focus-visible:opacity-100 group-hover:opacity-100"
      >
        react
      </button>
      {open ? (
        <span className="absolute top-full right-0 z-10 mt-1 flex gap-1 rounded border border-border bg-background p-1 shadow-sm">
          {ACKS.map((ack) => (
            <button
              key={ack.emoji}
              type="button"
              data-react-add={`${message}:${ack.emoji}`}
              // The reader's own state decides what the click MEANS, so
              // pressing an emoji already on from this reader takes it off
              // rather than writing a second identical entry.
              onClick={() => {
                onReact(message, ack.emoji, !mine.has(ack.emoji));
                setOpen(false);
              }}
              aria-label={`${mine.has(ack.emoji) ? "take back" : ack.label} on message ${shortId(message)}`}
              aria-pressed={mine.has(ack.emoji)}
              className={`rounded px-1 py-0.5 text-sm transition hover:bg-muted ${
                mine.has(ack.emoji) ? "bg-muted" : ""
              }`}
            >
              {ack.emoji}
            </button>
          ))}
        </span>
      ) : null}
    </span>
  );
}

/**
 * What is on a message now.
 *
 * The COUNT is drawn and the NAMES are on hover, which is the split the store's
 * shape exists for: in a room of four seats, three acks from seats that do not
 * have to act and one from the seat that does are not the same fact, and a bare
 * "3" cannot tell them apart. Anybody reading the room can see who; nobody has
 * to read four names to see that it was acked.
 *
 * A chip is a button only when this reader may react. Otherwise it is a span -
 * not a disabled button, because a disabled control still says "this is for
 * you" to everyone who can see it, and a read-only surface has no such offer to
 * make.
 */
/**
 * The inside of one chip: the glyph, then how many.
 *
 * A function outside the map rather than a fragment inside it. The element is
 * used once, in an already-keyed parent, but a fragment built inside a `.map`
 * reads to the linter as an unkeyed item in an iterable and the rule is right
 * often enough not to silence.
 *
 * The glyph is aria-hidden because the chip's own aria-label already says it in
 * words, with the names beside it - a screen reader announcing the emoji twice
 * and the names once has the emphasis exactly backwards.
 */
function chipBody(r: Reaction) {
  return (
    <>
      <span aria-hidden="true">{r.emoji}</span> <span className="font-mono">{r.actors.length}</span>
    </>
  );
}

function ReactionStrip({
  message,
  on,
  onReact,
}: {
  message: string;
  on: Reaction[];
  onReact?: (message: string, emoji: string, added: boolean) => void;
}) {
  if (on.length === 0) return null;
  return (
    <div className="mt-1 flex flex-wrap gap-1" data-reactions={message}>
      {on.map((r) => {
        const label = `${r.emoji} ${r.actors.length}: ${r.actors.join(", ")}`;
        const className = `rounded-full border px-1.5 py-0.5 text-[11px] transition ${
          r.mine ? "border-primary/50 bg-muted" : "border-border text-muted-foreground"
        }`;
        const body = chipBody(r);
        return onReact ? (
          <button
            key={r.emoji}
            type="button"
            data-reaction={`${message}:${r.emoji}`}
            data-reaction-count={r.actors.length}
            data-reaction-mine={r.mine ? "yes" : "no"}
            title={label}
            aria-label={label}
            aria-pressed={r.mine}
            onClick={() => onReact(message, r.emoji, !r.mine)}
            className={`${className} hover:border-primary/50`}
          >
            {body}
          </button>
        ) : (
          <span
            key={r.emoji}
            data-reaction={`${message}:${r.emoji}`}
            data-reaction-count={r.actors.length}
            data-reaction-mine={r.mine ? "yes" : "no"}
            title={label}
            aria-label={label}
            className={className}
          >
            {body}
          </span>
        );
      })}
    </div>
  );
}
