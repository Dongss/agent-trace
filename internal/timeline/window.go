package timeline

import (
	"sort"
	"time"

	"github.com/Dongss/agent-trace/internal/event"
)

// Window narrows a run to the steps whose own timestamp falls in [from, to].
// Either bound may be zero, meaning open at that end; both zero returns the run
// unchanged.
//
// A step is in or out by its own timestamp, tool calls included — so a call
// that began before the window and finished inside it is not shown. The
// alternative, keeping anything that overlaps, makes the window's tool time
// include work done outside it, which is worse in a view whose whole purpose is
// "what happened between these two times".
//
// Two things deliberately do not survive. Stated totals are dropped, because
// the transcript states them for the session and a slice of a session has no
// stated cost. Compactions are re-placed from the original run: they carry no
// timestamp of their own and are positioned between their neighbours in file
// order, which needs the neighbours that the window is about to remove.
func Window(run *event.Run, from, to time.Time) *event.Run {
	full := *run
	full.FullStart, full.FullEnd = run.Start, run.End
	if from.IsZero() && to.IsZero() {
		return &full
	}

	in := func(t time.Time) bool {
		if !from.IsZero() && t.Before(from) {
			return false
		}
		if !to.IsZero() && t.After(to) {
			return false
		}
		return true
	}

	out := full
	out.Windowed = true
	out.WindowFrom, out.WindowTo = from, to
	out.Stated = nil
	out.Steps = nil
	out.Compacts = nil
	out.Start, out.End = time.Time{}, time.Time{}

	// Tool results are paired by the reader, so a kept call keeps its own
	// duration and outcome; nothing here has to re-pair anything.
	for i := range run.Steps {
		st := run.Steps[i]
		if !st.HasTime || !in(st.At) {
			continue
		}
		out.Steps = append(out.Steps, st)
		if out.Start.IsZero() || st.At.Before(out.Start) {
			out.Start = st.At
		}
		if st.At.After(out.End) {
			out.End = st.At
		}
	}

	for _, c := range run.Compacts {
		at, ok := c.At, c.HasTime
		if !ok {
			at, ok = PlaceBySeq(run, c.Seq)
		}
		if ok && in(at) {
			out.Compacts = append(out.Compacts, c)
		}
	}
	return &out
}

// DayRange is one local calendar day the run was worked on, for offering as a
// range to look at.
type DayRange struct {
	// From and To bracket the day's activity, not the calendar day: they are
	// the first and last timestamp in it, so selecting one cannot pull in a
	// neighbouring day through a rounding edge.
	From, To  time.Time
	Steps     int
	Responses int
	Tools     int
	Active    time.Duration
}

// Day is the calendar day, at local midnight.
func (d DayRange) Day() time.Time {
	t := d.From.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// ActiveDays buckets a run's steps by local calendar day.
//
// Local, not UTC: a person choosing "Sep 17" means their own Sep 17. The
// transcript stores UTC and this is the one place that decides what a day is,
// so every label the page shows goes through the same conversion.
//
// Days are the offered unit rather than working stretches because their number
// is bounded and predictable — a 24-day run has at most 24 of them, and had 10
// with any activity — where stretches split on every coffee break.
func ActiveDays(run *event.Run, idleCutoff time.Duration) []DayRange {
	byDay := map[time.Time]*DayRange{}
	var order []time.Time
	perDay := map[time.Time][]time.Time{}

	for i := range run.Steps {
		st := &run.Steps[i]
		if !st.HasTime {
			continue
		}
		local := st.At.Local()
		key := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
		d, ok := byDay[key]
		if !ok {
			d = &DayRange{From: st.At, To: st.At}
			byDay[key] = d
			order = append(order, key)
		}
		d.Steps++
		if st.Usage != nil {
			d.Responses++
		}
		if st.Kind == event.KindTool {
			d.Tools++
		}
		if st.At.Before(d.From) {
			d.From = st.At
		}
		if st.At.After(d.To) {
			d.To = st.At
		}
		perDay[key] = append(perDay[key], st.At)
	}

	// Active time is what the day's own clock says, so it excludes the idle
	// stretches inside the day the same way the axis does.
	for key, d := range byDay {
		d.Active = NewClock(perDay[key], idleCutoff).Active()
	}

	sort.Slice(order, func(i, j int) bool { return order[i].Before(order[j]) })
	out := make([]DayRange, 0, len(order))
	for _, key := range order {
		out = append(out, *byDay[key])
	}
	return out
}
