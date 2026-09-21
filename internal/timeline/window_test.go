package timeline

import (
	"testing"
	"time"

	"github.com/Dongss/agent-trace/internal/event"
)

func windowRun() *event.Run {
	step := func(seq int, ts string, kind event.StepKind, out int) event.Step {
		s := event.Step{Seq: seq, Kind: kind, At: at(ts), HasTime: true}
		if out > 0 {
			s.Usage = &event.Usage{Output: out, CacheRead: 100 * out}
		}
		if kind == event.KindTool {
			s.Tool = &event.Tool{ID: ts, Name: "Bash", Outcome: event.OutcomeOK}
		}
		return s
	}
	return &event.Run{
		Start: at("2026-09-01T10:00:00Z"),
		End:   at("2026-09-03T12:00:00Z"),
		Steps: []event.Step{
			step(1, "2026-09-01T10:00:00Z", event.KindPrompt, 0),
			step(2, "2026-09-01T10:01:00Z", event.KindAssistant, 10),
			step(3, "2026-09-02T09:00:00Z", event.KindAssistant, 20),
			step(4, "2026-09-02T09:05:00Z", event.KindTool, 0),
			step(6, "2026-09-03T12:00:00Z", event.KindAssistant, 30),
			{Seq: 7, Kind: event.KindNote}, // untimed: cannot be in any window
		},
		// Compaction 5 sits between seq 4 and seq 6 and carries no timestamp.
		Compacts: []event.Compact{{Seq: 5, Trigger: "auto", Pre: 900, Post: 40}},
		Stated:   &event.Stated{CostUSD: 12.5, ByModel: map[string]event.Usage{"m": {Output: 60}}},
		Skipped:  map[string]int{"attachment": 3},
	}
}

func TestWindowWithNoBoundsIsTheWholeRun(t *testing.T) {
	run := windowRun()
	got := Window(run, time.Time{}, time.Time{})
	if got.Windowed {
		t.Error("an unbounded window marked the run as windowed")
	}
	if len(got.Steps) != len(run.Steps) {
		t.Errorf("got %d steps, want all %d", len(got.Steps), len(run.Steps))
	}
	if got.Stated == nil {
		t.Error("stated totals were dropped without a window")
	}
	// The full span is recorded either way, so a windowed view can name it.
	if !got.FullStart.Equal(run.Start) || !got.FullEnd.Equal(run.End) {
		t.Errorf("full span = %s → %s", got.FullStart, got.FullEnd)
	}
}

func TestWindowKeepsOnlyStepsInside(t *testing.T) {
	run := windowRun()
	got := Window(run, at("2026-09-02T00:00:00Z"), at("2026-09-02T23:59:00Z"))

	if !got.Windowed {
		t.Error("Windowed is false")
	}
	if len(got.Steps) != 2 {
		t.Fatalf("got %d steps, want the 2 on the 2nd: %+v", len(got.Steps), got.Steps)
	}
	for _, st := range got.Steps {
		if st.At.Day() != 2 {
			t.Errorf("step from day %d survived", st.At.Day())
		}
	}
	// Start and End describe the window's contents, not the bounds asked for.
	if !got.Start.Equal(at("2026-09-02T09:00:00Z")) || !got.End.Equal(at("2026-09-02T09:05:00Z")) {
		t.Errorf("span = %s → %s", got.Start, got.End)
	}
	// The transcript states its cost for the session; a slice has none.
	if got.Stated != nil {
		t.Error("stated totals survived into a window")
	}
	// The original is untouched: callers reuse it for other windows.
	if len(run.Steps) != 6 || run.Stated == nil {
		t.Error("Window mutated the run it was given")
	}
	// Facts about reading the file are not facts about the window, but they do
	// still describe how the data got here.
	if got.Skipped["attachment"] != 3 {
		t.Error("the read's own counts were dropped")
	}
}

// An untimed step has no time to be inside a window, so it cannot be kept.
func TestWindowDropsUntimedSteps(t *testing.T) {
	got := Window(windowRun(), at("2026-09-01T00:00:00Z"), at("2026-09-04T00:00:00Z"))
	for _, st := range got.Steps {
		if !st.HasTime {
			t.Error("an untimed step was kept in a window")
		}
	}
	if len(got.Steps) != 5 {
		t.Errorf("got %d steps, want the 5 timed ones", len(got.Steps))
	}
}

// A compaction with no timestamp of its own is placed from the original run,
// whose neighbours the window is about to remove.
func TestWindowPlacesCompactionsFromTheFullRun(t *testing.T) {
	run := windowRun()
	placed, ok := PlaceBySeq(run, 5)
	if !ok {
		t.Fatal("cannot place the compaction at all")
	}

	if got := Window(run, placed.Add(-time.Minute), placed.Add(time.Minute)); len(got.Compacts) != 1 {
		t.Errorf("a window around the compaction kept %d of them", len(got.Compacts))
	}
	if got := Window(run, at("2026-09-01T00:00:00Z"), at("2026-09-01T23:00:00Z")); len(got.Compacts) != 0 {
		t.Errorf("a window before the compaction kept %d of them", len(got.Compacts))
	}
}

func TestWindowOpenEnded(t *testing.T) {
	run := windowRun()
	if got := Window(run, at("2026-09-03T00:00:00Z"), time.Time{}); len(got.Steps) != 1 {
		t.Errorf("open-ended upper bound kept %d steps, want 1", len(got.Steps))
	}
	if got := Window(run, time.Time{}, at("2026-09-01T23:00:00Z")); len(got.Steps) != 2 {
		t.Errorf("open-ended lower bound kept %d steps, want 2", len(got.Steps))
	}
}

func TestActiveDaysBucketsByLocalDay(t *testing.T) {
	days := ActiveDays(windowRun(), time.Minute)
	if len(days) == 0 {
		t.Fatal("no active days")
	}
	// Ascending, non-overlapping, and each one's bounds bracket its own steps.
	for i, d := range days {
		if d.Steps == 0 {
			t.Errorf("day %d has no steps", i)
		}
		if d.To.Before(d.From) {
			t.Errorf("day %d runs backwards", i)
		}
		if i > 0 && !days[i-1].From.Before(d.From) {
			t.Errorf("days are not ascending at %d", i)
		}
		// Selecting a day must return exactly the steps it counted.
		got := Window(windowRun(), d.From, d.To)
		if len(got.Steps) != d.Steps {
			t.Errorf("day %s claims %d steps, its window holds %d",
				d.Day().Format("2006-01-02"), d.Steps, len(got.Steps))
		}
	}
	var total int
	for _, d := range days {
		total += d.Steps
	}
	if total != 5 {
		t.Errorf("days account for %d steps, want the 5 timed ones", total)
	}
}

func TestActiveDaysCountsResponsesAndTools(t *testing.T) {
	var resp, tools int
	for _, d := range ActiveDays(windowRun(), time.Minute) {
		resp += d.Responses
		tools += d.Tools
	}
	if resp != 3 {
		t.Errorf("responses = %d, want 3", resp)
	}
	if tools != 1 {
		t.Errorf("tools = %d, want 1", tools)
	}
}
