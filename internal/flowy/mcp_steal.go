package flowy

// The rebalancing tool: asking for work somebody else is carrying, with a clock
// on the asking.
//
// There is nothing here but the tool declaration and one adapter. The steps, the
// deadline, what makes a request stale and every refusal wording are
// store.StealTodo's, and this is one caller of that path rather than a parallel
// implementation of it - the rule mcp_assign.go follows for the verb one field
// along. See the head of internal/store/steal.go for why the deadline is the
// whole point and why the take is restricted by seat rather than by handle.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

var stealTools = []tool{
	{
		Name: "todo_steal",
		Description: "Ask for a queue item somebody else is carrying, and take it if " +
			"nobody answers in time. This is how an agent with an empty queue gets work " +
			"from an agent with too much of it, and how work gets off a seat that died " +
			"or was decommissioned. Leave `step` out to ASK: it records the request on " +
			"the item, names a deadline, and says so in the item's room, where the " +
			"holder can see it. The holder answers `yes` (they hand it over now) or " +
			"`no` (they keep it, with a reason). If nobody answers by the deadline the " +
			"asker calls `take`, which is legal only then, only from the seat that " +
			"asked, and only while the same party still has it - a request against one " +
			"holder does not mature into a taking from another. `withdraw` drops your " +
			"own request. Every step is a signed entry, so agreed handovers and " +
			"unanswered takings stay tellable apart forever. This adds NO lock: " +
			"todo_assign still moves any item you can read, exactly as before, and the " +
			"log will say that is what you did. It works on merge requests too - a " +
			"merge request is work in the same queue.",
		InputSchema: object(props{
			"todo": str("The item's id: a todo, feature, handoff or merge request."),
			"step": enum("What you are doing. Leave it out to ask. `yes` and `no` are "+
				"the answer; `take` is the asker after the deadline; `withdraw` is the "+
				"asker calling it off.", store.StealSteps),
			"as": str("ASK ONLY. Who the work would go to - a handle, the same kind of " +
				"claim an assignee is. Usually you. Every later step is about the " +
				"request already on the item, so it is not read there."),
			"wait_minutes": integer("ASK ONLY. How long the holder has to answer. " +
				"Default 30. A live holder answers in seconds and a dead one never " +
				"will, which is what this number is for; it must be between 1 and 1440."),
			"reason": str("Why. Worth saying on a `no` above all - a refusal with a " +
				"reason is an answer, and one without is indistinguishable from " +
				"stubbornness."),
		}, []string{"todo"}),
		call: todoSteal,
	},
}

// todoSteal is the tool over store.StealTodo.
//
// wait arrives in MINUTES here and as a Duration below, because a tool argument
// somebody types wants a number with a named unit and the store wants the thing
// itself. Zero means "did not say", which the store turns into the default -
// deliberately not into an instant deadline.
func todoSteal(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Todo    string `json:"todo"`
		Step    string `json:"step"`
		As      string `json:"as"`
		Minutes int    `json:"wait_minutes"`
		Reason  string `json:"reason"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	// THE HOLDER HAS TO BE WOKEN, or the deadline is a formality.
	//
	// A steal entry lands in the item's room, but the inbox only ever delivers
	// CHAT events - so a holder who is alive and listening would hear nothing,
	// fail to answer, and have the work taken exactly as if they were dead. That
	// would make every request mature and turn a negotiation into a countdown.
	// So a step also says itself out loud, and it is handed to the verb rather
	// than posted after it: the room hearing about the ask and the ask existing
	// are one write or neither.
	said, err := stealSaid(ctx, m, p, a.Todo, a.Step, a.As, a.Reason)
	if err != nil {
		return nil, err
	}
	res, err := m.db.StealTodo(ctx, p, a.Todo, a.Step, a.As, a.Reason,
		time.Duration(a.Minutes)*time.Minute, said)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"item": res.Item, "step": res.Step, "assignee": res.Assignee,
		// Whether the room was told, because "the holder could have answered"
		// and "the holder never heard" are the difference between a deadline
		// that means something and one that only runs out.
		"announced": said != nil,
	}
	if res.Request != nil {
		out["request"] = res.Request
	}
	// Said whenever a step moved the work, including off nobody: a handover that
	// reports a bare success is how work moves off somebody silently, which is
	// the failure Held exists for one file along.
	if res.Held != "" {
		out["held"] = res.Held
	}
	return out, nil
}

// stealSaid is the chat message a step says in the item's room, or nil when
// there is nowhere to say it.
//
// nil for an item raised in no room at all: stealRoom parks the ENTRY under
// "steal" so it is findable, but nobody watches that as a room, and a message
// posted where nobody is reading is worse than none - it would let this answer
// claim the holder was told. A roomless item is negotiated by whoever is looking
// at it, and `announced` says so.
//
// It re-reads the item rather than taking the caller's word for the room,
// because the room decides who hears this and an argument does not.
func stealSaid(
	ctx context.Context, m *mcpServer, p *store.Principal, todo, step, by, reason string,
) (*store.Event, error) {
	art, err := m.db.ReadArtifact(ctx, p, strings.TrimSpace(todo), false)
	if err != nil {
		// Not this function's refusal to make: the verb re-reads the item and
		// answers an unreachable id exactly as a read of it would. Saying
		// nothing here leaves that answer intact instead of turning it into a
		// different error from a different place.
		return nil, nil
	}
	room := store.RoomOf(art)
	if room == "" {
		return nil, nil
	}
	// Who the sentence is about. On an ask it is the argument; on every other
	// step it is whoever the STANDING REQUEST names, because the argument is not
	// read there and a sentence built from it would say the work was handed to
	// nobody. If there is no standing request the verb is about to refuse, and
	// the sentence never reaches anybody.
	if strings.TrimSpace(step) != "" && !strings.EqualFold(step, store.StealAsk) {
		open, err := m.db.StealRequestOn(ctx, p, art.ID, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if open == nil {
			return nil, nil
		}
		by = open.By
	}
	actor, kind := chatActor(p)
	meta, err := json.Marshal(speakerMeta(p, kind, speakerNameOf(ctx, m.db, p)))
	if err != nil {
		return nil, err
	}
	return &store.Event{
		Type:    chatEventType,
		Project: art.Project,
		Room:    room,
		Actor:   actor,
		Body:    stealSentence(art.Title, store.AssigneeOf(art), step, by, reason),
		Meta:    meta,
	}, nil
}

// stealSentence is what the room reads. It names the holder, because the holder
// is who has to answer and a room full of agents needs to know which one is
// being asked; and on an ask it says what happens if nobody does, because that
// is the part somebody scrolling past has to act on.
func stealSentence(title, holder, step, by, reason string) string {
	if step == "" {
		step = store.StealAsk
	}
	switch strings.ToLower(step) {
	case store.StealAsk:
		return by + " asked " + holder + " for " + title +
			" - answer with todo_steal yes or no, or it can be taken after the deadline"
	case store.StealYes:
		return holder + " handed " + title + " to " + by
	case store.StealNo:
		s := holder + " kept " + title
		if strings.TrimSpace(reason) != "" {
			s += " - " + reason
		}
		return s
	case store.StealTake:
		return by + " took " + title + " off " + holder + " - the deadline passed unanswered"
	case store.StealWithdraw:
		return by + " withdrew the request for " + title
	}
	return step + " on " + title
}

// rebalanceCap is how many items of each sort the offer names. An offer is a
// prompt to act, not a second copy of the board: a hundred rows in an answer
// somebody got for asking "what is mine" is noise, and the counts beside the
// lists say what was left out - a cap nobody is told about reads as "that was
// all of them", which is the one thing a truncated list must not do.
const rebalanceCap = 10

// rebalanceItem is one row worth asking for, as the offer names it.
type rebalanceItem struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	Project string `json:"project,omitempty"`
	Status  string `json:"status,omitempty"`
	Holder  string `json:"holder,omitempty"`
	// Listening is what the node last saw of the holder's ear, and it is the
	// reason the offer is worth anything: a row held by a party with no live
	// tracked waiter is a row nobody is coming back for, and it is the one to ask
	// for first. "tracked" is listening and able to wake somebody; "forked" is
	// attached and unable to; "none" is a handle the node has never seen poll.
	Listening string `json:"listening"`
	// Asked is a request already standing on this row, so two agents do not both
	// ask for the same thing and start two clocks on one holder.
	Asked *store.StealRequest `json:"asked,omitempty"`
}

// rebalanceOffer is what an empty own-queue is answered with: the work that
// exists, who has it, and whether that party is still listening.
//
// Unowned rows come first and separately, because they need no negotiation at
// all - asking a deadline's worth of permission for work nobody is carrying
// would be a protocol wrapped around an assignment. Held rows are ordered by
// whether the holder is reachable, since that is the whole of what makes one
// candidate better than another here.
//
// nil when there is nothing to offer, so the answer stays an honest empty rather
// than gaining a block that says the board is empty too.
func rebalanceOffer(
	ctx context.Context, m *mcpServer, p *store.Principal,
	board []*store.Artifact, asker string,
) map[string]any {
	// Who has an ear on, by the label they poll under - which is the same kind of
	// string an assignee is, so a holder matches a listener by name or not at
	// all. A presence read that fails is not a reason to refuse the offer: it
	// downgrades every holder to "unknown", which is the safe direction (it makes
	// nobody look more reachable than they are).
	listening := map[string]string{}
	if rows, err := m.db.Presence(ctx); err == nil {
		for _, r := range rows {
			// Attached is a poll in flight AND recent enough to be one - see
			// PresenceRow. A row whose poll never ended is on the roster as
			// lost and does not read as attached here, so a holder whose seat
			// went deaf six hours ago is offered up for rebalancing instead of
			// counted as somebody you could go and ask.
			if !r.Attached {
				continue
			}
			// Tracked beats anything else already recorded for this label: one
			// live tracked waiter is enough to say somebody can be reached, and a
			// forked one beside it does not take that away.
			if listening[r.Reader] != store.WaiterTracked {
				listening[r.Reader] = r.Kind
			}
		}
	}

	now := time.Now().UTC()
	var unowned, held []rebalanceItem
	var unownedTotal, heldTotal int
	for _, art := range board {
		holder := store.AssigneeOf(art)
		if holder == asker {
			continue
		}
		item := rebalanceItem{
			ID: art.ID, Title: art.Title, Kind: art.Kind,
			Project: deref(art.Project), Status: art.Status, Holder: holder,
		}
		if holder == "" {
			unownedTotal++
			if len(unowned) < rebalanceCap {
				unowned = append(unowned, item)
			}
			continue
		}
		item.Listening = listening[holder]
		if item.Listening == "" {
			item.Listening = "none"
		}
		heldTotal++
		held = append(held, item)
	}
	// Which of these somebody is already waiting on, in ONE query rather than a
	// fold per row: a request is the fold of an item's log now, and asking per
	// row would put a query on every line of the offer. A read that fails leaves
	// every Asked nil, which understates rather than invents.
	if len(held) > 0 {
		ids := make([]string, 0, len(held))
		for _, it := range held {
			ids = append(ids, it.ID)
		}
		if asked, err := m.db.StealRequests(ctx, p, ids, now); err == nil {
			for i := range held {
				held[i].Asked = asked[held[i].ID]
			}
		}
	}
	if unownedTotal == 0 && heldTotal == 0 {
		return nil
	}
	// Unreachable holders first: those are the rows whose deadline will actually
	// have to expire, and the ones the operator raised this for.
	sort.SliceStable(held, func(i, j int) bool {
		return reachRank(held[i].Listening) < reachRank(held[j].Listening)
	})
	if len(held) > rebalanceCap {
		held = held[:rebalanceCap]
	}

	out := map[string]any{
		"unowned": unowned, "unowned_total": unownedTotal,
		"held": held, "held_total": heldTotal,
		"showing": rebalanceCap,
		"how": "Unowned items need no negotiation - todo_assign one to yourself. For an " +
			"item somebody is carrying, todo_steal {todo, as: <you>} asks and starts a " +
			"clock; the holder answers yes or no, and if nobody answers by the deadline " +
			"todo_steal {todo, step: take} is yours to call. Ask for one held by a party " +
			"whose listening is \"none\" or \"forked\" first: those are the ones nobody " +
			"is coming back for.",
	}
	return out
}

// reachRank orders holders by how likely they are to answer at all: a party with
// no waiter is the best candidate to ask, a forked one is next (attached and
// unable to wake anybody), and a tracked one is the one actually working.
func reachRank(kind string) int {
	switch kind {
	case store.WaiterTracked:
		return 2
	case store.WaiterForked:
		return 1
	default:
		return 0
	}
}

// deref is the empty string for an absent project. An artifact's project is a
// pointer because a personal item is in none, and "none" is what an offer should
// print rather than a nil that renders as null beside nine real names.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
