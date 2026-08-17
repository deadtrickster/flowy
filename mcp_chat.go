package main

// The chat tools: saying something in a room, and reading one back.
//
// THIS IS THE VERB THE SURFACE DID NOT HAVE. Everything else here - the todos,
// the deps, the merge queue, the reports, the worklog - lets an agent act on the
// board and say nothing about it. So an agent whose only door is MCP could read
// a room's work and could not answer the room, and the nearest thing in its tool
// list was some other system's chat: the agent this was reported by answered
// fourteen messages into a host harness's room while believing it was replying
// here, and nobody saw any of them until the operator asked why it never
// replied. A verb that is missing does not read as missing; it reads as the
// wrong verb being the right one.
//
// There is nothing here but the two declarations and two adapters. The refusals,
// the permission filter, the speaker's name, the thread rules and the citation
// handling are sayInRoom's and roomBefore's in chat.go - the same functions
// POST /api/chat/{room}/say and GET /api/chat/{room} run - and this is one more
// caller of that path rather than a parallel implementation of it. That is the
// whole point: a second implementation is where an agent's messages arrive with
// no speaker, or land in a conversation the room cannot read, or answer a
// refusal the HTTP door would have made. See mcp_assign.go and mcp_steal.go for
// the same shape over the verbs one field along.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// chatTools is the room surface, appended in allTools beside the assignment and
// rebalancing tools rather than written into the memory list, so each surface
// stays its own file.
var chatTools = []tool{
	{
		Name: "chat_say",
		Description: "Say something in a room. This is how you ANSWER - the operator, " +
			"another agent, anybody watching a room - and it is the only way from here " +
			"to do it: reading a room's todos is not being in the room. The message " +
			"lands in the log as an event like any other, under your name, readable by " +
			"exactly the principals who already read that room. `to` addresses it at one " +
			"principal by handle or by id: it ROUTES AND WAKES rather than hides, since " +
			"an addressed message reaches the named seat's inbox and forces a turn there, " +
			"while an unaddressed one is ambient and may be read whenever somebody looks. " +
			"Address the person or agent you are answering, or your reply may sit unread " +
			"beside a question they are still waiting on. Writing @their-name in the text " +
			"does the same thing - the name in the prose IS the addressing. Leave `thread` " +
			"out to start a conversation and pass back the thread you were given to " +
			"continue one.",
		InputSchema: object(props{
			"room": str("The room to say it in - one segment, no slashes. A room exists " +
				"because somebody spoke in it; there is nothing to create. `general` is " +
				"where a fleet talks."),
			"text": str("What you are saying, as prose. It is read by people."),
			"to": str("Who it is for: a handle or a principal id. This wakes that seat. " +
				"Leave it out and the message is for the room. A name nothing answers to " +
				"is refused here rather than delivered to nobody."),
			"thread": str("The conversation to continue. Leave it out to start one; the " +
				"answer carries the thread you were given, and passing it back is what " +
				"keeps a reply beside what it answers."),
		}, []string{"room", "text"}),
		call: chatSay,
	},
	{
		Name: "chat_read",
		Description: "Read a room: the NEWEST messages in it, oldest-first, filtered to " +
			"what you may see. This is the other half of chat_say - what was said while " +
			"you were working, and the context a reply needs. It opens on the END of the " +
			"log the way the console does, because the beginning of a busy room is the " +
			"one page nobody wants; `before` pages backwards from there, one window at a " +
			"time, using the `before` the previous answer handed you. A room you cannot " +
			"read and a room nobody has ever spoken in answer the same way - an empty " +
			"list - because that is what a read of it says, and an answer that " +
			"distinguished them would be telling you a room exists that you may not see.",
		InputSchema: object(props{
			"room":  str("The room to read - one segment, no slashes."),
			"limit": integer("How many messages. The newest ones. Default and cap are the store's."),
			"before": str("Page backwards: only messages OLDER than this reading, which " +
				"is the `before` from your previous answer. Leave it out for the newest " +
				"window. It is a cursor and not a time - hand back what you were given."),
		}, []string{"room"}),
		call: chatRead,
	},
}

// chatSay is the tool over sayInRoom.
//
// The argument is `text` and the field it fills is Body, which is the one place
// this adapter renames anything: a tool argument is read by a model choosing
// what to type, and `text` is what it is. Everything else is handed over as it
// arrived - `to` is not resolved here, the thread is not defaulted here, and the
// speaker is not named here, because all three are decisions the say path makes
// for both doors at once.
func chatSay(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Room   string `json:"room"`
		Text   string `json:"text"`
		To     string `json:"to"`
		Thread string `json:"thread"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	// The say path refuses this anyway - it is the same rule and it stays down
	// there - but it refuses it as "body is required", naming a field this tool
	// does not have. An agent told to fix an argument it never sent will send the
	// same call again.
	if strings.TrimSpace(a.Text) == "" {
		return nil, errors.New("text is required: a message with nothing in it says nothing")
	}
	said, err := sayInRoom(ctx, m.db, p, a.Room, chatSayRequest{
		Body:   a.Text,
		To:     a.To,
		Thread: a.Thread,
	})
	if err != nil {
		return nil, chatRefusal(err)
	}
	// The message itself, and then the three things a caller acts on next: the
	// thread to pass back to keep answering in the same conversation, WHICH
	// PRINCIPAL the name in `to` turned out to be - a handle is a spelling and
	// the addressee is an id - and the reading to read the room from, so an
	// agent that says something and then looks does not re-read its own message.
	return map[string]any{
		"message": said,
		"thread":  said.Thread,
		"to":      said.Addressee,
		"cursor":  strconv.FormatInt(said.SeqHLC, 10),
	}, nil
}

// chatRead is the tool over roomBefore: the same read, the same filter and the
// same citation resolution the console's backwards page uses.
//
// scope=all is not offered. It is a view of the node rather than of a room, it
// belongs to this node's operator, and a tool that took it would be a permission
// filter with an argument.
func chatRead(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Room   string `json:"room"`
		Limit  int    `json:"limit"`
		Before string `json:"before"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	room, err := roomNamed(a.Room)
	if err != nil {
		return nil, chatRefusal(err)
	}
	// The door's own parser, so a cursor means the same thing here as in the
	// query string it came from.
	before, err := cursorParam(a.Before)
	if err != nil {
		return nil, err
	}
	list, err := roomBefore(ctx, m.db, p, room, "", before, a.Limit, false)
	if err != nil {
		return nil, err
	}
	cursor, older := chatWindowEnds(before, list)
	// Both cursors as strings, and that is not decoration. A packed hlc is a
	// wall clock shifted left sixteen bits, which is well past the 53 bits a
	// JSON number survives in a client that parses one as a double - so a cursor
	// handed back as a number could come back rounded, and a rounded cursor
	// skips messages that are never redelivered and never reported. A string
	// round-trips through every client there is.
	return map[string]any{
		"room":   room,
		"events": list,
		"cursor": strconv.FormatInt(cursor, 10),
		"before": strconv.FormatInt(older, 10),
	}, nil
}

// chatRefusal says a chat rule's refusal in this transport's idiom.
//
// A permission refusal - a thread that is not this principal's to write into -
// is a PROTOCOL error here rather than a tool result, for the reason written
// down at `forbidden` in mcp.go: a refusal that arrives in a success envelope is
// a refusal nobody notices, and the failure this whole surface keeps producing
// is a write that reports success and changes nothing. Everything else is an
// ordinary tool error: a room that is not one segment, a name nothing answers
// to, an id out of reach. The wording is the rule's own either way.
func chatRefusal(err error) error {
	var fault chatFault
	if errors.As(err, &fault) && fault.status == http.StatusForbidden {
		return refuseForbidden("%s", fault.why)
	}
	return err
}
