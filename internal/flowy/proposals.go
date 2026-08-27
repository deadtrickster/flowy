package flowy

// Proposals over HTTP: the read half, so the console can show what a room
// agreed to without going through MCP.
//
// It is deliberately read-only in this pass. The console's room panel is being
// worked on by somebody else, and a second hand in it is how a bad merge
// happens - so the data and the API land here and the view does not. What the
// two routes answer is what a view would need: the proposal, its votes in log
// order, and the tally that reads them.
//
// The permission story is the store's and not a second one: both routes go
// through ReadProposal and ProposalVotes, which put the filter in the WHERE
// clause. A proposal you may not read is answered as one that is not here.

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/deadtrickster/flowy/internal/store"
)

// proposalType is the artifact type the whole surface reads and writes.
const proposalType = store.ProposalType

// proposalView is one proposal as every surface hands it back: the artifact,
// the votes behind it and what they add up to.
//
// The votes are in it rather than behind a second call because the tally is
// the derived number and the log is the record - a reader given only the tally
// would have to know to go and ask for the history, and the whole point of
// this surface is that the history is the thing.
type proposalView struct {
	Item     *store.Artifact `json:"item"`
	Votes    []store.Vote    `json:"votes"`
	Tally    store.Tally     `json:"tally"`
	Closed   bool            `json:"closed"`
	ClosedAt string          `json:"closed_at,omitempty"`
	Outcome  string          `json:"outcome,omitempty"`
}

// viewProposal reads the votes on a proposal already read and assembles the
// answer. The artifact has been through the read filter by the time it gets
// here; the votes go through it again on their own way out.
//
// Both surfaces call this - the route below and proposal_read - so a console
// and an agent are looking at one answer rather than at two that agree today.
func viewProposal(
	ctx context.Context, db *store.DB, p *store.Principal, art *store.Artifact,
) (*proposalView, error) {
	votes, err := db.ProposalVotes(ctx, p, art.ID)
	if err != nil {
		return nil, err
	}
	at, outcome := store.ProposalClosure(art)
	return &proposalView{
		Item: art, Votes: votes, Tally: store.TallyOf(votes),
		Closed: at != "", ClosedAt: at, Outcome: outcome,
	}, nil
}

// handleGetProposal reads one proposal, with its votes and its tally.
//
// GET /api/proposal/{id}
func (s *server) handleGetProposal(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	art, err := s.db.ReadProposal(r.Context(), p, r.PathValue("id"), scopeAll(r, p))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such proposal"))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	view, err := viewProposal(r.Context(), s.db, p, art)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleListProposals lists the proposals this principal may read, newest
// first, optionally narrowed to a room or to the ones still open.
//
// GET /api/proposals?room=general&status=open&limit=50
func (s *server) handleListProposals(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := store.ArtifactQuery{Type: proposalType, ScopeAll: scopeAll(r, p)}

	room, err := roomArg(r.URL.Query().Get("room"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	q.Room = room
	if status := r.URL.Query().Get("status"); status != "" {
		if status != store.ProposalOpen && status != store.ProposalClosed {
			writeJSON(w, http.StatusBadRequest,
				errorBody("status is "+store.ProposalOpen+" or "+store.ProposalClosed+", not "+status))
			return
		}
		q.Status = status
	}
	if limit := r.URL.Query().Get("limit"); limit != "" {
		n, err := strconv.Atoi(limit)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("limit is a number"))
			return
		}
		q.Limit = n
	}

	list, err := s.db.ListArtifacts(r.Context(), p, q)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(list), "items": list})
}
