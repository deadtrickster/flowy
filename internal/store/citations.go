package store

// Citations: what a message says it is ABOUT, at a finer grain than the DAG
// edge beside it - another message, whole, or one span of one.
//
// THE ROW STORES THE SPAN AND NEVER THE QUOTED TEXT, and everything in this
// file follows from that one choice. A citation is `<id>` for a whole message
// and `<id>:<start>:<end>` for a part of one; the words a reader sees are
// derived, on the read, from the row being quoted.
//
// Storing the text would have been simpler and it is the wrong shape twice
// over. It is a COPY THE CITING AUTHOR CONTROLS, so a citation could say
// somebody said something they did not and render as a quotation of them - a
// forgery surface in a log whose whole value is that its rows are signed, and
// through the one door this fabric otherwise closes everywhere: no principal
// speaks as another. And it cannot be kept from a reader who may not read the
// source, because a copy on the citing row is readable by everybody who can
// read the citing row, and it replicates with it. A stored quote would have to
// be stripped on the way out by the same permission check this file makes
// anyway - at which point the copy is doing nothing except being a second
// version of the truth that can disagree with the first.
//
// Offsets into text are fragile in general and they are not fragile here. The
// events table is append-only and a body is inside the signature, so the bytes
// a span points into cannot change without the row ceasing to verify. What can
// still happen is a span that never fitted - a peer's merge does not check one,
// for the reason it does not check parents - so the span is bounds-checked
// again at read time and derives nothing rather than deriving the wrong words.
//
// The cost of the choice is the case it forces this file to answer: a reader
// who cannot read the source gets the citation and no quote. That is the honest
// answer, and it is the same answer every other read here gives about something
// out of reach.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lib/pq"
)

// CiteMetaKey is the meta key a message's citation rides in.
//
// It is the node's to write, like the actor keys and the resolved mentions
// beside it, and for the sharpest version of their reason: this is what a
// console draws as a quotation of another principal, under that principal's
// name and in their colour. A client that could write its own would be putting
// words in somebody else's mouth on a row that is correctly signed and
// correctly actored. So it is stripped off meta a client hands in - see
// speakerStripped - and stamped only where the node has checked that the source
// is readable and that the span is inside it.
const CiteMetaKey = "cite"

// actorMetaName is the key the speaker's name rides under, which is what a
// citation is attributed to. It is the same key chat.go stamps.
const actorMetaName = ActorMetaPrefix + "name"

// maxCiteQuote caps the derived quote. A whole-message citation of a megabyte
// body would otherwise be a megabyte per citing message per reader, and a room
// of a hundred of them one response of a hundred megabytes - an amplification
// a client asks for by scrolling. What is cut is said so on the citation rather
// than hidden, because a quote silently ending early is a misquote.
const maxCiteQuote = 4096

// CiteRef is a citation as the row records it: the message, and the span into
// its body when the citation is of a part rather than of the whole.
//
// Offsets are BYTES into the body, which is what the body is - the node slices
// it and the signature is over those bytes. A console counting UTF-16 units has
// to convert, and gets told at the door when it does not.
//
// Message is a bare id, stored in a signed event's meta and replicated with
// the row - see CiteMetaKey - so under the (project, type, id) ruling
// (01M08FK999F2JWY9RQV5VC821N) it stays one: widening the stored shape here
// would need every peer and every row already written to agree on a new
// format, for a resolution step that was never a guess to begin with. The
// (*DB).Citations method resolves Message through EventFilterSQL, the same
// permission-filtered query every other read of the log uses, and that query
// already reads the source event's own project off its row - see
// Event.Project - so nothing has to be carried alongside the id to find it.
// A caller that wants an addressable triple for a RESOLVED citation has the
// source event's project and type in hand at that point and can build one
// the way RefOf builds one from an artifact; it does not belong on the
// stored ref, which names an id in another node's log and nothing else.
type CiteRef struct {
	Message string
	Start   int
	End     int
}

// Whole reports whether the citation is of the whole message. No span and a
// zero-length span at the start are the same thing here, which is what a
// selection of nothing collapses to.
func (c CiteRef) Whole() bool { return c.Start == 0 && c.End == 0 }

// EncodeCiteRef is the meta value: the id on its own for a whole message, and
// the id with the span after it for a part. A ULID holds no colon, so the
// encoding is unambiguous, and a whole-message citation encodes to exactly the
// id - which is what a reader that only wants the pointer can use as one.
func EncodeCiteRef(c CiteRef) string {
	if c.Whole() {
		return c.Message
	}
	return c.Message + ":" + strconv.Itoa(c.Start) + ":" + strconv.Itoa(c.End)
}

// ParseCiteRef reads a meta value back. Anything that is not one of the two
// shapes is not a citation rather than a broken one: this parses rows written
// by other builds and by peers, and half a citation drawn as a quotation is
// worse than none.
func ParseCiteRef(value string) (CiteRef, bool) {
	parts := strings.Split(value, ":")
	switch len(parts) {
	case 1:
		if parts[0] == "" {
			return CiteRef{}, false
		}
		return CiteRef{Message: parts[0]}, true
	case 3:
		start, startErr := strconv.Atoi(parts[1])
		end, endErr := strconv.Atoi(parts[2])
		if parts[0] == "" || startErr != nil || endErr != nil || start < 0 || end <= start {
			return CiteRef{}, false
		}
		return CiteRef{Message: parts[0], Start: start, End: end}, true
	}
	return CiteRef{}, false
}

// CiteOf is the citation an event's meta records, if it records one.
func CiteOf(meta json.RawMessage) (CiteRef, bool) {
	value, found := metaText(meta, CiteMetaKey)
	if !found {
		return CiteRef{}, false
	}
	return ParseCiteRef(value)
}

// metaText reads one string key out of an event's meta. A meta that is not a
// JSON object, or whose key is not a string, says nothing - which is what a row
// from a build that never wrote the key says too.
func metaText(meta json.RawMessage, key string) (string, bool) {
	if len(meta) == 0 {
		return "", false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(meta, &fields); err != nil {
		return "", false
	}
	raw, found := fields[key]
	if !found {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

// CiteSpanFault answers why start:end is not a span of body, or "" when it is
// one. It is asked on the way in, of a client's write, so a span that could
// never derive a quote is refused where the writer can still fix it.
//
// The last clause is the one a byte count gets wrong on real prose. A span that
// ends inside a multi-byte character derives bytes that are not text, and a
// console showing them shows replacement marks under somebody's name - so the
// boundary is checked rather than the quote silently repaired, because repairing
// it means quoting something other than what was selected.
func CiteSpanFault(body string, start, end int) string {
	switch {
	case start < 0 || end < 0:
		return "a citation counts from the start of the message it cites, so its offsets cannot be negative"
	case end <= start:
		return "a citation of a span names some of the message: this one ends where it starts, or before"
	case end > len(body):
		return "the span runs past the end of the message it cites"
	case !utf8.RuneStart(body[start]) || (end < len(body) && !utf8.RuneStart(body[end])):
		return "the span cuts a character in half, so the quote it would derive is not text"
	}
	return ""
}

// Citation is a message's citation as one reader gets it: the pointer the row
// records, and - only when that reader may read the source - who was quoted and
// the words derived from their row.
//
// Readable is not decoration and it is never absent. Rooms are scoped by
// project and the log is not, so a citation of a message the reader cannot see
// is an ordinary thing to meet: the reply is in front of them and what it
// answers is not. A client is told that plainly instead of being handed an
// empty quote it would draw as somebody having said nothing.
type Citation struct {
	Message string `json:"message"`
	Whole   bool   `json:"whole"`
	Start   int    `json:"start,omitempty"`
	End     int    `json:"end,omitempty"`

	// Whether the reader may read the message this cites. Everything below is
	// filled exactly when it is true.
	Readable bool `json:"readable"`

	Actor string `json:"actor,omitempty"`
	Name  string `json:"name,omitempty"`
	Text  string `json:"text,omitempty"`
	// Truncated says the quote was cut at maxCiteQuote, so a client draws an
	// ellipsis rather than a quotation that appears to end mid-sentence.
	Truncated bool `json:"truncated,omitempty"`
}

// Citations fills Citation on every event in list whose meta records one, and
// leaves the rest alone.
//
// The filter is on the CITED event, in the same WHERE clause as the match, for
// the reason replacedBy puts it on the replacement: the answer is another row's
// contents, and handing them to a reader who may not read that row would leak
// it out of a row they are entitled to see. A reader who cannot reach the
// source is told the citation is of something out of reach and nothing else -
// not the words, not who said them - which is the answer a read of it would
// give.
//
// One query for the whole page rather than one per row, and none at all when
// nothing on the page cites anything.
func (d *DB) Citations(ctx context.Context, p *Principal, list []*Event, scopeAll bool) error {
	refs := make(map[string]CiteRef, len(list))
	var ids []string
	asked := map[string]bool{}
	for _, e := range list {
		if e == nil {
			continue
		}
		ref, found := CiteOf(e.Meta)
		if !found {
			continue
		}
		refs[e.ID] = ref
		if !asked[ref.Message] {
			asked[ref.Message] = true
			ids = append(ids, ref.Message)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	a := &args{}
	idsArg := a.next(pq.Array(ids))
	filter := EventFilterSQL(p, "e", a, scopeAll)
	rows, err := d.sql.QueryContext(ctx,
		`SELECT e.id, coalesce(e.actor, ''), coalesce(e.body, ''), e.meta
		   FROM events e
		  WHERE e.id = ANY(`+idsArg+`) AND `+filter, a.vals...)
	if err != nil {
		return fmt.Errorf("store: read the messages these cite: %w", err)
	}
	defer rows.Close()

	type source struct{ actor, name, body string }
	sources := map[string]source{}
	for rows.Next() {
		var id, actor, body string
		var meta []byte
		if err := rows.Scan(&id, &actor, &body, &meta); err != nil {
			return fmt.Errorf("store: read the messages these cite: %w", err)
		}
		name, _ := metaText(meta, actorMetaName)
		sources[id] = source{actor: actor, name: name, body: body}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: read the messages these cite: %w", err)
	}

	for _, e := range list {
		ref, found := refs[e.ID]
		if !found {
			continue
		}
		cite := &Citation{
			Message: ref.Message,
			Whole:   ref.Whole(),
			Start:   ref.Start,
			End:     ref.End,
		}
		if src, reachable := sources[ref.Message]; reachable {
			cite.Readable = true
			cite.Actor, cite.Name = src.actor, src.name
			cite.Text, cite.Truncated = QuoteOf(src.body, ref)
		}
		e.Citation = cite
	}
	return nil
}

// QuoteOf is the words a citation quotes, derived from the body it cites, and
// whether they were cut at maxCiteQuote.
//
// A span that does not fit the body quotes nothing rather than being clamped to
// what does fit. Clamping would answer the question by misquoting, which is the
// one thing this whole design is arranged to make impossible - and a span that
// does not fit only arrives from a peer whose merge did not check it, where
// "this cites something that is not there" is the true answer.
func QuoteOf(body string, ref CiteRef) (text string, truncated bool) {
	text = body
	if !ref.Whole() {
		if CiteSpanFault(body, ref.Start, ref.End) != "" {
			return "", false
		}
		text = body[ref.Start:ref.End]
	}
	if len(text) <= maxCiteQuote {
		return text, false
	}
	cut := maxCiteQuote
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut], true
}
