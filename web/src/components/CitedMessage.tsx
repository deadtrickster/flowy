import { Quote } from "lucide-react";

import type { Citation } from "@/lib/api";
import { speakerColour, speakerStyle } from "@/lib/speakercolour";
import { shortId } from "@/lib/utils";

/**
 * Who a citation quotes: the name the node recorded on the message being cited,
 * and the tail of its actor id when it has none. It is the same fallback
 * `speaker` applies to a message, because it is the same question about the
 * same row - a citation drawn under a different name from the message it quotes
 * would be two names for one person on one screen.
 */
export function citedSpeaker(citation: Citation) {
  return citation.name || shortId(citation.actor ?? "", 8);
}

/**
 * What a message is answering, drawn above it.
 *
 * These ARE the quoted person's own words, and that is a property of how the
 * citation is stored rather than a claim this component makes: the row carries
 * a pointer and a byte span, and the node cuts the quote out of the signed
 * message it points at, for whoever is reading. Nothing the citing author typed
 * can appear here. So it is rendered as a quotation - under the quoted person's
 * name, in the colour they speak in everywhere else - which would be a lie if
 * the text had travelled on the citing row.
 *
 * The unreadable case is not an error state and is drawn as an ordinary one.
 * Rooms are scoped by project and the log is not, so a reply reaching somebody
 * whose source message does not is expected: they get the pointer and none of
 * the words, and the block says which of the two situations they are in rather
 * than showing an empty quotation, which reads as somebody having said nothing.
 */
export function CitedMessage({ citation }: { citation: Citation }) {
  const name = citedSpeaker(citation);
  return (
    <div
      data-citation={citation.message}
      data-cite-whole={String(citation.whole)}
      className="mb-2 rounded-r border-l-2 bg-muted/40 py-1 pr-2 pl-2"
      style={{ borderLeftColor: speakerColour(name) }}
    >
      <div className="flex items-center gap-1.5 pb-0.5 text-[11px]">
        <Quote className="h-3 w-3 shrink-0 text-muted-foreground" />
        {citation.readable ? (
          <span
            data-cite-speaker
            className="rounded px-1 font-mono"
            style={speakerStyle(name)}
            title={citation.actor}
          >
            {name}
          </span>
        ) : null}
        <span className="text-muted-foreground">
          {citation.readable
            ? citation.whole
              ? "said, in full"
              : "said, in part"
            : "quotes a message you cannot read"}
        </span>
        <span className="ml-auto font-mono text-muted-foreground">
          #{shortId(citation.message)}
        </span>
      </div>
      {/*
        Three states, and the third is the one a span-based citation has that a
        stored quote would not: a span that does not fit the body it points at.
        The node derives nothing rather than clamping, because clamping answers
        by misquoting - so there is a citation here with no words in it, and
        saying so is better than drawing an empty pair of quotes.
      */}
      {citation.readable && citation.text ? (
        <div
          data-cite-text
          className="line-clamp-3 whitespace-pre-wrap break-words text-xs italic opacity-90"
        >
          {citation.text}
          {citation.truncated ? " …" : ""}
        </div>
      ) : citation.readable ? (
        <div className="text-muted-foreground text-xs">
          the span this cites is not inside the message it cites
        </div>
      ) : null}
    </div>
  );
}
