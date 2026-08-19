package main

// POST /api/merge/{id}/land {sha} - record what master became, release the lock.
//
// The fast-forward itself is the lander's, in the repository: the node has no
// git and should not. This door is the other half of landing - the record and
// the exclusivity. A land that does not pass through it leaves the lock held
// until expiry and the row open with no landed_tip, which are both visible,
// which is the point: the queue can now SEE a land that skipped the protocol
// instead of silently believing a fast-forward nobody announced.
//
// 409 for a lost lock, 400 for everything else the store refuses. The refusal
// sentences are the store's, so the door and the MCP surface, if one grows
// here, cannot disagree about what a land needs.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// mergeLandRequest is the sha master became, as it arrives on the wire. Named
// for the same reason mergeGateRequest is, and decoded by the same strict
// decoder: a misspelt `sha` here refuses with a sentence about the sha being
// too short to name a commit, which is a true refusal for a reason that is not
// the caller's actual mistake.
type mergeLandRequest struct {
	SHA string `json:"sha"`
}

func (s *server) handleMergeLand(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req mergeLandRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	// WHAT LANDED, SAID IN THE ROOM. The store already writes a merge.land
	// entry carrying the room - and an entry is not a message, so no room view,
	// TUI or waiter shows it. The measured consequence: fifteen landings in one
	// evening were announced BY HAND, every one the same four facts, and the
	// ones that were not announced were invisible to everybody but the person
	// who ran them.
	//
	// Same shape as the handover door: an ordinary chat message in the room the
	// row was raised in, written in the same transaction as the landing.
	said, err := s.landingHeardIn(r, r.PathValue("id"), req.SHA)
	if err != nil {
		serverError(w, r, err)
		return
	}
	art, entry, err := s.db.LandMerge(r.Context(), p, r.PathValue("id"), req.SHA, said...)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such merge request"))
		return
	}
	var held *store.ErrLandRefused
	if errors.As(err, &held) && held.Held != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": err.Error(),
			"held":  true,
			"lock":  held.Held,
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": art, "event": entry})
}

// landingHeardIn is the message the room reads when a branch lands, or nothing
// when the row is in no room.
//
// READ BEFORE THE LANDING, because afterwards the row is closed and the
// sentence is about what it WAS. A failure to build it is not a failure to
// land: the room hearing about a landing matters less than the landing, and a
// node that refused to land because it could not announce would trade a silent
// success for a loud nothing.
//
// It says the branch, the sha and the target - the facts this door has. It does
// NOT say the gate numbers, because this node does not have them: the verdict
// carries a tip, not a count, and the passed/failed line lives in the drainer's
// log. Inventing them here would be the announcement claiming more than the
// store knows, which is the failure the whole authorship effort exists about,
// one floor down.
// landingRoom is where a landing is said when its row names no room. It is the
// room this fleet reads, and it is a constant rather than a setting because a
// node that could be configured to announce into a room nobody reads would have
// a way to be quiet that looks like a way to be loud.
const landingRoom = "general"

func (s *server) landingHeardIn(r *http.Request, id, sha string) ([]*store.Event, error) {
	art, err := s.db.ReadArtifact(r.Context(), principalOf(r), id, false)
	if err != nil {
		// The verb answers not-found and not-readable; answering them here
		// would answer them twice.
		return nil, nil
	}
	fields, err := store.ArtifactFields(art)
	if err != nil {
		return nil, nil
	}
	// A LANDING IS FLEET NEWS, NOT CONVERSATION NEWS, which is where this
	// differs from the handover door and why it needs a fallback the other one
	// must not have.
	//
	// A handover belongs to the conversation that raised the work: no room, no
	// audience, nothing to say. A landing on master is read by every seat here
	// whatever conversation produced it - and MEASURED on 2026-08-19, four of
	// the six merge rows landed that day carried no room at all, because the
	// queue is filed through the artifacts door and nothing there asks for one.
	// So the first cut of this announced about a third of landings and was
	// silent for the rest, which is worse than uniformly silent: it reads as
	// "nothing landed" rather than as "this tool does not cover that".
	//
	// NOT categoryRoom's fallback, which the store's own merge.land entry uses.
	// That is CategoryRoom, "category" - a room nobody watches - so routing
	// there would be silence dressed as speech, and the two of those are the
	// pair this fleet has spent two days telling apart.
	room, _ := fields[store.RoomField].(string)
	if strings.TrimSpace(room) == "" {
		room = landingRoom
	}
	p := principalOf(r)
	actor, kind := chatActor(p)
	meta, err := json.Marshal(speakerMeta(p, kind, s.speakerName(r.Context(), p)))
	if err != nil {
		return nil, err
	}
	branch := store.BranchOf(art)
	if branch == "" {
		branch = art.Title
	}
	body := "landed " + branch + " as " + sha
	if target := store.TargetOf(art); target != "" {
		body += " on " + target
	}
	return []*store.Event{{
		Type:   chatEventType,
		Room:   room,
		Thread: art.ID,
		Actor:  actor,
		Body:   body,
		Meta:   withTrace(json.RawMessage(meta), traceIDOf(r)),
	}}, nil
}
