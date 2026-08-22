/**
 * The node's HTTP API, as the console sees it.
 *
 * Every call carries the bearer token the node resolves to a principal, so the
 * console never decides what anybody may read: it asks, and the permission
 * filter on the other end answers. A room that comes back empty is a room this
 * token cannot see, and that is the correct thing to render.
 */

const TOKEN_KEY = "flowy.token";

/**
 * WHETHER THE CREDENTIAL THIS TAB IS USING STILL WORKS. 01M0K76WY4.
 *
 * IT LIVES IN THIS FILE BECAUSE THIS FILE CANNOT IMPORT ANYTHING.
 * scripts/api-error-check.mjs transforms api.ts and imports it AS A DATA URL,
 * where no specifier resolves - so a `lib/credential` module beside this one
 * could not be reached from here, and the gate says so. See lib/utils, which
 * carries the same constraint for the same reason. I wrote that module first
 * and deleted it.
 *
 * WHAT IT IS FOR. The operator's console "stopped working" and every check an
 * agent could run said it was healthy - an agent authenticates with a bearer
 * token, a person's browser with a session, and the session was the half nobody
 * could see. Measured on the live console with a rejected credential:
 *
 *   with a good one   /memory 11521 chars, 30 rooms in the sidebar
 *   with a dead one   /memory 454 chars, no rooms, every pane empty
 *   what it says      NOTHING. Silent 401s in the browser console.
 *
 * The frame renders and everything inside is blank, which is indistinguishable
 * from a node that lost its data. This console keeps EMPTY, FORBIDDEN and
 * UNREACHABLE apart everywhere else; a blank page is how they become one.
 *
 * EXPIRED AND SWEPT ARE NOT TOLD APART, deliberately. The session behind the
 * report had not expired - the surviving row's expiry was in the future. An
 * EARLIER login reached its fourteen days, login.go:488 deleted the row
 * (`DELETE FROM sessions WHERE expires <= now()`), and the cookie outlived it.
 * Both give the identical 401, the browser cannot see which, and "no longer
 * valid, sign in again" is true of both.
 *
 * 401 ONLY, NEVER 403. Unauthorized means the node does not know who you are;
 * forbidden means it does, and the answer is no. A banner over the second tells
 * somebody to sign in again to fix a permission - a wrong instruction that
 * costs a login to disprove. api_vm.go answers 403 to every non-operator on a
 * page they are allowed to open, so this is not hypothetical.
 */
let credentialDead = false;
const credentialListeners = new Set<(dead: boolean) => void>();

function publishCredential(next: boolean) {
  if (next === credentialDead) return;
  credentialDead = next;
  for (const listener of credentialListeners) listener(credentialDead);
}

/**
 * watchCredential calls back now and on every change, and returns an
 * unsubscribe - the same shape lib/fresh uses for the staleness bar.
 *
 * The CLEAR is the half that keeps this honest. Somebody who signs in in
 * another tab, or pastes a working token into the rail, has a credential again
 * and nothing else would ever take the message down - and a warning that
 * outlives its own cause is one people learn to ignore, including the time it
 * is right.
 */
export function watchCredential(onChange: (dead: boolean) => void): () => void {
  credentialListeners.add(onChange);
  onChange(credentialDead);
  return () => {
    credentialListeners.delete(onChange);
  };
}

/**
 * Citation is what a message says it is about, resolved by the node for the
 * token that read it: the message it cites, and - only when this reader may
 * read that message - who was quoted and the words themselves.
 *
 * The words are DERIVED, not stored. The row carries a pointer and a byte span
 * into the cited body, and the node cuts the quote out of the signed source on
 * the way out - so a citation cannot misquote, and the console may draw it as
 * the quoted person's own words rather than as the citing author's account of
 * them. See citations.go.
 *
 * `readable` is never absent, and it is the field the console has to draw
 * rather than assume: rooms are scoped by project and the log is not, so a
 * reply is often in front of somebody whose source message is not. Then there
 * is no text, no actor and no name - the node hands over none of it - and the
 * honest thing on screen is that this quotes something out of reach.
 */
export interface Citation {
  message: string;
  whole: boolean;
  start?: number;
  end?: number;
  readable: boolean;
  actor?: string;
  name?: string;
  text?: string;
  /** The quote was cut at the node's cap, so it ends in an ellipsis. */
  truncated?: boolean;
}

/** FlowyEvent is one row of the append-only log. A chat message is one of these. */
/**
 * Disowned is the repudiation covering a row's author at that row's reading,
 * resolved by the node for whoever is reading - see store.FillDisowned.
 *
 * IT IS A THIRD READING AND IT REPLACES NEITHER of the other two. authorship
 * records whether a signature verified at the node, and that stays true of a
 * stolen key: the bytes really were signed with it. So a disowned row is not
 * "attributed", it is "authored, and its author disowns it" - a stranger
 * sentence than either half and the only accurate one. A surface that swapped
 * one for the other would lose the difference between a stolen key and a
 * forgery.
 *
 * `by` is the repudiation's own id, so a mark always has something behind it
 * that somebody signed. A mark with no route is a rumour with a nicer font.
 */
export interface Disowned {
  /** The repudiation that says so. */
  by: string;
  /** Who disowned it, which is also the row's author. */
  subject: string;
  /** What they said, absent when they said nothing. */
  reason?: string;
  /** The closed window they disowned, as packed clock readings. */
  from: number;
  to: number;
}

export interface FlowyEvent {
  id: string;
  type: string;
  project: string | null;
  room: string;
  thread: string;
  parents: string[];
  actor: string;
  artifact: string;
  seq_hlc: number;
  node: string;
  body: string;
  /**
   * addressee is who the message is directed at, absent for the room. It
   * changes how a message is drawn and nothing about who may read it: an
   * addressed message is a room message, read by exactly the principals that
   * could read the room without it.
   */
  addressee?: string;
  /**
   * private says this is a direct message: the actor and the addressee are the
   * only two principals who can read it, decided by one clause in the node's
   * event filter over the row's own columns - no project, no room, an
   * addressee.
   *
   * The node derives it and the console draws it. Setting it here makes
   * nothing private: what decided whether this row arrived at all happened in
   * the database before the response was written.
   */
  private?: boolean;
  /**
   * meta is where the node stamps who is speaking. actor_name is what they
   * were called when they said it, and it is optional in the strong sense:
   * every message said before the node recorded a name has none, so a reader
   * falls back to the actor id rather than drawing a gap.
   *
   * mentions is the @names in the body that meant somebody, as the node
   * resolved them when the message was said: "name:id" pairs, space separated,
   * in the order they were written. The first of them is also the addressee -
   * see mentions.go. Optional in the same strong sense: absent on every
   * message that named nobody, which is every message written before mentions
   * existed.
   *
   * cite is the citation as the row records it - "<id>" for a whole message,
   * "<id>:<start>:<end>" for a span of one. The console does not read it: the
   * resolved `citation` below is the same fact with the permission filter
   * already applied, and a client deriving a quote from the raw pointer would
   * be deriving it from a message it may not be able to read.
   */
  meta?: {
    actor_kind?: "user" | "agent";
    actor_user?: string;
    actor_name?: string;
    mentions?: string;
    cite?: string;
    /** attachment ids a message carries, space separated, in the order named. */
    attachments?: string;
  };
  /** What this message is answering, as the node resolved it for this reader. */
  citation?: Citation;
  /**
   * authorship is what THIS node can say about who wrote the message, and it is
   * the node's finding rather than a claim on the row: "authored" means a
   * signature made with the actor's own key verified here, "attributed" means
   * it did not and the message rests on the word of whichever node relayed it.
   *
   * A message carries two signatures. The node's says which machine wrote the
   * bytes; the principal's says who the words are from. Pinning a peer's node
   * key is agreeing to carry what it relays - which is what federation is - and
   * was being read as agreeing to whatever it said about who wrote what, so a
   * pinned peer could put words in anybody's mouth and every reader here saw
   * them as that person's own.
   *
   * Most rows are attributed today, and that is honest rather than alarming: it
   * is what a store holds until principals have keys.
   */
  authorship?: "authored" | "attributed";
  /**
   * disowned is present when this message's ACTOR has repudiated the window it
   * falls in - see Disowned. Absent means nobody has, which is every message on
   * this fabric today, so a surface must draw nothing rather than drawing
   * "not disowned" on every line.
   */
  disowned?: Disowned;
  created: string;
}

/**
 * NodeInfo is what the node says about itself. `bundle` is the hashed console
 * asset this binary embeds - the fingerprint a running tab compares itself
 * against to find out a deploy has happened underneath it.
 */
export interface NodeInfo {
  node: string;
  version: string;
  console: boolean;
  bundle: string;
}

/** PinEntry is one line of the log behind a room's strip. */
export interface PinEntry {
  message: string;
  verb: string;
  actor: string;
  actor_kind?: string;
  at: string;
  event: string;
}

/**
 * PinsView is a room's strip: the ids that are up, and the log they were folded
 * out of. `pinned` is in the order each message was FIRST pinned, so re-pinning
 * an old decision does not reshuffle the strip under a reader.
 */
export interface PinsView {
  room: string;
  pinned: string[];
  log: PinEntry[];
}

/** One line of a reader's own bookmark log. */
export interface BookmarkEntry {
  message: string;
  verb: string;
  at: string;
  event: string;
}

/**
 * What a reader is keeping, newest first.
 *
 * `kept` and `messages` are deliberately not the same length. A message that
 * has stopped being readable is dropped from `messages` and stays in `kept` -
 * the bookmark is a pointer and the node never kept a copy - so a list that is
 * shorter than the count is telling the truth about what is still reachable.
 */
export interface BookmarksView {
  kept: string[];
  messages: FlowyEvent[];
  log: BookmarkEntry[];
}

/**
 * What threads this reader has unfolded, newest unfold first.
 *
 * The same shape as BookmarksView's `kept` and for the same reason: the set is
 * the state, and the log is how it got here. There is no `messages` twin - an
 * unfold is a pointer to a thread the stream is already drawing, so nothing is
 * fetched on its behalf.
 */
export interface ThreadsUnfoldedView {
  threads: string[];
  log: { thread: string; verb: string; at: string; event: string }[];
}

/**
 * WorkloadShare is one participant's slice of the open board.
 *
 * The denominator is every open row, unowned included: they are work in flight,
 * and a share computed over claimed rows alone would rise as the board fills
 * with rows nobody has taken.
 */
export interface WorkloadShare {
  assignee: string;
  open: number;
  share: number;
}

/**
 * Workload is the distribution probe, whole, INCLUDING BOTH ITS LINES so that
 * nothing re-derives them from the shares.
 *
 * The two verdicts that matter mean different things to do - `check` is look at
 * this, `rebalance` is hand some back - so a reader given the word and not the
 * number it crossed is left doing the arithmetic the probe exists to end.
 */
export interface Workload {
  open: number;
  unowned: number;
  /**
   * The slice as the node sends it, WHICH IS null WHEN IT IS EMPTY - Go
   * marshals an empty slice that way, and a board with nothing on it has one.
   * Typed honestly so a reader has to deal with it; SpreadCard normalises it
   * once. It blanked the whole overview before it was typed this way.
   */
  shares: WorkloadShare[] | null;
  top: string;
  top_share: number;
  /** ok, check, rebalance, alone, or empty. The node's word, never recomputed. */
  verdict: string;
  /** The check line, under its old name. */
  threshold: number;
  check: number;
  rebalance: number;
}

/**
 * NagView is what an idle seat should know, computed where the rows are.
 *
 * EVERY COUNT IS THE CALLER'S. There is no name parameter on the door, so this
 * is the board as this token can see it and a row it cannot read is in no total
 * here.
 */
export interface NagView {
  mine: number;
  unowned: number;
  open: number;
  mine_todo: number;
  /**
   * Which rows mine_todo counts, from the same loop that counts them.
   *
   * OPTIONAL BECAUSE A NODE MAY NOT SEND IT. An older node answers no key at
   * all, which is not the same as an empty list - one means "cannot say which",
   * the other means "none" - and the rail draws its dot either way.
   */
  mine_todo_ids?: string[];
  stale: number;
  stale_after_seconds: number;
  workload: Workload;
  /**
   * WHICH PROJECT THESE COUNTS ARE ABOUT. Five projects hold rows on the
   * dogfood node, and until the doors were stamped every board answer came
   * back indistinguishable from the same answer about any other. Absent when
   * the caller is reading across all of them, which only an operator can.
   */
  project?: string;
  all_projects?: boolean;
}

/** ChatPage is what a room read or a long poll answers with. */
export interface ChatPage {
  room?: string;
  events: FlowyEvent[];
  since: number;
  cursor: number;
  /**
   * before is the other end of a window, and only a backwards read carries it -
   * see api.roomWindow. It is the reading of the OLDEST message in the page,
   * strictly exclusive, so handing it back asks for the page before this one.
   * Absent on a forward read, which has nothing older to offer.
   */
  before?: number;
  /**
   * What is on each message of this page, keyed by message id. Absent from an
   * older node, which is why every reader treats it as optional rather than
   * assuming a map is there.
   *
   * Keyed BESIDE the events rather than hung on them: an event is the row that
   * replicates and is signed, and a fold of other people's rows does not belong
   * inside the shape a peer receives.
   */
  reactions?: Record<string, Reaction[]>;
  /**
   * How many messages each thread on this page holds, keyed by thread id.
   * Counted by the node over the whole log, because this client holds a WINDOW
   * of the room: counting the thread ids on screen answers "how much of this
   * thread did I happen to be sent", which is a different question and is wrong
   * whenever the thread is older than the window.
   *
   * Optional, and a missing key is not a zero. Every thread here came off a
   * message on this page, so it holds at least that message; absent means the
   * count was not taken - an older node, or a fold the node dropped - and the
   * reader is shown nothing rather than "0 replies".
   */
  threads?: Record<string, number>;
}

/**
 * One emoji on one message and everybody who put it there.
 *
 * Actors rather than a count, because in a room of four seats WHO is the whole
 * signal - an ack from the seat that has to act is worth more than three from
 * seats that do not. The count is drawn from the length; the names are what a
 * reader gets on hover.
 */
export interface Reaction {
  emoji: string;
  actors: string[];
  /** True when this reader is one of them, so the control draws as pressed. */
  mine: boolean;
}

/**
 * InboxReader is one reader's place in the log, as the node holds it: the
 * label, and the last reading it has acknowledged. `acked_delivery` and
 * `acked_quiet` say why the mark last moved and the console does not draw
 * them, but they are on the row and dropping them here would make this type
 * disagree with the endpoint.
 */
export interface InboxReader {
  reader: string;
  cursor: number;
  acked_delivery: number;
  acked_quiet: number;
  created: string;
  updated: string;
}

/**
 * Presence is the two rosters a room view wants. Members is who has spoken;
 * listeners is who holds a reader place. The node sees polling, not processes,
 * so a listener line says when a poll last started and whether one is in
 * flight - "polled 4s ago" is checkable, "online" would be a claim.
 *
 * waiter_kind is what the listener said it is: "tracked" wakes somebody when
 * it hears, "forked" hears and wakes nobody, "unknown" has not said. It is the
 * only field here that answers the question a roster is actually read for, and
 * the node reports "unknown" rather than assuming, so it is never absent.
 *
 * state is what the seat is DOING, which is not the same question: "listening"
 * polled inside the window, "starting" has never polled and was declared a
 * moment ago, and "lost" is holding a poll that never ended and is older than
 * any poll can be - something armed a waiter there and it stopped. A lost row
 * is deliberately still on this list: the panel's job is to say a seat has gone
 * deaf, and dropping the row would have deleted the only record that it had.
 */
export interface Presence {
  members: {
    actor: string;
    name: string;
    kind: string;
    role?: string;
    /**
     * Where this speaker's readable messages landed. A room belongs to a
     * project, so a name whose projects do not include the room's project
     * cannot hear that room - this is the field the @ list and the roster
     * filter on. Absent when the node had nothing to measure, which the
     * console reads as "cannot say" and keeps offering rather than hiding a
     * name the node has not judged.
     */
    projects?: string[];
  }[];
  listeners: {
    principal: string;
    project: string;
    reader: string;
    user_name: string;
    attached: boolean;
    waiter_kind: string;
    state: string;
    last_poll_at?: string | null;
    /**
     * When this seat last WROTE something, which is a different question from
     * when it last polled and the one somebody is actually asking. Derived by
     * the node from the events the seat authored, so there is nothing for an
     * agent to report - a signal an agent has to send is one a blocked agent
     * cannot send.
     *
     * Absent for a seat that has never authored anything, which is a real
     * answer and not zero.
     */
    last_acted_at?: string | null;
    /**
     * WHICH PROCESS the waiter says holds this reader, so a repair can name it
     * instead of hunting for it. Absent when the waiter has not claimed one,
     * which is every waiter that predates the column - and that is a real
     * answer rather than a missing one: this listener cannot be named, fall
     * back to what you did before.
     *
     * The node cannot see the process, only the poll, so this is a CLAIM in
     * the same standing as waiter_kind. It arrives all three parts or none:
     * the pid alone is not an identity, because pids are reused, and the pid
     * without its host is a number somebody might act on from the wrong
     * machine.
     */
    process?: {
      waiter_pid: number;
      waiter_since: string;
      waiter_host: string;
    } | null;
    updated: string;
  }[];
}

/** One end of a provenance relation, as the node hands it to a reader. */
export interface OriginRef {
  id: string;
  ref?: string;
  title?: string;
}

export interface Whoami {
  user: string;
  agent?: string;
  project?: string;
  operator?: boolean;
  /**
   * Where this token's writes land, as the registry sees it. project_fixture
   * is the one a person has to act on: a fixture is demo seed data and is
   * perfectly writable, so nothing refuses a write into one - which is exactly
   * why the console has to say it rather than leave the project as a word.
   */
  project_declared?: boolean;
  project_fixture?: boolean;
  project_origin?: string;
  /**
   * The projects this PERSON belongs to - where they may work, which is not
   * the same question as what they may read. A grant points at projects they
   * have never joined; membership is where their writes are allowed to land.
   *
   * [] for somebody who belongs to nothing, and that is the state every person
   * on this node is in right now: project_members is empty everywhere. ABSENT
   * would mean "this node does not report memberships", which is a different
   * answer and one a client cannot tell from an empty list - see the field's
   * comment on the node side.
   *
   * An agent has none at all: a seat's reach is minted into its token, a
   * different mechanism for a different kind of credential.
   */
  memberships?: string[] | null;
}

/** MeUser is the registry's row for the person or seat behind this request. */
export interface MeUser {
  id: string;
  handle?: string;
  display?: string;
  auto_delegate?: boolean;
  hlc?: number;
  node?: string;
}

/**
 * Me is GET /api/me: the row, whether a password exists, and whether this
 * request arrived on a bearer token rather than a cookie. The last two are what
 * decide whether changing a password has to prove the old one.
 */
export interface Me {
  user: MeUser;
  has_password: boolean;
  by_bearer: boolean;
}

/** Project is one row of the registry every project column points at. */
export interface Project {
  id: string;
  name: string;
  created_by?: string;
  provenance: string;
  fixture: boolean;
  origin?: string;
  superseded?: string[];
}

export interface ProjectsPage {
  count: number;
  current?: string;
  current_is_fixture: boolean;
  projects: Project[];
  /**
   * The subset of `projects` whose rows this token can reach. It is shorter than
   * `projects` whenever a grant points the other way: the registry shows a name
   * on an edge in either direction, and reading only travels along one of them.
   * Anything that states how far a list reaches has to use this one.
   */
  reads?: string[];
}

/**
 * What a read could NOT hand over, and why.
 *
 * A refusal nobody sees is indistinguishable from success. The node refuses a row
 * that names a principal it holds a signing key for, at or after that key's
 * epoch, with no signature of theirs that verifies - see FlowyEvent.authorship -
 * and until this existed the only party told was the peer that pushed it. On this
 * side the row simply was not there, and a queue read handed back a shorter list
 * that reads as "that is all the work there is".
 *
 * So it is the count AND the reason, together, and it is absent rather than zero
 * when there is nothing to say. A page with a `withheld` of 0 on it every day is
 * a page nobody reads the day it says 3.
 */
export interface Withheld {
  rows: number;
  reason: string;
}

/**
 * The authorship claims this node refused for GOOD, and why.
 *
 * `Withheld` above is what is missing right now, and it clears the moment the row
 * turns up properly signed. This is the decision behind it, and it outlives the
 * rule that made it: a claim the node refused is refused again on sight, without
 * being re-judged against whatever the rule has become since. Before that, a
 * refusal was only a delay - the peer went on offering the row, and the next
 * change that widened what this node takes let it in.
 *
 * Claims and not rows, deliberately. What is terminal is one unbacked assertion
 * that a named person wrote this, not the row and not the person: the same words
 * carrying that person's own signature are a different claim and they land. So a
 * row can be counted here and be present in the list beside it, and both numbers
 * are true.
 */
export interface Refused {
  claims: number;
  reason: string;
}

/**
 * One merge request as the node reports it, flat.
 *
 * Flat because every reader that has had to dig a value out of `fields` got it
 * wrong at least once - status read from the wrong level, an owner read from
 * body text. The node knows where these live; the page should not have to.
 *
 * `admissible` is absent, not false, when no tip was stated. Absent means "not
 * decided"; false means "decided, and no". Collapsing those two is how a page
 * ends up drawing a green light because nobody asked the question.
 */

/**
 * The last red a merge row carries, as the node sends it (api_mergequeue.go:405).
 * A declaration clears it, so it always describes the current run rather than
 * one three landings ago.
 */
export interface MergeRed {
  tip: string;
  base?: string;
  at?: string;
  note?: string;
}
export interface MergeRequest {
  id: string;
  /**
   * The row's own artifact type, which is `memory` - a merge row is a memory
   * with kind `merge`. It is sent rather than remembered so that a link to it
   * is built out of data; see artifactPath.
   */
  type: string;
  title: string;
  project?: string;
  branch: string;
  target: string;
  gated_tip: string;
  gate_run: string;
  status: string;
  /**
   * What to do first - now, next, later - or "" for a row nobody has judged.
   *
   * SENT EVEN WHEN EMPTY, so an unjudged row reads as a fact rather than as an
   * older node that does not rank at all (the wire field has no omitempty, the
   * same call priorityView makes). The queue ORDER follows the word too -
   * QueuedOrder sorts priority-first, age within a rank, so setting it moves
   * the row where the ranking says. The vocabulary is the node's:
   * store/todopriority.go.
   */
  priority: string;
  assignee?: string;
  admissible?: boolean;
  /**
   * WHY it is not admissible, as the node's own token rather than a word this
   * page invents. `merge.ungated` and `merge.stale_gate` mean nobody has
   * measured this row yet, or measured a tip that has since moved - neither is
   * a refusal, and drawing them as one told the operator that four healthy rows
   * waiting their turn had been rejected. Measured 2026-08-21: of four rows the
   * pane called refused, one had a red and three were simply unmeasured.
   */
  code?: string;
  reason?: string;
  /** True while a run is measuring this branch and has not reported yet. */
  gating: boolean;
  /**
   * The row somebody already wrote about this refusal, when there is one.
   *
   * It arrives attached to the refusal because that is the whole point: the
   * reader is looking at a no they did not expect, and this is the moment they
   * would otherwise start diagnosing it from scratch. `ref` is project/type/id -
   * the console's own route - and is absent for a row personal to its author,
   * which no route reaches.
   */
  known_issue?: KnownIssue;
  /**
   * The skip somebody already recorded against this row, or absent.
   *
   * THE CONSOLE WAS BLIND TO THIS FIELD until 01M0G82K03, and the merge queue
   * tab paid for it: with nothing to distinguish "a drainer refused this" from
   * "its turn has not come", it counted every `admissible === false` as
   * REFUSED - in red, under a card whose own text read "no gate has measured
   * it". The field has been on the wire the whole time
   * (api_mergequeue.go, `Blocked *mergeQueueBlocked`), omitempty, which is
   * exactly why a reader listing the keys of a healthy payload concluded it did
   * not exist.
   *
   * Absent means nobody has skipped this row, and that is the COMMON case on a
   * working queue - see queueBlockedOf, which returns nil for a reading that has
   * aged out and for a row somebody has since declared, so what survives here is
   * a reason nobody has disproved by taking the row.
   */
  blocked?: MergeBlocked;
  /**
   * The last verdict that said no: the tip that was measured and found bad.
   *
   * IT IS HOW A RED ROW IS RECOGNISED AT ALL, because the refusal code does not
   * say so. `applyRed` deliberately never writes gated_tip - a written tip is
   * what MergeAdmissible reads as evidence FOR landing, so a red recorded there
   * would make a broken branch landable - and a row with no gated_tip refuses
   * as `merge.ungated`, the same code as a row nobody has ever looked at.
   *
   * mergered_test.go:50-59 asserts exactly that on master, so it is the
   * repository's intent rather than an accident to be tidied away.
   *
   * Absent when the row has no red, which is the ordinary case.
   */
  red?: MergeRed;
}

/**
 * The landing lock: who has the target reserved, for which row, and until when.
 *
 * THREE STATES, NOT TWO, and the third is the one a reader is looking for when
 * they ask whether the queue is stuck.
 *
 *   held, with a holder    a gate is measuring right now. Nothing else may
 *                          land until `until`, and that is not a fault.
 *   not held, with a holder the last holder never gave it back. Release
 *                          DELETES the row (internal/store/mergelock.go
 *                          ReleaseMergeLock), so a lock still here and past
 *                          its `until` means the drainer died or overran. It
 *                          BLOCKS NOTHING - MergeLock.WouldTake treats an
 *                          expired lock as a free target - and saying so is
 *                          the difference between waiting and stuck.
 *   not held, no holder    the target is free.
 *
 * `until` and `taken_at` ARE ALWAYS ON THE WIRE, including as the zero time
 * `0001-01-01T00:00:00Z`. Their Go tags say omitempty, which does not omit a
 * time.Time - a struct is never empty to encoding/json - so a reader that
 * treats presence as meaning is reading a date two thousand years past as a
 * deadline. Key off `held` and `holder`, never off whether these are here.
 */
export interface MergeLock {
  held: boolean;
  /** The principal id. Present whenever a lock row exists, held or expired. */
  holder?: string;
  /**
   * The handle that principal answers to NOW - resolved by join at read time,
   * not snapshotted at take. Absent when the holder resolves to no user, which
   * is why nothing here may assume a name is available to print.
   */
  holder_name?: string;
  /** WHICH row the target is held for. Two sessions of one seat are two rows. */
  item?: string;
  until?: string;
  taken_at?: string;
}

/**
 * A project firecode may spawn a VM over.
 *
 * `exists` is false for a registered project whose directory has gone. The node
 * treats those as unknown when resolving a spawn, so the panel must not offer
 * them - an option that is refused on click is a dead button with extra steps.
 */
export interface VMProject {
  name: string;
  path: string;
  exists: boolean;
}

/**
 * A running VM, as `firecode ps --json` reports it and the node passes through.
 *
 * `probed` is false because `ps` deliberately does NOT ask each guest over
 * vsock - that costs a 25s timeout per VM, so ten of them would be four minutes
 * for one page refresh. So this is the host's view of what it started, and
 * last_output_s is how long ago the run last printed anything, which is the
 * closest thing to liveness available without paying for the probe.
 */
export interface VM {
  id: string;
  name: string;
  project: string;
  parent: string | null;
  backend: string;
  last_output_s: number;
  probed: boolean;
}

/** Why a row was skipped, when it was skipped, and by which seat. */
export interface MergeBlocked {
  why: string;
  at?: string;
  by?: string;
}

export interface KnownIssue {
  code: string;
  id: string;
  title: string;
  ref?: string;
}

/**
 * NoteEntry is one thing that was LEARNED about a row after it was filed: the
 * text, who wrote it and when.
 *
 * A note is not an edit and this type is where that shows up on the wire. The
 * words are somebody else's, sitting beside the author's body rather than
 * replacing it, and nothing here can be rewritten or deleted - the node has no
 * verb for either, so a note that turned out to be wrong is answered by a
 * further note saying so. See internal/store/todonote.go, which is where the
 * rule lives and is not repeated over here.
 *
 * actor_kind and actor_user are both carried because "the agent that did the
 * work measured this" and "the operator says this" are the two things a reader
 * of a note is telling apart, and the seat alone does not separate them.
 *
 * actor_name is the name the node resolves for the seat, by the same rules as
 * Artifact.author - a person's handle, an agent's person's handle or else
 * their runtime kind. Absent or "" is unnameable, and a surface falls back to
 * the id's tail rather than draw a blank.
 */
export interface NoteEntry {
  id: string;
  type: string;
  todo: string;
  note: string;
  actor: string;
  actor_kind?: string;
  actor_user?: string;
  actor_name?: string;
  seq_hlc: number;
  node: string;
  created: string;
}

/**
 * OpenspecConflict is one clash between two changes over one capability,
 * exactly as GET /api/openspec/{id}/conflicts answers it: the other change
 * and the capability the two deltas both touch.
 *
 * Lives here rather than in lib/openspec because THIS FILE CANNOT IMPORT
 * ANYTHING - scripts/api-error-check.mjs loads it as a data URL, where no
 * specifier resolves - and the openspec methods answer this type.
 */
export interface OpenspecConflict {
  change: string;
  spec: string;
}

export interface Artifact {
  id: string;
  type: string;
  kind?: string;
  project: string | null;
  owner_user: string;
  title: string;
  body: string;
  discovery: string;
  status: string;
  severity: string;
  tags: string[] | null;
  user_tags: string[] | null;
  related: string[] | null;
  visibility: string;
  file_path: string;
  hlc: number;
  node: string;
  tombstone: boolean;
  created: string;
  updated: string;
  /** fields is jsonb the node signs with the row; reports keep as_of and
   * supersedes there, announcements their scope. unknown because each type
   * owns its own shape - narrow at the use site. */
  fields?: unknown;
  /**
   * replaced_by is the newer artifact that supersedes this one. It is not
   * stored anywhere: supersedes points backwards from the replacement, and the
   * node turns that round on the way out, through the same permission filter
   * as the row itself. So it is here exactly when there is a replacement this
   * token may read - absent means either nothing replaced it or the thing that
   * did is out of reach, and the console cannot tell those apart on purpose.
   */
  replaced_by?: string;
  /**
   * replaced_by_ref is where that replacement lives - project/type/id, read off
   * the replacement's own row. It is here because replaced_by alone is not an
   * address: a replacement may sit in another project and may be another type,
   * and a console holding only the id has nothing to build a link out of but
   * the row it is already showing, which is the wrong row. Absent when the
   * replacement is personal to its author and so has no project - see refPath,
   * which is where that turns into no link rather than a broken one.
   */
  replaced_by_ref?: string;
  /**
   * authorship is what this node can say about the owner's claim to the words:
   * "authored" when a signature made with the owner's own key verified here,
   * "attributed" when it did not and the row rests on the word of whichever
   * node relayed it. See FlowyEvent.authorship - it is the same finding about
   * the other kind of row, and what an owner signs is what only an owner writes
   * (title, body, project, tags), so a party's status move does not disturb it.
   */
  authorship?: "authored" | "attributed";
  /**
   * author is the NAME the node resolves for the row's owner, by the same
   * rules chat resolves a speaker: a person's handle, an agent's person's
   * handle or else their runtime kind. It is the seat whose token wrote the
   * row - not the raiser, who is the party that asked for it - and a queue
   * read by four seats is read as names, not ids. Absent or "" is
   * unnameable, and a surface draws nothing rather than the raw id dressed
   * as a name.
   */
  author?: string;
  /**
   * disowned is present when this row's OWNER has repudiated the window it
   * falls in - see Disowned. Absent means nobody has.
   */
  disowned?: Disowned;
  /**
   * notes is what has been learned about this row since it was filed, oldest
   * first, and it arrives from a SINGLE-ROW read only. The list reads do not
   * carry it on purpose - a queue of 200 rows with every note on each is a
   * different endpoint's answer - so absent here means "this read does not
   * carry notes" as often as it means "there are none". Only a page that read
   * one row may treat an absent list as an empty one.
   */
  notes?: NoteEntry[];
}

/**
 * refPath turns a reference the node sent - project/type/id - into the route
 * that shows it, and returns undefined for anything that is not one.
 *
 * Undefined rather than a best effort, because the caller's alternative to a
 * link is showing the id as text, and that is the better answer: a link built
 * out of two guessed segments looks exactly like a good one and lands on a row
 * that is not the one it named. Anything short of three non-empty segments is
 * not a route.
 *
 * The segments are encoded one at a time and joined after, not encoded whole:
 * a project name may hold a ? or a #, which the router would otherwise read as
 * the start of a query or a hash. It may never hold a /, which is what makes
 * splitting on one unambiguous.
 */
export function refPath(ref: string | undefined): string | undefined {
  const parts = (ref ?? "").split("/");
  if (parts.length !== 3 || parts.some((part) => part === "")) return undefined;
  return `/p/${parts.map(encodeURIComponent).join("/")}`;
}

/**
 * artifactPath is the one place a link to a row is built, and every caller that
 * used to assemble one out of a template literal now asks this.
 *
 * There were thirteen of those, and they did not agree. Four hardcoded the type
 * segment - three said `memory` and one said `artifact` - for rows that were
 * bugs, notes, merge requests and reports. It went unnoticed because
 * ArtifactView fetches by id and never reads the type out of the path, so a
 * wrong segment routes correctly and only shows up as a breadcrumb that
 * contradicts the badge two lines below it. The cost of that was a debugging
 * session and three withdrawn theories about a 404 that was never about the
 * link at all: see 01M08FK999.
 *
 * A missing type is `_` rather than a guess. The segment exists so that a link
 * says what it points at without being followed, and a guess makes it say
 * something false, which is worse than saying nothing - `_` is legible as
 * unspecified. ArtifactView shows the row's OWN type once it has the row, so a
 * link with `_` in it is never what a reader ends up believing.
 *
 * An id is the one thing it will not default: without one there is no row, and
 * undefined is the caller's signal to render text rather than a link. Same
 * discipline as refPath above, and for the same reason.
 */
export function artifactPath(ref: {
  project?: string | null;
  type?: string | null;
  id?: string | null;
}): string | undefined {
  const id = (ref.id ?? "").trim();
  if (id === "") return undefined;
  const project = (ref.project ?? "").trim() || "_";
  const type = (ref.type ?? "").trim() || "_";
  return `/p/${[project, type, id].map(encodeURIComponent).join("/")}`;
}

/**
 * A repro run's status, as cmd/handoff-runner reports it.
 *
 * CONFIRMED, NOT-CONFIRMED AND ERROR ARE THREE DIFFERENT THINGS and ReproPanel
 * must never fold one into another. confirmed/not-confirmed are both verdicts
 * about the FINDING - the bug did or did not reproduce. error is a verdict
 * about the SANDBOX - the docker run broke before it could ask the question -
 * and drawing it the same way as not-confirmed is a finding silently declared
 * fixed because its own reproduction environment fell over.
 */
export type ReproStatus =
  | "queued"
  | "building"
  | "running"
  | "confirmed"
  | "not-confirmed"
  | "error";

/**
 * ReproRun is one row of a finding's repro history, as cmd/handoff-runner's Run
 * is written on the wire: GET /runs. The runner keeps every attempt rather than
 * the latest verdict, which is what lets the per-version table in ReproPanel
 * show a version going red after it was once green - see
 * internal/store/findingruns.go's own head comment for why that history is the
 * point.
 *
 * THE FIELDS ARE THE DOOR'S, not a shape this console would have chosen. It
 * carries `finding` because /runs answers with every run the process knows
 * about and the filtering is the caller's (see api.reproRuns), and its three
 * timestamps are unix seconds off that binary's own record rather than an `at`
 * string - a run has three interesting moments and which one matters depends on
 * where the run got to.
 *
 * confirmed is THREE-VALUED and must stay that way: true, false, and absent for
 * a run that has no verdict - queued, running, or ended in error. A reader that
 * flattened absent to false would be reporting a broken sandbox as a finding
 * that did not reproduce.
 */
export interface ReproRun {
  id: string;
  finding: string;
  project?: string;
  version: string;
  sha?: string;
  status: ReproStatus;
  confirmed?: boolean | null;
  note?: string;
  queued_at?: number;
  started_at?: number;
  ended_at?: number;
}

/**
 * ReproRuns is GET /runs' whole answer, and `linked` is the half that matters
 * as much as the list.
 *
 * A runner built without its run queue linked in answers every run route with a
 * refusal that names what is missing, and an empty `runs` from one of those is
 * indistinguishable from a runner nobody has asked to do anything yet. So the
 * door states which it is, and the panel draws a run button only when the
 * answer is yes - see cmd/handoff-runner/queue.go, which is where that word
 * comes from.
 */
export interface ReproRuns {
  runs: ReproRun[];
  linked: boolean;
}

/**
 * ReproQueued is POST /run's answer: what it accepted and what it turned down,
 * because a call naming several findings must not fail all of them over one,
 * nor drop the one silently. One finding queued reads as a one-entry list.
 */
export interface ReproQueued {
  queued: { run: string; finding: string }[];
  refused?: { finding: string; error: string }[];
  version: string;
}

/**
 * ReproVersion is what the runner can say about a version label - "latest", a
 * release tag, a branch, a bare sha - without running anything: GET /version.
 * buildable/source_build say whether asking for a run would need a build
 * first, so the panel can show that rather than let the reader find out from
 * a run that sits in "queued" for minutes.
 *
 * binary_ready is a BOOLEAN and not a path, deliberately, on the door's side:
 * whether a binary for this commit is already built is the caller's business,
 * and where it sits on that host is not. `runnable` is the same fact /runs
 * carries as `linked` - whether this deployment can run what it just resolved,
 * or only describe and package it.
 */
export interface ReproVersion {
  project: string;
  requested: string;
  sha: string;
  image: string;
  binary_ready: boolean;
  buildable: boolean;
  source_build: boolean;
  note: string;
  runnable: boolean;
}

/**
 * Task is one handoff: an artifact, the two people it is between, the thread
 * they talk in and where it got to. artifact_title is joined in by the node
 * through the same permission filter a direct read would use, so it is present
 * exactly when the share is live.
 */
export interface Task {
  id: string;
  artifact: string;
  from_user: string;
  to_user: string;
  project?: string;
  state: TaskState;
  assignee_agent?: string;
  thread: string;
  hlc: number;
  node: string;
  artifact_title?: string;
  artifact_type?: string;
}

export type TaskState = "open" | "delegated" | "done";

/** TaskMove is what /delegate and /state answer with: the row, and the event. */
export interface TaskMove {
  task: Task;
  event: FlowyEvent;
}

/** StatusMove is what a lifecycle transition answers with. */
export interface StatusMove {
  artifact: Artifact;
  event: FlowyEvent;
}

/**
 * History is an artifact's status trail. next is where the workflow allows it
 * to go from here - the console draws the dropdown from it rather than keeping
 * its own copy of the rules, which is how the two stay in agreement.
 */
export interface History {
  artifact: string;
  status: string;
  next: string[];
  events: FlowyEvent[];
}

export interface User {
  id: string;
  handle: string;
  display: string;
  auto_delegate: boolean;
  hlc: number;
  node: string;
}

/** The room assignment threads are opened in, on both sides of the wire. */
export const ASSIGN_ROOM = "handoffs";

/**
 * The page size the cross-project queue asks for, which is also the node's own
 * ceiling (store.maxLimit). Asking for it and knowing the number are the same
 * fact: a page that hit the cap has to say so, and it can only say so by
 * comparing what came back against what it asked for.
 */
export const TODO_PAGE = 1000;

/**
 * The artifact types that have a lifecycle to move through. finding is on
 * this list because lifecycle.go's lifecycleTypes puts it there - "a finding
 * behaves exactly like bug" - and a second, out-of-step copy of that set here
 * is exactly how StatusControl silently stops being offered on a type the
 * node still moves through open -> triaged -> ... -> done.
 */
export const LIFECYCLE_TYPES = ["bug", "feature", "note", "task", "finding"];

export interface NodeCounts {
  ok: boolean;
  node: string;
  version: string;
  db: string;
  hlc: number;
  uptime_ms: number;
  counts?: Record<string, number>;
}

/**
 * Availability is on every metric group: whether it was measured, and when it
 * was not, why.
 *
 * The console renders the reason wherever it would otherwise render a zero.
 * "0 artifacts" and "we could not read the artifacts" are different sentences,
 * and a dashboard that shows the first for the second is a dashboard that says
 * everything is fine when nothing was looked at.
 */
export interface Availability {
  available: boolean;
  reason?: string;
  measured?: string;
}

export interface MetricScope {
  user?: string;
  agent?: string;
  project?: string;
  operator: boolean;
  all: boolean;
  key: string;
}

export interface NodeGroup extends Availability {
  uptime_s?: number;
  started?: string;
  build?: string;
  db?: Availability & { up: boolean; engine: string; latency_ms: number; hlc: number };
  pool?: Availability & {
    in_use: number;
    idle: number;
    open: number;
    max_open: number;
    of: string;
  };
  cpu?: Availability & { core_share: number; of: string; window_s: number; cores: number };
  memory?: Availability & { rss_bytes: number; source: string };
  traces?: Availability & { kept: number; dropped: number; exporter?: string };
}

export interface CorpusGroup extends Availability {
  artifacts: number;
  events: number;
  by_type: Record<string, number>;
  by_scope: Record<string, number>;
  by_project: Record<string, number>;
  by_user: Record<string, number>;
  index: { artifacts: number; text_indexed: number; embedded: number } & Availability;
  storage: { tables_bytes: Record<string, number>; total_bytes: number } & Availability;
  growth: { artifacts_24h: number; artifacts_7d: number; events_24h: number; of: string };
  embedding: Availability & {
    embedded: number;
    bm25_only: number;
    denominator: number;
    of: string;
  };
}

export interface PeerMetrics {
  peer: string;
  pull_cursor: number;
  pushed_cursor: number;
  last_seen?: string;
  last_seen_age_s?: number;
  pending_push: number;
  conflicts: number;
  refused: number;
  applied: number;
}

export interface SyncGroup extends Availability {
  peers: PeerMetrics[];
  local_hwm: number;
  offline_queue: number;
  conflicts_total: number;
  pending_pull: Availability;
}

export interface CollabGroup extends Availability {
  messages_24h: number;
  messages_by_day: { day: string; count: number }[];
  tasks_by_state: Record<string, number>;
  open_todos: number;
  active_rooms_24h: number;
  active_users_24h: number;
  active_agents_24h: number;
  handoffs_in_flight: number;
  window: string;
}

export interface PermGroup extends Availability {
  grants: number;
  artifact_shares: number;
  cross_project_grants: number;
  tombstoned_grants: number;
  denied_24h: number;
  denied_by_status: Record<string, number>;
  window: string;
}

/** Anomaly is one series' verdict, with what the verdict rests on. */
export interface Anomaly {
  series: string;
  verdict: "normal" | "unusual" | "insufficient samples";
  latest: number;
  baseline?: number;
  sigma?: number;
  z?: number;
  samples: number;
  required: number;
  reason?: string;
}

export interface AnomaliesGroup extends Availability {
  min_samples: number;
  series: Anomaly[];
  unusual: number;
  insufficient: number;
  basis: string;
}

export interface Metrics {
  node: string;
  version: string;
  generated: string;
  scope: MetricScope;
  groups: {
    node?: NodeGroup;
    corpus?: CorpusGroup;
    sync?: SyncGroup;
    collaboration?: CollabGroup;
    permissions?: PermGroup;
    anomalies?: AnomaliesGroup;
  };
}

/** Span is one recorded operation, as the waterfall draws it. */
export interface Span {
  span_id: string;
  trace_id: string;
  parent_id?: string;
  name: string;
  kind: string;
  node: string;
  actor?: string;
  user?: string;
  project?: string;
  artifact?: string;
  status?: string;
  started: string;
  ended: string;
  duration_us: number;
  attrs?: Record<string, string>;
}

export interface Trace {
  trace_id: string;
  spans: Span[];
  nodes: string[];
  root?: string;
  started: string;
  ended: string;
  duration_us: number;
  errors: number;
}

export interface TraceSummary {
  trace_id: string;
  root: string;
  spans: number;
  nodes: string[];
  started: string;
  duration_us: number;
  errors: number;
}

/**
 * WorklogMeta is what a worklog entry says, as the node stamped it onto the
 * event. The timeline hands meta back verbatim, so the worklog view reads the
 * entry's own fields from there rather than parsing them back out of the body
 * the way a surface that knows nothing about the kind has to.
 *
 * Every field but what is optional in the strong sense: an entry that arrived
 * from a peer running a build older than the field has none, and a reader
 * drawing a gap for it would be inventing one.
 */
export interface WorklogMeta {
  what?: string;
  next?: string;
  as_of?: string;
  /** The branch or worktree the shift worked in, on the entries that name one. */
  branch?: string;
  refs?: string[];
  /**
   * subject is whose work the entry is about, on the entries written by one seat
   * about another's shift - a harness recording a run it drove, say. The actor is
   * still whoever wrote it. An entry with a subject is VOUCHED rather than
   * authored, and the difference has to reach the reader: an entry written by
   * something else about flowy-claude drawn as flowy-claude's own account is the
   * impersonation shape this fabric refuses everywhere, so the view says which
   * of the two it is. See isVouched.
   */
  subject?: string;
  /** The run the work was done in, and what the gate said about it. */
  run?: string;
  verify?: string;
}

/**
 * isVouched says an entry is one seat's report of another's work rather than
 * that seat's own account.
 *
 * Derived from the two ids rather than read off a flag, for the reason the node
 * derives it the same way: a subject equal to the actor is somebody's own account
 * however it got written, and two fields that can disagree about one fact is one
 * too many.
 */
export function isVouched(item: ActivityItem) {
  const subject = item.meta?.subject?.trim() ?? "";
  return subject !== "" && subject !== item.actor;
}

/**
 * ActivityItem is one line of the timeline: a turn, a log line, a message, a
 * steer, or a worklog entry. The worklog kind is read-only here - entries are
 * written with the worklog_append tool or POST /api/worklog, both of which check
 * the artifact ids they reference, and the post box below deliberately cannot
 * write one: it takes no refs and so could not check them.
 */
export interface ActivityItem {
  id: string;
  kind: "turn" | "log" | "chat" | "steer" | "worklog" | "activity";
  type: string;
  actor: string;
  actor_kind?: string;
  actor_user?: string;
  /** What the speaker was called, on the lines that carry one - see FlowyEvent. */
  actor_name?: string;
  project: string | null;
  room?: string;
  /** Who a message named, and whether the two of them were its only readers. */
  addressee?: string;
  private?: boolean;
  thread?: string;
  artifact?: string;
  parents: string[];
  body: string;
  trace?: string;
  seq_hlc: number;
  node: string;
  created: string;
  /** What the node stamped on the event. A worklog entry keeps its fields here. */
  meta?: WorklogMeta & { actor_kind?: string; actor_user?: string; actor_name?: string };
}

export interface ActivityPage {
  items: ActivityItem[];
  since: number;
  cursor: number;
  query: string;
}

/**
 * Announcement is an artifact of type 'announcement'. The scope, the resource
 * and the mode live in fields, which is a jsonb column the node signs as part
 * of the row - so what the banner reads is what the posting node wrote, even
 * when the announcement arrived through a peer.
 */
/**
 * The title of the personal note that holds this reader's closed rooms. It is
 * the KEY - the row is found by it - so it is a constant rather than a string
 * typed in two places, and it says what it is for to anybody who finds the row
 * in their own memory list.
 */
export const HIDDEN_ROOMS_TITLE = "console: rooms I have closed";

/**
 * The tag that FINDS that note. The title is for a person reading their own
 * memory list; this is the key, because GET /api/artifacts filters on tag and
 * not on title - so looking the row up is exact rather than a scan of a page
 * that the row may have fallen off.
 */
export const HIDDEN_ROOMS_TAG = "console-hidden-rooms";

/**
 * The rooms this reader has asked not to be TOLD about, which is a different
 * axis from the rooms they have taken out of their sidebar.
 *
 * 01M0GHF3JQ, the operator after saying something into a room they had closed:
 * "humans close windows to focus but dont want to miss. what would be a 'real
 * close' is *ignoring*".
 *
 *   close    not in front of me       sidebar only, delivery untouched
 *   ignore   do not tell me about it  no badge, no wake, still readable
 *   leave    I am not a member        a permission act, and the wrong
 *                                     instrument for either of the first two
 *
 * THE NODE READS THIS ONE, and that is the whole reason it is a second note
 * rather than a flag on the first. Closing is a fact about this browser's
 * sidebar and nothing else consults it. Ignoring has to stop a delivery - the
 * waiter that wakes an agent, the unread count, the mention that forces a turn
 * - and all three are decided on the node. See ignorerooms.go, which finds this
 * row by the same tag; a check asserts the two spellings agree.
 */
export const IGNORED_ROOMS_TITLE = "console: rooms I have ignored";
export const IGNORED_ROOMS_TAG = "console-ignored-rooms";

export interface AnnouncementFields {
  scope: "node" | "project" | "federation";
  resource?: string;
  mode?: "drain" | "pause" | "ack-required";
  resolved_at?: string;
}

export interface Announcement extends Artifact {
  fields?: AnnouncementFields;
  /**
   * Whether THIS reader may resolve it, decided by the node.
   *
   * Not worked out here, because the rule has two limbs and one of them is "is
   * this token the operator of this node", which nothing in a browser knows.
   * A resolve button that appears and then answers 403 is worse than no button
   * - and no button is what this surface had: the only control was ack, and it
   * renders solely for an announcement that names a resource, so a plain
   * warning sat on every page with no affordance at all.
   *
   * Optional because a node built before this key answers without it, and the
   * honest reading of "the node did not say" is not "you may".
   */
  may_resolve?: boolean;
}

/** Quiesce is what an announcement is still waiting for before it may resolve. */
export interface Quiesce {
  announcement: string;
  resource: string;
  mode: string;
  holders: string[];
  acked: string[];
  pending: string[];
  state: "held" | "released";
}

/** ApiError carries the status, because 401 and 404 mean different things to the UI. */
export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

export function getToken(): string {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? "";
  } catch {
    return "";
  }
}

export function setToken(token: string) {
  try {
    if (token) {
      localStorage.setItem(TOKEN_KEY, token);
    } else {
      localStorage.removeItem(TOKEN_KEY);
    }
  } catch {
    // A browser with storage switched off still gets a working console for the
    // length of the page: the token lives in memory either way.
  }
  memoryToken = token;
}

let memoryToken = "";

// Exported for lib/stream, which cannot go through `request` above: an SSE
// connection is a long-lived fetch the browser reconnects by itself, so it
// needs the header rather than the wrapper around it. One place decides what a
// flowy request carries, here, whichever door opens it.
export function authHeader(): HeadersInit {
  const token = getToken() || memoryToken;
  return token ? { Authorization: `Bearer ${token}` } : {};
}

/** statusText is what an error says when the body said nothing usable. */
function statusText(response: Response): string {
  return `${response.status} ${response.statusText}`.trim();
}

/**
 * parseBody reads the body as JSON, and turns a body that is not JSON into an
 * ApiError carrying the status.
 *
 * Not JSON at all is a proxy's HTML error page, or a plain-text 502 from
 * something standing in front of the node. Parsing it throws a SyntaxError that
 * says where the '<' was and nothing about what happened, and that is what the
 * console showed instead of the status. What is kept of the body is the first
 * of it - enough to recognise whatever sent it, short enough for a toast.
 */
function parseBody(text: string, response: Response) {
  if (!text) {
    return {};
  }
  try {
    return JSON.parse(text);
  } catch {
    throw new ApiError(response.status, text.trim().slice(0, 200) || statusText(response));
  }
}

// Exported so a module that owns its own corner of the API (see lib/diagrams)
// can reuse this door rather than build a second one. Everything that makes a
// request safe lives here - the token header, the body parse, the ApiError -
// and a second fetch wrapper is a second place for any of it to be missing.
export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    ...init,
    // SAME-ORIGIN, SAID OUT LOUD. It is the default today and it is the whole
    // of how a logged-in person authenticates - the session cookie is httpOnly,
    // so this is the only way it reaches the node, and a default that changed
    // under us would log everybody out with no line of code to blame.
    credentials: "same-origin",
    headers: { ...authHeader(), ...(init.headers ?? {}) },
  });
  const text = await response.text();
  const body = parseBody(text, response);
  if (!response.ok) {
    // THE ONE PLACE EVERY READ OF THIS NODE GOES THROUGH, which is why the
    // credential's state is noticed here rather than at a hundred call sites: a
    // credential that stops working stops working for all of them at once.
    //
    // THIS FUNCTION AND NOT reproRequest. That one talks to a different host
    // with a different credential, and a 401 from the repro runner says nothing
    // about whether you are signed in here - a banner over it would send
    // somebody to sign in again to fix somebody else's service.
    //
    // 401 only. See the note at credentialDead for why a 403 must not raise it.
    if (response.status === 401) publishCredential(true);
    throw new ApiError(response.status, body?.error ?? statusText(response));
  }
  publishCredential(false);
  return body as T;
}

const REPRO_BASE_KEY = "flowy.reproBase";

/**
 * The repro runner is not flowy. cmd/handoff-runner is a separate binary on a
 * separate trusted host with its own Docker access, so its base URL cannot be
 * "" (relative to this origin) the way every other call above is - a relative
 * /run would land on the flowy node, which does not serve it, and a 404 read
 * as "not confirmed" is exactly the confirmed/not-confirmed/error mixup this
 * panel exists to prevent.
 *
 * Kept as a RUNTIME setting in localStorage, the same way the token above is,
 * rather than a Vite build-time env var. web/dist is embedded into the flowy
 * binary with go:embed, so a build-time value would mean one flowy binary per
 * runner host - every deployment that wants repro runs baking its own
 * console. A runtime setting lets one build of the console be pointed at
 * whichever runner a given flowy deployment trusts, changed without a
 * rebuild, and left unset (see ReproPanel) on every deployment that has none.
 */
export function getReproBase(): string {
  try {
    return (localStorage.getItem(REPRO_BASE_KEY) ?? "").trim();
  } catch {
    return "";
  }
}

export function setReproBase(base: string) {
  const trimmed = base.trim().replace(/\/+$/, "");
  try {
    if (trimmed) {
      localStorage.setItem(REPRO_BASE_KEY, trimmed);
    } else {
      localStorage.removeItem(REPRO_BASE_KEY);
    }
  } catch {
    // As with the token: a browser with storage switched off still runs for
    // the length of the tab, just without the setting surviving a reload.
  }
  memoryReproBase = trimmed;
}

let memoryReproBase = "";

/**
 * Thrown by every repro call when no runner base is configured, so ReproPanel
 * can say so plainly rather than let a call fall through to a relative fetch
 * against flowy itself - see getReproBase above for why that would be worse
 * than doing nothing.
 */
export class ReproUnconfigured extends Error {
  constructor() {
    super("no repro runner configured");
    this.name = "ReproUnconfigured";
  }
}

/**
 * The base every repro call is made against.
 *
 * Empty string means "this origin", which is where the node's own /api/repro/*
 * lives - so with nothing configured anywhere, the panel talks to the node and
 * the node talks to the runner. That is the default, and it is why nobody has
 * to type an address.
 *
 * An explicit override still wins, and still goes straight to that runner. It
 * no longer throws when there is none: a deployment without a runner now gets
 * the node's 503 and its sentence, which says more than this file could.
 */
function reproBase(): string {
  const override = getReproBase() || memoryReproBase;
  return override || "/api/repro";
}

/**
 * reproRequest is `request` for the runner's door: an absolute base that must
 * be configured or nothing is sent, and THE READER'S OWN TOKEN on every call.
 *
 * An earlier version of this sent no Authorization at all, reasoning that the
 * runner is a different service and so a different audience for any token.
 * cmd/handoff-runner/http.go's authed() says the opposite in its own head
 * comment - "THE TOKEN IS THE CALLER'S OWN" - and resolves the bearer against
 * the SAME Postgres this node writes to, precisely so that a run is recorded
 * against the person or agent who asked for it rather than against a daemon.
 * Every route there but /healthz is behind it, so the panel's every call was a
 * 401: run, runs, log, package and version alike.
 *
 * Nothing in this repository could see that. `go test` has one process, the
 * types agree either way so `vite build` is happy, and repro-contract-check
 * stands fetch in so no header is ever inspected. It took a browser and a
 * second origin - see web/scripts/run-journey-check.mjs.
 *
 * The token is the same one authHeader sends to the node, and deliberately so:
 * two services, one principal, one credential. If the runner is ever moved
 * behind a boundary that must not see this token, the answer is a token minted
 * for that audience - not a call with none.
 */
async function reproRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`${reproBase()}${path}`, {
    ...init,
    headers: { ...authHeader(), ...(init.headers ?? {}) },
  });
  const text = await response.text();
  const body = parseBody(text, response);
  if (!response.ok) {
    throw new ApiError(response.status, body?.error ?? statusText(response));
  }
  return body as T;
}

/** reproText is reproRequest for the one endpoint that answers in plain text
 * rather than JSON - the run log. */
async function reproText(path: string): Promise<string> {
  const response = await fetch(`${reproBase()}${path}`, { headers: authHeader() });
  const text = await response.text();
  if (!response.ok) {
    throw new ApiError(response.status, text.trim().slice(0, 200) || statusText(response));
  }
  return text;
}

/** A room as the node knows it, which is the only place that knows. */
export type Room = {
  project: string;
  name: string;
  topic?: string;
  created_by?: string;
  created: string;
  members: number;
  /** The caller's role here, empty when they are not a member. */
  role?: string;
  /**
   * False for a room that exists only because somebody spoke in it. It has no
   * owner, so nobody can be invited to it until it is created - the invite door
   * refuses and says so rather than behaving like a real room until somebody
   * tries.
   */
  declared: boolean;
};

/**
 * How many todos of a room the pane asks for.
 *
 * THE DOOR'S DEFAULT IS 200 AND #general HAS 324. Measured 2026-08-21:
 * `?kind=todo&room=general` returns 200, the same query with `limit=1000`
 * returns 324. So the pane held a WINDOW, and every count and filter drawn from
 * it described the window while reading as the room. A search box over that says
 * "nothing matches" about rows that exist - which is the exact failure the
 * search was added to prevent, at a different boundary. Found by @orchestrator
 * reviewing the search.
 *
 * THIS IS THE DOOR'S CEILING, NOT A GUESS. store/artifacts.go: defaultLimit
 * is 200 and maxLimit is 1000, and clampLimit silently reduces anything larger.
 * So 1000 is the most this door will ever hand back, and "raise the limit" is
 * not a fix available to a later reader - the parameter would be accepted and
 * ignored, which is the worst of the three outcomes.
 *
 * WHICH IS WHY THE TRUNCATION NOTICE IS THE LOAD-BEARING HALF. A room with
 * more than 1000 todos cannot be fully read through this door at all, and the
 * pane then says what it searched instead of denying the rest exist. That
 * makes this number a performance choice rather than a correctness one, which
 * is the only kind of number it is safe to pick.
 */
export const ROOM_TODO_LIMIT = 1000;

export const api = {
  /**
   * The rooms this node has, rather than the three this file used to name.
   *
   * ROOMS in lib/unread.tsx was a literal array, so a room nobody had typed
   * into it could not appear in the sidebar however much traffic it carried,
   * and a room created through the API was invisible until somebody edited the
   * console. A client with a hardcoded list is always eventually wrong; one
   * that asks cannot be.
   */
  rooms: (project?: string) =>
    request<{ rooms: Room[]; project: string }>(
      project ? `/api/rooms?project=${encodeURIComponent(project)}` : "/api/rooms",
    ),

  /**
   * Make a room, and be its owner.
   *
   * POST /api/rooms has existed since rooms became objects and nothing in this
   * console called it - so the operator could read rooms and not make one, and
   * read that as a missing feature rather than a missing button. Measured
   * 2026-08-19: of the four room doors, this console called exactly one.
   *
   * A taken name answers 409 rather than 400, because "that name is in use" and
   * "that is not a name" send a person to different places - see api_rooms.go.
   */
  createRoom: (name: string, topic?: string) =>
    request<{ room: Room }>("/api/rooms", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, ...(topic ? { topic } : {}) }),
    }),

  /**
   * Leave a room, and only yourself.
   *
   * `left` is false when you were not a member, and that is not an error: the
   * caller wanted to not be in the room and they are not in the room. The
   * console says which happened rather than reporting both as success.
   */
  leaveRoom: (room: string) =>
    request<{ left: boolean; room: string }>(`/api/rooms/${encodeURIComponent(room)}/leave`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    }),

  /** Add somebody to a room you own. The node refuses with the reason. */
  inviteToRoom: (room: string, principal: string) =>
    request<{ invited: string; room: string }>(`/api/rooms/${encodeURIComponent(room)}/invite`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ principal }),
    }),

  whoami: () => request<Whoami>("/api/whoami"),

  /**
   * WHO THIS BROWSER IS, WITH A NAME ON IT.
   *
   * whoami answers ids - user, agent, project - which is the honest answer for
   * a permission question and useless for a panel that has to show somebody
   * their own handle. An empty box cannot tell "no handle is set" from "not
   * loaded yet", so the panel renders a value or says it has none, and this is
   * where the value comes from.
   *
   * has_password and by_bearer are not decoration either. A browser holding a
   * bearer token may set a first password without proving an old one; a browser
   * on a cookie must send `current`. The panel asks for that field or does not
   * ask, and it decides from these two rather than from a guess about how the
   * reader got here.
   */
  me: () => request<Me>("/api/me"),

  /**
   * And changing it. `current` - not current_password - is the old password,
   * required when a cookie session changes one and ignored when there is none
   * to prove.
   *
   * sessions_ended comes back because a password change signs other browsers
   * out, and a panel that did not say so would log somebody out of the page
   * they were standing on with no warning.
   */
  updateMe: (body: { handle?: string; password?: string; current?: string }) =>
    request<{ user: MeUser; sessions_ended: number }>("/api/me", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }),

  /**
   * What this node is, and which console it serves. `bundle` is the hashed
   * asset its index.html loads, which is how a tab open across a deploy finds
   * out it is running code that has since been replaced.
   */
  node: () => request<NodeInfo>("/api/node"),

  /**
   * projects is the registry, narrowed to what this token may be shown - and
   * `reads` beside it is the narrower thing: the ones whose rows this token can
   * actually reach. See store.ReadableProjects for why they are not the same
   * list, and the todos page for what goes wrong if a scope line uses the wider
   * one.
   */
  projects: () => request<ProjectsPage>("/api/projects"),

  /** room reads a room from a cursor, exclusive. */
  room: (room: string, since = 0) =>
    request<ChatPage>(`/api/chat/${encodeURIComponent(room)}?since=${since}`),

  /**
   * roomWindow is the room read from its NEW end: the newest `limit` messages,
   * or the `limit` before a reading somebody has already got.
   *
   * It is what a room opens on. `room` above pages FORWARDS from a cursor, so
   * opening on it means taking the oldest page and dragging everything ever
   * said in the room in behind the long poll - reported by the operator as "on
   * reload the whole chat history loads". This takes the last screenful
   * instead, and `before` walks back from there as the reader scrolls up.
   *
   * The page comes back in log order, oldest first, like every other read - so
   * a view prepends it and never sorts. `before` in the answer is the reading
   * to ask for next, strictly exclusive and safe to hand back whole: the node
   * completes the reading its window cut at, so paging back neither repeats a
   * message nor skips one. Nothing older is left when a page comes back short
   * of its limit.
   */
  roomWindow: (room: string, limit: number, before = 0) =>
    request<ChatPage>(
      `/api/chat/${encodeURIComponent(room)}?order=recent&limit=${limit}${
        before > 0 ? `&before=${before}` : ""
      }`,
    ),

  /**
   * roomThread is ONE thread of a room, from the log rather than from whatever
   * page of the room a view happens to be holding.
   *
   * It exists because the pane that draws a thread was filtering the window:
   * a thread whose first message is older than the sixty on screen was drawn
   * from its middle, and the reader was shown a conversation with its opening
   * missing and nothing saying so. Measured by thread-count-check, which opens
   * a thread whose root is sixty-one messages back and looks for it.
   *
   * The same door the room read uses, with `thread` on it, so the filter, the
   * message type and the citations are the room's and not a second idea of
   * them.
   */
  roomThread: (room: string, thread: string, limit = 200) =>
    request<ChatPage>(
      `/api/chat/${encodeURIComponent(room)}?thread=${encodeURIComponent(thread)}&limit=${limit}`,
    ),

  /**
   * wait is the watcher: it blocks on the server for up to ~25s and returns
   * whatever landed, or nothing. The signal is what cancels it when the view
   * goes away, so a room the user has left stops holding a request open.
   */
  wait: (room: string, cursor: number, signal?: AbortSignal, thread?: string) =>
    request<ChatPage>(
      `/api/chat/${encodeURIComponent(room)}/wait?cursor=${cursor}${
        thread ? `&thread=${encodeURIComponent(thread)}` : ""
      }`,
      { signal },
    ),

  /**
   * cite is what the message is about: a message id, and the byte span into
   * its body when it quotes one part of it. The node checks both - that the
   * source is readable and that the span is inside it - and stamps the pointer
   * on the row. It never takes the quoted words from here, which is why there
   * is nowhere to put them.
   */
  say: (
    room: string,
    body: string,
    parents: string[] = [],
    thread?: string,
    to?: string,
    cite?: { message: string; start?: number; end?: number },
    attachments: string[] = [],
  ) =>
    request<FlowyEvent>(`/api/chat/${encodeURIComponent(room)}/say`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        body,
        parents,
        ...(thread ? { thread } : {}),
        ...(to ? { to } : {}),
        ...(cite ? { cite } : {}),
        ...(attachments.length > 0 ? { attachments } : {}),
      }),
    }),

  /**
   * MAX_ATTACHMENT is the node's ceiling, mirrored here so the refusal arrives
   * before the bytes do. 4 MiB, from maxAttachment in mcp_attachments.go.
   *
   * Mirrored rather than asked for, and that is a real cost: two numbers that
   * must agree. It buys the one thing a person actually notices - a phone photo
   * refused instantly instead of after uploading eight megabytes of it - and the
   * node still refuses on its own, with its own sentence, so the copy here is an
   * optimisation and never the rule.
   */
  MAX_ATTACHMENT: 4 << 20,

  /**
   * writeAttachment puts bytes in the project and answers with the artifact.
   *
   * The bytes go up base64 because that is what the node stores and hands back
   * byte for byte; a multipart form would be smaller on the wire and would have
   * to be undone at both ends.
   *
   * content_type is what the CALLER believes, and the node records it as a
   * claim and decides the real type from the bytes. So nothing here needs to be
   * trusted, and the render path reads the node's answer rather than this.
   */
  writeAttachment: (a: {
    content_base64: string;
    title?: string;
    filename?: string;
    content_type?: string;
    room?: string;
    body?: string;
  }) =>
    request<{
      item: Artifact;
      size_bytes: number;
      digest_sha256: string;
      content_type: string;
    }>("/api/attachment", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(a),
    }),

  /**
   * dms is the private log: every direct message this token is a party to,
   * whoever the other party is. There is no room in the path because a direct
   * message is not in one - that is part of what makes it private.
   */
  dms: (since = 0) => request<ChatPage>(`/api/dm?since=${since}`),

  /** dmWait is wait over the private log, with the same finite window. */
  dmWait: (cursor: number, signal?: AbortSignal) =>
    request<ChatPage>(`/api/dm/wait?cursor=${cursor}`, { signal }),

  /**
   * sendDm sends a message only the sender and `to` can read. The addressee is
   * the path and not an optional field: a private message with nobody to send
   * it to is the one mistake here that would publish something quietly.
   */
  sendDm: (to: string, body: string, parents: string[] = [], thread?: string) =>
    request<FlowyEvent>(`/api/dm/${encodeURIComponent(to)}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ body, parents, ...(thread ? { thread } : {}) }),
    }),

  /**
   * inbox is everything you may read and did not write.
   *
   * WHICH END OF THE LOG IS THE WHOLE QUESTION. With no order the door answers
   * the OLDEST page - right for a cursor-follower asking "the next page after
   * my mark", and wrong for "what happened while I was away". Measured on the
   * live node 2026-08-20: 200 events, the newest of them four days old, on a
   * node that had taken thousands since. The overview card had been showing a
   * fixed window from the 16th to everybody who opened it.
   *
   * recent takes the other end and hands them back NEWEST FIRST, which is the
   * order a glance wants. A caller that follows a cursor keeps the default.
   */
  inbox: ({ since = 0, recent = false, limit = 0 } = {}) =>
    request<ChatPage>(
      `/api/inbox?${recent ? "order=recent" : `since=${since}`}${limit > 0 ? `&limit=${limit}` : ""}`,
    ),

  /**
   * Where this token's readers have got to. The console holds one per room and
   * reads them back on every refresh rather than remembering them in the tab:
   * the mark is the node's, and another tab - or the same person's other
   * browser - moves it.
   */
  /**
   * inboxWait blocks on the node until something is said to this reader, or the
   * window runs out, and answers what landed.
   *
   * IT IS NOT SHAPED LIKE chatWait AND THE DIFFERENCE MATTERS. The room's
   * waiter takes a cursor the client holds; this one takes a NAME and reads
   * from a durable reader row the node keeps. The two words are the same and
   * the mechanisms are not: a name is a cursor with one owner, so a second
   * consumer of one name is a second writer of one mark.
   *
   * WHICH IS WHY THE CONSOLE USES ITS OWN LABEL and never an agent's. A browser
   * waiting under `flowy-claude` would be handed the same page as the agent's
   * own waiter and could ack past messages that seat had not finished with -
   * a silent drop with nothing recording it.
   *
   * The mark does NOT move on delivery. inbox.go:274: "A waiter that is handed
   * messages and dies before it has written them out has lost them permanently
   * if the server counted the handover as delivery." So a caller that does not
   * ack simply sees the same page again, which is the safe direction.
   */
  inboxWait: (as: string, window = 25, signal?: AbortSignal) =>
    request<{ reader: string; events: FlowyEvent[]; skipped: number; cursor: number }>(
      `/api/inbox/wait?as=${encodeURIComponent(as)}&kind=cursor&window=${window}`,
      { signal },
    ),

  inboxReaders: () => request<{ readers: InboxReader[] }>("/api/inbox/readers"),

  /**
   * How much one reader has not read in one room. THE NODE COUNTS IT, and that
   * is the point of the call: counting here would mean handing the reader's
   * mark back as a cursor, and a mark is a `seq_hlc` - 57 bits, held here as a
   * double, and therefore up to eight readings out. Measured: a console that
   * asked with the mark it had just been handed was answered with five
   * messages it had already read.
   */
  /**
   * unreadDirect is the same count over DIRECT MESSAGES, which have no room.
   *
   * A separate call rather than unreadIn(as, "") because an empty room already
   * means "everywhere" at that door - the badge beside a room name asks with
   * one, the total asks with none - so spelling direct as an absent room would
   * take the everywhere answer away from whoever asks for it. The node refuses
   * direct and room together rather than resolving them.
   */
  unreadDirect: (as: string) =>
    request<{ reader: string; direct: boolean; cursor: number; unread: number }>(
      `/api/inbox/unread?as=${encodeURIComponent(as)}&direct=1`,
    ),

  unreadIn: (as: string, room: string) =>
    request<{ reader: string; room: string; unread: number }>(
      `/api/inbox/unread?as=${encodeURIComponent(as)}&room=${encodeURIComponent(room)}`,
    ),

  /**
   * Declare a reader, at the head of what this token can already read. It is
   * idempotent - an existing label comes back where it stands - so the console
   * can call it for a room it has not read before without checking first.
   */
  declareInboxReader: (as: string, kind?: string) =>
    request<InboxReader>("/api/inbox/reader", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ as, kind }),
    }),

  /**
   * Move a reader's mark to the message it has read through. The node only
   * ever moves a mark forward, so two tabs acking different positions cannot
   * fight.
   *
   * THE MESSAGE AND NOT ITS READING, and that is not a preference. `seq_hlc`
   * is a 57-bit number and every number here is a double, so a reading this
   * console was handed comes back off `JSON.parse` up to eight readings away
   * from the one the node holds - measured, as a mark that landed two readings
   * short of the message the reader had just read and left it unread for good.
   * The id is a string and survives the trip; the node resolves it, through the
   * same read filter as any other id that arrives from outside.
   */
  ackInbox: (as: string, event: string) =>
    request<InboxReader>("/api/inbox/ack", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ as, event, delivered: true }),
    }),

  /**
   * Drop this token's own reader row. A console reader is a bookmark a tab
   * keeps, not a place in the log a process holds, and a tab that closed used
   * to leave its rows behind forever - polling never, kind unknown, the ghost
   * half of a roster that only ever grew. keepalive is for the one call that
   * matters on pagehide: the tab is going away as it is made, and the request
   * has to outlive it by a moment.
   */
  deleteInboxReader: (as: string, keepalive = false) =>
    request<{ deleted: string }>(`/api/inbox/reader/${encodeURIComponent(as)}`, {
      method: "DELETE",
      keepalive,
    }),

  reports: () => request<{ artifacts: Artifact[] }>("/api/artifacts?type=report"),
  attachment: (id: string) =>
    request<{ item: Artifact; content: string | null; bytes?: string }>(
      `/api/attachment/${encodeURIComponent(id)}`,
    ),
  /**
   * The node's ranked search, narrowed to reports.
   *
   * It is the node's and not the page's: the match covers the title, the body,
   * the discovery and the tags, and the list only ever renders titles. A filter
   * over what is already on screen would answer a different question and would
   * miss every report whose subject is in its text, which is most of them.
   */
  searchReports: (q: string) =>
    request<{ query: string; artifacts: Artifact[] }>(
      `/api/search?type=report&q=${encodeURIComponent(q)}`,
    ),

  /** findings/searchFindings are reports()/searchReports() over a different
   * type - the same permission-filtered door, so a findings list is
   * permission-filtered by construction rather than by anything this page
   * does. See Findings.tsx for the status/kind/severity/project/tag filters,
   * which narrow the artifacts these two already returned rather than
   * widening what either endpoint is asked for - ArtifactQuery has no
   * severity or tag column to ask it with. */
  findings: () => request<{ artifacts: Artifact[] }>("/api/artifacts?type=finding"),
  searchFindings: (q: string) =>
    request<{ query: string; artifacts: Artifact[] }>(
      `/api/search?type=finding&q=${encodeURIComponent(q)}`,
    ),

  presence: () => request<Presence>("/api/presence"),
  /** Todos are memory artifacts of kind todo - the same store, filtered by
   * kind rather than by a type of their own, because a todo is a memory
   * item with work still in it. */
  /**
   * Every todo this token can read, in every project it can read one in.
   *
   * It is the SAME endpoint the room panel uses with one narrowing dropped -
   * there is no project on the query, so the answer is whatever the permission
   * filter reaches. That filter is the only reason this is safe to widen: a
   * cross-project read through a door of its own would be a second place for it
   * to be missing, which is the shape of the finding this project already has
   * open. Widening what one query returns adds no door.
   *
   * The limit is the node's cap rather than the default 200, because a queue
   * across every project a fleet works in is exactly the list that quietly
   * stopped at 200 and read as "that was all of them". TODO_PAGE is exported
   * because the page has to know the number it asked for in order to say it
   * stopped there, and two copies of it would drift into a list that hits the
   * cap silently - which is the failure the number exists to report.
   */
  /**
   * WRITE ONE ROW, from the one place a person can make one.
   *
   * The type is the RESOLVED type - what the row is - and this is the door that
   * decides how that is spelled: the memory bucket plus a kind, which is what
   * 194 of 195 todos, 50 of 52 notes and 9 of 25 reports on this node are
   * already written as. The other spelling exists on five stray rows that
   * nothing refused, and the ruling behind this door (01M0ANFYWY) is that a
   * create surface must not offer both - the day somebody picks the second one
   * out of a dropdown, "todo as a type" stops being five strays and becomes
   * something this project supports.
   *
   * scope=project, because a row nobody else can read is not what somebody
   * making one from the console means by it, and the store's own default here
   * is personal.
   */
  /**
   * A PERSON LOGS IN. The node answers a cookie - httpOnly, so nothing here can
   * read it and there is nothing to keep. "Am I logged in" is whoami answering
   * 200, never a value this console stored: a page holding its own idea of
   * signed-in disagrees with the node the first time a session ends, and the
   * disagreement is invisible until somebody tries to write.
   *
   * The refusal is the node's sentence, not one composed here. It says one
   * thing for a wrong handle and a wrong password on purpose - which of the two
   * was wrong is an oracle for which accounts exist - and this console must not
   * improve on it.
   */
  /**
   * WHERE A ROW CAME FROM. Not what blocks it - see internal/store/origin.go,
   * where the two are deliberately different verbs because an edge the ready
   * query never reads must not share a name with one it does.
   *
   * Each origin carries its id always, and a ref and title only when this
   * token can already read that row. The id on its own is a real answer: it
   * says this came out of something you cannot see, which is the honest half
   * and the reason the console must render an unresolved origin rather than
   * drop it.
   */
  origins: (id: string) =>
    request<{ artifact: string; origins: OriginRef[]; log: unknown[] }>(
      `/api/artifact/${encodeURIComponent(id)}/origins`,
    ),

  login: (handle: string, password: string) =>
    request<{ user: string; handle: string }>("/api/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ handle, password }),
    }),

  /** And out. Always 200, cookie cleared, whether or not one was sent. */
  logout: () => request<{ ok: boolean }>("/api/logout", { method: "POST" }),

  /**
   * Put this session into one of the projects this person belongs to.
   *
   * A SESSION ACT, NOT A CREDENTIAL ONE: nothing about who you are changes,
   * only where your writes land - which is why it needs no re-auth and why a
   * bearer token cannot call it. The node answers where you are now writing
   * rather than "ok", and this returns that answer so the caller says it out
   * loud rather than assuming the click worked.
   */
  enterProject: (project: string) =>
    request<{ project: string; writing_in: string }>(
      `/api/projects/${encodeURIComponent(project)}/enter`,
      { method: "POST" },
    ),

  /**
   * The rooms this reader has closed, as a personal note on the node.
   *
   * PER PRINCIPAL AND NOT PER BROWSER. localStorage would have been smaller and
   * it is the wrong shape: the operator runs more than one machine, and a room
   * closed on one of them coming back on the next is the same "did that work?"
   * that leaving a room already produced. lib/unread.tsx made the same call for
   * the same reason - "THE NODE HOLDS IT, not localStorage".
   *
   * A NOTE RATHER THAN A NEW DOOR. visibility personal is a store rule, not a
   * convention: nobody else can read it and nobody else can be confused by it.
   * The title is the key - one row per principal, found by title, updated in
   * place - so this costs no schema and no endpoint.
   */
  hiddenRooms: async (): Promise<{ id: string; rooms: string[]; read: boolean }> => {
    // FOUND BY TAG, NOT BY SCANNING A PAGE FOR A TITLE. The first cut read
    // `?type=memory&kind=note&limit=200` and looked for the title inside that
    // page, which breaks in two silent ways past 200 personal notes: the row is
    // not in the page, "not found" reads as "you have closed nothing", and the
    // next close CREATES A SECOND ROW because the id is empty. Once there are
    // two, `.find` takes whichever the page happens to order first and the
    // preference flaps. Measured by @claude-host: 54 notes for one seat in one
    // day, four agents, against a limit of 200.
    //
    // The door has no title filter but it has a tag one, and a tag is exact -
    // so this is a lookup rather than a search, and absence means absence.
    const page = await request<{ artifacts?: Artifact[] }>(
      `/api/artifacts?type=memory&kind=note&tag=${encodeURIComponent(HIDDEN_ROOMS_TAG)}`,
    );
    const row = (page.artifacts ?? [])[0];
    if (!row) return { id: "", rooms: [], read: true };
    // A body that will not parse is not a reason to lose the sidebar: an
    // unreadable preference reads as "nothing hidden", which is the state that
    // shows MORE rather than less.
    try {
      const rooms = JSON.parse(row.body || "[]");
      return {
        id: row.id,
        rooms: Array.isArray(rooms) ? rooms.filter((r) => typeof r === "string") : [],
        read: true,
      };
    } catch {
      return { id: row.id, rooms: [], read: true };
    }
  },

  /**
   * The ignored list, found by tag exactly as the closed one is - and for the
   * same reason: a title has to be searched for inside a page, and past a
   * couple of hundred personal notes the row falls off the end, where "not
   * found" reads as "you have ignored nothing" and the next write files a
   * SECOND row.
   */
  ignoredRooms: async (): Promise<{ id: string; rooms: string[] }> => {
    const page = await request<{ artifacts?: Artifact[] }>(
      `/api/artifacts?type=memory&kind=note&tag=${encodeURIComponent(IGNORED_ROOMS_TAG)}`,
    );
    const row = (page.artifacts ?? [])[0];
    if (!row) return { id: "", rooms: [] };
    try {
      const rooms = JSON.parse(row.body || "[]");
      return {
        id: row.id,
        rooms: Array.isArray(rooms) ? rooms.filter((r) => typeof r === "string") : [],
      };
    } catch {
      // An unreadable preference reads as "nothing ignored", which delivers
      // MORE than was asked for. The opposite default would silence a room on
      // the strength of a corrupt note, and a reader cannot tell that from a
      // room where nobody is talking.
      return { id: row.id, rooms: [] };
    }
  },

  /** Write the ignored list back, creating the note the first time. */
  setIgnoredRooms: (id: string, rooms: string[]) =>
    request<Artifact>("/api/artifacts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        ...(id ? { id } : {}),
        type: "memory",
        kind: "note",
        title: IGNORED_ROOMS_TITLE,
        body: JSON.stringify(rooms),
        visibility: "personal",
        tags: [IGNORED_ROOMS_TAG],
      }),
    }),

  /** Write the closed list back, creating the note the first time. */
  setHiddenRooms: (id: string, rooms: string[]) =>
    request<Artifact>("/api/artifacts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        ...(id ? { id } : {}),
        type: "memory",
        kind: "note",
        title: HIDDEN_ROOMS_TITLE,
        body: JSON.stringify(rooms),
        visibility: "personal",
        // The tag is how the row is found again. Without it the lookup is a
        // scan of every note this principal has ever written.
        tags: [HIDDEN_ROOMS_TAG],
      }),
    }),

  writeEntity: (opts: { type: string; title: string; body?: string }) =>
    request<Artifact>("/api/artifacts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        type: "memory",
        kind: opts.type,
        title: opts.title,
        body: opts.body ?? "",
        visibility: "project",
      }),
    }),

  /**
   * FIX YOUR OWN WORDS. An item's title and body are its author's - the store
   * refuses a stranger rewriting them, and says so in one sentence - while its
   * queue metadata moves for anybody who can read it. Two rules, and this is
   * the door for the first one.
   *
   * POST /api/artifacts with an id is the update branch of an upsert, which is
   * the same door the diagram editor saves through: there is no second write
   * path to keep in step, and a save is not a different verb from a create.
   */
  editWords: (opts: { id: string; type: string; kind?: string; title: string; body: string }) =>
    request<Artifact>("/api/artifacts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      // The type rides along because this door is an UPSERT and an upsert has
      // to know what it would create: the node refuses a write with no type
      // rather than guessing one, which is the same refusal a create gets.
      // Taken off the row being edited, so an edit cannot quietly change what
      // a row IS while claiming to fix its words.
      body: JSON.stringify({
        id: opts.id,
        type: opts.type,
        ...(opts.kind ? { kind: opts.kind } : {}),
        title: opts.title,
        body: opts.body,
      }),
    }),

  todos: () =>
    request<{ artifacts: Artifact[]; withheld?: Withheld; refused?: Refused }>(
      `/api/artifacts?type=memory&kind=todo&limit=${TODO_PAGE}`,
    ),
  /**
   * The merge queue, with a verdict per request.
   *
   * The verdicts come from the node, not from here. Whether a branch may land
   * is one rule - it compares the tip the gate measured against the tip the
   * merge would land on - and a second implementation of it in TypeScript would
   * be a second answer, disagreeing with the first on the day it matters.
   *
   * A browser has no git, so it cannot know where master is. The node answers
   * with `tip_from`: "stated" when a caller passed one, "deployed" when it fell
   * back to the commit the node was built from, "none" when it has neither.
   * `decided` says outright whether there are verdicts at all, so a page can
   * never read a missing verdict as a yes.
   */
  /**
   * The notes: memory items that are not work. The queue tabs show todos and
   * merges; nothing showed the rest, so a note could be written, searched by an
   * agent, and never seen by the person who asked for it - which is how the
   * operator ended up asking the room where a note had been filed.
   */
  notes: () =>
    request<{ artifacts: Artifact[] }>(`/api/artifacts?type=memory&kind=note&limit=${TODO_PAGE}`),
  /**
   * The merge queue of ONE ROOM: the merges that came out of this conversation.
   *
   * Same endpoint, same verdicts, narrowed by the room the request was raised
   * in - a narrowing, not a second kind of visibility, exactly as roomTodos is
   * to todos. What a room's pane shows is the work that belongs to the
   * conversation happening in it.
   */
  roomMergeQueue: (room: string) =>
    request<{
      target: string;
      target_tip: string;
      tip_from: "stated" | "landed" | "deployed" | "none";
      decided: boolean;
      gating: number;
      items: MergeRequest[];
      /**
       * Optional on the TYPE because an older node does not send it, not
       * because a current one might omit it: api_mergequeue.go sets
       * `response.Lock` unconditionally, as held:false when nothing is held,
       * so that a caller gets "the target is free" as a fact rather than as
       * the absence of a key it has to know the meaning of.
       */
      lock?: MergeLock;
    }>(`/api/merge-queue?room=${encodeURIComponent(room)}`),
  /**
   * THE VMs. Six doors, all of them operator-only (roleguard.go), all of them
   * one exec of `firecode` away on the machine serving this node.
   *
   * FOUR ANSWERS THAT MUST NOT COLLAPSE INTO ONE EMPTY PAGE, which is the whole
   * reason these are typed here rather than inlined at the call site:
   *
   *   403  this is the operator's and you are not the operator
   *   503  this node has no firecode on its PATH - it CANNOT run VMs
   *   502  firecode was there and refused
   *   200 with an empty list  nothing is running
   *
   * The node is careful to keep these apart - api_vm.go returns 503 rather than
   * an empty list precisely so a console cannot tell the operator "no VMs" when
   * the truth is "this machine cannot start one". A caller that catches these
   * into a blank panel undoes that at the last layer. ApiError carries .status,
   * so the panel branches on it.
   */
  vmProjects: () => request<{ projects: VMProject[] }>("/api/vm/projects"),
  vmList: () => request<{ vms: VM[] }>("/api/vm/list"),
  /**
   * Answers 202 the moment the process is STARTED, not when the agent is done -
   * a run is minutes to hours. What happened next is `vmList` and `vmLog`.
   *
   * The project is a NAME from vmProjects, never a path: a caller that can name
   * a directory can pack any directory into a VM that has the network. An
   * unknown name is refused with the list that would have worked.
   */
  vmSpawn: (project: string, prompt: string) =>
    request<{ project: string; workdir: string; started: boolean; prompt_given: boolean }>(
      "/api/vm/spawn",
      { method: "POST", body: JSON.stringify({ project, prompt }) },
    ),
  /** Console output, as text/plain - the node does not wrap it in a field. */
  vmLog: async (name: string) => {
    const r = await fetch(`/api/vm/${encodeURIComponent(name)}/log`, {
      credentials: "same-origin",
      headers: authHeader(),
    });
    const text = await r.text();
    if (!r.ok) throw new ApiError(r.status, parseBody(text, r)?.error ?? statusText(r));
    return text;
  },
  vmSay: (name: string, text: string) =>
    request<{ vm: string; said: boolean }>(`/api/vm/${encodeURIComponent(name)}/say`, {
      method: "POST",
      body: JSON.stringify({ text }),
    }),
  /** Stops it AND copies the work back out, which is why the node allows it three minutes. */
  vmDown: (name: string) =>
    request<{ vm: string; stopped: boolean }>(`/api/vm/${encodeURIComponent(name)}/down`, {
      method: "POST",
    }),
  mergeQueue: () =>
    request<{
      target: string;
      target_tip: string;
      tip_from: "stated" | "landed" | "deployed" | "none";
      decided: boolean;
      gating: number;
      items: MergeRequest[];
      /** See roomMergeQueue: always sent by a current node, held:false when free. */
      lock?: MergeLock;
    }>(`/api/merge-queue?limit=${TODO_PAGE}`),
  /**
   * The todos of one room: the same list, narrowed by the room the item was
   * raised in. It is the same endpoint and the same permission filter - room is
   * a narrowing beside type and kind, not a second kind of visibility - so a
   * todo that carries no room is absent here and present in `todos` above,
   * which is what makes this a filter rather than a move.
   */
  roomTodos: (room: string) =>
    request<{ artifacts: Artifact[] }>(
      `/api/artifacts?type=memory&kind=todo&room=${encodeURIComponent(room)}&limit=${ROOM_TODO_LIMIT}`,
    ),
  /**
   * Raise a todo out of a room. message is the id of the message it came out
   * of, and the node keeps it on the item: the plan says what to do and the
   * message says what was being talked about when somebody decided it had to
   * happen. The node writes the item and one message in the room together.
   */
  /**
   * A room's pinned strip: what is up, and the log it was folded out of.
   *
   * The log comes back with it because "who decided this was the decision" is
   * most of why a room pins anything, and a list of ids cannot answer it.
   */
  /**
   * What an idle seat should know: the caller's rows, what nobody has, what has
   * gone stale, and how the work is spread. The node decides all of it - see
   * api_nag.go - and this asks rather than counting the board again.
   */
  nag: () => request<NagView>("/api/nag"),

  pins: (room: string) => request<PinsView>(`/api/chat/${encodeURIComponent(room)}/pins`),

  /**
   * A READER'S OWN LIST, which is the pin's private twin and not the same thing
   * at all: a pin rearranges what four other seats see, and somebody who wants
   * to find their own way back to a message tomorrow has no business doing
   * that. 01M0HGTV9B.
   *
   * No room in any of these paths - a bookmark is about a message, and the
   * reader who kept it may have kept messages from four rooms. The list comes
   * back with the MESSAGES and not only their ids, because this is a page of
   * its own rather than a strip over a transcript, and a page of twenty ULIDs
   * is a page nobody can read.
   */
  bookmarks: () => request<BookmarksView>("/api/bookmarks"),

  bookmark: (message: string) =>
    request<FlowyEvent>("/api/bookmark", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ message }),
    }),

  /** Drop one. The log still gains an entry - see the DELETE handler. */
  unbookmark: (message: string) =>
    request<FlowyEvent>(`/api/bookmark/${encodeURIComponent(message)}`, { method: "DELETE" }),

  /**
   * WHICH THREADS THIS READER HAS UNFOLDED IN THE STREAM, the bookmark's
   * private twin for the other piece of per-reader state. Collapsing replies
   * into their head row is the default; the unfold is the exception a reader
   * records, and it is theirs alone - nobody else's stream is this reader's
   * business to rearrange.
   *
   * No room in any of these paths, for the bookmark's reason: a thread a
   * reader unfolded may be in any room they have been in, and the state is
   * about the reader, not the room.
   */
  threadsUnfolded: () => request<ThreadsUnfoldedView>("/api/threads-unfolded"),

  /** Unfold one thread in this reader's stream. */
  threadUnfold: (thread: string) =>
    request<FlowyEvent>("/api/thread-unfolded", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ thread }),
    }),

  /** Fold it back. The log still gains an entry - see the DELETE handler. */
  threadFold: (thread: string) =>
    request<FlowyEvent>(`/api/thread-unfolded/${encodeURIComponent(thread)}`, { method: "DELETE" }),

  /**
   * Put a message up in the room it was said in. The room is in the path as
   * well as the id for assignTodo's reason: the node refuses a message that is
   * not in this room, so a stale id cannot put a line in a strip whose readers
   * cannot open it.
   */
  pin: (room: string, message: string) =>
    request<FlowyEvent>(`/api/chat/${encodeURIComponent(room)}/pin`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ message }),
    }),

  /**
   * Put one emoji on one message, or take this reader's own off.
   *
   * `on` is sent explicitly rather than toggled here: the console knows whether
   * the control is pressed, and a client that flipped what it last drew would
   * fight a second tab that had already changed it.
   */
  react: (room: string, message: string, emoji: string, on: boolean) =>
    request<FlowyEvent>(`/api/chat/${encodeURIComponent(room)}/react`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ message, emoji, on }),
    }),

  /** Take it down. The log still gains an entry - see the DELETE handler. */
  unpin: (room: string, message: string) =>
    request<FlowyEvent>(
      `/api/chat/${encodeURIComponent(room)}/pin/${encodeURIComponent(message)}`,
      { method: "DELETE" },
    ),

  raiseTodo: (
    room: string,
    title: string,
    body = "",
    message?: string,
    category = "",
    attachments: string[] = [],
  ) =>
    request<{ item: Artifact; event: FlowyEvent }>(`/api/chat/${encodeURIComponent(room)}/todo`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      // category is sent only when chosen: empty means unclassified, which is a
      // legal state and what most of the queue is, so sending "" would be
      // indistinguishable from choosing it and is left out instead.
      body: JSON.stringify({
        title,
        body,
        ...(message ? { message } : {}),
        ...(category ? { category } : {}),
        // Left out when empty for the same reason category is: the node reads
        // a stated list as the list, and this control has nothing to say about
        // attachments when nobody picked one.
        ...(attachments.length > 0 ? { attachments } : {}),
      }),
    }),

  /**
   * Say who is carrying one of the room's todos. An empty name says nobody is.
   *
   * The room is in the path as well as the id because a panel edits its own
   * room's plan: the node refuses a todo that is not in this room, so a stale
   * id cannot write into another room's queue and announce it in this one.
   */
  /**
   * expect is who the caller read as carrying it when they opened the editor -
   * the cell's own text. The write is a claim against that reading, so a row
   * that changed hands underneath the editor is refused naming the winner
   * rather than overwritten, and a held row cannot be moved by a write that
   * never said whose it was.
   */
  /**
   * setPriority ranks a work item - a todo or a merge row - or takes its
   * ranking away with "".
   *
   * The vocabulary comes back on every answer rather than being kept here: a
   * console that carried its own copy would draw a control that is wrong the
   * day a fourth word is added, and the node is the thing that refuses.
   */
  /**
   * WHOSE MOVE IT IS. An empty waitingOn takes the question back.
   *
   * It does not touch the assignee and there is no parameter here that could -
   * the whole point of the field is that the carrier keeps carrying it while
   * somebody else owes the next move. See internal/store/todowaiting.go.
   */
  setWaitingOn: (id: string, waitingOn: string, asked: string) =>
    request<{ item: Artifact; waiting_on: string; asked: string }>(
      `/api/todo/${encodeURIComponent(id)}/waiting-on`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ waiting_on: waitingOn, asked }),
      },
    ),

  setPriority: (id: string, priority: string) =>
    request<{ item: Artifact; priority: string; vocabulary: string[] }>(
      `/api/todo/${encodeURIComponent(id)}/priority`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ priority }),
      },
    ),

  assignTodo: (room: string, id: string, assignee: string, expect: string) =>
    request<{ item: Artifact; event: FlowyEvent }>(
      `/api/chat/${encodeURIComponent(room)}/todo/${encodeURIComponent(id)}/assignee`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ assignee, expect }),
      },
    ),

  /**
   * assignRow says who is carrying a todo, from anywhere - no room needed.
   *
   * 01M0KXZ6VT, the operator: "i cannot reassign / assign todos". Two reasons,
   * and this is the door for both. assignTodo above posts to
   * /api/chat/{room}/todo/{id}/assignee, so it needs a room the caller is
   * looking at; measured on the board, 3 of 26 open rows carry none and could
   * be assigned from nowhere at all. And the board never had a control -
   * `git log -S'assignTodo' -- routes/Todos.tsx` is empty, so this is a gap
   * rather than a regression.
   *
   * THE ROOMLESS DOOR ALREADY EXISTED. assign.go serves POST
   * /api/todo/{id}/assignee and it answered 200 when I tested it against the
   * live node before writing any of this. Nothing new is being opened here.
   *
   * `expect` IS ALWAYS SENT, and that is the point of using this rather than a
   * bare assign: the handler treats it as a pointer - present means
   * compare-and-set through ClaimTodo, absent means an unconditional
   * AssignTodo. Two people reassigning one row must not both come away
   * believing they won, which is the same rule the CLI's --expect enforces.
   * Pass the owner the caller was looking at, "" for a row nobody held.
   */
  assignRow: (id: string, assignee: string, expect: string) =>
    request<{ assignment?: { assignee?: string }; item?: Artifact }>(
      `/api/todo/${encodeURIComponent(id)}/assignee`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ assignee, expect }),
      },
    ),

  artifact: (id: string) => request<Artifact>(`/api/artifact/${encodeURIComponent(id)}`),

  /**
   * openspec is the openspec board: the spec and change rows, newest first,
   * the same two kinds POST /api/openspec writes. The rows are ordinary
   * artifacts - the fields that make them openspec are dug in lib/openspec.
   */
  openspec: () => request<{ artifacts: Artifact[] }>("/api/openspec"),

  /**
   * openspecConflicts is one change's clash edges: every other change whose
   * spec delta touches the same capability, as the store keeps them (p2).
   */
  openspecConflicts: (id: string) =>
    request<{ conflicts: OpenspecConflict[] }>(`/api/openspec/${encodeURIComponent(id)}/conflicts`),

  /**
   * openspecTodos is the todos a change's tasks.md derived (p2). The rows are
   * ordinary todos - this door exists because no filter on GET /api/artifacts
   * reaches a todo's origin fields.
   */
  openspecTodos: (id: string) =>
    request<{ todos: Artifact[] }>(`/api/openspec/${encodeURIComponent(id)}/todos`),

  /**
   * fileUpstream records where a finding went upstream, or takes the filing
   * back off it.
   *
   * THE VERB EXISTED ONLY OVER MCP until this door. An agent with an MCP
   * connection could file a finding upstream and a person sitting in the
   * console could not, while every finding rendered "upstream: unfiled" at
   * them. findingevidence.go's head comment names the same failure about the
   * axis beside it, and api_mergegate.go names it about the gate: a door only
   * agents can knock on is half a door.
   *
   * `refs` is left out on purpose rather than passed as an empty array. The
   * store reads a stated list as "these are the citations NOW" and replaces
   * them whole, so sending [] from a control that is only setting a state would
   * silently clear every citation on the row. Absent means leave them alone,
   * and this control has nothing to say about them.
   */
  fileUpstream: (
    finding: string,
    filing: { state: string; kind?: string; id?: string; url?: string; tracker?: string },
  ) =>
    request<{ item: Artifact }>(`/api/finding/${encodeURIComponent(finding)}/upstream`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(filing),
    }),

  /** thread reads one thread of the log, whichever room it was said in. */
  thread: (thread: string) =>
    request<{ events: FlowyEvent[] }>(`/api/events?thread=${encodeURIComponent(thread)}`),

  /** tasks is the work waiting for this principal, newest first. */
  tasks: (state = "") =>
    request<{ tasks: Task[] }>(
      `/api/inbox/tasks${state ? `?state=${encodeURIComponent(state)}` : ""}`,
    ),

  task: (id: string) => request<Task>(`/api/task/${encodeURIComponent(id)}`),

  /** assign shares an artifact with somebody and hands them the work on it. */
  assign: (artifact: string, toUser: string, note?: string) =>
    request<Task>("/api/assign", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ artifact, to_user: toUser, ...(note ? { note } : {}) }),
    }),

  /** delegate hands a task to the assignee's agent. Only the assignee may. */
  delegate: (id: string, agent?: string) =>
    request<TaskMove>(`/api/task/${encodeURIComponent(id)}/delegate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(agent ? { agent } : {}),
    }),

  taskState: (id: string, state: TaskState) =>
    request<TaskMove>(`/api/task/${encodeURIComponent(id)}/state`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ state }),
    }),

  /** autoDelegate is the standing answer to inbound work: give it to my agent. */
  autoDelegate: (on: boolean) =>
    request<User>("/api/me/auto_delegate", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ on }),
    }),

  /**
   * status moves a row, and for a close it carries what was measured.
   *
   * The note is one field on this call rather than a second call beside it:
   * the node writes it in the same transaction as the closure, so a row cannot
   * end up closed with the measurement missing. The issue workflow ignores it -
   * notes hang off queue items. See store.SetTodoStatus.
   */
  status: (id: string, status: string, note?: string) =>
    request<StatusMove>(`/api/artifact/${encodeURIComponent(id)}/status`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(note ? { status, note } : { status }),
    }),

  history: (id: string) => request<History>(`/api/artifact/${encodeURIComponent(id)}/history`),

  /**
   * noteTodo attaches what somebody learned to a row: an append, not an edit,
   * so it takes no `saw` and is not refused because the work has started.
   *
   * The answer carries the row with every note on it, the one just written
   * included, which is why there is no read door beside this one. The page got
   * its notes from the single-row read it already does and gets them again from
   * here, out of the same permission-filtered read the node made - a second
   * fetch would be the same rows asked twice, with a window in between where
   * the two answers disagree. See todonote.go's viewNotes.
   */
  noteTodo: (id: string, note: string) =>
    request<{ item: Artifact; notes: NoteEntry[] }>(`/api/todo/${encodeURIComponent(id)}/note`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ note }),
    }),

  /**
   * health needs no token: it is the one thing the console can show logged out.
   * The counts are the exception - they come back only for the operator's
   * token, which request() sends when there is one - so a logged-out console
   * shows the node and its version and no tiles.
   */
  health: () => request<NodeCounts>("/healthz?counts=1"),

  /**
   * metrics is the whole set, scope-filtered by the token. all=true asks for
   * the node's own view and is answered that way only for the operator - for
   * anybody else it comes back as their own numbers, which the response's scope
   * block says plainly.
   */
  metrics: (all = false) => request<Metrics>(`/api/metrics${all ? "?scope=all" : ""}`),

  /** traces lists the recent traces this token may read. */
  traces: (all = false) =>
    request<{ node: string; traces: TraceSummary[] }>(`/api/traces${all ? "?scope=all" : ""}`),

  /** trace reads one trace: this node's spans of it, in start order. */
  trace: (id: string, all = false) =>
    request<{ node: string; trace: Trace }>(
      `/api/trace/${encodeURIComponent(id)}${all ? "?scope=all" : ""}`,
    ),

  /** activity reads the timeline: turns, logs, chat and steers, oldest first. */
  activity: (
    params: { q?: string; kind?: string; room?: string; thread?: string; order?: string } = {},
  ) => {
    const search = new URLSearchParams();
    for (const [key, value] of Object.entries(params)) {
      if (value) search.set(key, value);
    }
    const query = search.toString();
    return request<ActivityPage>(`/api/activity${query ? `?${query}` : ""}`);
  },

  /**
   * The worklog: the chronology, newest first.
   *
   * It is the timeline read narrowed to the one kind, and deliberately not an
   * endpoint of its own - the permission filter that decides which entries a
   * token may see is on /api/activity, and a second door onto the same rows is
   * a second place for that filter to be forgotten. order=recent is what makes
   * it the NEWEST entries rather than the first page of the oldest ones.
   */
  worklog: (q = "") => api.activity({ kind: "worklog", order: "recent", q }),

  /**
   * announcements is what the banner reads: the ones that are still active and
   * that this token may see. The node decides both - which is why the banner
   * has no filter of its own.
   */
  announcements: () => request<{ announcements: Announcement[] }>("/api/announcements"),

  /** quiesce is who an announcement is still waiting on. */
  quiesce: (id: string) => request<Quiesce>(`/api/announcement/${encodeURIComponent(id)}/quiesce`),

  /** ack says this principal has seen the announcement and is out of the way. */
  ack: (id: string) =>
    request<{ quiesce: Quiesce; event: FlowyEvent }>(
      `/api/announcement/${encodeURIComponent(id)}/ack`,
      { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" },
    ),

  /**
   * resolve closes an announcement's window, which is what clears the banner.
   *
   * There is no dismiss and there should not be: what takes an announcement off
   * the screen is the announcement's own state, not this browser's, so a reader
   * who clears it clears it for everybody and the next reader is not told
   * something the last one hid.
   */
  resolve: (id: string) =>
    request<{ announcement: Announcement; quiesce: Quiesce | null }>(
      `/api/announcement/${encodeURIComponent(id)}/resolve`,
      { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" },
    ),

  /** post says something into the timeline: into a room, or into a run's thread. */
  postActivity: (post: { kind: string; body: string; room?: string; thread?: string }) =>
    request<ActivityItem>("/api/activity", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(post),
    }),

  /**
   * The repro runner's door - cmd/handoff-runner, not this node. Every one of
   * these throws ReproUnconfigured when no base is set; see reproBase above
   * and ReproPanel, which is the only caller and the only place that
   * decides what to show for it.
   */

  /**
   * Enqueue a repro run of `finding` against `version`. The answer names what
   * was queued and what was refused rather than a bare id - see ReproQueued,
   * and ReproPanel, which shows the refusal instead of a run that never
   * started.
   *
   * A REQUEST THAT QUEUED NOTHING IS A 400 CARRYING THE REASONS, NOT AN
   * ERROR. cmd/handoff-runner's handleRun answers 202 when it queued
   * something and 400 when it queued nothing, and in both cases the reason
   * per finding is in `refused` - there is no top-level `error` key on that
   * body. So it cannot go through reproRequest, which would raise an ApiError
   * carrying the status line: the panel would print "400 Bad Request" over
   * "this runner is not configured for project serenedb", which is the whole
   * of what the reader needed. Asking for one finding and being told nothing
   * about why is indistinguishable from the button not working.
   *
   * Anything else that fails still throws, including a 400 with no refusals
   * on it - a malformed request body, say - because there is nothing better
   * to show for those than the status.
   */
  reproRun: async (finding: string, version: string): Promise<ReproQueued> => {
    const response = await fetch(`${reproBase()}/run`, {
      method: "POST",
      headers: { ...authHeader(), "Content-Type": "application/json" },
      body: JSON.stringify({ finding, version }),
    });
    const text = await response.text();
    const body = parseBody(text, response);
    if (!response.ok) {
      if (Array.isArray(body?.refused) && body.refused.length > 0) {
        return { queued: [], refused: body.refused, version: body?.version ?? version };
      }
      throw new ApiError(response.status, body?.error ?? statusText(response));
    }
    return body as ReproQueued;
  },

  /**
   * Every run the runner knows about, and whether it can run anything at all.
   *
   * THERE IS NO PER-FINDING DOOR: GET /runs takes no filter and answers with
   * every run whose finding the caller may read, so narrowing to one finding is
   * this side's job and ReproPanel does it on `run.finding`. Asking for it in
   * the query string instead - which this call used to do - got every other
   * finding's runs back and drew them under whichever finding was open, because
   * an ignored query parameter looks exactly like an honoured one.
   */
  reproRuns: () => reproRequest<ReproRuns>("/runs"),

  /** One run's log, plain text. Polled, not streamed - see ReproPanel. */
  reproLog: (id: string) => reproText(`/run/${encodeURIComponent(id)}/log`),

  /**
   * A self-contained docker-compose repro package for one finding at one
   * version, as a downloadable blob. Not JSON on success, so this bypasses
   * reproRequest and reads the response itself - the filename comes off
   * Content-Disposition when the runner sends one, and a fallback name
   * otherwise so the download always has one.
   */
  reproPackage: async (finding: string, version: string) => {
    const response = await fetch(
      `${reproBase()}/package?finding=${encodeURIComponent(finding)}&version=${encodeURIComponent(version)}`,
      // The token here too - a package carries the finding's repro tree
      // verbatim and a finding can be private, which is why that route is
      // behind the same door as the rest.
      { cache: "no-store", headers: authHeader() },
    );
    if (!response.ok) {
      const text = await response.text();
      let message = text.trim().slice(0, 200) || statusText(response);
      try {
        message = JSON.parse(text)?.error ?? message;
      } catch {
        // text wasn't JSON either - the slice above is what there is to say.
      }
      throw new ApiError(response.status, message);
    }
    const blob = await response.blob();
    const disposition = response.headers.get("content-disposition") ?? "";
    const named = /filename="?([^";]+)"?/.exec(disposition)?.[1];
    return { blob, filename: named ?? `repro-${finding}-${version}.tgz` };
  },

  /**
   * What the runner can say about a version label without running anything.
   *
   * The project is named because a runner holds several - one checkout, one
   * base image and one cache each - and it can only guess when it happens to
   * hold exactly one. A finding knows whose code it is about, so the caller
   * passes it and gets "this runner is not configured for project X" instead of
   * "name a project" from a deployment that holds two.
   */
  reproVersion: (project: string | null | undefined, v: string) =>
    reproRequest<ReproVersion>(
      `/version?v=${encodeURIComponent(v)}${
        project ? `&project=${encodeURIComponent(project)}` : ""
      }`,
    ),
};

/** isAgent reads the speaker's kind off the message the node stamped it with. */
export function isAgent(event: FlowyEvent) {
  return event.meta?.actor_kind === "agent";
}
