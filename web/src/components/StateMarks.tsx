import { Badge } from "@/components/ui/badge";
import { SEVERITY_BANDS, type Tone, severityTone, toneColour, toneStyle } from "@/lib/statecolour";
import type * as React from "react";

/**
 * The three marks every state-carrying surface draws, so that one fact is drawn
 * one way everywhere. See lib/statecolour.ts for which fact gets which tone and
 * why; this file is only the shapes.
 *
 * Each of them keeps the WORD on screen beside the colour. Colour is the second
 * signal and never the only one: a reader who cannot separate amber from green
 * has to be able to read the same page, and a mark whose meaning is only its
 * hue cannot be quoted, searched or reported.
 */

/**
 * A tinted chip: one state, in its own pair.
 *
 * `state` is on the element as a data attribute rather than inferred from the
 * text, because the label a reader sees carries more than the state - "filed pr
 * #16958" - and a check that had to parse the label back into a state would be
 * asserting against a sentence. It is also what makes "the same fact is the same
 * colour on the list and on the finding page" a thing that can be measured
 * rather than eyeballed.
 */
export function StateChip({
  tone,
  axis,
  state,
  title,
  className,
  children,
}: {
  tone: Tone;
  axis: string;
  state: string;
  title?: string;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <Badge
      variant="secondary"
      className={className}
      style={toneStyle(tone)}
      title={title}
      data-state-axis={axis}
      data-state={state}
      data-tone={tone}
    >
      {children}
    </Badge>
  );
}

/**
 * The severity dot: an 8px round mark before a title, so a row's severity is
 * legible without reading a word.
 *
 * A row with no severity gets NO dot rather than a muted one. The dot is a claim
 * about how bad something is, and drawing one for a row where nobody made that
 * claim would be inventing the weakest answer - the same argument lib/findings.ts
 * makes about never defaulting an unstated evidence state to "source".
 *
 * aria-hidden with the severity in a title: to a screen reader this is noise
 * repeated from the chip that follows it, and the word is already on the row.
 */
export function SeverityDot({ severity }: { severity?: string }) {
  const word = (severity ?? "").trim();
  if (!word) return null;
  return (
    <span
      aria-hidden="true"
      title={`severity: ${word}`}
      data-severity-dot={word}
      className="inline-block size-2 shrink-0 rounded-full"
      style={{ backgroundColor: toneColour(severityTone(word)) }}
    />
  );
}

/**
 * The severity bar: one 7px stacked bar across a whole list, so a LIST has a
 * shape - how much of this corpus is high and how much is low, in one glance,
 * before anything is opened or filtered.
 *
 * This is the mark a list of rows cannot get from per-row colour, however good
 * that colour is: forty dots down a page do not add up to a proportion by eye.
 *
 * Worst first, left to right, so the end somebody is looking for is always in
 * the same place. Rows with no severity are counted into the total but drawn as
 * the muted remainder rather than being dropped, because a bar that silently
 * excluded them would over-report the share that is high.
 */
export function SeverityBar({
  items,
  label,
}: {
  items: { severity?: string }[];
  label: string;
}) {
  if (items.length === 0) return null;

  // BUCKETED BY BAND, NOT BY WORD, and the band comes from severityTone, so the
  // bar and the dots cannot disagree about the same finding. Counting exact words
  // meant the corpus's own vocabulary - med, medhigh, lowmed - fell into the
  // unrated remainder, and a bar over sixteen rated findings drew twelve of them
  // as "nobody said".
  const counts = new Map<Tone, number>();
  for (const item of items) {
    const band = severityTone(item.severity);
    counts.set(band, (counts.get(band) ?? 0) + 1);
  }

  // Worst first, left to right, so the end somebody is looking for is always in
  // the same place. Unrated is last because it is the remainder rather than a
  // severity, and it is still COUNTED - a bar that dropped those rows would
  // over-report the share that is high.
  const segments = SEVERITY_BANDS.flatMap((band) => {
    const n = counts.get(band.tone) ?? 0;
    if (n === 0) return [];
    return [{ ...band, n }];
  });

  return (
    <div
      aria-label={label}
      data-severity-bar={items.length}
      title={segments.map(({ word, n }) => `${n} ${word}`).join(", ")}
      className="flex h-[7px] w-full max-w-60 overflow-hidden rounded-sm"
      style={{ backgroundColor: toneColour("mute") }}
    >
      {segments.map(({ word, tone, n }) => (
        <span
          key={word}
          data-severity-segment={word}
          data-count={n}
          style={{
            width: `${(n / items.length) * 100}%`,
            backgroundColor: toneColour(tone),
          }}
        />
      ))}
    </div>
  );
}
