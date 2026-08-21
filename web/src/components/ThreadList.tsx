import { AttachmentCards } from "@/components/AttachmentCards";
import { CitedMessage } from "@/components/CitedMessage";
import { MessageBody } from "@/components/MessageBody";
import type { FlowyEvent } from "@/lib/api";
import { speakerStyle } from "@/lib/speakercolour";
import { clock, speaker } from "@/lib/utils";

/**
 * A thread, top to bottom, the way a person reads a conversation.
 *
 * The pane drew the thread as its DAG and nothing else, which is why the
 * operator called it "some diagram editor, not a message thread really". The
 * DAG is the honest picture - this log really is a graph, a message can name
 * several parents, and two replies to the same message really are siblings
 * rather than a sequence. But almost every thread here is a straight line, and
 * drawing a straight line as a graph makes the reader do layout in their head
 * to recover the one thing they wanted: what was said, in order.
 *
 * So the default is the reading view and the graph is a keystroke away. Neither
 * is a simplification of the other: this shows ORDER, the DAG shows STRUCTURE,
 * and the moment a thread stops being a line the DAG is the only one that can
 * tell you so - which is why the header says how many parents are in play
 * rather than quietly flattening them.
 */
export function ThreadList({
  events,
  selected,
  onSelect,
  me,
}: {
  events: FlowyEvent[];
  selected: FlowyEvent | null;
  onSelect: (event: FlowyEvent) => void;
  /**
   * Who the reader is, so a message addressed to them is marked here as it is
   * in the room. MessageList has taken this since the ring landed; this pane is
   * a SECOND RENDERER of the same events and never took it, so a mention of you
   * was ringed in the room and plain in the thread - 01M0HHFF54, raised by the
   * operator the day after they started using threads.
   *
   * Optional, and absent means NO ring rather than a wrong one: a pane that
   * cannot say who the reader is must not guess.
   */
  me?: { user?: string; agent?: string };
}) {
  if (events.length === 0) {
    return (
      <div className="px-4 py-3 text-muted-foreground text-xs">
        select a message to follow its thread
      </div>
    );
  }

  return (
    <ul className="flex h-full flex-col overflow-auto">
      {events.map((event) => {
        const name = speaker(event);
        // FOR YOU, AND NOT FROM YOU - the same rule MessageList applies, in the
        // same words, because two renderers deciding this differently is how the
        // ring came to mean one thing in the room and nothing here.
        const isMe = (id?: string) => !!id && (id === me?.user || id === me?.agent);
        const forMe = isMe(event.addressee) && !isMe(event.actor);
        return (
          // THE WHOLE ROW STILL SELECTS, and this handler is what gives that
          // back after the body moved out of the button.
          //
          // Reported in review by claude-host: "a reader who clicks the words
          // of a thread message to reply to it gets nothing". Stating the
          // regression in the commit message was not the same as accepting it,
          // and this is the half I had left out.
          //
          // GUARDED ON THE ORIGIN, because the reason the body left the button
          // has not gone away: the body has links in it and attachment cards
          // have their own buttons, and a click on either of those is a click
          // on THAT, never a selection. closest() answers exactly that question
          // about the element the click started on.
          //
          // THE KEYBOARD PATH IS THE SPEAKER BUTTON BELOW - a real <button>, in
          // the tab order, doing the same thing. A keydown here would be a
          // second copy of it that also fires while somebody is tabbing through
          // a link in the body.
          //
          // The suppression is one line because a biome-ignore has to be the
          // LAST comment before the node it covers: the first version of this
          // put four lines of reasoning after the directive and the rule fired
          // anyway, with the explanation sitting right above it.
          // biome-ignore lint/a11y/useKeyWithClickEvents: the speaker button below is the keyboard path
          <li
            key={event.id}
            onClick={(clicked) => {
              if ((clicked.target as HTMLElement).closest("a,button")) return;
              onSelect(event);
            }}
            data-thread-for-me={forMe ? "" : undefined}
            className={`flex flex-col gap-1 border-border/60 border-b px-4 py-2 text-xs ${
              forMe ? "border-l-2 border-l-primary bg-primary/5 " : ""
            }${selected?.id === event.id ? "bg-accent/60" : ""}`}
          >
            {/*
              THE HEADER IS THE KEYBOARD CONTROL AND THE BODY IS CONTENT, which
              is a change forced by drawing the body properly.

              The whole row used to be one <button>, which is fine for a span of
              plain text and impossible for a rendered one: a link inside a
              button is interactive content nested in interactive content, and
              clicking a link would have selected the message instead of
              following it. Attachment cards are worse - they have their own
              buttons.

              So the speaker line is the control that lives in the tab order,
              the row keeps the click, and everything below behaves like what it
              is: a body you can click, quote and open.

              THE BODY IS text-sm IN BOTH LISTS, and that is a decision rather
              than a leak from the extraction - raised in review, where it would
              otherwise have arrived as an unexplained density change. The same
              content is drawn at the same size wherever it appears, and the
              speaker and the clock stay text-xs around it. That hierarchy is
              what the room has and the pane had a flat one only because its
              body was a span it had styled itself.
            */}
            <button
              type="button"
              onClick={() => onSelect(event)}
              data-thread-select={event.id}
              aria-label={`select ${name}'s message ${event.id}`}
              className="flex w-full items-center gap-2 text-left hover:underline"
            >
              <span className="rounded px-1 font-mono" style={speakerStyle(name)}>
                {name}
              </span>
              <span className="ml-auto text-muted-foreground">{clock(event.created)}</span>
            </button>
            {/*
              WHAT IT IS ANSWERING, drawn before the words that answer it, the
              same order the room uses. A thread is where quoting happens most
              and it was the one view that showed none of it.
            */}
            {event.citation ? <CitedMessage citation={event.citation} /> : null}
            <MessageBody
              id={event.id}
              body={event.body}
              mentions={event.meta?.mentions}
              user={me?.user}
              agent={me?.agent}
            />
            {/*
              AND WHAT IT CARRIES. 01M0HP4N06, the operator: "messages in threads
              dont show attachements". Same key, same splitter and the same cards
              the room draws - the pane simply never drew them.
            */}
            {event.meta?.attachments ? (
              <AttachmentCards ids={event.meta.attachments.split(" ").filter(Boolean)} />
            ) : null}
            {/*
                A reply that names more than one parent is the case the linear
                view cannot show, so it says so rather than pretending. That is
                the whole reason the DAG stays: this line is a hint that the
                other view has something to add, not decoration.
              */}
            {event.parents.length > 1 ? (
              <span className="font-mono text-[11px] text-muted-foreground">
                ← {event.parents.length} parents - press d for the graph
              </span>
            ) : null}
          </li>
        );
      })}
    </ul>
  );
}
