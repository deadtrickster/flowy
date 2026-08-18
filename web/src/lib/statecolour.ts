/**
 * THE STATE PAIRS: the one place a fact about a row becomes a colour.
 *
 * Taken off the python handoff console's stylesheet
 * (tools/handoff-service/console.html) rather than described from memory, and
 * the thing worth taking from it is not the hex codes. It is the rule that made
 * that page read as alive while ours read as bland: EVERY COLOUR IN IT CARRIES A
 * FACT. A panel with no colour is a panel where nothing is being said, and a
 * panel coloured for variety is worse than a grey one, because it teaches the
 * reader that colour here means nothing and they stop looking at it.
 *
 * So the test every entry below has to pass is this: can a reader NAME what the
 * colour told them. filed rather than unfiled. reproduced rather than read off
 * the source. admissible rather than refused. Somebody is measuring this right
 * now rather than nobody has looked. Anything that cannot answer that question
 * does not get a colour, it gets the muted pair.
 *
 * WHY A PAIR AND NOT A COLOUR. Every state has a foreground AND a background,
 * which is what makes a filter row read as a control panel rather than as a
 * paragraph of links. Outlined chips all look alike from two feet away because
 * the only thing carrying the state is the text inside them; a tinted chip
 * carries it in the fill, which is legible before the word is.
 *
 * WHY IT IS ONE MODULE. The console had invented this shape three times already
 * and slightly differently - statusStyle and kindStyle in lib/todos.ts, and a
 * local GREEN/RED pair in MergeQueue.tsx - and a fourth copy for the findings
 * axes would be a fourth vocabulary for a reader to learn. The mix percentage,
 * the tones and the fallback all live here now, and the three surfaces that draw
 * state read them off this file. MergeQueue's own comment records what the
 * duplicate cost: a verdict passed through the wrong scale fell through to the
 * grey that means "waiting" and drew a refusal as a shrug.
 *
 * THE DARK COLUMN, because this console is dark once and on purpose - see
 * index.css. The python console carries both themes; these are its dark values,
 * which are the ones that are legible on our paper.
 */

/** A tone is a MEANING, not a colour name, so that a use site reads as the fact
 * it is stating. What each one is for is the whole of its definition:
 *
 *   ok      it stands, it is settled, it was proved
 *   accent  it is live: in somebody's hands, running now, ready to run
 *   info    it is stated but weaker
 *   cite    it only NAMES something over there, and nobody sent anything
 *   warn    somebody needs to look: nobody has said, or it was taken back
 *   bad     it failed, it was refused, or it is the one to fix first
 *   mute    nothing has happened here, and that is the ordinary condition
 */
export type Tone = "ok" | "accent" | "info" | "cite" | "warn" | "bad" | "mute";

/**
 * The tones themselves. Named for what they mean rather than for the hue, so
 * the day one of them moves nothing has to be re-read to find out what it was
 * for.
 */
const TONE_COLOUR: Record<Tone, string> = {
  ok: "#4ade80", // green - it stands
  accent: "#2dd4bf", // teal - it is live
  info: "#93b4f7", // blue - stated, and weaker
  cite: "#a481d4", // violet - a citation, and deliberately off the filing hues
  warn: "#e2b03a", // amber - somebody needs to look
  bad: "#f0906a", // rust - it failed, or fix this first
  mute: "#9aa0a6", // grey - nothing has happened, and that is normal
};

/**
 * toneStyle is a state pair: the foreground and the tint behind it.
 *
 * 18% is the same mix statusStyle and kindStyle already use, kept identical
 * rather than tuned per axis - two chips at two tint strengths on one row read
 * as two different KINDS of chip, which is a distinction nothing here means.
 */
export function toneStyle(tone: Tone): { color: string; backgroundColor: string } {
  const colour = TONE_COLOUR[tone];
  return { color: colour, backgroundColor: `color-mix(in srgb, ${colour} 18%, transparent)` };
}

/** toneColour is the bare foreground, for the marks that are a shape rather
 * than a chip - the severity dot and the severity bar, which are filled solid
 * because at 8px a tint is invisible. */
export function toneColour(tone: Tone): string {
  return TONE_COLOUR[tone];
}

/**
 * THE FILING AXIS: what THEIR tracker says. See lib/findings.ts for why this is
 * not our lifecycle and why referenced is not a weaker filed.
 *
 * The split that matters to the eye is between "nobody has sent this" and
 * "somebody has", because that is the question the list is worked from before
 * filing anything. So unfiled is deliberately quiet - most of the corpus is
 * unfiled and a list where the ordinary condition shouts has no signal in it -
 * and the three states where something is actually over there are the loud ones.
 *
 * filed is ACCENT and accepted/fixed are OK, which is a distinction rather than
 * a gradient: filed means it is in their hands and the outcome is unknown, and
 * green for that would be reading a submission as a result.
 *
 * REFERENCED IS VIOLET, AND IT IS OFF THE FILING CHAIN ON PURPOSE. filed,
 * accepted and fixed are one story told in teal and green - a thing we sent,
 * moving through their tracker - and the first version of this drew referenced
 * in blue, one hue over from filed's teal. That is exactly the mistake the state
 * itself exists to prevent: referenced means a pull request or an issue over
 * there MENTIONS this, and nobody filed anything. Two shades of one colour say
 * "a weaker filed", which is the reading that turned one filing into eight. A
 * colour cannot be a little bit of another colour when the fact it carries is
 * categorically not a weaker version of that fact.
 *
 * A state word this console does not know draws WARN, not mute. It came from
 * something newer or from a hand, the reader is the only one who can find out
 * what it meant, and drawing it in the colour of "nothing happened" is how it
 * stops being noticed. This mirrors knownUpstream's argument in lib/findings.ts.
 */
export function upstreamTone(state: string): Tone {
  switch (state) {
    case "unfiled":
      return "mute";
    case "referenced":
      return "cite";
    case "filed":
      return "accent";
    case "accepted":
    case "fixed":
      return "ok";
    case "rejected":
      return "bad";
    case "withdrawn":
      return "warn";
    default:
      return "warn";
  }
}

/**
 * THE EVIDENCE AXIS: how sure anybody is, and on what.
 *
 * UNSTATED IS WARN AND THIS IS THE IMPORTANT ONE. Absent evidence is the most
 * common state in the corpus and it is the state that decides whether a finding
 * may be filed at all - nothing goes upstream until its reproduction has run
 * against current main. Drawn in the grey of "nothing to see", it reads as fine,
 * which is the exact wrong reading and is the reason the badge is drawn at all
 * rather than omitted. The colour has to say "somebody needs to look at this",
 * because that is the fact.
 *
 * source is INFO because it is a real claim, weaker: somebody read the code.
 * reproduced is ACCENT because somebody ran it and watched it happen. verified
 * is OK because it was run against a named commit and the sha is recorded beside
 * it. verified and refuted are the two SETTLED states, and they are opposite
 * outcomes of the same act rather than two steps along a scale - see the case
 * below on why refuted is not a shade of verified.
 */
export function evidenceTone(state?: string): Tone {
  switch (state) {
    case "source":
      return "info";
    case "reproduced":
      return "accent";
    case "verified":
      return "ok";
    // REFUTED IS THE OPPOSITE OUTCOME OF VERIFIED, not a weaker one, so it is not
    // a neighbouring shade of it. Both are a run against a named commit; one
    // found the bug and one found nothing, and they are the two ends of this axis
    // rather than two steps along it.
    //
    // It draws MUTE, and mute is the right answer for a reason that is about the
    // reader rather than about the result. This axis colours by how much a row
    // should pull your attention, and a refuted finding is the only state on it
    // that is finished and needs nobody: somebody ran it, it did not happen, the
    // commit is recorded beside it. Amber would say "look at this", green would
    // say it stands, and rust would say it is severe. All three are false.
    case "refuted":
      return "mute";
    default:
      return "warn";
  }
}

/** Whether the finding SHIPS SOMETHING THAT CAN BE RUN, which is a separate
 * fact from what running it proved - a finding can carry a script nobody has
 * run, and one can be marked reproduced from a run whose tree was never
 * attached. Having one is a capability, so ok; not having one is ordinary, so
 * mute rather than a warning about a thing most findings legitimately lack. */
export function reproTone(runnable: boolean): Tone {
  return runnable ? "ok" : "mute";
}

/**
 * HOW BAD IT IS. This is the one an operator scans a list for, so it is the one
 * drawn as a dot and stacked into a bar rather than only written into a chip.
 *
 * Unrecognised words are muted rather than guessed at. severity is a free string
 * on an artifact, the corpus importers do not agree about it, and painting an
 * unknown word in the colour of "fix this first" would be this console asserting
 * a judgement nobody made.
 */
export function severityTone(severity?: string): Tone {
  const word = (severity ?? "").trim().toLowerCase();
  if (!word) return "mute";
  // MATCHED BY PRECEDENCE, NOT BY EQUALITY, because the corpus does not speak in
  // three words. The imported findings rate themselves low, lowmed, med, medhigh
  // and high - a five point scale - and an equality table for critical/medium/low
  // drew twelve of the sixteen rated findings in the grey that means NOBODY HAS
  // RATED THIS. Saying "unrated" about a row somebody rated is a false statement,
  // and it is worse than saying nothing because the reader cannot tell the two
  // apart.
  //
  // Highest term wins, so a compound lands in its upper band: medhigh reads as
  // high rather than as medium, and lowmed reads as medium. That is the reading
  // somebody scanning for what to fix first would want, and it is the safe
  // direction to be wrong in.
  if (word.includes("critical") || word.includes("high")) return "bad";
  if (word.includes("med")) return "warn";
  if (word.includes("low")) return "info";
  // A word outside the scale entirely is still not guessed at. severity is a free
  // string, the importers do not agree about it, and painting an unknown word as
  // "fix this first" would be this console asserting a judgement nobody made.
  return "mute";
}

/** The bands a severity bar stacks, worst first, so the left-hand end of every
 * bar on the page is the part somebody is looking for. `word` is what the band
 * is CALLED in a tooltip and in the check's selector - a band rather than a
 * literal severity, because the corpus's words (med, medhigh, lowmed) do not map
 * one to one onto anything. unrated is last: it is the remainder, not a
 * severity, and it is still counted so the bar cannot over-report the share that
 * is high. */
export const SEVERITY_BANDS: { tone: Tone; word: string }[] = [
  { tone: "bad", word: "high" },
  { tone: "warn", word: "medium" },
  { tone: "info", word: "low" },
  { tone: "mute", word: "unrated" },
];

/**
 * IS THIS STILL THE REPORT TO READ. Both halves are drawn, and drawing the
 * current one is not decoration: a page that marks only the replaced ones says
 * "current" with an absence, and an absence is also what an unmarked row looks
 * like when the mark failed to render. One row shape, two states, both stated.
 */
export function reportTone(replaced: boolean): Tone {
  return replaced ? "bad" : "ok";
}

/**
 * MAY THIS LAND. Four answers, not two, and the two quiet ones are the ones
 * that were being confused.
 *
 * undecided is MUTE: nobody stated a tip, so no question was asked, and any
 * colour here would be a light somebody switched on by accident.
 *
 * gating is ACCENT because a run is measuring this branch RIGHT NOW and landing
 * anything else on the target invalidates it. That is the fact nobody could see
 * before it was drawn, and it cost two rebuilds in an hour; it was then drawn in
 * the same grey as "not judged", which is a mark that says nothing.
 *
 * held is WARN and not bad: the target is reserved by somebody else's lock, so
 * the answer is wait rather than no, and an agent that reads a wait as a refusal
 * re-gates when it should sleep.
 */
export type Verdict = "admissible" | "refused" | "undecided" | "gating" | "held";

export function verdictTone(verdict: Verdict): Tone {
  switch (verdict) {
    case "admissible":
      return "ok";
    case "refused":
      return "bad";
    case "gating":
      return "accent";
    case "held":
      return "warn";
    default:
      return "mute";
  }
}
