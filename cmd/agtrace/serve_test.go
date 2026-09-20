package main

import (
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dongss/agent-trace/internal/event"
	"github.com/Dongss/agent-trace/internal/reader/claudecode"
)

func stepAt(seq int, t time.Time) event.Step {
	return event.Step{Seq: seq, Kind: event.KindAssistant, At: t, HasTime: true,
		Usage: &event.Usage{Input: 1, Output: 1}}
}

// The presets are relative to today, so a session that ended weeks ago must
// not offer one: it would land the reader on an empty timeline.
func TestQuickRangesOnlyWhereSomethingHappened(t *testing.T) {
	u, _ := url.Parse("/s/abc")
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	got := func(run *event.Run) map[string]string {
		out := map[string]string{}
		for _, r := range buildQuick(run, 2*time.Minute, u, time.Time{}, time.Time{}) {
			out[r.Label] = r.Href
		}
		return out
	}

	today := got(&event.Run{Steps: []event.Step{stepAt(1, midnight.Add(9*time.Hour))}})
	if _, ok := today["Today"]; !ok {
		t.Errorf("a session worked on today does not offer Today: %v", today)
	}
	if _, ok := today["Past week"]; !ok {
		t.Errorf("today is inside the past week: %v", today)
	}
	if _, ok := today["Yesterday"]; ok {
		t.Errorf("nothing happened yesterday, so it must not be offered: %v", today)
	}

	yday := got(&event.Run{Steps: []event.Step{stepAt(1, midnight.AddDate(0, 0, -1).Add(14*time.Hour))}})
	if _, ok := yday["Yesterday"]; !ok {
		t.Errorf("a session worked on yesterday does not offer Yesterday: %v", yday)
	}
	if _, ok := yday["Today"]; ok {
		t.Errorf("nothing happened today: %v", yday)
	}

	// Six days back is inside a seven-day window that ends today; three weeks
	// back is outside every preset.
	if _, ok := got(&event.Run{Steps: []event.Step{stepAt(1, midnight.AddDate(0, 0, -6).Add(time.Hour))}})["Past week"]; !ok {
		t.Error("the sixth day back is inside the past week")
	}
	if n := len(got(&event.Run{Steps: []event.Step{stepAt(1, midnight.AddDate(0, 0, -21))}})); n != 0 {
		t.Errorf("a three-week-old session offered %d presets", n)
	}
	if n := len(got(&event.Run{})); n != 0 {
		t.Errorf("a run with no timestamps offered %d presets", n)
	}
}

// A preset reads as selected only when the page is actually showing it, and
// the bounds it links to are the ones that come back.
func TestQuickRangeMarksItselfActive(t *testing.T) {
	u, _ := url.Parse("/s/abc")
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	run := &event.Run{Steps: []event.Step{stepAt(1, midnight.Add(9*time.Hour))}}

	from := midnight
	to := midnight.AddDate(0, 0, 1).Add(-time.Minute)
	for _, r := range buildQuick(run, 2*time.Minute, u, from, to) {
		if (r.Label == "Today") != r.Active {
			t.Errorf("%s: active = %v, want it only on Today", r.Label, r.Active)
		}
	}
	for _, r := range buildQuick(run, 2*time.Minute, u, time.Time{}, time.Time{}) {
		if r.Active {
			t.Errorf("%s is active on an unwindowed page", r.Label)
		}
	}
}

// A wildcard bind reports itself as [::] on a dual-stack machine, which is not
// an address anybody can type. What gets printed has to be what to open.
func TestListenURLsOnAWildcardBind(t *testing.T) {
	addr := &net.TCPAddr{IP: net.IPv6unspecified, Port: 7391}
	ips := []net.IP{net.IPv4(192, 168, 1, 23)}

	for _, host := range []string{"0.0.0.0", "::", ""} {
		got := listenURLs(addr, host, ips)
		want := []string{"http://localhost:7391", "http://192.168.1.23:7391"}
		if len(got) != len(want) {
			t.Fatalf("host %q gave %v, want %v", host, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("host %q: url %d = %q, want %q", host, i, got[i], want[i])
			}
		}
	}

	// Nothing to share: the machine is still reachable at its own keyboard.
	if got := listenURLs(addr, "0.0.0.0", nil); len(got) != 1 || got[0] != "http://localhost:7391" {
		t.Errorf("with no LAN address = %v", got)
	}
}

// A named host is repeated back as typed. It is what the reader chose, and
// what the socket reports for it says nothing extra.
func TestListenURLsKeepsAHostThatWasNamed(t *testing.T) {
	for _, tc := range []struct{ host, want string }{
		{"127.0.0.1", "http://127.0.0.1:7391"},
		{"localhost", "http://localhost:7391"},
		{"::1", "http://[::1]:7391"},
	} {
		addr := &net.TCPAddr{IP: net.ParseIP(tc.host), Port: 7391}
		got := listenURLs(addr, tc.host, []net.IP{net.IPv4(192, 168, 1, 23)})
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("host %q = %v, want [%s]", tc.host, got, tc.want)
		}
	}
}

// Every address lanIPs offers has to be one somebody else could reach.
func TestLANIPsAreReachableFromElsewhere(t *testing.T) {
	for _, ip := range lanIPs() {
		if ip.To4() == nil {
			t.Errorf("%v is not IPv4", ip)
		}
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			t.Errorf("%v is not an address to hand to anybody", ip)
		}
	}
}

// Every sortable cell is rendered for reading rather than for comparing —
// "1.4B", "$771.71", "117.3M" — so the row carries the raw value for the
// script to order by. Sorting the text would put 1.4B below 336k.
func TestSortKeysCarryRawValues(t *testing.T) {
	cost := 771.71
	started := time.Date(2026, 8, 24, 14, 24, 0, 0, time.UTC)
	touched := time.Date(2026, 9, 17, 18, 2, 0, 0, time.UTC)
	s := claudecode.Session{
		Totals:  &claudecode.Totals{Tokens: event.Usage{Input: 1, CacheRead: 2, CacheWrite: 3, Output: 4}},
		CostUSD: &cost,
		First:   started, HasFirst: true,
		ModTime: touched,
		Size:    123456789,
	}
	got := sortKeys(s)
	want := []string{
		"10", "771.71",
		strconv.FormatInt(started.Unix(), 10),
		strconv.FormatInt(touched.Unix(), 10),
		"123456789",
	}
	if fields := strings.Split(got, "|"); !slices.Equal(fields, want) {
		t.Errorf("sortKeys = %q, want %q", fields, strings.Join(want, "|"))
	}
}

// A session with no cost snapshot, or one whose transcript was never scanned,
// has no value at all — 71 of the 95 surveyed sessions state no cost. The
// field is left empty so the script can sort those last in both directions
// rather than treating an em dash as a zero.
func TestSortKeysLeaveMissingValuesEmpty(t *testing.T) {
	got := sortKeys(claudecode.Session{ModTime: time.Unix(1700000000, 0), Size: 42})
	fields := strings.Split(got, "|")
	if len(fields) != 5 {
		t.Fatalf("sortKeys = %q, want five fields", got)
	}
	for i, name := range []string{"tokens", "cost", "started"} {
		if fields[i] != "" {
			t.Errorf("%s = %q with nothing to report, want empty", name, fields[i])
		}
	}
	// The two a listing always has stay filled: every file has an mtime and a
	// size, so those columns never sort anything to the bottom.
	if fields[3] != "1700000000" || fields[4] != "42" {
		t.Errorf("touched/size = %q/%q, want 1700000000/42", fields[3], fields[4])
	}
}

// The order is part of the view, so the way back carries it beside the filter.
func TestBackHrefCarriesFilterAndOrder(t *testing.T) {
	for _, tc := range []struct {
		q, sort, dir string
		want         []string
	}{
		{"", "", "", []string{"agent=claude-code"}},
		{"vme", "", "", []string{"agent=claude-code", "q=vme"}},
		{"vme", "cost", "desc", []string{"agent=claude-code", "dir=desc", "q=vme", "sort=cost"}},
		{"", "size", "asc", []string{"agent=claude-code", "dir=asc", "sort=size"}},
	} {
		got := backHref("claude-code", tc.q, tc.sort, tc.dir)
		want := "/?" + strings.Join(tc.want, "&")
		if got != want {
			t.Errorf("backHref(%q, %q, %q) = %q, want %q", tc.q, tc.sort, tc.dir, got, want)
		}
	}
}
