/**
 * A colour per speaker, so a room of five is readable at a glance.
 *
 * Four agents and a person post into one log, and until now every line was the
 * same colour with a name in front of it. That makes scanning a room a reading
 * exercise: you find who said something by reading, not by looking. Colour is
 * the cheapest possible index into "who", and it costs nothing to maintain
 * because it is DERIVED, not assigned - nobody registers a colour, nothing has
 * to be kept in sync, and a name that appears for the first time already has
 * one.
 *
 * Two rules make it honest rather than decorative:
 *
 * It is keyed on the name, so the same speaker is the same colour in the
 * transcript, in the roster and on the todo they own. A colour that meant one
 * thing in one panel and another elsewhere would be worse than no colour.
 *
 * And it never carries meaning ON ITS OWN. The name is always still there.
 * Colour that replaces a label excludes anybody who cannot tell those hues
 * apart, and about one man in twelve cannot - so this is an accelerator for
 * people who see it and a no-op for people who do not, never the only channel.
 */

/**
 * The palette. Hand-picked rather than generated: evenly spaced around the
 * wheel, all at a lightness that stays legible on both themes, and skipping the
 * yellow-green band that turns to mud on a light background.
 *
 * They are set as explicit colours rather than theme tokens because that is
 * what they are - an identity, not a role. A token would drag them into the
 * meaning system, where "primary" and "destructive" already say things.
 */
const PALETTE = [
  "#e06c9a", // rose
  "#d98a3f", // amber
  "#4fae7a", // green
  "#3fa3c9", // cyan
  "#7b8ee8", // indigo
  "#b07ae0", // violet
  "#d1636b", // red
  "#3fb0a5", // teal
];

/**
 * A small, stable hash. FNV-1a: a couple of lines, no dependency, and the same
 * answer in every session - which matters, because a speaker whose colour
 * changed on reload would be an anti-feature. Math.random would be worse than
 * nothing here.
 */
function hash(text: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

/** The colour for a speaker, by the name they are shown under. */
export function speakerColour(name: string): string {
  const key = name.trim().toLowerCase();
  if (!key) return PALETTE[0];
  return PALETTE[hash(key) % PALETTE.length];
}

/**
 * Style for a name rendered in its speaker's colour: the colour on the text,
 * and a matching wash behind it so it reads as a tag rather than as an
 * arbitrarily coloured word.
 *
 * color-mix keeps the wash at the same hue without a second palette to keep in
 * step, and falls back to no background on an engine that does not support it -
 * which degrades to exactly today's appearance.
 */
export function speakerStyle(name: string): { color: string; backgroundColor: string } {
  const colour = speakerColour(name);
  return { color: colour, backgroundColor: `color-mix(in srgb, ${colour} 14%, transparent)` };
}
