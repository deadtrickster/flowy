// Package cron parses the five-field crontab spec a person types into the
// schedule table, and answers the two questions the node needs of it: WHEN DOES
// THIS FIRE NEXT, and CAN THIS EVER FIRE AT ALL.
//
// The second question is the reason this package exists rather than a regexp on
// the save door. Row 01M0EW45RE asked for a spec that can never fire to be
// REFUSED AT SAVE with the reason, instead of being stored and then silently
// never firing - which is the same defect shape as every empty-versus-absent
// bug this fleet has found: one code path for two states, returning a confident
// wrong answer. A schedule that is saved, displayed, and dead is worse than one
// that was rejected, because the display is evidence that it works.
//
// TWO SEMANTIC DECISIONS, WRITTEN DOWN BECAUSE THE ROW GOT ONE OF THEM WRONG.
//
//  1. DAY-OF-MONTH AND DAY-OF-WEEK ARE OR'd WHEN BOTH ARE RESTRICTED. This is
//     Vixie cron's rule and it is what every crontab a person has ever written
//     means: `0 0 31 * 1` fires on the 31st AND on every Monday, not on Mondays
//     that fall on a 31st. The row named "a day-of-week and day-of-month pair
//     that never coincides" as a never-fires case; under OR it is not one, and
//     refusing it would be refusing a legal spec - a worse failure than storing
//     a dead one, because it tells a person their correct input is wrong.
//
//  2. THE ONLY REAL NEVER-FIRES CASE IS MONTH AGAINST DAY-OF-MONTH.
//     `0 0 30 2 *` (February 30th) and `0 0 31 4 *` (April 31st) are the shape.
//     `0 0 29 2 *` IS NOT ONE - it fires every leap year, and a checker that
//     counts February as 28 days rejects a spec that works. Because of rule 1,
//     an impossible month/day pair is only fatal when day-of-week is
//     unrestricted; `0 0 30 2 1` still fires every February Monday.
//
// Reachability is not a separate arithmetic from Next - it IS Next, run over a
// window wide enough to contain a leap cycle. One matcher answers both, so the
// two can never disagree about what a spec means. The window is eight years
// (see reachWindow) which covers the worst legal case, February 29th, twice.
//
// TIME ZONE: Next works in the location of the time it is given. The node hands
// it UTC; a console that wants to show a person their own midnight converts on
// the way out. The parser has no opinion about zones and stores none.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// reachWindow is how far Reachable looks before it calls a spec dead. Eight
// years contains two February 29ths, so the rarest legal spec is seen twice
// rather than once - a window that catches it exactly once is a window that
// depends on which day the scan starts.
const reachWindow = 8 * 366 * 24 * time.Hour

// scanEpoch is where Reachable starts. It is a FIXED DATE rather than the
// current time, because "can this ever fire" must not depend on the day it is
// asked. A spec saved on the 1st and re-validated on the 30th has to get the
// same answer, or the save door and the console disagree about the same row.
// It is a common year (2023) so a scan that only works from a leap year fails
// here rather than in production.
var scanEpoch = time.Date(2023, time.January, 1, 0, 0, 0, 0, time.UTC)

// Spec is a parsed crontab line. The five fields are bitsets - bit n is set
// when value n is allowed - because every question asked of them is membership.
type Spec struct {
	raw string

	minute uint64 // 0-59
	hour   uint64 // 0-23
	dom    uint64 // 1-31
	month  uint64 // 1-12
	dow    uint64 // 0-6, Sunday is 0

	// domRestricted and dowRestricted are what turn rule 1 on. A field is
	// UNRESTRICTED when it was written `*` or `*/n` - and those two are
	// different: `*/2` names a subset but is still a wildcard for the
	// purposes of the OR rule, which is Vixie's behaviour and surprises
	// people, so it is stated rather than left in the parse.
	domRestricted bool
	dowRestricted bool
}

// String returns the spec as it was written, so an error message or a console
// row quotes the person's own text rather than a normalised form they would
// have to recognise.
func (s *Spec) String() string { return s.raw }

// Error is a refusal with the field and token that caused it. The console shows
// this to whoever is editing the schedule, so it names what to change.
type Error struct {
	Spec  string
	Field string // "minute", "hour", "day-of-month", "month", "day-of-week", or "" for whole-spec
	Token string
	Why   string
}

func (e *Error) Error() string {
	switch {
	case e.Field == "":
		return fmt.Sprintf("cron %q: %s", e.Spec, e.Why)
	case e.Token == "":
		return fmt.Sprintf("cron %q: %s field: %s", e.Spec, e.Field, e.Why)
	default:
		return fmt.Sprintf("cron %q: %s field: %q %s", e.Spec, e.Field, e.Token, e.Why)
	}
}

// descriptors are the @-forms a person actually types. @reboot is deliberately
// absent: this node has no boot a schedule could hang off, and accepting it as
// a synonym for anything would be inventing a meaning.
var descriptors = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

type fieldDef struct {
	name  string
	min   int
	max   int
	names map[string]int
}

var fields = []fieldDef{
	{"minute", 0, 59, nil},
	{"hour", 0, 23, nil},
	{"day-of-month", 1, 31, nil},
	{"month", 1, 12, monthNames},
	{"day-of-week", 0, 6, dowNames},
}

// Parse turns a crontab line into a Spec, or refuses it with the reason.
//
// It refuses three kinds of thing, and the third is the one this package is
// for: a spec that parses, is in range, and can still never fire.
func Parse(spec string) (*Spec, error) {
	raw := strings.TrimSpace(spec)
	if raw == "" {
		return nil, &Error{Spec: spec, Why: "is empty - an unchecked schedule with an empty cron means NEVER, and that is stored as never rather than as a spec"}
	}

	text := raw
	if strings.HasPrefix(text, "@") {
		expanded, ok := descriptors[strings.ToLower(text)]
		if !ok {
			if strings.EqualFold(text, "@reboot") {
				return nil, &Error{Spec: raw, Why: "@reboot has no meaning here - the schedule lives in the node and outlives any one process, so there is no boot to hang it off"}
			}
			return nil, &Error{Spec: raw, Why: "is not a descriptor this parser knows (@yearly @annually @monthly @weekly @daily @midnight @hourly)"}
		}
		text = expanded
	}

	parts := strings.Fields(text)
	if len(parts) != 5 {
		why := fmt.Sprintf("has %d fields, want 5 (minute hour day-of-month month day-of-week)", len(parts))
		if len(parts) == 6 {
			why = "has 6 fields - this is a five-field crontab and the leading seconds field of the six-field dialect is not accepted, because reading it as a minute would silently move every firing"
		}
		return nil, &Error{Spec: raw, Why: why}
	}

	s := &Spec{raw: raw}
	sets := []*uint64{&s.minute, &s.hour, &s.dom, &s.month, &s.dow}
	for i, part := range parts {
		bits, restricted, err := parseField(raw, fields[i], part)
		if err != nil {
			return nil, err
		}
		*sets[i] = bits
		switch i {
		case 2:
			s.domRestricted = restricted
		case 4:
			s.dowRestricted = restricted
		}
	}

	// Sunday is both 0 and 7 in every crontab a person has read. parseField
	// accepts 7 by folding it here rather than widening the range, so the
	// range error still says 0-6.
	if s.dow&(1<<7) != 0 {
		s.dow = (s.dow &^ (1 << 7)) | 1
	}

	if _, ok := s.next(scanEpoch, reachWindow); !ok {
		return nil, &Error{Spec: raw, Why: neverWhy(s)}
	}
	return s, nil
}

// neverWhy names the impossible pair rather than saying "never fires", because
// a person who typed `0 0 30 2 *` needs to be told it is February that has no
// 30th, not that their line is invalid.
func neverWhy(s *Spec) string {
	if s.dowRestricted {
		// Rule 1 means a restricted day-of-week alone can carry a spec, so
		// reaching here with one set is a case this comment does not
		// predict. Say so rather than inventing a cause - two true facts
		// and an invented reason is the fleet's commonest wrong claim.
		return "can never fire, and the cause is not the usual month-against-day pair - day-of-week is restricted too, which normally keeps a spec alive on its own"
	}
	var dead []string
	for m := 1; m <= 12; m++ {
		if s.month&(1<<uint(m)) == 0 {
			continue
		}
		dead = append(dead, fmt.Sprintf("%s has %d days", time.Month(m), maxDayIn(time.Month(m))))
	}
	return "can never fire: no day it names occurs in any month it names (" + strings.Join(dead, ", ") + ")"
}

// maxDayIn is the longest that month can be. It is REASON-ONLY: nothing decides
// with it. Reachability is a scan over real dates, so a wrong number here gives
// a wrong SENTENCE rather than a wrong verdict - and that distinction is worth
// stating, because the obvious reading of this function is that it IS the
// check, and a negative control proved it is not: setting February to 28 left
// every test passing until the reason itself was asserted.
//
// February is 29 because leap years exist and `0 0 29 2 *` fires. Writing 28
// here refuses the 30th while telling a person February ends on the 28th - the
// wrong reason for the right answer.
func maxDayIn(m time.Month) int {
	switch m {
	case time.February:
		return 29
	case time.April, time.June, time.September, time.November:
		return 30
	default:
		return 31
	}
}

func parseField(spec string, def fieldDef, text string) (bits uint64, restricted bool, err error) {
	if text == "" {
		return 0, false, &Error{Spec: spec, Field: def.name, Why: "is empty"}
	}
	for _, token := range strings.Split(text, ",") {
		b, wildcard, err := parseToken(spec, def, token)
		if err != nil {
			return 0, false, err
		}
		bits |= b
		if !wildcard {
			restricted = true
		}
	}
	if bits == 0 {
		return 0, false, &Error{Spec: spec, Field: def.name, Token: text, Why: "names no values"}
	}
	return bits, restricted, nil
}

// parseToken handles one comma-separated term. wildcard reports whether the
// term began with `*` - see Spec.domRestricted for why that is tracked rather
// than derived from how many values it allows.
func parseToken(spec string, def fieldDef, token string) (bits uint64, wildcard bool, err error) {
	body, step := token, 1
	if slash := strings.Index(token, "/"); slash >= 0 {
		body = token[:slash]
		stepText := token[slash+1:]
		n, convErr := strconv.Atoi(stepText)
		if convErr != nil || n <= 0 {
			return 0, false, &Error{Spec: spec, Field: def.name, Token: token, Why: "has a step that is not a positive whole number"}
		}
		step = n
	}

	lo, hi := def.min, def.max
	switch {
	case body == "*":
		wildcard = true
	case strings.Contains(body, "-"):
		dash := strings.Index(body, "-")
		var err1, err2 error
		lo, err1 = parseValue(spec, def, body[:dash])
		hi, err2 = parseValue(spec, def, body[dash+1:])
		if err1 != nil {
			return 0, false, err1
		}
		if err2 != nil {
			return 0, false, err2
		}
		if lo > hi {
			return 0, false, &Error{Spec: spec, Field: def.name, Token: token, Why: "is a range that runs backwards - wrapping ranges are not accepted, write two terms"}
		}
	default:
		v, err := parseValue(spec, def, body)
		if err != nil {
			return 0, false, err
		}
		lo, hi = v, v
		if step > 1 {
			// `5/10` is a Vixie extension meaning 5,15,25...  It is
			// accepted here because a person who writes it means it,
			// and refusing would refuse a legal crontab.
			hi = def.max
		}
	}

	for v := lo; v <= hi; v += step {
		bits |= 1 << uint(v)
	}
	return bits, wildcard, nil
}

func parseValue(spec string, def fieldDef, text string) (int, error) {
	t := strings.TrimSpace(text)
	if t == "" {
		return 0, &Error{Spec: spec, Field: def.name, Token: text, Why: "is empty"}
	}
	if def.names != nil {
		if v, ok := def.names[strings.ToLower(t)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(t)
	if err != nil {
		why := "is not a number"
		if def.names != nil {
			why = "is neither a number nor a name this field knows"
		}
		return 0, &Error{Spec: spec, Field: def.name, Token: text, Why: why}
	}
	// Sunday as 7 is folded after parsing; see Parse.
	if def.name == "day-of-week" && v == 7 {
		return 7, nil
	}
	if v < def.min || v > def.max {
		return 0, &Error{Spec: spec, Field: def.name, Token: text, Why: fmt.Sprintf("is outside %d-%d", def.min, def.max)}
	}
	return v, nil
}

// Next returns the first firing strictly after t, in t's own location.
//
// The bool is false when there is none within the search window, which for a
// spec that came from Parse means only one thing - the window ran out - because
// Parse already refused every spec with no firing at all. A caller that built a
// Spec any other way gets the same answer for the other reason, and cannot tell
// them apart; that is why Spec has no exported fields to build one with.
func (s *Spec) Next(t time.Time) (time.Time, bool) {
	return s.next(t, reachWindow)
}

func (s *Spec) next(after time.Time, window time.Duration) (time.Time, bool) {
	// Firings land on whole minutes, so the search starts at the next one.
	// Truncate on a wall clock rather than time.Truncate, which measures
	// from the absolute zero time and is wrong for locations whose offset
	// is not a whole number of hours.
	t := time.Date(after.Year(), after.Month(), after.Day(), after.Hour(), after.Minute(), 0, 0, after.Location()).Add(time.Minute)
	limit := after.Add(window)

	for day := 0; day < int(window/(24*time.Hour))+2; day++ {
		if t.After(limit) {
			return time.Time{}, false
		}
		if s.matchesDay(t) {
			if fire, ok := s.firstInDay(t, after); ok {
				if fire.After(limit) {
					return time.Time{}, false
				}
				return fire, true
			}
		}
		// Next midnight. Constructing the date rather than adding 24h is
		// what keeps this correct across a daylight-saving change, where
		// a day is 23 or 25 hours long.
		t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).AddDate(0, 0, 1)
	}
	return time.Time{}, false
}

// matchesDay applies rule 1. Both restricted is an OR; one restricted means
// that one decides; neither restricted matches every day.
func (s *Spec) matchesDay(t time.Time) bool {
	if s.month&(1<<uint(t.Month())) == 0 {
		return false
	}
	domHit := s.dom&(1<<uint(t.Day())) != 0
	dowHit := s.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case s.domRestricted && s.dowRestricted:
		return domHit || dowHit
	case s.domRestricted:
		return domHit
	case s.dowRestricted:
		return dowHit
	default:
		return true
	}
}

// firstInDay returns the first firing on t's own day that lands strictly after
// floor. Both conditions are needed and neither implies the other: a wall clock
// is not a clock during a daylight-saving change, so the earliest matching WALL
// time of the day can be an INSTANT that has already passed.
//
// The strictly-after test is what makes Next monotonic, and monotonic is not a
// nicety here - a scheduler whose Next can return its own input fires the same
// entry forever and calls it a schedule. That is what this returned before the
// spring-forward arm was written: America/New_York, `30 2 * * *`, 2024-03-10.
func (s *Spec) firstInDay(t, floor time.Time) (time.Time, bool) {
	start := 0
	if sameDay(t, floor) {
		start = floor.Hour()
	}
	for h := start; h < 24; h++ {
		if s.hour&(1<<uint(h)) == 0 {
			continue
		}
		for m := 0; m < 60; m++ {
			if s.minute&(1<<uint(m)) == 0 {
				continue
			}
			fire, ok := atWall(t, h, m)
			if !ok || !fire.After(floor) {
				continue
			}
			return fire, true
		}
	}
	return time.Time{}, false
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// atWall resolves hour:minute on t's calendar day to a real instant.
//
// A wall time is not guaranteed to exist. On a spring-forward day the zone
// skips an hour, and time.Date does not report that - it silently returns the
// instant with the OLD offset, whose wall clock reads an hour EARLIER than
// what was asked for. Reading that back as the firing is how a daily 02:30 in
// America/New_York became a firing at 01:30 that never advanced.
//
// When the asked-for wall time does not exist, this fires at the first instant
// the zone does have at or after it - so a schedule loses no day to a clock
// change, which matches what a person means by "every day at 02:30" and what
// Vixie cron does with the same line.
//
// On a fall-back day a wall time exists TWICE. time.Date returns the first, and
// the first is what fires: one line, one firing, and the caller's floor keeps
// the repeat from being handed out again.
func atWall(t time.Time, h, m int) (time.Time, bool) {
	y, mon, d := t.Date()
	want := time.Date(y, mon, d, h, m, 0, 0, t.Location())
	if want.Hour() == h && want.Minute() == m && want.Day() == d {
		return want, true
	}
	// The gap. Walk forward to the first instant on the same calendar day
	// whose wall clock has reached the asked-for time. A transition is at
	// most a few hours, so a bounded minute walk covers every zone in the
	// database and refuses rather than looping if one ever exceeds it.
	target := h*60 + m
	for i := 1; i <= 4*60; i++ {
		c := want.Add(time.Duration(i) * time.Minute)
		if c.Day() != d {
			break
		}
		if c.Hour()*60+c.Minute() >= target {
			return c, true
		}
	}
	return time.Time{}, false
}
