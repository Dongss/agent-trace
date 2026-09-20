package claudecode

import (
	"testing"
	"time"

	"github.com/Dongss/agent-trace/internal/timeline"
)

func TestScanMatchesFullParseAndIsFaster(t *testing.T) {
	root := DefaultRoot()
	sessions, err := Discover(root)
	if err != nil || len(sessions) == 0 {
		t.Skip("no transcripts")
	}
	if len(sessions) > 20 {
		sessions = sessions[:20]
	}
	var scanDur, parseDur time.Duration
	var bytes int64
	for _, s := range sessions {
		t0 := time.Now()
		tot, err := Scan(s.Path)
		if err != nil {
			t.Fatal(err)
		}
		scanDur += time.Since(t0)
		bytes += s.Size

		t1 := time.Now()
		run, err := ReadFile(s.Path, Options{})
		if err != nil {
			t.Fatal(err)
		}
		parseDur += time.Since(t1)
		full := timeline.Compute(run)

		if tot.Tokens != full.All {
			t.Errorf("%s: scan tokens %+v != full parse %+v", s.ID, tot.Tokens, full.All)
		}
		if tot.Responses != full.Responses {
			t.Errorf("%s: scan responses %d != %d", s.ID, tot.Responses, full.Responses)
		}
		if tot.ToolCalls != full.ToolCalls {
			t.Errorf("%s: scan tool calls %d != %d", s.ID, tot.ToolCalls, full.ToolCalls)
		}
	}
	t.Logf("%d sessions, %.0f MB: scan %v, full parse %v", len(sessions), float64(bytes)/1e6, scanDur.Round(time.Millisecond), parseDur.Round(time.Millisecond))
}
