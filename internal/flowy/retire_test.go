package flowy

import (
	"strings"
	"testing"
)

// The id may precede its flags, which is how it is typed.
//
// 01M2GVZMSC1V4KK2PPCT2ANVQG. Go's flag package stops parsing at the first
// non-flag argument, so `retire ID --dry-run` parses zero flags: --dry-run
// lands in Args() and a dry run writes. Same defect as 2c729ff on skills.
// Caught by running the verb, not by reading it.
func TestRetireTakesItsIdBeforeOrAfterFlags(t *testing.T) {
	// No node is reached: every case here is refused during argument handling,
	// before any call. A case that got as far as the network would be a test
	// that deletes something.
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no id at all", []string{}, "one id"},
		{"only flags", []string{"--dry-run"}, "one id"},
		{"two ids", []string{"01AAA", "01BBB"}, "one id"},
		{"id then a second id after flags", []string{"01AAA", "--dry-run", "01BBB"}, "one id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := retireCmd(tc.args)
			if err == nil {
				t.Fatalf("args %v were accepted; retiring the wrong row is not undone by running it again", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal was %q, which does not say %q", err, tc.want)
			}
		})
	}
}

// LIMIT: the id goes first or last, not between flags. Supporting
// `--json ID --dry-run` means knowing which flags take values, which is
// reimplementing the flag package. Stated in the usage rather than pretended.
//
// THE CASE THAT DISCRIMINATES. A refusal is produced by both the broken and
// the fixed version, so asserting refusals proves nothing about this defect.
// What separates them is an ACCEPTED call whose flags took effect.
func TestRetireFlagsAfterTheIdTakeEffect(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"id first, the way it is typed", []string{"01AAA", "--dry-run"}},
		{"flags first", []string{"--dry-run", "01AAA"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRetireArgs(tc.args)
			if err != nil {
				t.Fatalf("args %v refused: %v", tc.args, err)
			}
			if got.id != "01AAA" {
				t.Fatalf("id = %q, want 01AAA", got.id)
			}
			if !got.dryRun {
				t.Fatal("--dry-run did not take effect, so this call would WRITE while reporting a dry run")
			}
		})
	}
}

// help is a word, not an id: `retire help` must print usage rather than try to
// tombstone a row called "help".
func TestRetireHelpIsNotAnId(t *testing.T) {
	if err := retireCmd([]string{"help"}); err != nil {
		t.Fatalf("retire help returned %v; it must print usage and stop", err)
	}
}
