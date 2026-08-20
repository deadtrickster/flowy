package main

// The attachment tools. An attachment is an artifact of type 'attachment' with
// bytes: title, project, scope, owner, tags, the same permission filter and the
// same event on write as a memory item or a report. There is no second store
// and no second visibility rule - what is new is the payload, and it is the
// only new thing.
//
// It exists because the surfaces around it already promised it. report_write
// refuses a body over 100KB and tells the caller to attach the document
// instead; agents hand each other logs, diffs and captured output all night and
// the only place to put them was a message body, where a megabyte of stack
// trace is unreadable, unbounded, and in the append-only log every reader pages
// through. So:
//
//   - the bytes are out of both. events keeps its rows small, artifacts.body
//     stays the prose that search reaches, and the payload lives in
//     attachment_bytes - see the table's comment in schema.sql, and note that
//     artifacts.body is text and could not have carried a NUL byte anyway.
//   - there is a ceiling and the refusal names it. Nothing here truncates:
//     half a log that does not say it is half is the failure mode this surface
//     is supposed to remove, not one to introduce.
//   - the content type is recorded as a CLAIM and is never what a reader
//     renders from. See the field names below.
//   - an attachment is written once. There is no id argument and no update
//     path: the bytes somebody was handed a digest for do not change under
//     them afterwards.
//
// The verbs mirror the memory and report tools - write, read, list - so an
// agent that has learned mem_* and report_* transfers here with no brief.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// attachmentType is the artifact type every one of these tools reads and writes.
const attachmentType = "attachment"

// maxAttachment is the ceiling on one attachment's bytes, after decoding.
//
// It is what fits through the transport that carries it. One JSON-RPC message
// is capped at maxLine (8 MiB) on both transports, the content rides it as
// base64, and base64 is four bytes for every three - so a ceiling above 6 MiB
// would be a ceiling the pipe refuses first, in a place that says "parse error"
// rather than saying what is wrong. 4 MiB leaves the envelope room and is
// several times the size of the logs and diffs this exists for.
//
// Over it is a refusal that names this number. It is never a truncation:
// somebody debugging against the first half of a log, with nothing on the page
// saying it is the first half, loses more time than the failed upload would
// have cost them.
const maxAttachment = 4 << 20

// maxClaimedType and maxFilename cap the two strings a client makes up about
// its own bytes. They are recorded, so they are bounded: a field kept only to
// be shown back to a person is not a place to put a megabyte.
const (
	maxClaimedType = 255
	maxFilename    = 255
)

// The fields an attachment carries, beside the room and the message every
// artifact can carry. They ride fields rather than columns, the way as_of and
// supersedes ride a report.
//
// The naming is the security property, not decoration. contentTypeField holds
// what the BYTES are, sniffed here from the payload; claimedTypeField holds what
// the client said they were. A console that renders whatever a client claimed
// is an injection surface - "image/png" on a payload of markup is how that goes
// wrong - so the obvious name, the one a render path reaches for without
// thinking, is the one derived from the content, and the claim is parked under
// a name that says out loud that it is a claim.
const (
	contentTypeField = "content_type"
	claimedTypeField = "claimed_type"
	sizeField        = "size"
	digestField      = "sha256"
	filenameField    = "filename"
)

// The kinds, derived from the bytes and never from the claim: an attachment is
// text or it is not. It is a filter for lists - "the logs" against "the
// screenshots" - and it is the same column mem_list narrows on.
const (
	attachmentText   = "text"
	attachmentBinary = "binary"
)

var attachmentKinds = []string{attachmentText, attachmentBinary}

// attachmentTools is the attachment surface, appended in allTools rather than
// written into the memory list, so each surface stays its own file - the rule
// the report and observability tools follow.
var attachmentTools = []tool{
	{
		Name: "attachment_write",
		Description: "Attach bytes to the project: a log, a diff, a capture, a screenshot - " +
			"anything a message body or a report body is the wrong place for. " +
			"The content is base64 and is stored exactly, byte for byte. " +
			"Up to 4194304 bytes; over that is refused with the number, never truncated. " +
			"Born at scope=project. Written once - there is no update.",
		InputSchema: object(props{
			"title": str("One line: what these bytes are, and of what run."),
			"content_base64": str("The bytes, base64 encoded. Encode whatever you have - " +
				"a text log goes in as base64 of its UTF-8 bytes - because that is what " +
				"makes a binary come back out identical."),
			"content_type": str("What you believe the bytes are, e.g. text/plain, image/png. " +
				"Recorded as your claim. It is not what a reader renders from: that is " +
				"decided here, from the bytes."),
			"filename": str("The name the bytes had where they came from, e.g. build.log. " +
				"Recorded for a person to read; it is not a path and is not used as one."),
			"body":  str("Optional prose: what to look at in it, what run it came from. Searched."),
			"scope": enum("Who may read it. Default project.", memScopes),
			"tags":  strArray("Free-form labels; searched with the title and the body."),
			"room":  str("The chat room this belongs to. A filter, not a permission."),
			"message": str("Id of the chat message this is attached to - the conversation it " +
				"came out of. A message you cannot read is refused."),
		}, []string{"content_base64"}),
		call: attachmentWrite,
	},
	{
		Name: "attachment_read",
		Description: "Fetch one attachment by id: the bytes, base64, exactly as they were " +
			"written, with what they are and how big they are. An attachment you may " +
			"not read is reported exactly as one that does not exist.",
		InputSchema: object(props{"id": str("The attachment's id.")}, []string{"id"}),
		call:        attachmentRead,
	},
	{
		Name:        "attachment_list",
		Description: "List attachments you may read, newest first. The bytes are not in the list - fetch one by id.",
		InputSchema: object(props{
			"scope": enum("Narrow to one scope.", memScopes),
			"kind":  enum("Narrow to text attachments or binary ones. Decided from the bytes.", attachmentKinds),
			"limit": integer("Most attachments to return. Default 200."),
		}, nil),
		call: attachmentList,
	},
}

// attachmentWriteArgs is what attachment_write takes. There is no id: see the
// file comment on why an attachment is written once.
type attachmentWriteArgs struct {
	Title    string   `json:"title"`
	Content  string   `json:"content_base64"`
	Type     string   `json:"content_type"`
	Filename string   `json:"filename"`
	Body     string   `json:"body"`
	Scope    string   `json:"scope"`
	Tags     []string `json:"tags"`
	Room     string   `json:"room"`
	Message  string   `json:"message"`
}

// noSuchAttachment is the only thing an unreadable id ever gets back - the same
// answer for an id that is not here, one that was deleted, and one that is here
// and out of reach.
func noSuchAttachment(id string) error { return fmt.Errorf("no such attachment: %s", id) }

func attachmentWrite(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a attachmentWriteArgs
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	written, err := writeAttachmentFrom(ctx, m.db, p, a)
	if err != nil {
		return nil, err
	}
	// The artifact, what the bytes are, and - when this token writes into a
	// fixture - the sentence nobody was shown the day a week of real memory
	// went into pa. See mcp_projects.go. The content is not echoed back: the
	// caller has it, and a megabyte round trip for nothing is a megabyte
	// through the model's context.
	return withFixtureWarning(ctx, m, p, map[string]any{
		"item":           written.Item,
		sizeField:        written.Size,
		digestField:      written.Digest,
		contentTypeField: written.ContentType,
	}), nil
}

// writtenAttachment is what both doors say about a capture that landed: the
// row, and the three facts about the bytes that are not on it.
type writtenAttachment struct {
	Item        *store.Artifact
	Size        int
	Digest      string
	ContentType string
}

// writeAttachmentFrom is what WRITING AN ATTACHMENT IS: the checks, the scope,
// the message it hangs off, what the bytes turn out to be, and the row.
//
// It is a function of a context and a database rather than of an MCP server for
// sayInRoom's reason, and the defect is the same one measured a different way.
// attachment_write was the ONLY caller, so an agent could attach a file to a
// message and a person could not - the console renders a card for bytes it has
// no way to produce. A second implementation behind POST /api/attachment would
// be a second answer to every question here: what scope a capture is born at,
// whether the message is readable, what a claimed content type may say, and
// what the filename is allowed to steer. Those are decisions, and they are made
// once.
func writeAttachmentFrom(
	ctx context.Context, db *store.DB, p *store.Principal, a attachmentWriteArgs,
) (*writtenAttachment, error) {
	if p.UserID == "" {
		return nil, errors.New("this token resolves to no user, so it cannot own an attachment")
	}

	content, err := decodeAttachment(a.Content)
	if err != nil {
		return nil, err
	}
	claimed, err := claimedType(a.Type)
	if err != nil {
		return nil, err
	}
	filename, err := attachmentFilename(a.Filename)
	if err != nil {
		return nil, err
	}

	// An attachment is born in the project, like a report and unlike a memory
	// item: a fact is usually somebody's before it is anybody's, and a capture
	// exists because somebody else has to look at it.
	scope, err := oneOf("scope", a.Scope, memScopes, "project")
	if err != nil {
		return nil, err
	}
	visibility := visibilityOf(scope)

	room, err := roomArg(a.Room)
	if err != nil {
		return nil, err
	}
	// The message it is attached to, through the same check and the same field
	// a todo raised out of a conversation uses. It is deliberately not a second
	// linking mechanism: a message names an artifact on the event row already,
	// and the item names the message in fields already, so an attachment is
	// reachable from a conversation with nothing new to learn.
	if err := readableMessage(ctx, db, p, a.Message); err != nil {
		return nil, err
	}

	sniffed := sniffType(content)
	kind := attachmentBinary
	if strings.HasPrefix(sniffed, "text/") {
		kind = attachmentText
	}
	sum := sha256.Sum256(content)

	fields := map[string]any{
		contentTypeField: sniffed,
		sizeField:        len(content),
		digestField:      hex.EncodeToString(sum[:]),
	}
	if claimed != "" {
		fields[claimedTypeField] = claimed
	}
	if filename != "" {
		fields[filenameField] = filename
	}
	fields = withRoom(fields, room, strings.TrimSpace(a.Message))
	rawFields, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}

	art := &store.Artifact{
		Type:  attachmentType,
		Kind:  kind,
		Title: attachmentTitle(a.Title, filename),
		Body:  a.Body,
		Tags:  a.Tags,
		// Not FilePath. That column names a file in the FUSE mount, and a name
		// a client made up has no business steering where the mount writes.
		Fields:     rawFields,
		OwnerUser:  p.UserID,
		Visibility: visibility,
	}
	if visibility == store.VisibilityPersonal {
		// The floor is a property of the row, whatever anyone writes into
		// grants afterwards.
		art.Project = nil
	} else {
		if p.Project == "" {
			return nil, fmt.Errorf("this token has no project, so it can only write scope=personal, not %s",
				scopeOf(visibility))
		}
		here := p.Project
		art.Project = &here
	}

	actor := p.AgentID
	if actor == "" {
		actor = p.UserID
	}
	if err := db.WriteAttachment(ctx, art, content, &store.Event{
		Type:  "attachment.write",
		Room:  "attachments",
		Actor: actor,
		Body:  art.Title,
	}); err != nil {
		return nil, err
	}
	return &writtenAttachment{
		Item:        art,
		Size:        len(content),
		Digest:      hex.EncodeToString(sum[:]),
		ContentType: sniffed,
	}, nil
}

func attachmentRead(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	if a.ID == "" {
		return nil, errors.New("id is required")
	}

	art, content, err := m.db.ReadAttachment(ctx, p, a.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, noSuchAttachment(a.ID)
	}
	if err != nil && !errors.Is(err, store.ErrNoBytes) {
		return nil, err
	}

	// The namespace answer comes FIRST, ahead of anything this surface has to
	// say about the bytes. Every readable artifact that is not an attachment
	// has no attachment_bytes row, so an order that reported the missing
	// payload first answered "this id is here and its content is not" for every
	// memory item, report and bug the caller could read - which is a probe for
	// what is readable, told in a sentence about attachments. It is the same
	// answer an id that is not there gets: one namespace, and no way to
	// enumerate another one through this door.
	if art == nil || art.Type != attachmentType {
		return nil, noSuchAttachment(a.ID)
	}
	if errors.Is(err, store.ErrNoBytes) {
		// An attachment, readable, and the payload is not on this node. Said
		// plainly rather than as an empty file, which is the shape a peer that
		// pulled the row will hit - see the attachment_bytes comment in
		// schema.sql.
		return nil, fmt.Errorf("attachment %s is here and its bytes are not: "+
			"the row replicated to this node and the content did not", a.ID)
	}

	// What the row says it is, checked against what came back. The digest is
	// inside the artifact's signature - fields is signed, see
	// internal/sign.CanonicalArtifact - and the bytes are outside it, so this
	// is the one comparison that says whether the payload is still the payload
	// the author signed for. A mismatch is refused rather than served with a
	// note: somebody is about to debug against these bytes.
	sum := sha256.Sum256(content)
	got := hex.EncodeToString(sum[:])
	stored := attachmentField(art, digestField)
	if stored != "" && stored != got {
		return nil, fmt.Errorf("attachment %s does not match its signed digest: "+
			"the row says %s and the bytes here are %s", a.ID, stored, got)
	}

	return map[string]any{
		"item":           art,
		"content_base64": base64.StdEncoding.EncodeToString(content),
		sizeField:        len(content),
		digestField:      got,
		// From the bytes, both here and on the way in. A reader that renders
		// this is rendering what the payload is; claimed_type on the item is
		// what the client said, and is for a person to read.
		contentTypeField: sniffType(content),
	}, nil
}

func attachmentList(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Scope string `json:"scope"`
		Kind  string `json:"kind"`
		Limit int    `json:"limit"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	q := store.ArtifactQuery{Type: attachmentType, Limit: a.Limit}
	if a.Scope != "" {
		v, err := oneOf("scope", a.Scope, memScopes, "")
		if err != nil {
			return nil, err
		}
		q.Visibility = visibilityOf(v)
	}
	if a.Kind != "" {
		k, err := oneOf("kind", a.Kind, attachmentKinds, "")
		if err != nil {
			return nil, err
		}
		q.Kind = k
	}

	list, err := m.db.ListArtifacts(ctx, p, q)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(list), "items": list}, nil
}

// decodeAttachment turns the argument into the bytes that will be stored, or
// says why it will not be.
//
// Whitespace is stripped first because a client that wrapped its base64 at 76
// columns is not making a mistake, and a decoder that refused it would send the
// agent looking at its payload rather than at its lines. Unpadded base64 is
// accepted for the same reason. Neither changes a single byte of the result.
func decodeAttachment(arg string) ([]byte, error) {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		}
		return r
	}, arg)
	if cleaned == "" {
		// Empty is not legal. An attachment is bytes; a row that says "here is
		// the log" and carries nothing is the same lie as a truncated one, and
		// the writer is the only person who can still fix it cheaply.
		return nil, errors.New("content_base64 is empty - an attachment with no bytes " +
			"is a claim that something was captured when nothing was; " +
			"attach the bytes, or write it as a memory item or a report instead")
	}

	content, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		if raw, rawErr := base64.RawStdEncoding.DecodeString(cleaned); rawErr == nil {
			content = raw
		} else {
			return nil, fmt.Errorf("content_base64 is not base64: %w - the bytes go in "+
				"encoded, which is what makes them come back out identical", err)
		}
	}
	if len(content) > maxAttachment {
		return nil, fmt.Errorf("attachment is %d bytes, over the %d ceiling - "+
			"nothing here is truncated, because half a log that does not say it is "+
			"half costs more than a refused upload; attach the part that matters, "+
			"or split it across attachments", len(content), maxAttachment)
	}
	if len(content) == 0 {
		// Base64 of nothing, which arrived as something. Same answer.
		return nil, errors.New("content_base64 decodes to no bytes - an attachment " +
			"with no bytes is a claim that something was captured when nothing was")
	}
	return content, nil
}

// sniffType is what the bytes are, decided here and never taken from the
// caller. It is the standard library's sniffer, which reads at most the first
// 512 bytes and answers application/octet-stream when it does not recognise
// them - which is the right answer for a render path to be given.
func sniffType(content []byte) string { return http.DetectContentType(content) }

// claimedType bounds the string a client makes up about its own bytes.
//
// Control characters are refused, not stripped. The value is recorded to be
// shown back to a person, and the places it could end up - a header, a log
// line, a terminal - are all places where a newline in the middle of it is
// somebody else's sentence.
func claimedType(claim string) (string, error) {
	claim = strings.TrimSpace(claim)
	if claim == "" {
		return "", nil
	}
	if len(claim) > maxClaimedType {
		return "", fmt.Errorf("content_type is %d bytes, over the %d ceiling - "+
			"it is a media type, not a document", len(claim), maxClaimedType)
	}
	if strings.ContainsFunc(claim, isControl) {
		return "", errors.New("content_type carries a control character, and a media " +
			"type has none - a newline in a recorded string becomes somebody else's line later")
	}
	return claim, nil
}

// attachmentFilename bounds and refuses the other made-up string.
//
// A separator or a .. is refused rather than trimmed, and that is worth being
// blunt about: nothing here treats this value as a path today, and the way it
// becomes a traversal is somebody later joining it onto a directory because it
// was called filename and looked like one. Refusing at the door is cheaper than
// finding every future reader of it.
func attachmentFilename(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	if len(name) > maxFilename {
		return "", fmt.Errorf("filename is %d bytes, over the %d ceiling", len(name), maxFilename)
	}
	if strings.ContainsFunc(name, isControl) {
		return "", errors.New("filename carries a control character, which no name has")
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return "", fmt.Errorf("filename %q is a path, and this is a name - "+
			"pass what the file was called, without directories", name)
	}
	return name, nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// attachmentTitle is what the attachment is called when the caller did not say.
// A list of untitled rows is a list nobody can use, and the filename is the
// thing they already typed.
func attachmentTitle(title, filename string) string {
	title = strings.TrimSpace(title)
	if title != "" {
		return title
	}
	if filename != "" {
		return filename
	}
	return "attachment"
}

// attachmentField reads one string out of an artifact's fields. A row whose
// fields will not parse is not a reason to fail a read of the bytes - it is the
// same judgement scanArtifact makes about a forge link that will not parse.
func attachmentField(art *store.Artifact, key string) string {
	if len(art.Fields) == 0 {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal(art.Fields, &fields); err != nil {
		return ""
	}
	s, _ := fields[key].(string)
	return s
}
