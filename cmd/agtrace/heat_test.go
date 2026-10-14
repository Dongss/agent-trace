package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dongss/agent-trace/internal/event"
	"github.com/Dongss/agent-trace/internal/reader/claudecode"
)

// session builds a listed session whose responses were made at the given
// times, each with the same made-up usage.
func heatSession(mod time.Time, at ...time.Time) claudecode.Session {
	tot := &claudecode.Totals{Activity: map[int64]*claudecode.Slot{}}
	for _, t := range at {
		k := claudecode.SlotOf(t)
		if tot.Activity[k] == nil {
			tot.Activity[k] = &claudecode.Slot{}
		}
		sl := tot.Activity[k]
		sl.Responses++
		sl.Tokens.Input += 1
		sl.Tokens.CacheRead += 100
		sl.Tokens.CacheWrite += 10
		sl.Tokens.Output += 5
	}
	return claudecode.Session{ModTime: mod, Totals: tot}
}

// A zone half an hour off the hour is why the reader keeps quarter hours: an
// hour cut in UTC would put 04:15Z and 04:45Z in one local hour, where they
// fall either side of 10:00.
func TestHeatPlacesResponsesInLocalHours(t *testing.T) {
	zone := time.FixedZone("UTC+5:30", 5*3600+1800)
	utc := func(s string) time.Time {
		tm, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, zone)
	a := heatSession(utc("2026-09-18T05:00:00Z"),
		utc("2026-09-18T04:15:00Z"), // 09:45 local
		utc("2026-09-18T04:20:00Z"), // 09:50
		utc("2026-09-18T04:45:00Z"), // 10:15
	)
	b := heatSession(now, utc("2026-09-19T20:00:00Z")) // 01:30 on the 20th
	// Never scanned, and the oldest transcript: last written on the 17th,
	// which leaves the 18th whole.
	none := claudecode.Session{ModTime: utc("2026-09-17T00:00:00Z")}
	h := buildHeat([]claudecode.Session{a, none, b}, now)

	if want := []string{"2026-09-18", "2026-09-19", "2026-09-20"}; !reflect.DeepEqual(h.Days, want) {
		t.Errorf("days %v, want %v", h.Days, want)
	}
	const total = 1 + 100 + 10 + 5
	want := [][][4]int{
		{{9, 2 * total, 2, 10}, {10, total, 1, 5}},
		{},
		{{2*24 + 1, total, 1, 5}},
	}
	if !reflect.DeepEqual(h.Rows, want) {
		t.Errorf("rows %v, want %v", h.Rows, want)
	}
}

// The grid starts the day after the oldest transcript's last write, since a
// cleanup can have taken sessions from that day and every one before it, and
// runs to today, so the days since the last session read as idle.
func TestHeatDrawsOnlyDaysNoCleanupCanHaveTouched(t *testing.T) {
	loc := time.FixedZone("X", 0)
	day := func(d, h int) time.Time { return time.Date(2026, 9, d, h, 0, 0, 0, loc) }
	now := day(25, 9)
	sessions := []claudecode.Session{
		heatSession(day(22, 12), day(18, 10), day(22, 11)), // started before the oldest write
		heatSession(day(21, 8), day(21, 7)),                // the oldest transcript, last written on the 21st
	}
	h := buildHeat(sessions, now)
	if want := []string{"2026-09-22", "2026-09-23", "2026-09-24", "2026-09-25"}; !reflect.DeepEqual(h.Days, want) {
		t.Errorf("days %v, want %v", h.Days, want)
	}
	if want := [][][4]int{{{11, 116, 1, 5}}, {}}; !reflect.DeepEqual(h.Rows, want) {
		t.Errorf("rows %v, want %v", h.Rows, want)
	}
}

// No cleanup period is shorter than a day, so today is whole even when every
// transcript was written today: a machine on its first day gets its first day.
func TestHeatAlwaysDrawsToday(t *testing.T) {
	loc := time.FixedZone("X", 0)
	now := time.Date(2026, 9, 25, 9, 0, 0, 0, loc)
	h := buildHeat([]claudecode.Session{heatSession(now.Add(-time.Hour), now.Add(-2*time.Hour))}, now)
	if !reflect.DeepEqual(h.Days, []string{"2026-09-25"}) || len(h.Rows[0]) != 1 {
		t.Errorf("got %+v, want today alone, its response drawn", h)
	}
	// Begun yesterday and written today: yesterday is not whole.
	h = buildHeat([]claudecode.Session{heatSession(now, now.Add(-12*time.Hour), now.Add(-time.Hour))}, now)
	if !reflect.DeepEqual(h.Days, []string{"2026-09-25"}) || len(h.Rows[0]) != 1 {
		t.Errorf("got %+v, want today alone and yesterday's response left out", h)
	}
}

// Past heatDays the oldest columns go, rather than every column shrinking to
// a sliver.
func TestHeatIsCapped(t *testing.T) {
	loc := time.FixedZone("X", 0)
	now := time.Date(2026, 10, 14, 9, 0, 0, 0, loc)
	old := now.AddDate(0, 0, -200)
	// Kept for a year, so nothing a cleanup took is in the way of the cap.
	kept := claudecode.Session{ModTime: now.AddDate(0, 0, -300)}
	h := buildHeat([]claudecode.Session{heatSession(now, old, now.Add(-time.Hour)), kept}, now)
	if len(h.Days) != heatDays {
		t.Fatalf("%d days, want %d", len(h.Days), heatDays)
	}
	if h.Days[len(h.Days)-1] != "2026-10-14" {
		t.Errorf("last day %s, want today", h.Days[len(h.Days)-1])
	}
	if len(h.Rows[0]) != 1 {
		t.Errorf("row %v: the response from before the first day was drawn", h.Rows[0])
	}
}

// Nothing timed means no days at all, not a grid of zeros: absent is not idle.
func TestHeatWithNothingToPlace(t *testing.T) {
	now := time.Now()
	h := buildHeat([]claudecode.Session{{ModTime: now}, {ModTime: now, Totals: &claudecode.Totals{Tokens: event.Usage{Output: 5}}}}, now)
	if len(h.Days) != 0 || len(h.Rows) != 2 || len(h.Rows[0]) != 0 || len(h.Rows[1]) != 0 {
		t.Errorf("got %+v", h)
	}
	if got := string(heatJSON(h)); !strings.Contains(got, `"days":null`) || !strings.Contains(got, `"rows":[[],[]]`) {
		t.Errorf("json %s", got)
	}
}
