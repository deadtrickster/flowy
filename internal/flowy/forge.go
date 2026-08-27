package flowy

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/forge"
	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// The forge bridge.
//
// A node's bugs are artifacts and the world's bugs are issues on GitHub or
// GitLab, and Phase 6 is the join between them. It is three operations:
//
//   - file, which opens an issue from an artifact and writes the link back onto
//     it as an external ref;
//   - status, which reads the issue's state and moves the artifact to done when
//     the issue is closed or merged;
//   - sync, which is the reviewer loop: every new comment on the issue becomes
//     an event in the artifact's thread, and every reply written in that thread
//     since the last push goes back out as a comment.
//
// All three are gated on reading the artifact and nothing more. Reading is what
// makes somebody a participant here, the same as it does for the lifecycle: an
// assignee in another project can move a shared bug through the workflow, and
// they can answer its reviewer too.
//
// Filing is separate and explicit, because it is the one operation that is
// visible outside this machine. Nothing files an artifact because it looked
// like a bug.
const (
	// forgeRoom is the room an issue's conversation lands in when the artifact
	// has no handoff thread of its own. It is a room like any other.
	forgeRoom = "forge"
	// forgeEventType is what the bridge's own moves are logged as - filed,
	// state changes - as opposed to the conversation, which is chat.
	forgeEventType = "forge"
	// forgeActorPrefix names the synthetic principal a comment from the forge
	// is attributed to: forge:octocat is the octocat who wrote it over there,
	// and is deliberately not a users row - they have no token here, they
	// cannot read anything, and the only thing the node knows about them is the
	// login the forge printed.
	forgeActorPrefix = "forge:"
)

// forgeExternalActor is the actor a comment by author becomes.
func forgeExternalActor(author string) string {
	if author == "" {
		author = "unknown"
	}
	return forgeActorPrefix + author
}

// isForgeActor reports whether an event came in from a forge. Those are never
// pushed back out: a comment that came from the issue does not belong on the
// issue a second time, and a loop that echoes is not a loop anybody wants.
func isForgeActor(actor string) bool { return strings.HasPrefix(actor, forgeActorPrefix) }

// forgeClient returns the node's forge, or answers 503 and reports false. A
// node with no gh, no glab and no FLOWY_FORGE is a perfectly good node - it
// simply cannot file anything, and it says so rather than pretending.
func (s *server) forgeClient(w http.ResponseWriter) (forge.ForgeClient, bool) {
	if s.forge == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			errorBody("no forge on this node: "+s.forgeWhy+
				" (set FLOWY_FORGE=gh|glab|mock, or install gh)"))
		return nil, false
	}
	return s.forge, true
}

// handleForgeCapability says which forge this node would use and why, and which
// CLIs it can see. It is the quickest answer to "why did filing that bug go
// nowhere", and it is what the gate asserts to show that FLOWY_FORGE=mock
// really did select the fake.
//
// GET /api/forge
func (s *server) handleForgeCapability(w http.ResponseWriter, _ *http.Request) {
	kind := ""
	if s.forge != nil {
		kind = s.forge.Kind()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"forge":     kind,
		"why":       s.forgeWhy,
		"available": forge.Available(),
		"mock":      s.mockForge != nil,
	})
}

// forgeOwner reports whether p may make this node act on art outside itself,
// and answers 403 when it may not.
//
// Reading an artifact is not permission to publish it. The body of a bug goes
// out of this machine over a credential the caller does not hold and cannot be
// taken back, so filing and pushing replies are the owner's to do - or the
// operator's, who holds the credential in the first place. Everyone else who
// can read it can still read it, comment in the thread, and move the status:
// none of that leaves the building.
func (s *server) forgeOwner(w http.ResponseWriter, r *http.Request, art *store.Artifact) bool {
	p := principalOf(r)
	if p != nil && (p.Operator || (p.UserID != "" && art.OwnerUser == p.UserID)) {
		return true
	}
	writeJSON(w, http.StatusForbidden,
		errorBody("only the owner of "+art.ID+" can send it to a forge"))
	return false
}

// forgeMayPublish reports whether what actor said may leave this node over the
// node's own credential. It is forgeOwner's rule, applied to an event's author
// rather than to the caller holding the token: the owner of the artifact, an
// agent acting for them, this node's operator, or an agent acting for the
// operator.
//
// The two are asked in different places for the same reason. Who may write in
// an artifact's thread is a wide set - any project mate, any party to the task,
// see mayWriteThread and EventFilterSQL - and none of that leaves the building.
// Who may publish is the narrow one, and a sync run by the owner must not carry
// somebody else's words out with it.
func (s *server) forgeMayPublish(ctx context.Context, art *store.Artifact, actor string) bool {
	if actor == "" {
		return false
	}
	if actor == art.OwnerUser || (s.operator != "" && actor == s.operator) {
		return true
	}
	// An agent posts as itself, so the owner's agent is an actor id that is not
	// the owner's. It reaches exactly what its user reaches - the same rule
	// PrincipalForToken keeps for a token that names only an agent.
	agent, err := s.db.GetAgent(ctx, actor)
	if err != nil {
		return false
	}
	return agent.UserID == art.OwnerUser || (s.operator != "" && agent.UserID == s.operator)
}

// forgeRepoAllowed reports whether this node files into repo, and answers 403
// when it does not.
//
// The repository used to be whatever the request body said, which made the
// node's forge credential a general-purpose publisher: name any repo you can
// write to and the artifact's body lands in it. Where this node may file is the
// operator's decision, made once, in FLOWY_FORGE_REPOS.
func (s *server) forgeRepoAllowed(w http.ResponseWriter, repo string) bool {
	if s.forgeRepos[repo] {
		return true
	}
	writeJSON(w, http.StatusForbidden,
		errorBody("this node does not file into "+repo+
			"; the repositories it files into are the operator's list (FLOWY_FORGE_REPOS)"))
	return false
}

// forgeFileRequest is the body of a filing.
type forgeFileRequest struct {
	Artifact string `json:"artifact"`
	Repo     string `json:"repo"`
}

// handleForgeFile opens an issue for an artifact and writes the link back onto
// it.
//
// An artifact that is already filed is a conflict rather than a second issue:
// filing twice is how a tracker ends up with two issues nobody closes, and the
// caller that wanted the link already has it in the error.
//
// POST /api/forge/file  {artifact, repo}
func (s *server) handleForgeFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req forgeFileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	if req.Artifact == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("artifact is required"))
		return
	}
	if !forge.ValidRepo(req.Repo) {
		writeJSON(w, http.StatusBadRequest, errorBody("repo must be owner/name"))
		return
	}
	client, ok := s.forgeClient(w)
	if !ok {
		return
	}
	art, ok := s.readableArtifact(w, r, req.Artifact)
	if !ok {
		return
	}
	if !s.forgeOwner(w, r, art) {
		return
	}
	if !s.forgeRepoAllowed(w, req.Repo) {
		return
	}
	if art.External != nil {
		writeAlreadyFiled(w, art.External, "")
		return
	}

	// The comment cursor starts at the filing, like the push cursor below: the
	// reviewer loop carries what is said about the issue from the moment it
	// existed, and there is nothing before that to carry.
	//
	// It is read before the round trip and not after it. Opening an issue takes
	// as long as the forge takes, and a reviewer watching the tracker can
	// answer inside that window - a cursor set afterwards is a cursor set past
	// their comment, and ListComments never offers it again.
	filed := time.Now().UTC()

	number, issueURL, err := client.FileIssue(ctx, req.Repo, forgeIssueTitle(art), forgeIssueBody(art))
	if err != nil {
		// The forge refused, or is not there. That is not this node being
		// broken, so it is not a 500: nothing here has changed.
		writeJSON(w, http.StatusBadGateway, errorBody("forge: "+err.Error()))
		return
	}

	ref := &store.ExternalRef{
		Forge:  client.Kind(),
		Repo:   req.Repo,
		Number: number,
		URL:    issueURL,
		State:  forge.StateOpen,
		Thread: s.forgeThread(ctx, art),
		Author: s.forgeSelfLogin(ctx, client),
		Since:  filed,
		Filed:  filed,
	}

	// The filing goes in the thread, and its reading becomes the push cursor:
	// the reviewer loop carries what was said after the issue existed, not the
	// whole conversation that led to it being filed.
	event, err := s.forgeEvent(r, art, ref, "filed "+ref.Repo+"#"+strconv.Itoa(number), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}

	// The entry and the link land together, and the push cursor is the entry's
	// own reading - see LinkArtifactExternal. Written separately, a failure
	// between them left the log saying the bug had been filed and the artifact
	// saying it had not, and the next call to this endpoint would file it
	// again: two issues for one bug, one of them belonging to nobody.
	//
	// The issue itself is already open, and no rollback here closes it. So a
	// failure names it: whoever reads this error is the only one who can go and
	// look, and telling them "internal error" would lose the number.
	//
	// The check at the top of this handler, the round trip above and this write
	// are three steps, and a second filing of the same artifact can be inside
	// all three at once. Only one of them writes the link - the statement
	// carries `external IS NULL` - and the one that does not is answered the
	// same 409 that check gives, naming the link that won and the issue this
	// call opened for nothing, which is the only record of it there will be.
	if err := s.db.LinkArtifactExternal(ctx, art, ref, true, event); err != nil {
		if errors.Is(err, store.ErrAlreadyFiled) {
			log.Printf("forge: %s was filed by another request while this one was "+
				"opening %s#%d (%s); that issue is now unreferenced",
				art.ID, ref.Repo, number, issueURL)
			writeAlreadyFiled(w, s.filedLink(ctx, r, art.ID),
				"and this call opened "+ref.Repo+"#"+strconv.Itoa(number)+" ("+issueURL+
					"), which nothing points at")
			return
		}
		serverErrorSaying(w, r, err,
			"filed as "+ref.Repo+"#"+strconv.Itoa(number)+" ("+issueURL+
				"), and this node could not record it")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"artifact": art,
		"external": ref,
		"event":    event,
	})
}

// writeAlreadyFiled is the one answer a second filing gets, whether the first
// one landed before this request started or during it: 409, the link that is on
// the artifact, and nothing invented. also is appended to the message when there
// is something more to say about this particular attempt.
func writeAlreadyFiled(w http.ResponseWriter, ref *store.ExternalRef, also string) {
	said := "already filed"
	if ref != nil {
		said += " as " + ref.Repo + "#" + strconv.Itoa(ref.Number)
	}
	if also != "" {
		said += ", " + also
	}
	writeJSON(w, http.StatusConflict, map[string]any{"error": said, "external": ref})
}

// filedLink re-reads the link that is on an artifact now, for the request that
// lost a race to write one. It is read back rather than assumed: what the caller
// needs is the issue that survived, and this request's own ref is the one that
// did not. A read that fails answers nothing rather than something untrue - the
// 409 stands either way.
func (s *server) filedLink(ctx context.Context, r *http.Request, id string) *store.ExternalRef {
	art, err := s.db.ReadArtifact(ctx, principalOf(r), id, false)
	if err != nil {
		log.Printf("forge: cannot read back the link that won on %s: %v", id, err)
		return nil
	}
	return art.External
}

// forgeSelfLogin is the login this node's own comments appear under on the
// forge, recorded on the link when it is filed.
//
// It has to be asked for rather than assumed. The sync skips comments written
// under it - that is what stops the reviewer loop echoing - and a node talking
// to a real GitHub posts as whoever `gh` is logged in as, which is not the
// mock's name for itself. Getting it wrong means the node threads its own
// replies back in as if a reviewer had said them, and then pushes them out
// again.
//
// A forge that cannot say - the mock, or a CLI that refuses - leaves it at the
// name the mock posts under, which is what it was before.
func (s *server) forgeSelfLogin(ctx context.Context, client forge.ForgeClient) string {
	who, ok := client.(forge.SelfLoginer)
	if !ok {
		return forge.SelfAuthor
	}
	login, err := who.SelfLogin(ctx)
	if err != nil || login == "" {
		log.Printf("forge: cannot read this node's own login (%v); "+
			"treating %q as its own comments", err, forge.SelfAuthor)
		return forge.SelfAuthor
	}
	return login
}

// handleForgeStatus refreshes an issue's state, and moves the artifact to done
// when the issue is finished.
//
// The move is deliberately not a workflow transition. The lifecycle refuses
// shortcuts because a status that can jump is a status nobody trusts - but the
// forge is the authority on its own issue, and an artifact still claiming
// in-progress about an issue somebody closed a week ago is worse than a jump.
// The move is recorded as a status event like every other, with via=forge in
// its meta, so the trail says who really moved it.
//
// GET /api/forge/status?artifact=
func (s *server) handleForgeStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	client, ok := s.forgeClient(w)
	if !ok {
		return
	}
	art, ref, ok := s.filedArtifact(w, r, r.URL.Query().Get("artifact"))
	if !ok {
		return
	}
	// A refresh is not a read. It spends this node's forge credential on a
	// repository the caller does not have to hold one for, and it writes: a
	// terminal issue moves the artifact to done and records a status event
	// under the caller's name. Filing and syncing are the owner's for exactly
	// those reasons, and so is this - everyone else who can read the artifact
	// can still read it, and can still see the state the last refresh found.
	if !s.forgeOwner(w, r, art) {
		return
	}
	// The repository comes off the link on the artifact, and a link is a
	// replicated column: a peer can write one that names any repository at all.
	// So the operator's list is checked here too, exactly as filing and syncing
	// check it - otherwise a pushed artifact is enough to point this node's
	// credential at somebody else's tracker.
	if !s.forgeRepoAllowed(w, ref.Repo) {
		return
	}

	state, err := client.GetState(ctx, ref.Repo, ref.Number)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("forge: "+err.Error()))
		return
	}

	was := statusOf(art)
	moved := false
	if forge.Terminal(state) && lifecycleTypes[art.Type] && !terminalStatus[was] {
		event, err := s.statusEventVia(r, art, was, statusDone, map[string]string{
			"via":    forgeEventType,
			"forge":  ref.Forge,
			"repo":   ref.Repo,
			"number": strconv.Itoa(ref.Number),
			"state":  state,
		})
		if err != nil {
			serverError(w, r, err)
			return
		}
		// The move and the entry that says the forge made it, together: a
		// status the trail cannot account for is worse here than anywhere else,
		// because this is the one move nobody in the fabric made.
		if err := s.db.MoveArtifactStatus(ctx, art, statusDone, event); err != nil {
			serverError(w, r, err)
			return
		}
		moved = true
	}

	// Only write when something actually changed: a status poll that finds
	// nothing new should not bump the artifact's clock and make every peer
	// merge a row that says the same thing.
	if state != ref.State || moved {
		ref.State = state
		if err := s.db.SetArtifactExternal(ctx, art, ref, art.Reported); err != nil {
			serverError(w, r, err)
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"artifact": art,
		"external": ref,
		"state":    state,
		"status":   statusOf(art),
		"moved":    moved,
	})
}

// forgeSyncRequest is the body of a sync.
type forgeSyncRequest struct {
	Artifact string `json:"artifact"`
}

// handleForgeSync is the reviewer loop, in both directions.
//
// In: every comment on the issue that has not been threaded in yet becomes a
// chat event in the artifact's thread, written by a synthetic external
// principal named for whoever wrote it on the forge. The node's own comments
// are skipped, which is what stops the loop echoing.
//
// Out: every reply the owner wrote in that thread newer than the push cursor
// goes to the forge as a comment, attributed to whoever wrote it here. What
// anybody else said in the thread stays here - see forgePushReplies.
//
// It is idempotent. Both cursors live on the external ref, both only move
// forward, and a sync that finds nothing new writes nothing at all - not even a
// clock bump.
//
// POST /api/forge/sync  {artifact}
func (s *server) handleForgeSync(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req forgeSyncRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	client, ok := s.forgeClient(w)
	if !ok {
		return
	}
	art, ref, ok := s.filedArtifact(w, r, req.Artifact)
	if !ok {
		return
	}
	if !s.forgeOwner(w, r, art) {
		return
	}
	if !s.forgeRepoAllowed(w, ref.Repo) {
		return
	}

	// Both halves report what they finished before they report what went
	// wrong, and the ref is written either way. The pull side threads one
	// comment at a time and marks each one seen as it lands; a failure halfway
	// through - the log refusing a row, the request being cancelled - leaves
	// the ones before it in the thread, so throwing the ref away would mean the
	// next sync threaded them in a second time. Pushing is not attempted after
	// a pull that broke: whatever stopped it is likely to stop that too, and a
	// half-threaded conversation is not one to reply to.
	threaded, pullErr := s.forgePullComments(r, art, ref, client)
	pushed := 0
	var pushErr error
	if pullErr == nil {
		pushed, pushErr = s.forgePushReplies(r, art, ref, client)
	}

	// The cursors describe what actually happened: every comment that reached
	// the forge is behind the cursor and every one that did not is still in
	// front of it. Answering 502 first and writing nothing - which is what this
	// did - meant the next sync started again from before the first reply and
	// posted the ones that had already arrived a second time.
	if len(threaded) > 0 || pushed > 0 {
		if err := s.db.SetArtifactExternal(ctx, art, ref, art.Reported); err != nil {
			serverError(w, r, err)
			return
		}
	}
	if pullErr != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("forge: "+pullErr.Error()))
		return
	}
	if pushErr != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("forge: "+pushErr.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"artifact": art.ID,
		"external": ref,
		"thread":   ref.Thread,
		"pulled":   len(threaded),
		"pushed":   pushed,
		"events":   threaded,
	})
}

// forgePullComments threads the issue's new comments into the artifact's thread
// and advances the comment cursor over them.
//
// It returns what it threaded even when it stops on an error, because what it
// threaded is in the log by then: the caller writes the cursor over those and
// then reports the failure, so the ones that landed are not threaded again on
// the next sync.
func (s *server) forgePullComments(
	r *http.Request, art *store.Artifact, ref *store.ExternalRef, client forge.ForgeClient,
) ([]*store.Event, error) {
	ctx := r.Context()
	comments, err := client.ListComments(ctx, ref.Repo, ref.Number, ref.Since)
	if err != nil {
		return nil, err
	}

	self := ref.Author
	if self == "" {
		self = forge.SelfAuthor
	}

	out := []*store.Event{}
	for _, c := range comments {
		if c.Author == self {
			// Ours, from an earlier push. Skipped rather than recorded as seen:
			// there is nothing to remember about a comment this node wrote, and
			// remembering it would be a write on a sync that changed nothing.
			continue
		}
		if ref.AlreadySeen(c.ID, c.At) {
			continue
		}
		event, err := s.threadForgeComment(r, art, ref, c)
		if err != nil {
			return out, err
		}
		ref.MarkSeen(c.ID, c.At)
		if event.SeqHLC > ref.Pushed {
			// The comment is in the thread now, and the push side has already
			// accounted for it - it must not be pushed back out.
			ref.Pushed = event.SeqHLC
		}
		out = append(out, event)
	}
	return out, nil
}

// threadForgeComment writes one comment from the forge into the thread as an
// ordinary chat event, so the console renders it with the chat it already had
// and the permission filter treats it like every other message.
func (s *server) threadForgeComment(
	r *http.Request, art *store.Artifact, ref *store.ExternalRef, c forge.Comment,
) (*store.Event, error) {
	meta, err := json.Marshal(map[string]string{
		"actor_kind":    "external",
		"forge":         ref.Forge,
		"repo":          ref.Repo,
		"number":        strconv.Itoa(ref.Number),
		"author":        c.Author,
		"forge_comment": c.ID,
	})
	if err != nil {
		return nil, err
	}

	parents := []string{}
	if last, err := s.db.LatestThreadEvent(r.Context(), ref.Thread); err == nil {
		parents = append(parents, last.ID)
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	e := &store.Event{
		Type:     chatEventType,
		Project:  art.Project,
		Room:     forgeRoom,
		Thread:   ref.Thread,
		Parents:  parents,
		Actor:    forgeExternalActor(c.Author),
		Artifact: art.ID,
		Body:     c.Body,
		Meta:     json.RawMessage(meta),
	}
	if err := s.db.AppendEvent(r.Context(), e); err != nil {
		return nil, err
	}
	return e, nil
}

// forgePushReplies sends everything said in the thread since the last push out
// to the issue, and moves the cursor over everything it has finished with -
// including what it decided not to send, which is why a second sync sends
// nothing.
//
// The cursor advances one event at a time and only ever behind a comment the
// forge has accepted. It used to be raised to the highest event it had looked
// at whether or not the sending worked, which is two bugs in one line: on a
// refusal halfway through, the replies that did not go out were behind the
// cursor and never went out at all, and - because the caller threw the whole
// ref away on the error - the ones that had gone out were sent again on the
// next sync. Either way the issue is wrong, and the second way is wrong in
// public.
//
// What goes out is the owner's half of the conversation and nothing else. The
// thread is open to everyone the artifact is - a project mate, the assignee,
// the agent working on it - and every one of them can say something in it. That
// is a conversation here; on the issue it would be this node publishing a
// stranger's words under a credential they do not hold and cannot take back,
// which is the one thing forgeOwner exists to stop. So a reply from anybody
// else stays here, and the cursor passes it exactly as it passes a status move.
func (s *server) forgePushReplies(
	r *http.Request, art *store.Artifact, ref *store.ExternalRef, client forge.ForgeClient,
) (int, error) {
	ctx := r.Context()
	events, err := s.db.ThreadEvents(ctx, ref.Thread)
	if err != nil {
		return 0, err
	}

	pushed := 0
	for _, e := range events {
		if e.SeqHLC <= ref.Pushed {
			continue
		}
		// Only the conversation goes out. A status move or a task handoff is
		// this node's bookkeeping, and the issue's reviewer did not ask for it.
		// Nothing left this node, so the cursor may pass it either way.
		if e.Type != chatEventType || isForgeActor(e.Actor) || strings.TrimSpace(e.Body) == "" {
			ref.Pushed = e.SeqHLC
			continue
		}
		// And only what the owner said. Nothing left this node, so the cursor
		// may pass this one too - a reply held back is held back for good, not
		// queued for the next sync to send.
		if !s.forgeMayPublish(ctx, art, e.Actor) {
			ref.Pushed = e.SeqHLC
			continue
		}
		if err := client.Comment(ctx, ref.Repo, ref.Number, s.forgeReplyBody(ctx, e)); err != nil {
			// Stop at the first refusal, with the cursor behind this event: it
			// did not reach the forge, so it is still pending. Everything before
			// it did, and is not sent again.
			return pushed, err
		}
		ref.Pushed = e.SeqHLC
		pushed++
	}
	return pushed, nil
}

// forgeReplyBody is what a reply looks like on the issue. It is attributed,
// because the credential that posts it is the node's and the person who wrote
// it is not - a comment that arrives as "flowy" with no name on it is a comment
// the reviewer cannot answer.
func (s *server) forgeReplyBody(ctx context.Context, e *store.Event) string {
	return "**" + s.forgeAuthorName(ctx, e.Actor) + "** via flowy:\n\n" + e.Body
}

// forgeAuthorName is the handle behind an actor id, or the id when there is no
// handle to be had - an agent, or a user this node does not hold.
func (s *server) forgeAuthorName(ctx context.Context, actor string) string {
	if user, err := s.db.GetUser(ctx, actor); err == nil && user.Handle != "" {
		return user.Handle
	}
	if agent, err := s.db.GetAgent(ctx, actor); err == nil {
		if user, err := s.db.GetUser(ctx, agent.UserID); err == nil && user.Handle != "" {
			return user.Handle + "'s " + agent.Kind + " agent"
		}
	}
	return actor
}

// forgeThread decides where an issue's conversation lands. An artifact that has
// been handed to somebody already has a thread with both of them in it, and the
// reviewer's comments belong in it rather than in a second thread beside it.
// Otherwise the link opens one.
func (s *server) forgeThread(ctx context.Context, art *store.Artifact) string {
	if task, err := s.db.LatestTaskForArtifact(ctx, art.ID); err == nil && task.Thread != "" {
		return task.Thread
	}
	return ulid.NewString()
}

// forgeIssueTitle is what the issue is called: the artifact's title, or
// something that at least says what it is when it has none.
func forgeIssueTitle(art *store.Artifact) string {
	if title := strings.TrimSpace(art.Title); title != "" {
		return title
	}
	return art.Type + " " + art.ID
}

// forgeIssueBody is the artifact, written out for somebody who cannot read this
// node: the body, what was discovered, and where it came from.
func forgeIssueBody(art *store.Artifact) string {
	parts := []string{}
	if body := strings.TrimSpace(art.Body); body != "" {
		parts = append(parts, body)
	}
	if disc := strings.TrimSpace(art.Discovery); disc != "" {
		parts = append(parts, "**Discovery**\n\n"+disc)
	}
	parts = append(parts, "---\nFiled by flowy from "+art.Type+" `"+art.ID+"`.")
	return strings.Join(parts, "\n\n")
}

// forgeEvent builds the record of a move of the bridge itself - a filing - for
// the thread the issue's conversation uses.
//
// It is built rather than written: the entry and the link it describes go into
// the store together, in LinkArtifactExternal, because an entry saying a bug
// was filed on an artifact that says it was not is how the same bug gets filed
// twice.
func (s *server) forgeEvent(
	r *http.Request, art *store.Artifact, ref *store.ExternalRef, body string, extra map[string]string,
) (*store.Event, error) {
	p := principalOf(r)
	actor, kind := chatActor(p)

	fields := map[string]string{
		"actor_kind": kind,
		"actor_user": p.UserID,
		"forge":      ref.Forge,
		"repo":       ref.Repo,
		"number":     strconv.Itoa(ref.Number),
		"url":        ref.URL,
	}
	for k, v := range extra {
		fields[k] = v
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}

	parents := []string{}
	if last, err := s.db.LatestThreadEvent(r.Context(), ref.Thread); err == nil {
		parents = append(parents, last.ID)
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	return &store.Event{
		Type:     forgeEventType,
		Project:  art.Project,
		Room:     forgeRoom,
		Thread:   ref.Thread,
		Parents:  parents,
		Actor:    actor,
		Artifact: art.ID,
		Body:     body,
		Meta:     json.RawMessage(meta),
	}, nil
}

// readableArtifact reads the artifact the request names and answers 404 itself
// when the principal may not see it - which is the whole of the permission
// story here: file, status and sync are all gated on the ordinary read.
func (s *server) readableArtifact(
	w http.ResponseWriter, r *http.Request, id string,
) (*store.Artifact, bool) {
	if strings.TrimSpace(id) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("artifact is required"))
		return nil, false
	}
	art, err := s.db.ReadArtifact(r.Context(), principalOf(r), id, false)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such artifact"))
		return nil, false
	}
	if err != nil {
		serverError(w, r, err)
		return nil, false
	}
	return art, true
}

// filedArtifact is readableArtifact plus the link: an artifact nobody has filed
// has no state to read and no comments to thread.
func (s *server) filedArtifact(
	w http.ResponseWriter, r *http.Request, id string,
) (*store.Artifact, *store.ExternalRef, bool) {
	art, ok := s.readableArtifact(w, r, id)
	if !ok {
		return nil, nil, false
	}
	if art.External == nil {
		writeJSON(w, http.StatusBadRequest,
			errorBody("artifact "+art.ID+" has not been filed: POST /api/forge/file first"))
		return nil, nil, false
	}
	ref := art.External
	if ref.Thread == "" {
		// A link written before there was a thread, or one that arrived from a
		// peer without one. Give it one rather than dropping the comments.
		ref.Thread = s.forgeThread(r.Context(), art)
	}
	return art, ref, true
}

// --------------------------------------------------------------- the mock
//
// The mock forge's control surface, which is how a test plays the other side:
// the reviewer who closes an issue, the reviewer who comments on it, and the
// reader who checks what the node pushed.
//
// These routes are registered only when the mock is the selected forge, so on a
// node talking to GitHub they do not exist at all - there is no path here that
// could be used to make a real forge lie.

// mockIssueRequest addresses one issue on the mock forge.
type mockIssueRequest struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	State  string `json:"state"`
	Author string `json:"author"`
	Body   string `json:"body"`
}

// handleMockState moves an issue on the mock forge - what a reviewer closing it
// looks like from in here.
//
// POST /api/forge/mock/state  {repo, number, state}
func (s *server) handleMockState(w http.ResponseWriter, r *http.Request) {
	var req mockIssueRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	issue, err := s.mockForge.SetState(req.Repo, req.Number, req.State)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, issue)
}

// handleMockComment says something on a mock issue as somebody else.
//
// POST /api/forge/mock/comment  {repo, number, author, body}
func (s *server) handleMockComment(w http.ResponseWriter, r *http.Request) {
	var req mockIssueRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	if req.Author == "" || strings.TrimSpace(req.Body) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("author and body are required"))
		return
	}
	comment, err := s.mockForge.AddComment(req.Repo, req.Number, req.Author, req.Body)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, comment)
}

// mockFailRequest arms one refusal on the mock forge.
type mockFailRequest struct {
	After int `json:"after"`
}

// handleMockFail makes the mock forge refuse a comment, so a test can be the
// network going away in the middle of a reviewer loop rather than only ever the
// happy path.
//
// POST /api/forge/mock/fail  {after}
func (s *server) handleMockFail(w http.ResponseWriter, r *http.Request) {
	var req mockFailRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	s.mockForge.FailNext(req.After)
	writeJSON(w, http.StatusOK, map[string]any{"armed": true, "after": req.After})
}

// mockLoginRequest renames the login the mock posts under, and arms a comment
// for the window a filing takes.
type mockLoginRequest struct {
	Login  string `json:"login"`
	Author string `json:"author"`
	Body   string `json:"body"`
}

// handleMockLogin changes who the mock forge is logged in as. A real gh posts
// as whoever set the machine up, and a fake that is only ever called "flowy"
// cannot show that the node asked the forge rather than assumed.
//
// POST /api/forge/mock/login  {login}
func (s *server) handleMockLogin(w http.ResponseWriter, r *http.Request) {
	var req mockLoginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	s.mockForge.SetSelfAuthor(req.Login)
	writeJSON(w, http.StatusOK, map[string]any{"login": s.mockForge.SelfAuthor()})
}

// handleMockOnFile arms a comment that the next filing records as part of
// opening the issue: the reviewer who answered while the forge was still
// creating it. It is the filing window, made a fact rather than a race.
//
// POST /api/forge/mock/on-file  {author, body}
func (s *server) handleMockOnFile(w http.ResponseWriter, r *http.Request) {
	var req mockLoginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("body is required"))
		return
	}
	s.mockForge.CommentOnFile(req.Author, req.Body)
	writeJSON(w, http.StatusOK, map[string]any{"armed": true, "author": req.Author})
}

// handleMockIssue reads a mock issue back, comments and all - which is how a
// test asserts that a reply written here reached the forge.
//
// GET /api/forge/mock/issue?repo=&number=
func (s *server) handleMockIssue(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	number, err := strconv.Atoi(q.Get("number"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("number must be an integer"))
		return
	}
	issue, err := s.mockForge.Issue(q.Get("repo"), number)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, issue)
}
