package claudecode

import (
	"os"
	"regexp"
	"testing"

	"github.com/Dongss/agent-trace/internal/textfmt"

	"github.com/Dongss/agent-trace/internal/event"
)

// TestRealTranscripts runs the parser over the transcripts this machine
// actually has. It is the counterpart to the constructed shapes in
// claudecode_test.go: those pin the behaviour, this one notices when a CLI
// release writes something the survey never saw.
//
// It is skipped when there are no transcripts, so the suite still passes on a
// machine with no Claude Code installed — which is the rule for this
// repository's tests.
func TestRealTranscripts(t *testing.T) {
	root := DefaultRoot()
	if root == "" {
		t.Skip("no home directory")
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("no transcripts at %s", textfmt.Path(root))
	}
	sessions, err := Discover(root)
	if err != nil {
		t.Skipf("cannot list %s: %v", textfmt.Path(root), err)
	}
	if len(sessions) == 0 {
		t.Skip("no transcripts to read")
	}

	const limit = 12 // newest first; enough to cover several CLI versions
	if len(sessions) > limit {
		sessions = sessions[:limit]
	}

	for _, s := range sessions {
		run, err := ReadFile(s.Path, Options{})
		if err != nil {
			t.Errorf("%s: %v", s.ID, err)
			continue
		}

		// A transcript that parses at all should parse almost entirely. A
		// handful of bad lines is plausible; a wall of them means the format
		// moved.
		if run.Malformed > 5 {
			t.Errorf("%s: %d malformed lines", s.ID, run.Malformed)
		}

		for i := range run.Steps {
			st := &run.Steps[i]
			if st.Kind != event.KindTool {
				continue
			}
			if st.Tool == nil {
				t.Errorf("%s: tool step %d has no tool", s.ID, st.Seq)
				continue
			}
			if st.Tool.Outcome == "" {
				t.Errorf("%s: tool %q has no outcome", s.ID, st.Tool.Name)
			}
			// A duration only exists when both ends were timestamped and the
			// result did not predate the call.
			if st.Tool.HasDuration && st.Tool.Duration < 0 {
				t.Errorf("%s: tool %q reports a negative duration %v", s.ID, st.Tool.Name, st.Tool.Duration)
			}
			if !st.Tool.HasDuration && st.Tool.Duration != 0 {
				t.Errorf("%s: tool %q has a duration it says is unusable", s.ID, st.Tool.Name)
			}
		}

		// Paths that agtrace formats for display are shortened; free text is
		// not. A Bash command is the user's own command line and reaches the
		// page as they typed it — there is no redaction to assert about it.
		if m := homeWithName.FindString(run.Path); m != "" {
			t.Errorf("%s: %q survived in the transcript path %q", s.ID, m, run.Path)
		}
		if m := homeWithName.FindString(run.CWD); m != "" {
			t.Errorf("%s: %q survived in the working directory %q", s.ID, m, run.CWD)
		}

		if run.Stated != nil && run.Stated.CostUSD < 0 {
			t.Errorf("%s: negative stated cost %v", s.ID, run.Stated.CostUSD)
		}
	}
}

// homeWithName is a home directory with an account name in it, in either the
// real or the slugified spelling. Path fields are shown with it collapsed to
// "~", which is a display shortening and not a claim about privacy.
var homeWithName = regexp.MustCompile(`(?:/Users/|/home/|-Users-|-home-)[A-Za-z0-9._]+`)
