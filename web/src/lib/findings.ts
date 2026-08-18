import type { Artifact } from "@/lib/api";

/**
 * A finding stands on THREE AXES, and this module is the one place the console
 * reads them off a row. They are not three names for one state, and every
 * attempt to render one in place of another has produced a page that says
 * something false:
 *
 *   OUR LIFECYCLE     open, triaged, in-progress, done, ...   how far WE got
 *   UPSTREAM FILING   unfiled, filed, accepted, fixed, ...    what THEIR tracker says
 *   EVIDENCE          source, reproduced, verified-on <sha>   how sure we are, and on what
 *
 * The lifecycle is `status`, a column, owned by lifecycle.go. The other two are
 * keys in `fields`, which is a jsonb blob the node signs with the row - the same
 * place the repro manifest lives, for the reason internal/store/findingrepro.go
 * writes down: a finding gets no columns of its own.
 *
 * WHY A READER MODULE RATHER THAN `fields.upstream_state` AT EACH USE SITE.
 * Three surfaces need these - the list, the finding page, and anything that
 * filters - and a key spelled slightly differently on one of them is a page that
 * silently reports every finding as unfiled. The keys are also not the console's
 * to choose: they are the names the corpus importers and
 * internal/store/findingupstream.go were both given, and this file exists so
 * that there is exactly one copy of them on this side of the wire.
 *
 * ABSENCE IS A FACT, NOT A BLANK, on both of the fields-borne axes, and the two
 * absences mean different things:
 *
 *   an unstated FILING is "nobody sent it upstream" - unfiled, which is the
 *   ordinary condition of a finding and the state most of the corpus is in. The
 *   store answers the same way (FindingUpstreamOf), so a row carrying no keys
 *   and a row carrying upstream_state=unfiled read alike here.
 *
 *   an unstated EVIDENCE is "nobody has said", which is NOT the same as
 *   "source". source is a claim somebody made - I read the code and I believe
 *   this is wrong - and inventing it for a row that never made it would be this
 *   console asserting a fact on somebody's behalf. So it stays undefined and the
 *   surfaces say "not stated" rather than picking the weakest word.
 *
 * The corpus this is built for is not imported yet, and may not be while these
 * pages are written. Everything below therefore reads a TYPE and never a
 * fixture: a finding with none of these keys renders honestly today, and the
 * same code renders an imported one the day the import lands.
 */

/** UpstreamState is where a filing stands on somebody else's tracker.
 *
 * Five of the seven are THEIR judgements. withdrawn is ours - we pulled the
 * filing back - and it is a separate word because going back to unfiled would
 * erase an issue number that still exists over there, and rejected would
 * attribute our own retraction to maintainers who never looked at it.
 *
 * REFERENCED IS NOT A WEAKER FILED, and it is the one a reader will get wrong
 * if this list is skimmed: it means the finding NAMES issues or pull requests
 * over there and nobody is claiming we sent anything. Seven of the sixteen
 * RAGFlow findings are that, and reading them as filings is what turned one
 * filing into eight. Anything that counts "what have we sent upstream" counts
 * filed/accepted/fixed and never this.
 *
 * The list and its order are internal/store/findingupstream.go's
 * UpstreamStates, in the order a filing travels through it. */
export type UpstreamState =
  | "unfiled"
  | "referenced"
  | "filed"
  | "accepted"
  | "fixed"
  | "rejected"
  | "withdrawn";

export const UPSTREAM_STATES: UpstreamState[] = [
  "unfiled",
  "referenced",
  "filed",
  "accepted",
  "fixed",
  "rejected",
  "withdrawn",
];

/**
 * UpstreamFiling is the filing axis as it rides in fields, under the six keys
 * internal/store/findingupstream.go names: upstream_tracker, upstream_id,
 * upstream_url, upstream_state, filed_at, filed_by.
 *
 * `id` is a STRING and is never parsed into a number here for the reason that
 * file gives: not every tracker numbers with integers, and an id that has been
 * through a parse is one that can come back out different from the one they
 * gave.
 */
export interface UpstreamFiling {
  tracker?: string;
  /** issue or pr. "We reported it" and "we sent a fix" are different claims,
   * and the corpus field these came out of held both in one string. */
  kind?: string;
  id?: string;
  url?: string;
  state: UpstreamState;
  filed_at?: string;
  filed_by?: string;
  /** Everything upstream this finding TOUCHES - issues and pull requests, many
   * per finding, the same one repeated on every finding a pull request covers.
   * It asserts nothing about whether anybody filed anything; that is state's
   * job and only state's. */
  refs: UpstreamRef[];
}

/** UpstreamRef is one thing over there a finding cites: whose tracker, whether
 * it is an issue or a pull request, their number and the link. */
export interface UpstreamRef {
  tracker: string;
  kind: string;
  id: string;
  url?: string;
}

/**
 * EvidenceState is how strong the evidence is, which is the axis that makes
 * this a tracker rather than a list.
 *
 *   source      somebody read the code and believes this is wrong
 *   reproduced  somebody ran it and watched it happen
 *   verified    it was run against a named commit, which is recorded beside it
 *
 * verified is a word PLUS A COMMIT. REPORTABLE-FINDINGS' filing rule - nothing
 * goes upstream until its reproduction has been run against a build of current
 * origin/main HEAD, with that SHA on the item - is a transition on this axis and
 * not on either of the others, and "reproduced, but not against current main" is
 * the list an operator works from before filing anything.
 */
export type EvidenceState = "source" | "reproduced" | "verified";

export const EVIDENCE_STATES: EvidenceState[] = ["source", "reproduced", "verified"];

/**
 * Evidence is that axis as it rides in fields: evidence_state, plus the commit a
 * verified claim rests on and when, plus the run whose log backs it.
 *
 * state is OPTIONAL here and nowhere else in this file, because "nobody has
 * said" is a real and common answer - see the head of this module on why it is
 * not defaulted to source.
 */
export interface Evidence {
  state?: EvidenceState;
  verified_on?: string;
  verified_at?: string;
  last_run?: string;
}

/** ReproTree is what the row says about its own reproduction: the files, in the
 * order WriteFindingRepro wrote them, and how to run them. Read straight off the
 * manifest keys in internal/store/findingrepro.go - repro_files,
 * repro_entrypoint, repro_interp, isolation, cmd_override. */
export interface ReproTree {
  files: { path: string; attachment_id: string }[];
  entrypoint?: string;
  interp?: string;
  isolation?: string;
  cmd_override?: string;
}

/** fieldsOf is the one cast. `fields` is `unknown` on Artifact on purpose -
 * every type owns its own shape - so the narrowing happens here rather than at
 * five call sites with five slightly different guards. */
function fieldsOf(artifact: Artifact): Record<string, unknown> {
  const fields = artifact.fields;
  if (!fields || typeof fields !== "object" || Array.isArray(fields)) return {};
  return fields as Record<string, unknown>;
}

/** text reads a key that is meant to be a string, and treats anything else -
 * a number a tracker's export wrote, a null - as absent rather than rendering
 * "[object Object]" into a badge. */
function text(fields: Record<string, unknown>, key: string): string | undefined {
  const value = fields[key];
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  return trimmed === "" ? undefined : trimmed;
}

/**
 * upstreamOf reads the filing axis. A row that carries none of the keys is
 * unfiled, which is the store's own answer for one.
 *
 * A state word this console does not know is NOT quietly turned into unfiled:
 * that would be a page counting "what have we sent upstream" wrongly and saying
 * nothing about it. It comes back as-is, and the surfaces draw it as the
 * unfamiliar word it is - see UNKNOWN_UPSTREAM below.
 */
export function upstreamOf(artifact: Artifact): UpstreamFiling {
  const fields = fieldsOf(artifact);
  const state = text(fields, "upstream_state");
  const id = text(fields, "upstream_id");
  const refs = refsIn(fields);
  return {
    tracker: text(fields, "upstream_tracker"),
    kind: text(fields, "upstream_kind"),
    id,
    url: text(fields, "upstream_url"),
    // The store's own fallback, mirrored rather than approximated
    // (FindingUpstreamOf): a row with numbers on it and no state word is
    // REFERENCED, because something over there is named and nobody said they
    // sent it. Answering unfiled here would be this page contradicting the node
    // about the same row, and answering filed would be the count that turned
    // one filing into eight.
    state: (state ?? (id || refs.length > 0 ? "referenced" : "unfiled")) as UpstreamState,
    filed_at: text(fields, "filed_at"),
    filed_by: text(fields, "filed_by"),
    refs,
  };
}

/** refsIn reads upstream_refs, dropping anything that is not a reference. A
 * malformed entry is left out rather than rendered as a blank chip pointing
 * nowhere. */
function refsIn(fields: Record<string, unknown>): UpstreamRef[] {
  const raw = Array.isArray(fields.upstream_refs) ? fields.upstream_refs : [];
  return raw.flatMap((entry) => {
    if (!entry || typeof entry !== "object") return [];
    const ref = entry as Record<string, unknown>;
    const tracker = typeof ref.tracker === "string" ? ref.tracker : "";
    const kind = typeof ref.kind === "string" ? ref.kind : "";
    const id = typeof ref.id === "string" ? ref.id : "";
    const url = typeof ref.url === "string" ? ref.url : undefined;
    if (!id && !tracker) return [];
    return [{ tracker, kind, id, url }];
  });
}

/** refLabel is how a reference is named on screen - "ragflow pr #16958" - the
 * same sentence UpstreamRef.String builds on the other side. */
export function refLabel(ref: UpstreamRef): string {
  if (!ref.id) return ref.tracker || "nothing";
  const number = ref.kind === "pr" ? `pr #${ref.id}` : `#${ref.id}`;
  return ref.tracker ? `${ref.tracker} ${number}` : number;
}

/** knownUpstream reports whether a state is one of the six. A row carrying
 * anything else is showing a word somebody wrote that this build does not know,
 * and a reader is better served by seeing it than by seeing it silently
 * normalised. */
export function knownUpstream(state: string): state is UpstreamState {
  return (UPSTREAM_STATES as string[]).includes(state);
}

/** filedUpstream reports whether a filing STANDS - an issue is live over there
 * and nobody has taken it back. rejected and withdrawn are filings that happened
 * and no longer stand, which is why they are not in this set. Mirrors
 * UpstreamFiling.Filed in internal/store/findingupstream.go. */
export function filedUpstream(filing: UpstreamFiling): boolean {
  return filing.state === "filed" || filing.state === "accepted" || filing.state === "fixed";
}

/** evidenceOf reads the evidence axis, and leaves state undefined when the row
 * does not state one. See the head of this module. */
export function evidenceOf(artifact: Artifact): Evidence {
  const fields = fieldsOf(artifact);
  const state = text(fields, "evidence_state");
  return {
    state:
      state && (EVIDENCE_STATES as string[]).includes(state) ? (state as EvidenceState) : undefined,
    verified_on: text(fields, "verified_on"),
    verified_at: text(fields, "verified_at"),
    last_run: text(fields, "last_run"),
  };
}

/**
 * reproOf reads the repro tree off the manifest.
 *
 * A tree with no files is no tree: WriteFindingRepro refuses to write an empty
 * one, so a repro_files that is absent, empty or not an array all mean the same
 * thing - this finding has nothing to run - and the surfaces say exactly that
 * rather than offering a run button that could only fail.
 */
export function reproOf(artifact: Artifact): ReproTree {
  const fields = fieldsOf(artifact);
  const raw = Array.isArray(fields.repro_files) ? fields.repro_files : [];
  const files = raw.flatMap((entry) => {
    if (!entry || typeof entry !== "object") return [];
    const file = entry as Record<string, unknown>;
    const path = typeof file.path === "string" ? file.path : "";
    const attachment = typeof file.attachment_id === "string" ? file.attachment_id : "";
    if (!path) return [];
    return [{ path, attachment_id: attachment }];
  });
  return {
    files,
    entrypoint: text(fields, "repro_entrypoint"),
    interp: text(fields, "repro_interp"),
    isolation: text(fields, "isolation"),
    cmd_override: text(fields, "cmd_override"),
  };
}

/** hasRepro is the question every surface actually asks of a tree. */
export function hasRepro(tree: ReproTree): boolean {
  return tree.files.length > 0;
}

/**
 * reportDraftOf is the third document a finding carries: the text that goes
 * UPSTREAM, which is neither the finding (what is wrong, for whoever has to fix
 * it) nor the discovery (how it was found, what was tried). Writing a defect up
 * for your own record and writing it for a maintainer who has never seen your
 * setup are different jobs, and this key is where the second one is kept so it
 * can be drafted, read and then filed with its number recorded on the row beside
 * it. See the `report` key in internal/store/findingreport.go.
 */
export function reportDraftOf(artifact: Artifact): string | undefined {
  return text(fieldsOf(artifact), "report");
}

/** UNKNOWN_UPSTREAM is what a surface says about a state word outside the
 * vocabulary - see knownUpstream. It is a sentence rather than a shrug because
 * the reader is the one person who can find out what it was meant to mean. */
export const UNKNOWN_UPSTREAM =
  "this filing state is not one this console knows - it was written by something newer or by hand";
