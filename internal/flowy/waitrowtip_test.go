package flowy

import "testing"

// --tip BESIDE --row IS A NARROWING, NOT A SECOND SUBJECT.
//
// The usage has always told a caller to pass both - "--tip goes with it: a red
// measured at another tip is not the answer, which is what you want after
// re-tipping a red row" - and the subject counter refused the combination, so
// the documented way to ask the question errored out before the row case could
// hand it to `queue wait`, which is where --tip lives.
//
// MEASURED 2026-08-28: a merge row re-tipped at 15:19 was answered `red` from a
// verdict taken at 10:44, and the flag that would have excluded that verdict
// was rejected as a conflict.
//
// The two refusals that must NOT move are here as their own cases: no subject
// at all is still a sleep, and two genuine subjects still cannot say which one
// answered.
func TestWaitTakesATipBesideARowAndStillRefusesTwoSubjects(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		refused bool
		// what the refusal must mention, so a passing test cannot be a
		// different refusal wearing the same exit code
		says string
	}{
		{"a row and its tip is one question", []string{"--row", "01M0", "--tip", "master", "--deadline", "1"}, false, ""},
		{"no subject is still a sleep", []string{"--deadline", "1"}, true, "no subject"},
		{"a tip and a deploy are still two", []string{"--tip", "master", "--deploy", "--deadline", "1"}, true, "two subjects"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := cliWait(c.args)
			if c.refused {
				if err == nil {
					t.Fatalf("expected a refusal mentioning %q, got none", c.says)
				}
				if !contains(err.Error(), c.says) {
					t.Fatalf("refused with %q, which does not mention %q", err.Error(), c.says)
				}
				return
			}
			// Not refused for BEING two subjects. It may still fail for any
			// other reason - there is no node here - and that is not this
			// test's business.
			if err != nil && contains(err.Error(), "two subjects") {
				t.Fatalf("--row with --tip was refused as two subjects: %v", err)
			}
		})
	}
}

func contains(hay, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
