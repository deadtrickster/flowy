package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/otel"
)

// WHY NOTHING COULD TAKE THIS ROW.
//
// The queue has three answers for a row it will not work on, and until this
// file it could only say two of them. A row with no green verdict says so; a
// row whose gate failed says so since the red landed. A row that nothing could
// even PICK UP said nothing at all.
//
// Measured on 18 Aug: drain.sh skips a row whose branch is checked out in
// another worktree, writes "skipping X - branch checked out in Y" into its own
// log, and moves on. Three rows were held that way by their author's worktrees,
// so the drainer woke every ninety seconds, found nothing it was allowed to
// take, and slept - for twenty minutes, while the queue showed all three as
// plain `todo`. A row nobody can take and a row waiting its turn looked
// identical to every reader, and the only place the difference existed was a
// log file on one box.
//
// It is the same defect the red was and it takes the same shape: THE THING THAT
// TRIED WRITES WHY IT COULD NOT, on the row, where everybody reads the row.
//
// THE NODE CANNOT COMPUTE ANY OF THIS. It has no checkout, so "the branch is
// held elsewhere" and "this would conflict with master" are facts only a caller
// with a repository can establish. That is what makes this a recorded
// observation rather than a query.
//
// A SKIP IS A FACT ABOUT A MOMENT, NOT ABOUT THE ROW. "Branch checked out in
// wt-qorder" is true until somebody detaches it, and a reason left lying on a
// row would read as grounds to skip it forever. So it carries the time it was
// found and the agent that found it, and BlockedAt below refuses to repeat it
// once it is older than the window - the same trade GatingAt makes for a
// declaration, for the same reason: an old answer about a fast-moving fact is
// not evidence, it is furniture.

// BlockedWhyField is the sentence, BlockedAtField when it was found, and
// BlockedByField which agent found it. Three fields rather than one blob
// because every reader wants a different one of them: a person wants the
// sentence, a loop wants the age, and an operator deciding whether to believe
// it wants to know whose answer it is.
const (
	BlockedWhyField = "blocked_why"
	BlockedAtField  = "blocked_at"
	BlockedByField  = "blocked_by"
)

// BlockBelievedFor is how long a skip is taken seriously, and it is deliberately
// the same as GateBelievedFor. Both answer "is this still true?" about something
// a process observed once and cannot be asked to observe again, and two windows
// that could drift apart would be two rules about one question.
const BlockBelievedFor = GateBelievedFor

// BlockedWhyOf, BlockedAtOf and BlockedByOf read what a row carries. They are
// the raw fields, without the age judgement - see BlockedAt for that.
func BlockedWhyOf(a *Artifact) string { return strings.TrimSpace(artifactString(a, BlockedWhyField)) }

func BlockedAtOf(a *Artifact) string { return strings.TrimSpace(artifactString(a, BlockedAtField)) }

func BlockedByOf(a *Artifact) string { return strings.TrimSpace(artifactString(a, BlockedByField)) }

// BlockedAt is the reason a row could not be taken, as of now, or "" when there
// is none worth repeating.
//
// An unparseable or absent stamp reads as NOT blocked rather than as blocked
// forever, which is GatingAt's rule and made for the same case: a row written
// before this field existed has no stamp, and the safe reading of "I do not
// know when this was found" is not "nothing may ever take this".
func BlockedAt(a *Artifact, now time.Time) string {
	why := BlockedWhyOf(a)
	if why == "" {
		return ""
	}
	stamp := BlockedAtOf(a)
	if stamp == "" {
		return ""
	}
	found, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return ""
	}
	if now.Sub(found) >= BlockBelievedFor {
		return ""
	}
	return why
}

// EventMergeBlocked is what a skip leaves in the log.
//
// The event is the record and the fields are its projection, as with a gate:
// the fields say what is true now and are cleared by the next declaration, and
// the log answers "how long has this row been unpickable, and by whose
// reckoning" after the fields have moved on.
const EventMergeBlocked = "merge.blocked"

// SetMergeBlocked records that this caller could not take a merge request, and
// why.
//
// IT DOES NOT TOUCH THE LANDING LOCK, in either direction, and that is the one
// place it deliberately differs from every other verb on this row. A gate
// declaration takes the target and a verdict renews it; this is the verb for a
// caller that never got that far. Requiring the lock to say "I could not take
// this" would be requiring the thing whose absence is being reported.
//
// It does not touch the status either. A row nothing could pick up is still
// exactly as open as it was, and whoever is carrying it still is.
func (d *DB) SetMergeBlocked(
	ctx context.Context, p *Principal, id, why string,
) (*Artifact, *Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "merge.blocked")
	defer span.End()

	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, fmt.Errorf("store: this token resolves to nobody, so it cannot report a skip")
	}
	why = strings.TrimSpace(why)
	if why == "" {
		return nil, nil, fmt.Errorf("store: a skip says why - a row marked unpickable with no " +
			"reason is the silence this replaces, one field further along")
	}
	art, err := d.readWorkItem(ctx, p, strings.TrimSpace(id))
	if err != nil {
		return nil, nil, err
	}
	if art.Kind != MergeKind {
		// The same answer as an id that is not here, for the reason the gate
		// gives: this verb is about merge requests, and saying which other kind
		// it found would tell a caller about a row they did not ask about.
		return nil, nil, ErrNotFound
	}

	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	fields[BlockedWhyField] = why
	fields[BlockedAtField] = now.Format(time.RFC3339Nano)
	fields[BlockedByField] = actor

	column, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: record the skip on %s: %w", art.ID, err)
	}
	meta, err := json.Marshal(map[string]string{
		BlockedWhyField: why,
		BlockedByField:  actor,
		BranchField:     BranchOf(art),
		"actor_kind":    actorKind,
		"actor_user":    p.UserID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("store: record the skip on %s: %w", art.ID, err)
	}
	entry := &Event{
		Type:     EventMergeBlocked,
		Project:  art.Project,
		Room:     categoryRoom(art),
		Thread:   art.ID,
		Artifact: art.ID,
		Actor:    actor,
		Body:     fmt.Sprintf("could not take %s: %s", BranchOf(art), why),
		Meta:     meta,
	}
	if err := d.SetArtifactFields(ctx, art, column, entry); err != nil {
		return nil, nil, err
	}
	span.SetArtifact(art.ID)
	return art, entry, nil
}
