package store

// The derivation engine: the half of a change's tasks.md that is also a todo
// queue. Every checkbox line of a change derives one todo row, governed by the
// file - the file is authoritative, and the rows follow it.
//
// LINE IDENTITY. A derived todo must survive the file being edited, so a line
// needs an identity that is part of the file. It is an explicit id carried as
// a trailing HTML comment on the line:
//
//	- [ ] carry the thing <!-- flowy:01H... -->
//
// A markdown consumer never renders it, and it survives reordering and edits
// of every other line. Lines that arrive without one - a file written by
// hand, or by a surface that bypassed the write statements - bootstrap
// through the fallback the operator picked: the line's text hash, preferring
// the existing todo that held the same position, so the first write of a
// hand-written file finds nothing and mints, and a marker stripped by an
// editor finds its row again by content.
//
// THE ROWS ARE DERIVED, THE FILE IS THE TRUTH. One-way sync on every write of
// a change row, in the same transaction when the write has one:
//
//   - a line with no todo becomes one, carrying its origin in fields
//   - a line's text and checkbox move to its todo (a checked box closes it,
//     from any state; an unchecked box never demotes active work - in
//     progress is a state tasks.md cannot spell, so it survives the sync)
//   - a line removed from the file tombstones its todo
//   - a todo closed by hand while the checkbox stayed open is reopened by the
//     next sync and SAYS SO ON THE ROW: origin.openspec.reopened is set, so
//     the divergence is visible where the row is read, not only in the log
//
// Derived writes produce no events: the change row's own write is the record
// that triggered them, and the derived rows carry their origin in fields
// where a reader of the row finds it.
//
// The decisions this file implements are the operator's answers on thread
// 01M0K9WFBNBZ9V9XBK5NGD7D9K (message 01M0KENVHE554V04WN16B8M4RH), recorded
// on the p2 row 01M0KA7RJS84G4ZJXKJS3ZZH90.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// The keys of a derived todo's origin, under fields.origin.openspec: which
// change derived the row, which line of its tasks.md, and where that line
// stood at derivation time.
const (
	originField    = "origin"
	originOpenspec = "openspec"
	originChange   = "change"
	originLine     = "line"
	originNum      = "num"
	originReopened = "reopened"

	// derivedTodoKind is what a derived line is as a row: an ordinary todo,
	// distinguished by its origin rather than by a kind of its own, so the
	// whole existing todo surface - claim, status, notes, the board - works
	// on it unchanged.
	derivedTodoKind = "todo"
)

// A checkbox line looks like `- [ ] text` and nothing else: headings, prose
// and indented lines are the file's own words and derive nothing.
var taskLineRe = regexp.MustCompile(`^- \[([ xX])\] (.*)$`)

// The marker is the line's id, written as a trailing HTML comment after the
// text it belongs to. The whitespace ahead of it is part of the match so
// stripping the match strips exactly the marker and its gap.
var taskMarkerRe = regexp.MustCompile(`(\s+)<!-- flowy:([A-Za-z0-9]+) -->\s*$`)

// taskLine is one checkbox line of a tasks.md, with its identity resolved.
type taskLine struct {
	num  int    // position among the checkbox lines, 1-based
	text string // the line's content: checkbox and marker stripped
	done bool
	id   string // the trailing marker, empty until assigned
	hash string // identity of the text, for the position+hash bootstrap
}

// lineIdentity is what an existing derived todo knows about its line. The
// hash is computed from the todo's title - which IS the line's text - so a
// marker that got stripped finds its row by content.
type lineIdentity struct {
	id   string
	num  int
	hash string
}

// parseTasks reads the checkbox lines of a tasks.md. Lines that are not
// checkboxes are skipped; num counts only checkbox lines, so a heading added
// above the list does not shift every position.
func parseTasks(md string) []taskLine {
	var lines []taskLine
	for _, raw := range strings.Split(md, "\n") {
		m := taskLineRe.FindStringSubmatch(raw)
		if m == nil {
			continue
		}
		content := m[2]
		id := ""
		if mm := taskMarkerRe.FindStringSubmatch(content); mm != nil {
			id = mm[2]
			content = strings.TrimSuffix(content, mm[0])
		}
		lines = append(lines, taskLine{
			num:  len(lines) + 1,
			text: content,
			done: m[1] == "x" || m[1] == "X",
			id:   id,
			hash: taskTextHash(content),
		})
	}
	return lines
}

func taskTextHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// annotateTasks makes every line's identity explicit. A line that already
// carries a marker keeps it. One that does not takes, in order: the identity
// of an existing todo with the same text and the same position, the identity
// of one with the same text elsewhere, or a fresh id. The whole markdown is
// rewritten with only the markers added - headings, blank lines and prose
// come back byte for byte. The second return is the parsed lines, with every
// id resolved, for the caller that wants to diff in the same pass.
func annotateTasks(md string, known []lineIdentity) (string, []taskLine) {
	lines := parseTasks(md)
	byHash := map[string][]lineIdentity{}
	for _, k := range known {
		byHash[k.hash] = append(byHash[k.hash], k)
	}
	// byNum pairs a position with the identity that already holds it, so
	// when the file has two identical unmarked lines the one standing where
	// the old line stood keeps the old id, not whichever is scanned first.
	byNum := map[int]lineIdentity{}
	for _, k := range known {
		if k.num > 0 {
			if _, ok := byNum[k.num]; !ok {
				byNum[k.num] = k
			}
		}
	}
	// used tracks ids consumed this pass, so an id can never land on two
	// lines - a duplicate marker in the file is one id and a mint, not two
	// rows with one origin.
	used := map[string]bool{}
	changed := false
	raw := strings.Split(md, "\n")
	for i, li := 0, 0; i < len(raw); i++ {
		if taskLineRe.FindStringSubmatch(raw[i]) == nil {
			continue
		}
		line := lines[li]
		li++
		if line.id != "" && !used[line.id] {
			used[line.id] = true
			continue
		}
		// The same position and text first, then the same text anywhere,
		// then a mint. Position alone never pairs: a line whose text moved
		// is a new line, not a claim on the row that used to stand there.
		id := ""
		if nid, ok := byNum[line.num]; ok && !used[nid.id] && nid.hash == line.hash {
			id = nid.id
		}
		if id == "" {
			for _, k := range byHash[line.hash] {
				if used[k.id] {
					continue
				}
				// Reserved for its own position: a later line
				// standing there takes it, not this one.
				if r, reserved := byNum[k.num]; reserved && r.id == k.id && k.num != line.num {
					continue
				}
				id = k.id
				break
			}
		}
		if id == "" {
			id = ulid.NewString()
		}
		used[id] = true
		lines[li-1].id = id
		raw[i] += " <!-- flowy:" + id + " -->"
		changed = true
	}
	if !changed {
		return md, lines
	}
	return strings.Join(raw, "\n"), lines
}

// derivationPlan is what a re-sync will do, computed before any of it runs so
// one pass writes each row once: the todos to create, the ones to rewrite,
// the ones to tombstone.
type derivationPlan struct {
	create    []taskLine
	update    []derivedUpdate
	tombstone []*Artifact
}

// derivedUpdate is one existing todo being moved by its line.
type derivedUpdate struct {
	line   taskLine
	art    *Artifact
	status string // the status the row will hold after the write
	reopen bool   // the row was closed by hand and the checkbox says open
}

// planDerivation pairs the parsed lines of the tasks.md just stored against
// the todos already derived from this change, and decides the diff. Pairing
// is by the line id carried in the todo's origin; a line whose marker is
// missing falls back to the text hash and position the same way
// annotateTasks does. Existing todos that no line claims are tombstones -
// their line left the file, and the file is the truth.
func planDerivation(lines []taskLine, existing []*Artifact) derivationPlan {
	var plan derivationPlan
	byID := map[string]*Artifact{}
	byHash := map[string][]*Artifact{}
	for _, art := range existing {
		if line := originLineOf(art); line != "" {
			byID[line] = art
		}
		h := taskTextHash(art.Title)
		byHash[h] = append(byHash[h], art)
	}
	unclaimed := map[string]bool{}
	for id := range byID {
		unclaimed[id] = true
	}
	used := map[string]bool{}
	// byNum pairs a position with the todo that already holds it - see
	// annotateTasks, which needs the same preference for the same reason.
	byNum := map[int]*Artifact{}
	for _, art := range existing {
		if n := originNumOf(art); n > 0 && byNum[n] == nil {
			byNum[n] = art
		}
	}

	for i := range lines {
		line := lines[i]
		var art *Artifact
		if line.id != "" && !used[line.id] {
			// The file's id names the row; nil when the todo was deleted
			// by hand, in which case the file's id stands and a fresh row
			// is derived under it.
			art = byID[line.id]
		}
		if art == nil && line.id == "" {
			// Bootstrap: the same text, the same position first, then
			// the same text anywhere - a marker stripped by an editor
			// finds its row by content. Position alone never pairs: a
			// line whose text moved is a new line, not a claim on the
			// row that used to stand there.
			if a := byNum[line.num]; a != nil && !used[originLineOf(a)] &&
				taskTextHash(a.Title) == line.hash {
				art = a
			}
			if art == nil {
				for _, a := range byHash[line.hash] {
					ola := originLineOf(a)
					if ola == "" || used[ola] {
						continue
					}
					// Reserved for its own position: a later line
					// standing there takes it, not this one.
					if r := byNum[originNumOf(a)]; r == a && originNumOf(a) != line.num {
						continue
					}
					art = a
					break
				}
			}
		}
		if art != nil {
			if line.id == "" {
				// A bootstrapped line claims its row with a fresh id, so
				// the row carries a stable origin from here on and the
				// next write's annotation reuses it.
				line.id = ulid.NewString()
			}
			id := originLineOf(art)
			delete(unclaimed, id)
			used[id] = true
			if u, ok := syncDecision(line, art); ok {
				plan.update = append(plan.update, u)
			}
			continue
		}
		// A line no todo carries: a new row, under the file's id if it has
		// one and nothing else took it - a duplicated marker is one id and
		// a mint, not two rows with one origin.
		if line.id != "" && !used[line.id] {
			used[line.id] = true
		} else {
			line.id = ""
		}
		plan.create = append(plan.create, line)
	}

	// Tombstones in creation order, which is the order the load came back
	// in: deterministic for a reader and for a test.
	for _, art := range existing {
		if unclaimed[originLineOf(art)] {
			plan.tombstone = append(plan.tombstone, art)
		}
	}
	return plan
}

// syncDecision is what one line does to the todo it owns, if anything. A
// checked box closes the row from any state - the file is the authority on
// completion. An unchecked box never demotes active work, but it does reopen
// a row closed by hand, flagged so the row says so. A line whose text moved
// moves its todo's title. ok is false when the row already is what the line
// says.
func syncDecision(line taskLine, art *Artifact) (derivedUpdate, bool) {
	status := art.Status
	reopen := originReopenedOf(art)
	if line.done {
		status = DoneStatus
		reopen = false
	} else if art.Status == DoneStatus {
		status = TodoStatus
		reopen = true
	}
	// The origin is the file's to write: a line that moved position, or
	// claimed its row by bootstrap under a fresh id, says so on the row
	// even when nothing else changed.
	originMoved := false
	if line.id != "" {
		_, oline, onum, _ := originOf(art)
		originMoved = oline != line.id || onum != line.num
	}
	if !originMoved && art.Title == line.text && art.Status == status &&
		reopen == originReopenedOf(art) {
		return derivedUpdate{}, false
	}
	return derivedUpdate{line: line, art: art, status: status, reopen: reopen}, true
}

// derivedTodosOf loads the todos a change has derived so far. The WHERE says
// the key exists and then reads it, in that order - the shape fieldEq
// measures against the partial index over the key (artifacts.go), and a
// clause that skips the existence test is a sequential scan.
func (d *DB) derivedTodosOf(ctx context.Context, q execer, change string) ([]*Artifact, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT `+artifactColumns+`
		   FROM artifacts
		  WHERE kind = '`+derivedTodoKind+`'
		    AND coalesce(tombstone, false) = false
		    AND fields -> '`+originField+`' -> '`+originOpenspec+`' ? '`+originChange+`'
		    AND fields -> '`+originField+`' -> '`+originOpenspec+`' ->> '`+originChange+`' = $1
		  ORDER BY created`, change)
	if err != nil {
		return nil, fmt.Errorf("store: derived todos of %s: %w", change, err)
	}
	defer rows.Close()
	var out []*Artifact
	for rows.Next() {
		art, err := scanArtifact(rows, nil)
		if err != nil {
			return nil, fmt.Errorf("store: derived todos of %s: %w", change, err)
		}
		out = append(out, art)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: derived todos of %s: %w", change, err)
	}
	return out, nil
}

// prepareChangeWrite annotates a change's tasks.md before the row goes in, so
// the stored file carries explicit line ids and the re-sync after the write
// pairs lines to todos by id alone. It is asked of the same three write
// statements as checkOpenspecRow, for the same reason: every surface writes
// through one of them, and a rule kept per surface is a rule the next
// surface forgets.
func (d *DB) prepareChangeWrite(ctx context.Context, q execer, a *Artifact) error {
	if !IsEntityType(a, ChangeKind) {
		return nil
	}
	files, err := OpenspecFilesOf(a)
	if err != nil {
		return err
	}
	tasks, ok := files["tasks.md"]
	if !ok || tasks == "" {
		return nil
	}
	known, err := d.derivedTodosOf(ctx, q, a.ID)
	if err != nil {
		return err
	}
	identities := make([]lineIdentity, 0, len(known))
	for _, t := range known {
		identities = append(identities, lineIdentity{
			id:   originLineOf(t),
			num:  originNumOf(t),
			hash: taskTextHash(t.Title),
		})
	}
	annotated, _ := annotateTasks(tasks, identities)
	if annotated == tasks {
		return nil
	}
	files["tasks.md"] = annotated
	return setOpenspecFiles(a, files)
}

// deriveChange re-syncs the derived todos after a change row was written. It
// runs on the caller's execer - a transaction when the write was half of an
// operation, so the change and its todos land as one thing or not at all.
func (d *DB) deriveChange(ctx context.Context, q execer, change *Artifact) error {
	if !IsEntityType(change, ChangeKind) {
		return nil
	}
	files, err := OpenspecFilesOf(change)
	if err != nil {
		return err
	}
	lines := parseTasks(files["tasks.md"])
	existing, err := d.derivedTodosOf(ctx, q, change.ID)
	if err != nil {
		return err
	}
	plan := planDerivation(lines, existing)
	for _, l := range plan.create {
		if err := d.deriveCreate(ctx, q, change, l); err != nil {
			return err
		}
	}
	for _, u := range plan.update {
		if err := d.deriveSync(ctx, q, u); err != nil {
			return err
		}
	}
	for _, t := range plan.tombstone {
		if err := d.deriveTombstone(ctx, q, t); err != nil {
			return err
		}
	}
	return nil
}

// deriveCreate writes the todo a line has just become. The row carries its
// origin in fields - which change, which line, where it stood - and belongs
// to the change's owner in the change's project.
func (d *DB) deriveCreate(ctx context.Context, q execer, change *Artifact, l taskLine) error {
	id := l.id
	if id == "" {
		id = ulid.NewString()
	}
	fields, err := json.Marshal(map[string]any{
		originField: map[string]any{
			originOpenspec: map[string]any{
				originChange: change.ID,
				originLine:   id,
				originNum:    l.num,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("store: derive %s: %w", l.text, err)
	}
	status := TodoStatus
	if l.done {
		status = DoneStatus
	}
	art := &Artifact{
		ID:        ulid.NewString(),
		Type:      MemoryType,
		Kind:      derivedTodoKind,
		Project:   change.Project,
		OwnerUser: change.OwnerUser,
		Title:     l.text,
		Status:    status,
		Fields:    fields,
	}
	if err := d.fill(art); err != nil {
		return err
	}
	return d.createArtifact(ctx, q, art)
}

// deriveSync rewrites the todo its line owns. The row is the stored one, so
// what the line does not say - assignee, severity, notes, the title once
// synced - stays on it; only the title, the status and the origin move.
func (d *DB) deriveSync(ctx context.Context, q execer, u derivedUpdate) error {
	art := *u.art
	art.Title = u.line.text
	art.Status = u.status
	fields, err := setDerivedOrigin(&art, u.line, u.reopen)
	if err != nil {
		return err
	}
	art.Fields = fields
	if err := d.fill(&art); err != nil {
		return err
	}
	return d.upsertArtifact(ctx, q, &art)
}

// deriveTombstone takes a derived todo back because its line left the file.
// It is the same write TombstoneArtifact makes - the withdrawn stamp says
// who, and the change's owner is who the row belonged to - but on the
// caller's execer, so it rides the change write's transaction.
func (d *DB) deriveTombstone(ctx context.Context, q execer, art *Artifact) error {
	cp := *art
	cp.Tombstone = true
	fields, err := markWithdrawn(&cp, &Principal{UserID: cp.OwnerUser})
	if err != nil {
		return err
	}
	cp.Fields = fields
	if err := d.fill(&cp); err != nil {
		return err
	}
	return d.upsertArtifact(ctx, q, &cp)
}

// originOf reads the origin out of a derived todo's fields. A row that is not
// a derived todo answers zeros - it is the caller's job to have asked the
// right rows.
func originOf(art *Artifact) (change, line string, num int, reopened bool) {
	if art == nil || len(art.Fields) == 0 {
		return "", "", 0, false
	}
	var outer struct {
		Origin *struct {
			Openspec *struct {
				Change   string `json:"change"`
				Line     string `json:"line"`
				Num      int    `json:"num"`
				Reopened bool   `json:"reopened"`
			} `json:"openspec"`
		} `json:"origin"`
	}
	// Unparsable fields answer zeros rather than an error: the row is not
	// unreadable, it just does not carry an origin this code can see, which
	// is what every other reader of one key does (artifactField).
	if err := json.Unmarshal(art.Fields, &outer); err != nil {
		return "", "", 0, false
	}
	if outer.Origin == nil || outer.Origin.Openspec == nil {
		return "", "", 0, false
	}
	o := outer.Origin.Openspec
	return o.Change, o.Line, o.Num, o.Reopened
}

func originLineOf(art *Artifact) string {
	_, line, _, _ := originOf(art)
	return line
}

func originNumOf(art *Artifact) int {
	_, _, num, _ := originOf(art)
	return num
}

func originReopenedOf(art *Artifact) bool {
	_, _, _, reopened := originOf(art)
	return reopened
}

// setDerivedOrigin rewrites one todo's origin in place, keeping the rest of
// its fields. The origin is the one key of the row that is the file's to
// write; everything else belongs to the row.
func setDerivedOrigin(art *Artifact, line taskLine, reopened bool) ([]byte, error) {
	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, err
	}
	change, _, _, _ := originOf(art)
	fields[originField] = map[string]any{
		originOpenspec: map[string]any{
			originChange:   change,
			originLine:     line.id,
			originNum:      line.num,
			originReopened: reopened,
		},
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("store: derived origin of %s: %w", art.ID, err)
	}
	return raw, nil
}

// setOpenspecFiles rewrites a change row's fields with the files map given,
// keeping every other key. It is what an annotation does to the row before
// it is signed, so the signature covers the annotated file.
func setOpenspecFiles(a *Artifact, files map[string]string) error {
	fields, err := ArtifactFields(a)
	if err != nil {
		return err
	}
	fields["openspec"] = map[string]any{"files": files}
	raw, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("store: openspec fields of %s: %w", a.ID, err)
	}
	a.Fields = raw
	return nil
}
