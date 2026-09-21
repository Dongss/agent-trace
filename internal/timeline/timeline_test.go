package timeline

import (
	"testing"
	"time"

	"github.com/Dongss/agent-trace/internal/event"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// A session left open for days is the normal case, not the exception: the
// surveyed run spanned 24 days and accounted for 18 hours of work. On a linear
// axis it is a few pixels of data between empty weeks.
func TestClockCompressesIdleStretches(t *testing.T) {
	pts := []time.Time{
		at("2026-09-01T10:00:00Z"), at("2026-09-01T10:01:00Z"), at("2026-09-01T10:02:00Z"),
		at("2026-09-08T10:00:00Z"), at("2026-09-08T10:01:00Z"),
	}
	c := NewClock(pts, 2*time.Minute)

	if got, want := c.Span(), 7*24*time.Hour+time.Minute; got != want {
		t.Errorf("Span = %v, want %v", got, want)
	}
	// Active time is the two working stretches, not the week between them.
	if got, want := c.Active(), 3*time.Minute; got != want {
		t.Errorf("Active = %v, want %v", got, want)
	}
	if n := len(c.Gaps()); n != 1 {
		t.Fatalf("got %d gaps, want 1", n)
	}
	if g := c.Gaps()[0]; g.Dur < 6*24*time.Hour {
		t.Errorf("gap duration %v looks wrong", g.Dur)
	}

	// The first stretch is 2 of the 3 active minutes plus the compressed gap,
	// so it must occupy far more than the 0.02% a linear axis would give it.
	x := c.X(at("2026-09-01T10:02:00Z"))
	if x < 0.2 {
		t.Errorf("the first working stretch got %.4f of the axis; compression did not happen", x)
	}
}

func TestClockXIsMonotonicAndBounded(t *testing.T) {
	pts := []time.Time{
		at("2026-09-01T10:00:00Z"), at("2026-09-01T10:00:30Z"),
		at("2026-09-03T08:00:00Z"), at("2026-09-03T08:05:00Z"),
		at("2026-09-03T09:00:00Z"),
	}
	c := NewClock(pts, time.Minute)
	prev := -1.0
	for _, p := range pts {
		x := c.X(p)
		if x < 0 || x > 1 {
			t.Fatalf("X(%s) = %v, outside 0..1", p, x)
		}
		if x < prev {
			t.Fatalf("X went backwards at %s: %v after %v", p, x, prev)
		}
		prev = x
	}
	if c.X(at("2026-08-01T00:00:00Z")) != 0 {
		t.Error("a time before the run should clamp to 0")
	}
	if c.X(at("2026-10-01T00:00:00Z")) != 1 {
		t.Error("a time after the run should clamp to 1")
	}
	if c.X(time.Time{}) != 0 {
		t.Error("the zero time should map to 0, not panic")
	}
}

func TestClockDegenerateInputs(t *testing.T) {
	if c := NewClock(nil, 0); c.X(at("2026-09-01T10:00:00Z")) != 0 || c.Active() != 0 {
		t.Error("an empty clock should be harmless")
	}
	one := NewClock([]time.Time{at("2026-09-01T10:00:00Z")}, 0)
	if x := one.X(at("2026-09-01T10:00:00Z")); x < 0 || x > 1 {
		t.Errorf("single-point clock X = %v", x)
	}
	// Zero times are dropped rather than dragging the axis back to year 1.
	mixed := NewClock([]time.Time{{}, at("2026-09-01T10:00:00Z"), {}}, 0)
	if mixed.Span() != 0 {
		t.Errorf("Span = %v, want 0 for one real point", mixed.Span())
	}
}

func TestTicksThinEvenlyAndKeepTheEnds(t *testing.T) {
	var pts []time.Time
	base := at("2026-09-01T10:00:00Z")
	for i := 0; i < 12; i++ {
		// Each point a day apart, so every one opens its own working stretch.
		pts = append(pts, base.Add(time.Duration(i)*24*time.Hour))
	}
	c := NewClock(pts, time.Minute)
	got := c.Ticks(5)
	if len(got) != 5 {
		t.Fatalf("got %d ticks, want 5", len(got))
	}
	if !got[0].T.Equal(pts[0]) {
		t.Errorf("first tick %s, want the run's start", got[0].T)
	}
	if !got[len(got)-1].T.Equal(pts[len(pts)-1]) {
		t.Errorf("last tick %s, want the run's end", got[len(got)-1].T)
	}
}

// A compaction boundary that carries no timestamp of its own is placed
// between the entries it sits between in file order.
func TestPlaceBySeq(t *testing.T) {
	run := &event.Run{Steps: []event.Step{
		{Seq: 10, At: at("2026-09-01T10:00:00Z"), HasTime: true},
		{Seq: 30, At: at("2026-09-01T10:10:00Z"), HasTime: true},
	}}
	got, ok := PlaceBySeq(run, 20)
	if !ok {
		t.Fatal("no placement found")
	}
	if want := at("2026-09-01T10:05:00Z"); !got.Equal(want) {
		t.Errorf("placed at %s, want %s", got, want)
	}

	// Before the first and after the last timestamped entry, clamp.
	if got, _ := PlaceBySeq(run, 1); !got.Equal(at("2026-09-01T10:00:00Z")) {
		t.Errorf("clamp before start gave %s", got)
	}
	if got, _ := PlaceBySeq(run, 99); !got.Equal(at("2026-09-01T10:10:00Z")) {
		t.Errorf("clamp after end gave %s", got)
	}
	if _, ok := PlaceBySeq(&event.Run{}, 5); ok {
		t.Error("a run with no timestamps cannot place anything")
	}
}

// Timestamps are not monotonic in a real transcript, so Order sorts by time and
// keeps file order as the tiebreaker and for untimed steps.
func TestOrderSortsByTimeThenFileOrder(t *testing.T) {
	run := &event.Run{Steps: []event.Step{
		{Seq: 1, At: at("2026-09-01T10:00:05Z"), HasTime: true},
		{Seq: 2, At: at("2026-09-01T10:00:01Z"), HasTime: true},
		{Seq: 3}, // untimed
		{Seq: 4, At: at("2026-09-01T10:00:03Z"), HasTime: true},
	}}
	got := Order(run)
	var seqs []int
	for _, i := range got {
		seqs = append(seqs, run.Steps[i].Seq)
	}
	// 2 and 4 precede 1 by time; 3 has no time and holds its file position
	// relative to whatever it cannot be compared against.
	if len(seqs) != 4 {
		t.Fatalf("Order dropped steps: %v", seqs)
	}
	pos := map[int]int{}
	for i, s := range seqs {
		pos[s] = i
	}
	if pos[2] > pos[4] || pos[4] > pos[1] {
		t.Errorf("timed steps not in time order: %v", seqs)
	}
}

func TestComputeTotals(t *testing.T) {
	run := &event.Run{
		Steps: []event.Step{
			{Kind: event.KindPrompt},
			{Kind: event.KindAssistant, Model: "m1", Usage: &event.Usage{Input: 1, CacheRead: 100, CacheWrite: 10, Output: 5, Thinking: 2}},
			{Kind: event.KindAssistant, Model: "m1", Usage: &event.Usage{Input: 2, CacheRead: 900, CacheWrite: 20, Output: 7}},
			{Kind: event.KindAssistant, Model: "m2", Usage: &event.Usage{Output: 3}},
			{Kind: event.KindTool, Tool: &event.Tool{Name: "Bash", Outcome: event.OutcomeOK, HasDuration: true, Duration: 2 * time.Second}},
			{Kind: event.KindTool, Tool: &event.Tool{Name: "Bash", Outcome: event.OutcomeError}},
			{Kind: event.KindTool, Tool: &event.Tool{Name: "Read", Outcome: event.OutcomeUnpaired}},
		},
		Compacts: []event.Compact{{Dropped: 100}, {Dropped: 250}},
	}
	got := Compute(run)

	if got.Prompts != 1 || got.Responses != 3 || got.ToolCalls != 3 {
		t.Errorf("counts: %+v", got)
	}
	if got.All.CacheRead != 1000 || got.All.Output != 15 || got.All.Thinking != 2 {
		t.Errorf("totals: %+v", got.All)
	}
	if got.ByModel["m1"].Output != 12 || got.ByModel["m2"].Output != 3 {
		t.Errorf("per model: %+v", got.ByModel)
	}
	if got.ToolTime != 2*time.Second || got.TimedCalls != 1 {
		t.Errorf("tool time %v over %d calls", got.ToolTime, got.TimedCalls)
	}
	if got.ToolsByName["Bash"] != 2 || got.Outcomes[event.OutcomeError] != 1 {
		t.Errorf("tool mix %v / outcomes %v", got.ToolsByName, got.Outcomes)
	}
	// Dropped is already cumulative in the transcript, so the last one is the
	// run's figure — adding them up would double count.
	if got.DroppedByCompaction != 250 {
		t.Errorf("DroppedByCompaction = %d, want the last cumulative value 250", got.DroppedByCompaction)
	}
}
