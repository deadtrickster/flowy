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
          <li
            key={event.id}
            data-thread-for-me={forMe ? "" : undefined}
            className={`flex flex-col gap-1 border-border/60 border-b px-4 py-2 text-xs ${
              forMe ? "border-l-2 border-l-primary bg-primary/5 " : ""
            }${selected?.id === event.id ? "bg-accent/60" : ""}`}
          >
            {/*
              THE HEADER IS THE CONTROL AND THE BODY IS CONTENT, which is a
              change forced by drawing the body properly.

              The whole row used to be one <button>, which is fine for a span of
              plain text and impossible for a rendered one: a link inside a
              button is interactive content nested in interactive content, and
              clicking a link would have selected the message instead of
              following it. Attachment cards are worse - they have their own
              buttons.

              So the speaker line selects, and everything below it behaves like
              what it is. What is lost is clicking anywhere on the row to select
              it; what is gained is a body you can click, quote and open.
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
