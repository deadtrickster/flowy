package cron

import (
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, spec string) *Spec {
	t.Helper()
	s, err := Parse(spec)
	if err != nil {
		t.Fatalf("Parse(%q) refused a legal spec: %v", spec, err)
	}
	return s
}

func mustNext(t *testing.T, s *Spec, after time.Time) time.Time {
	t.Helper()
	got, ok := s.Next(after)
	if !ok {
		t.Fatalf("%s has no firing after %s, and Parse said it had one", s, after)
	}
	return got
}

// THE CASE THE PACKAGE EXISTS FOR. A spec that parses, is in range, and can
// never fire is refused at the door with the month named.
func TestFebruaryHasNoThirtieth(t *testing.T) {
	_, err := Parse("0 0 30 2 *")
	if err == nil {
		t.Fatal("February 30th was accepted - it would have been stored, displayed, and silently never fired")
	}
	if !strings.Contains(err.Error(), "February") {
		t.Errorf("the refusal does not name the month, so it does not say what to change: %v", err)
	}
	if _, err := Parse("0 0 31 4 *"); err == nil {
		t.Error("April 31st was accepted")
	}
	if _, err := Parse("0 0 31 2,4,6,9,11 *"); err == nil {
		t.Error("the 31st of five 30-day-or-shorter months was accepted")
	}
}

// THE NEGATIVE CONTROL ON THAT CHECK, and the reason it cannot be written with
// a 28-day February: this spec fires, every leap year, and a checker that
// rejects it has rejected a working schedule.
func TestFebruaryTwentyNinthFiresOnLeapYears(t *testing.T) {
	s := mustParse(t, "0 0 29 2 *")

	got := mustNext(t, s, time.Date(2023, time.March, 1, 0, 0, 0, 0, time.UTC))
	want := time.Date(2024, time.February, 29, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("next firing %s, want %s", got, want)
	}

	// And again from just after that one, to prove the four-year gap is
	// crossed rather than the first hit being luck.
	got = mustNext(t, s, want)
	want = time.Date(2028, time.February, 29, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("second firing %s, want %s", got, want)
	}
}

// RULE 1, and the row's premise it corrects. Both day fields restricted is an
// OR: this fires on every 31st AND on every Monday.
func TestDayOfMonthAndDayOfWeekAreOred(t *testing.T) {
	s := mustParse(t, "0 0 31 * 1")

	// 2024-01-29 is a Monday; the next firing is that same-week Monday.
	got := mustNext(t, s, time.Date(2024, time.January, 28, 12, 0, 0, 0, time.UTC))
	if want := time.Date(2024, time.January, 29, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("Monday arm: %s, want %s", got, want)
	}

	// 2024-01-31 is a Wednesday - reached by the day-of-month arm alone.
	got = mustNext(t, s, time.Date(2024, time.January, 30, 12, 0, 0, 0, time.UTC))
	if want := time.Date(2024, time.January, 31, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("day-of-month arm: %s, want %s", got, want)
	}
}

// The same rule keeping an impossible month/day pair ALIVE. Refusing this would
// be refusing a legal spec, which is the failure worse than storing a dead one.
func TestAnImpossiblePairSurvivesOnDayOfWeek(t *testing.T) {
	s := mustParse(t, "0 0 30 2 1")
	got := mustNext(t, s, time.Date(2024, time.January, 31, 0, 0, 0, 0, time.UTC))
	if want := time.Date(2024, time.February, 5, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("first February Monday: %s, want %s", got, want)
	}
}

// A restricted day-of-week that names only a weekday, in a month with no such
// day, is impossible only in the sense of "not this month" - it still fires.
// This is the arm that fails if matchesDay ANDs when only one field is set.
func TestOneRestrictedFieldDecidesAlone(t *testing.T) {
	dom := mustParse(t, "0 0 15 * *")
	if got := mustNext(t, dom, time.Date(2024, time.June, 1, 0, 0, 0, 0, time.UTC)); got.Day() != 15 {
		t.Errorf("day-of-month alone: %s", got)
	}
	dow := mustParse(t, "0 0 * * sun")
	if got := mustNext(t, dow, time.Date(2024, time.June, 1, 0, 0, 0, 0, time.UTC)); got.Weekday() != time.Sunday {
		t.Errorf("day-of-week alone: %s", got)
	}
}

func TestNamesAndDescriptors(t *testing.T) {
	if a, b := mustParse(t, "0 0 1 JAN *"), mustParse(t, "0 0 1 1 *"); a.month != b.month {
		t.Error("JAN and 1 are different months")
	}
	if a, b := mustParse(t, "0 0 * * SUN"), mustParse(t, "0 0 * * 0"); a.dow != b.dow {
		t.Error("SUN and 0 are different days")
	}
	// Sunday is 7 in half the crontabs ever written.
	if a, b := mustParse(t, "0 0 * * 7"), mustParse(t, "0 0 * * 0"); a.dow != b.dow {
		t.Error("7 did not fold to Sunday")
	}
	if a, b := mustParse(t, "@daily"), mustParse(t, "0 0 * * *"); a.minute != b.minute || a.hour != b.hour {
		t.Error("@daily is not midnight")
	}
	if _, err := Parse("@reboot"); err == nil {
		t.Error("@reboot was accepted, and this node has no boot to hang it off")
	}
}

func TestRefusals(t *testing.T) {
	for _, tc := range []struct{ spec, wants string }{
		{"", "empty"},
		{"0 0 * *", "5"},
		{"0 0 0 * * *", "six-field"},
		{"60 0 * * *", "0-59"},
		{"0 24 * * *", "0-23"},
		{"0 0 0 * *", "1-31"},
		{"0 0 * 13 *", "1-12"},
		{"0 0 * * 8", "0-6"},
		{"*/0 0 * * *", "step"},
		{"0 0 * * mon-sun", "backwards"},
		{"0 0 * * funday", "name"},
		{"0 0 * jam *", "name"},
		{"@fortnightly", "descriptor"},
	} {
		err := func() error { _, err := Parse(tc.spec); return err }()
		if err == nil {
			t.Errorf("Parse(%q) was accepted", tc.spec)
			continue
		}
		if !strings.Contains(err.Error(), tc.wants) {
			t.Errorf("Parse(%q) refused with %q, which does not mention %q", tc.spec, err, tc.wants)
		}
	}
}

// The step extension a person writes when they mean "every ten minutes from
// five past". Refusing it would refuse a legal crontab.
func TestStepFromAValue(t *testing.T) {
	s := mustParse(t, "5/10 * * * *")
	at := time.Date(2024, time.June, 1, 0, 0, 0, 0, time.UTC)
	for _, want := range []int{5, 15, 25, 35, 45, 55} {
		at = mustNext(t, s, at)
		if at.Minute() != want {
			t.Fatalf("firing at :%02d, want :%02d", at.Minute(), want)
		}
	}
}

// A wildcard with a step is still a wildcard for rule 1 - Vixie's behaviour,
// and the one that surprises people, so it is pinned rather than assumed.
func TestSteppedWildcardIsNotRestricted(t *testing.T) {
	s := mustParse(t, "0 0 */2 * 1")
	if s.domRestricted {
		t.Fatal("*/2 counted as a restricted day-of-month, which turns an OR into a filter")
	}
	// With day-of-month unrestricted, only Mondays fire.
	got := mustNext(t, s, time.Date(2024, time.June, 4, 0, 0, 0, 0, time.UTC)) // a Tuesday
	if got.Weekday() != time.Monday {
		t.Fatalf("fired on %s, want Monday", got.Weekday())
	}
}

// Next must be strictly increasing and every firing must match the spec. A
// thousand steps crosses month ends, a leap day, and a year boundary.
func TestNextIsMonotonicAndAlwaysMatches(t *testing.T) {
	s := mustParse(t, "17 3,15 * * *")
	at := time.Date(2024, time.February, 26, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 1000; i++ {
		got := mustNext(t, s, at)
		if !got.After(at) {
			t.Fatalf("step %d went backwards or stood still: %s then %s", i, at, got)
		}
		if got.Minute() != 17 || (got.Hour() != 3 && got.Hour() != 15) {
			t.Fatalf("step %d fired at %s, which the spec does not name", i, got)
		}
		at = got
	}
	if at.Year() != 2025 {
		t.Fatalf("1000 firings of a twice-daily spec ended in %d - the sweep did not cross a year", at.Year())
	}
}

// DAYLIGHT SAVING, SPRING FORWARD. 02:30 does not exist in New York on
// 2024-03-10. The day must not be lost, the firing must be a real instant, and
// - the defect this arm was written for - Next must not return its own input
// forever, which is what a wall time resolved with the pre-transition offset
// produces.
func TestSpringForwardFiresOnceAtTheFirstRealInstant(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no zone database: %v", err)
	}
	s := mustParse(t, "30 2 * * *")

	at := time.Date(2024, time.March, 9, 12, 0, 0, 0, ny)
	var fired []time.Time
	for i := 0; i < 4; i++ {
		next := mustNext(t, s, at)
		if !next.After(at) {
			t.Fatalf("step %d did not advance: %s then %s", i, at, next)
		}
		fired = append(fired, next)
		at = next
	}

	got := make([]string, len(fired))
	for i, f := range fired {
		got[i] = f.Format(time.RFC3339)
	}
	expect := []string{
		"2024-03-10T03:00:00-04:00", // the gap: first instant the zone has
		"2024-03-11T02:30:00-04:00",
		"2024-03-12T02:30:00-04:00",
		"2024-03-13T02:30:00-04:00",
	}
	for i := range expect {
		if got[i] != expect[i] {
			t.Errorf("firing %d: %s, want %s", i, got[i], expect[i])
		}
	}
}

// FALL BACK. 01:30 happens twice in New York on 2024-11-03. One line means one
// firing, and it is the first of the two.
func TestFallBackFiresOnce(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no zone database: %v", err)
	}
	s := mustParse(t, "30 1 * * *")
	at := time.Date(2024, time.November, 2, 12, 0, 0, 0, ny)

	first := mustNext(t, s, at)
	if want := "2024-11-03T01:30:00-04:00"; first.Format(time.RFC3339) != want {
		t.Fatalf("first firing %s, want %s", first.Format(time.RFC3339), want)
	}
	second := mustNext(t, s, first)
	if sameDay(second, first) {
		t.Errorf("the repeated hour fired twice: %s then %s", first.Format(time.RFC3339), second.Format(time.RFC3339))
	}
	if want := "2024-11-04T01:30:00-05:00"; second.Format(time.RFC3339) != want {
		t.Errorf("second firing %s, want %s", second.Format(time.RFC3339), want)
	}
}

// Reachability must not depend on the day it is asked. The fixed epoch is what
// guarantees it; this is the assertion that the epoch is doing that job.
func TestReachabilityDoesNotDependOnWhenItIsAsked(t *testing.T) {
	s := mustParse(t, "0 0 29 2 *")
	for _, start := range []time.Time{
		time.Date(2023, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, time.February, 29, 12, 0, 0, 0, time.UTC),
		time.Date(2025, time.December, 31, 23, 59, 0, 0, time.UTC),
		time.Date(2027, time.July, 4, 6, 0, 0, 0, time.UTC),
	} {
		if _, ok := s.next(start, reachWindow); !ok {
			t.Errorf("a leap-day spec looked dead when asked from %s", start)
		}
	}
}

// THE NEGATIVE CONTROL ON THE WHOLE PARSER: real crontab lines a person would
// type must all survive. A validator that refuses everything passes every test
// above except this one.
func TestRealCrontabsAreAccepted(t *testing.T) {
	for _, spec := range []string{
		"* * * * *",
		"*/5 * * * *",
		"0 9 * * 1-5",
		"0 0 1 * *",
		"30 6 * * SUN",
		"0 */4 * * *",
		"15,45 * * * *",
		"0 0 1,15 * *",
		"0 22 * * 1-5",
		"@hourly",
		"@weekly",
		"0 0 29 2 *",
		"0 0 31 1,3,5,7,8,10,12 *",
	} {
		if _, err := Parse(spec); err != nil {
			t.Errorf("Parse(%q) refused a crontab a person would write: %v", spec, err)
		}
	}
}
