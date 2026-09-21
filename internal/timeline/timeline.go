// Package timeline turns a Run into the things a renderer needs: totals that
// can be trusted, a stable order over steps whose timestamps cannot be, and a
// clock that survives a session left open for days.
package timeline

import (
	"math"
	"sort"
	"time"

	"github.com/Dongss/agent-trace/internal/event"
)

// Totals is everything recomputed from the run's steps.
//
// Recomputed is not the same as stated. On the survey session the two differ:
// the transcript's own cost snapshot names models the assistant messages never
// mention (a short haiku model, presumably a background task) and labels the
// main model differently ("claude-opus-5[1m]" against the messages'
// "claude-opus-5"), so summing messages cannot reproduce it. Both are reported
// and the disagreement is shown rather than reconciled by guesswork.
type Totals struct {
	All              event.Usage
	ByModel          map[string]event.Usage
	ResponsesByModel map[string]int
	Responses        int // API responses that carried usage, after deduplication

	ToolCalls   int
	ToolsByName map[string]int
	Outcomes    map[event.Outcome]int
	ToolTime    time.Duration // summed over calls with a usable duration
	TimedCalls  int

	Prompts int

	DroppedByCompaction int // as the last compaction reports it, cumulatively
}

// Compute aggregates a run. Usage arrives already deduplicated by the reader,
// so this only ever adds each response once.
func Compute(run *event.Run) Totals {
	t := Totals{
		ByModel:          map[string]event.Usage{},
		ResponsesByModel: map[string]int{},
		ToolsByName:      map[string]int{},
		Outcomes:         map[event.Outcome]int{},
	}
	for i := range run.Steps {
		st := &run.Steps[i]
		if u := st.Usage; u != nil {
			t.Responses++
			t.All = add(t.All, *u)
			model := st.Model
			if model == "" {
				model = "(unrecorded)"
			}
			t.ByModel[model] = add(t.ByModel[model], *u)
			t.ResponsesByModel[model]++
		}
		switch st.Kind {
		case event.KindPrompt:
			t.Prompts++
		case event.KindTool:
			if st.Tool == nil {
				continue
			}
			t.ToolCalls++
			t.ToolsByName[st.Tool.Name]++
			t.Outcomes[st.Tool.Outcome]++
			if st.Tool.HasDuration {
				t.ToolTime += st.Tool.Duration
				t.TimedCalls++
			}
		}
	}
	if n := len(run.Compacts); n > 0 {
		t.DroppedByCompaction = run.Compacts[n-1].Dropped
	}
	return t
}

func add(a, b event.Usage) event.Usage {
	return event.Usage{
		Input:      a.Input + b.Input,
		CacheRead:  a.CacheRead + b.CacheRead,
		CacheWrite: a.CacheWrite + b.CacheWrite,
		Output:     a.Output + b.Output,
		Thinking:   a.Thinking + b.Thinking,
	}
}

// Order sorts step indices for display. Timestamps in a real transcript are
// not monotonic — 248 out-of-order pairs in the surveyed session — and nine
// entry types carry none at all.
//
// Comparing "by time, or by file order when a time is missing" looks like the
// obvious rule and is not a valid ordering: with A(t=5,seq=1), B(t=1,seq=2),
// C(no time,seq=3), D(t=3,seq=4) it says B<C, C<D, D<A and B<A but also A<C,
// which is a cycle, and sort.Slice given a cycle returns an arbitrary
// permutation. So every step is given an effective time first — an untimed one
// inherits the last timestamp seen before it, which is where it belongs — and
// the sort is then a plain lexicographic (time, seq).
func Order(run *event.Run) []int {
	idx := make([]int, len(run.Steps))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return run.Steps[idx[a]].Seq < run.Steps[idx[b]].Seq })

	eff := make([]time.Time, len(run.Steps))
	var carry time.Time
	for _, i := range idx {
		if run.Steps[i].HasTime {
			carry = run.Steps[i].At
		}
		eff[i] = carry
	}
	// Steps before the first timestamp in the file have nothing to inherit;
	// give them the run's first known time so they sort ahead of it by Seq
	// rather than landing in year 1.
	var first time.Time
	for _, i := range idx {
		if run.Steps[i].HasTime {
			first = run.Steps[i].At
			break
		}
	}
	for _, i := range idx {
		if eff[i].IsZero() {
			eff[i] = first
		}
	}

	sort.SliceStable(idx, func(a, b int) bool {
		x, y := idx[a], idx[b]
		if !eff[x].Equal(eff[y]) {
			return eff[x].Before(eff[y])
		}
		return run.Steps[x].Seq < run.Steps[y].Seq
	})
	return idx
}

// PlaceBySeq gives an untimed entry a position on the clock by interpolating
// between the nearest timestamped steps on either side in file order. It
// exists for the compaction boundary: every surveyed one carried a timestamp,
// but one that does not still has to be plotted, because dropping it would
// lose the most important event in the file.
func PlaceBySeq(run *event.Run, seq int) (time.Time, bool) {
	var before, after time.Time
	var haveBefore, haveAfter bool
	var beforeSeq, afterSeq int
	for i := range run.Steps {
		st := &run.Steps[i]
		if !st.HasTime {
			continue
		}
		if st.Seq <= seq && (!haveBefore || st.Seq > beforeSeq) {
			before, beforeSeq, haveBefore = st.At, st.Seq, true
		}
		if st.Seq >= seq && (!haveAfter || st.Seq < afterSeq) {
			after, afterSeq, haveAfter = st.At, st.Seq, true
		}
	}
	switch {
	case haveBefore && haveAfter:
		if afterSeq == beforeSeq {
			return before, true
		}
		frac := float64(seq-beforeSeq) / float64(afterSeq-beforeSeq)
		d := after.Sub(before)
		return before.Add(time.Duration(frac * float64(d))), true
	case haveBefore:
		return before, true
	case haveAfter:
		return after, true
	}
	return time.Time{}, false
}

// Gap is a stretch of wall clock with nothing in it, shown compressed.
type Gap struct {
	Start, End time.Time
	Dur        time.Duration
	// At is where the gap sits on the compressed axis, 0..1.
	At float64
}

// Clock maps wall-clock time onto a 0..1 axis with idle stretches squeezed.
//
// This is not cosmetic. The surveyed session was opened on 24 August and last
// touched on 17 September while accounting for 19 hours of actual work: on a
// linear axis the entire run is a handful of pixels separated by empty weeks.
// Idle stretches longer than the cutoff are compressed to the cutoff's width
// and marked, so the time inside a working stretch stays proportional.
type Clock struct {
	segs   []segment
	total  float64
	gaps   []Gap
	cutoff time.Duration
	span   time.Duration
}

type segment struct {
	start, end     time.Time
	dispStart, len float64
	idle           bool
}

// NewClock builds a clock over points. Cutoff is how long a stretch of nothing
// has to be before it is compressed; zero picks a default.
func NewClock(points []time.Time, cutoff time.Duration) *Clock {
	if cutoff <= 0 {
		cutoff = 2 * time.Minute
	}
	ts := make([]time.Time, 0, len(points))
	for _, t := range points {
		if !t.IsZero() {
			ts = append(ts, t)
		}
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
	c := &Clock{cutoff: cutoff}
	if len(ts) == 0 {
		return c
	}
	if len(ts) == 1 {
		c.segs = []segment{{start: ts[0], end: ts[0], dispStart: 0, len: 1}}
		c.total = 1
		return c
	}
	c.span = ts[len(ts)-1].Sub(ts[0])

	disp := 0.0
	segStart := ts[0]
	for i := 1; i < len(ts); i++ {
		gap := ts[i].Sub(ts[i-1])
		if gap <= cutoff {
			continue
		}
		// Close the working stretch, then add the idle one at cutoff width.
		if d := ts[i-1].Sub(segStart); true {
			c.segs = append(c.segs, segment{start: segStart, end: ts[i-1], dispStart: disp, len: float64(d)})
			disp += float64(d)
		}
		c.segs = append(c.segs, segment{start: ts[i-1], end: ts[i], dispStart: disp, len: float64(cutoff), idle: true})
		c.gaps = append(c.gaps, Gap{Start: ts[i-1], End: ts[i], Dur: gap, At: disp})
		disp += float64(cutoff)
		segStart = ts[i]
	}
	last := ts[len(ts)-1]
	c.segs = append(c.segs, segment{start: segStart, end: last, dispStart: disp, len: float64(last.Sub(segStart))})
	disp += float64(last.Sub(segStart))

	if disp <= 0 {
		disp = 1
		c.segs = []segment{{start: ts[0], end: last, dispStart: 0, len: 1}}
	}
	c.total = disp
	for i := range c.gaps {
		c.gaps[i].At /= c.total
	}
	return c
}

// X maps a time to 0..1 on the compressed axis.
func (c *Clock) X(t time.Time) float64 {
	if c == nil || len(c.segs) == 0 || t.IsZero() {
		return 0
	}
	if t.Before(c.segs[0].start) {
		return 0
	}
	i := sort.Search(len(c.segs), func(i int) bool { return c.segs[i].end.After(t) })
	if i >= len(c.segs) {
		return 1
	}
	s := c.segs[i]
	span := s.end.Sub(s.start)
	frac := 0.0
	if span > 0 {
		frac = float64(t.Sub(s.start)) / float64(span)
	}
	return clamp01((s.dispStart + frac*s.len) / c.total)
}

// Gaps are the compressed idle stretches, for marking on the axis.
func (c *Clock) Gaps() []Gap { return c.gaps }

// Span is the real wall clock the run covers, idle time included.
func (c *Clock) Span() time.Duration { return c.span }

// Active is the wall clock left after compression: roughly the time the run
// was actually doing something.
func (c *Clock) Active() time.Duration {
	if c == nil {
		return 0
	}
	var d time.Duration
	for _, s := range c.segs {
		if !s.idle {
			d += s.end.Sub(s.start)
		}
	}
	return d
}

// Ticks returns up to n labelled positions along the axis, one per working
// stretch, so a compressed axis still says what time it is.
func (c *Clock) Ticks(n int) []struct {
	At float64
	T  time.Time
} {
	var out []struct {
		At float64
		T  time.Time
	}
	if c == nil || len(c.segs) == 0 {
		return out
	}
	for _, s := range c.segs {
		if s.idle {
			continue
		}
		out = append(out, struct {
			At float64
			T  time.Time
		}{clamp01(s.dispStart / c.total), s.start})
	}
	if len(out) <= n {
		return out
	}
	// Thin evenly rather than truncating: the end of a run matters as much as
	// its start.
	step := float64(len(out)-1) / float64(n-1)
	thinned := make([]struct {
		At float64
		T  time.Time
	}, 0, n)
	for i := 0; i < n; i++ {
		thinned = append(thinned, out[int(math.Round(float64(i)*step))])
	}
	return thinned
}

func clamp01(f float64) float64 {
	if f < 0 || math.IsNaN(f) {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
