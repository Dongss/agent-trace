package main

import (
	"encoding/json"
	"html/template"
	"sort"
	"time"

	"github.com/Dongss/agent-trace/internal/reader/claudecode"
)

// heatDays is the most days the listing's heatmap draws. Claude Code keeps
// a transcript for 30 days after it was last written unless told otherwise,
// so the default leaves room to spare; a machine set to keep a year would
// otherwise get columns a few pixels wide.
const heatDays = 90

// heatData is the listing's heatmap, as its script reads it. The server
// places every slot in a local day and hour, because the listing's other
// times are local to it too; the script only adds up rows and colours cells.
type heatData struct {
	// Days are the drawn local dates, oldest first, to today. Each is a whole
	// day no cleanup can have touched, so none is drawn short of sessions,
	// and none is more than heatDays old.
	Days []string `json:"days"`
	// Rows are the sessions in the listing's order, each a list of
	// [day*24+hour, total tokens, responses, output tokens] for the hours it
	// had a response in. A session that made none has an empty list.
	Rows [][][4]int `json:"rows"`
}

const dateKey = "2006-01-02"

func dayOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// firstWhole is the first day no cleanup can have taken a session from.
//
// Claude Code deletes a transcript once it has gone cleanupPeriodDays
// without a write, so whatever a cleanup took was last written before the
// oldest transcript still here, and every response after that is in a file
// that survived. The day of that last write is not whole, as a session last
// written earlier the same day can be gone, so the next one is the first.
// The period is a whole number of days, at least one, so the cutoff is a
// day behind the sweep at least and today is always whole: a machine whose
// every transcript was written today still gets today. The period itself is
// not read, since any settings file can set it, a project's own included.
func firstWhole(sessions []claudecode.Session, today time.Time) time.Time {
	var oldest time.Time
	for _, s := range sessions {
		if oldest.IsZero() || s.ModTime.Before(oldest) {
			oldest = s.ModTime
		}
	}
	if d := dayOf(oldest.In(today.Location())).AddDate(0, 0, 1); d.Before(today) {
		return d
	}
	return today
}

func buildHeat(sessions []claudecode.Session, now time.Time) heatData {
	loc := now.Location()
	h := heatData{Rows: make([][][4]int, len(sessions))}
	for i := range h.Rows {
		h.Rows[i] = [][4]int{}
	}

	var first, last time.Time
	for _, s := range sessions {
		if s.Totals == nil {
			continue
		}
		for k := range s.Totals.Activity {
			d := dayOf(time.Unix(k, 0).In(loc))
			if first.IsZero() || d.Before(first) {
				first = d
			}
			if last.IsZero() || d.After(last) {
				last = d
			}
		}
	}
	if first.IsZero() {
		return h // nothing timed to place; the page says so
	}
	// To today, so days without a session read as idle, which inside the kept
	// window they are. A response stamped later than now is a clock that ran
	// ahead, and is drawn rather than dropped.
	today := dayOf(now)
	if today.After(last) {
		last = today
	}
	// A day a cleanup may have taken sessions from would read as a quiet
	// one, so it is left out rather than drawn short.
	from := firstWhole(sessions, today)
	if floor := last.AddDate(0, 0, -(heatDays - 1)); floor.After(from) {
		from = floor
	}
	if first.Before(from) {
		first = from
	}

	// By date as written rather than by time.Time: a day's midnight can be
	// skipped by a clock change, and the date is what the page shows anyway.
	index := map[string]int{}
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		index[d.Format(dateKey)] = len(h.Days)
		h.Days = append(h.Days, d.Format(dateKey))
	}

	for i, s := range sessions {
		if s.Totals == nil {
			continue
		}
		cells := map[int]*[4]int{}
		for k, sl := range s.Totals.Activity {
			t := time.Unix(k, 0).In(loc)
			d, ok := index[t.Format(dateKey)]
			if !ok {
				continue // before the first drawn day
			}
			c := d*24 + t.Hour()
			if cells[c] == nil {
				cells[c] = &[4]int{c, 0, 0, 0}
			}
			u := sl.Tokens
			cells[c][1] += u.Input + u.CacheRead + u.CacheWrite + u.Output
			cells[c][2] += sl.Responses
			cells[c][3] += u.Output
		}
		row := make([][4]int, 0, len(cells))
		for _, c := range cells {
			row = append(row, *c)
		}
		sort.Slice(row, func(a, b int) bool { return row[a][0] < row[b][0] })
		h.Rows[i] = row
	}
	return h
}

// heatJSON is the heatmap as the listing embeds it. json.Marshal escapes <, >
// and &, so nothing in it can close the script element it sits in.
func heatJSON(h heatData) template.JS {
	b, err := json.Marshal(h)
	if err != nil {
		return "null"
	}
	return template.JS(b)
}
