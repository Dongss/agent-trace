package main

import (
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dongss/agent-trace/internal/agent"
	"github.com/Dongss/agent-trace/internal/event"
	"github.com/Dongss/agent-trace/internal/reader/claudecode"
	"github.com/Dongss/agent-trace/internal/render"
	"github.com/Dongss/agent-trace/internal/textfmt"
	"github.com/Dongss/agent-trace/internal/timeline"
	"github.com/Dongss/agent-trace/internal/version"
)

// idleCutoff is how long a stretch of nothing has to be before the timeline's
// axis compresses it. Two minutes keeps a coffee break from swallowing the
// part of the run with the work in it.
const idleCutoff = 2 * time.Minute

// serve runs the whole program: a page listing the sessions on this machine,
// and one page per session.
func serve(host string, port int) error {
	defaultAgent := agent.Default()

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	// Bind before announcing: printing a URL that is not listening yet sends
	// the reader to a connection error.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	totals := newTotalsCache()

	// resolve picks the agent for a request, falling back to the one the
	// command was started with.
	resolve := func(r *http.Request) (agent.Agent, error) {
		id := r.URL.Query().Get("agent")
		if id == "" {
			id = defaultAgent.ID
		}
		a, err := agent.Lookup(id)
		if err != nil {
			return agent.Agent{}, err
		}
		return a, nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		a, err := resolve(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data := indexData{
			ThemeCSS: template.CSS(render.ThemeCSS()),
			ThemeJS:  template.JS(render.ThemeJS()),
			Version:  version.String(),
			Agent:    a,
			Agents:   agent.List(),
		}
		switch {
		case !a.Implemented:
			data.Problem = a.Why
		case !a.HasRoot():
			data.Problem = "No transcripts on this machine: nothing at " + textfmt.Path(a.Root)
		default:
			sessions, err := a.Discover()
			if err != nil {
				data.Problem = "Cannot list " + textfmt.Path(a.Root) + ": " + err.Error()
				break
			}
			totals.fill(sessions)
			data.Sessions = sessions
			if len(sessions) == 0 {
				data.Problem = "No sessions under " + textfmt.Path(a.Root)
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = indexTmpl.Execute(w, data)
	})

	mux.HandleFunc("/s/", func(w http.ResponseWriter, r *http.Request) {
		a, err := resolve(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ref := strings.TrimPrefix(r.URL.Path, "/s/")
		// Every request re-reads the transcript, so reloading a page follows a
		// session that is still being written.
		run, err := loadRun(a, ref)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}

		q := r.URL.Query()
		from, ferr := parseBound(q.Get("from"))
		to, terr := parseBound(q.Get("to"))
		if ferr != nil || terr != nil {
			http.Error(w, "from and to must look like 2026-09-17T15:04 or 2026-09-17", http.StatusBadRequest)
			return
		}
		// An end-of-day bound with no time means the whole day.
		if to2, ok := endOfDay(q.Get("to"), to); ok {
			to = to2
		}
		ranges := buildRanges(run, idleCutoff, r.URL, from, to)
		quick := buildQuick(run, idleCutoff, r.URL, from, to)
		windowed := timeline.Window(run, from, to)

		page, err := render.Page(windowed, render.Options{
			IdleCutoff: idleCutoff,
			// A served page gets a way back to the list and controls to ask for
			// another window; a copy saved out of the browser gets neither, because
			// there is no server behind it to answer.
			Version:   version.String(),
			Back:      backHref(a.ID, q.Get("q")),
			BackLabel: a.Name + " sessions",
			Ranges:    ranges,
			Quick:     quick,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(page)
	})

	// One line per address and nothing else: everything this could say is on
	// the page it points at.
	for _, u := range listenURLs(ln.Addr(), host, lanIPs()) {
		fmt.Println(u)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(ln)
}

// listenURLs is what to open, given what the listener actually bound to.
//
// A wildcard bind is the case worth handling: asking for 0.0.0.0 on a
// dual-stack machine gets a socket that reports itself as [::], which is not
// an address anybody can type into a browser. Sharing is the whole reason to
// pass it, so the reply is localhost — for the person at the keyboard — plus
// every address the machine can be reached at from elsewhere.
func listenURLs(addr net.Addr, host string, ips []net.IP) []string {
	port := "0"
	if _, p, err := net.SplitHostPort(addr.String()); err == nil {
		port = p
	}
	if !isWildcard(host) {
		return []string{"http://" + net.JoinHostPort(host, port)}
	}
	out := []string{"http://" + net.JoinHostPort("localhost", port)}
	for _, ip := range ips {
		out = append(out, "http://"+net.JoinHostPort(ip.String(), port))
	}
	return out
}

// isWildcard reports whether host means "every interface". An empty host is
// the flag package's way of spelling the same thing.
func isWildcard(host string) bool {
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

// lanIPs is the addresses this machine can be reached at from another one.
// Loopback, link-local and anything on an interface that is down are left out:
// none of them is an address to hand to somebody else. IPv6 is left out too —
// it would double the list on most machines and be the wrong half of it on the
// rest.
func lanIPs() []net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := n.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ip)
		}
	}
	return out
}

// parseBound reads a from/to query value. Both the minute-precision form a
// datetime-local input produces and a bare date are accepted, in the reader's
// own zone — which is also the zone the page labels its times with.
func parseBound(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse %q as a time", v)
}

// endOfDay stretches a bare date used as an upper bound to the end of that day.
// "to=2026-09-17" means through the 17th, not up to its first instant.
func endOfDay(raw string, t time.Time) (time.Time, bool) {
	if len(strings.TrimSpace(raw)) != len("2006-01-02") || t.IsZero() {
		return t, false
	}
	return t.Add(24*time.Hour - time.Nanosecond), true
}

// buildRanges offers the whole run and each local day it was worked on. Days
// are the unit because their number is bounded and a person thinks in them;
// the free-form inputs on the page cover anything finer.
func buildRanges(run *event.Run, idle time.Duration, current *url.URL, from, to time.Time) []render.Range {
	withBounds := func(f, t string) string { return boundsHref(current, f, t) }

	out := []render.Range{{
		Label:  "Full run",
		Href:   withBounds("", ""),
		Active: from.IsZero() && to.IsZero(),
	}}

	days := timeline.ActiveDays(run, idle)
	if len(days) < 2 {
		// One day of activity is the full run; offering it twice says nothing.
		return out
	}
	for _, d := range days {
		f := d.From.Local().Format("2006-01-02T15:04")
		t := d.To.Local().Add(time.Minute).Format("2006-01-02T15:04")
		out = append(out, render.Range{
			Label: d.Day().Format("Mon 2 Jan"),
			Href:  withBounds(f, t),
			Note: fmt.Sprintf("%s active · %d responses · %d tool calls",
				shortDur(d.Active), d.Responses, d.Tools),
			Active: !from.IsZero() && from.Equal(mustParseLocal(f)) && !to.IsZero() && to.Equal(mustParseLocal(t)),
		})
	}
	return out
}

// boundsHref is this page's URL with from and to replaced. An empty bound
// drops the parameter, which is what an open one means on the way back in.
func boundsHref(current *url.URL, f, t string) string {
	u := *current
	v := u.Query()
	if f == "" {
		v.Del("from")
	} else {
		v.Set("from", f)
	}
	if t == "" {
		v.Del("to")
	} else {
		v.Set("to", t)
	}
	u.RawQuery = v.Encode()
	return u.RequestURI()
}

// buildQuick offers today, yesterday and the last seven days, in the reader's
// own zone. They are relative to now rather than to the run, so a preset that
// would select nothing is left out entirely: most sessions in a listing ended
// days ago, and a control that lands on an empty timeline is worse than one
// that is not there. Whether a day is worth offering comes from ActiveDays,
// which only reports days something actually happened on — the run's start and
// end bracket its idle stretches too, and testing against those would offer a
// Tuesday nobody worked on.
func buildQuick(run *event.Run, idle time.Duration, current *url.URL, from, to time.Time) []render.Range {
	days := timeline.ActiveDays(run, idle)
	if len(days) == 0 {
		return nil
	}
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	windows := []struct {
		label string
		from  time.Time
		days  int // how many days the window covers, ending today
	}{
		{"Today", midnight, 1},
		{"Yesterday", midnight.AddDate(0, 0, -1), 1},
		{"Past week", midnight.AddDate(0, 0, -6), 7},
	}

	var out []render.Range
	for _, w := range windows {
		end := w.from.AddDate(0, 0, w.days)
		worked := false
		for _, d := range days {
			if d.To.Local().After(w.from) && d.From.Local().Before(end) {
				worked = true
				break
			}
		}
		if !worked {
			continue
		}
		f := w.from.Format("2006-01-02T15:04")
		// One minute short of the next midnight, so a window never reaches
		// into the day after it.
		t := end.Add(-time.Minute).Format("2006-01-02T15:04")
		out = append(out, render.Range{
			Label:  w.label,
			Href:   boundsHref(current, f, t),
			Active: !from.IsZero() && from.Equal(mustParseLocal(f)) && !to.IsZero() && to.Equal(mustParseLocal(t)),
		})
	}
	return out
}

func mustParseLocal(v string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02T15:04", v, time.Local)
	return t
}

func shortDur(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// backHref points a session page back at the list it came from, filter
// included: the index puts its search query on each session link so that the
// way back lands on the same rows.
func backHref(agentID, q string) string {
	v := url.Values{}
	v.Set("agent", agentID)
	if q != "" {
		v.Set("q", q)
	}
	return "/?" + v.Encode()
}

// totalsCache keeps the per-session scan across requests. Titles and token
// counts need the whole transcript read, which is 1.6s for the 911 MB on the
// survey machine — fine once, not on every page load. The key includes size
// and mtime, so a session that has grown is rescanned and a finished one never
// is.
type totalsCache struct {
	mu sync.Mutex
	m  map[string]*claudecode.Totals
}

func newTotalsCache() *totalsCache {
	return &totalsCache{m: map[string]*claudecode.Totals{}}
}

func (c *totalsCache) fill(sessions []claudecode.Session) {
	for i := range sessions {
		s := &sessions[i]
		key := fmt.Sprintf("%s|%d|%d", s.Path, s.Size, s.ModTime.UnixNano())

		c.mu.Lock()
		hit, ok := c.m[key]
		c.mu.Unlock()
		if ok {
			s.Totals = hit
			if s.CostUSD == nil && hit.Cost != nil {
				s.CostUSD = hit.Cost
			}
			continue
		}

		t, err := claudecode.Scan(s.Path)
		if err != nil {
			continue // a row with holes beats no row
		}
		s.Totals = t
		if s.CostUSD == nil && t.Cost != nil {
			s.CostUSD = t.Cost
		}
		c.mu.Lock()
		c.m[key] = t
		c.mu.Unlock()
	}
}

type indexData struct {
	ThemeCSS template.CSS
	ThemeJS  template.JS
	Version  string
	Agent    agent.Agent
	Agents   []agent.Agent
	Sessions []claudecode.Session
	// Problem is why there is no table: no reader, no directory, or nothing in
	// it. Saying which beats an empty page.
	Problem string
}

var indexTmpl = template.Must(template.New("index").Funcs(template.FuncMap{
	"short": shortID,
	"size":  humanBytes,
	"tokens": func(s claudecode.Session) string {
		if s.Totals == nil {
			return "—"
		}
		return compactInt(s.Totals.Total())
	},
	"tokenDetail": func(s claudecode.Session) string {
		if s.Totals == nil {
			return "not read"
		}
		t := s.Totals.Tokens
		return fmt.Sprintf("fresh input %d · cache read %d · cache write %d · output %d (thinking %d) · %d responses, %d tool calls",
			t.Input, t.CacheRead, t.CacheWrite, t.Output, t.Thinking, s.Totals.Responses, s.Totals.ToolCalls)
	},
	"cost": func(c *float64) string {
		if c == nil {
			return "—"
		}
		return fmt.Sprintf("$%.2f", *c)
	},
	"when": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04") },
	// Started is the transcript's own first timestamp, which is UTC — unlike
	// the mtime beside it. It is missing when nothing in the head of the file
	// carried one.
	"started": func(s claudecode.Session) string {
		if !s.HasFirst {
			return "—"
		}
		return s.First.Local().Format("2006-01-02 15:04")
	},
	"live": func(s claudecode.Session) bool {
		// Touched in the last two minutes: likely still being written.
		return time.Since(s.ModTime) < 2*time.Minute
	},
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<title>agtrace</title>
<script>{{.ThemeJS}}</script>
<style>
{{.ThemeCSS}}
*{box-sizing:border-box}
body{margin:0;background:var(--plane);color:var(--ink);font:14px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif;
 padding:env(safe-area-inset-top,0px) 16px env(safe-area-inset-bottom,0px)}
.wrap{max-width:1140px;margin:0 auto;padding-block:28px 48px}
header{display:flex;flex-wrap:wrap;gap:10px 20px;align-items:flex-start;justify-content:space-between}
header .meta{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
/* What made the page, at the foot of it. The session page carries the same
   line and the two have to stay written the same way. */
.colophon{margin-top:16px;text-align:center;font-size:12px;color:var(--muted);
 font-variant-numeric:tabular-nums}
.colophon a{color:var(--ink-2)}
.colophon a:hover{color:var(--ink)}
h1{font-size:19px;margin:0 0 3px;letter-spacing:-.01em}
p.sub{color:var(--ink-2);font-size:13px;margin:0 0 18px}
p.sub code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace}
nav{display:flex;flex-wrap:wrap;gap:8px;margin:0 0 16px;align-items:center}
nav a{font-size:13px;padding:5px 12px;border-radius:999px;border:1px solid var(--border);
 background:var(--surface-1);text-decoration:none;color:var(--ink-2)}
nav a:hover{border-color:var(--ink-2);color:var(--ink)}
nav a.on{background:var(--accent);border-color:var(--accent);color:#fff;font-weight:550}
nav a.off{color:var(--muted);border-style:dashed}
nav a.off.on{background:none;color:var(--ink);border-color:var(--ink-2);font-weight:550}
.tools{display:flex;flex-wrap:wrap;gap:10px;align-items:center;margin:0 0 12px}
.tools input{font:inherit;font-size:13px;color:var(--ink);background:var(--surface-1);border:1px solid var(--border);
 border-radius:8px;padding:7px 11px;min-width:min(360px,100%);outline:none}
.tools input:focus{border-color:var(--accent)}
.tools .count{font-size:12px;color:var(--ink-2);font-variant-numeric:tabular-nums}
tr[hidden]{display:none}
.scroll{overflow-x:auto}
table{border-collapse:collapse;width:100%;background:var(--surface-1);border:1px solid var(--border);border-radius:10px}
th,td{text-align:right;padding:8px 12px;border-bottom:1px solid var(--border);white-space:nowrap;
 font-variant-numeric:tabular-nums}
th:first-child,td:first-child,th:last-child,td:last-child{text-align:left}
/* Text reads from the left wherever it sits; only the figures are right-aligned. */
th.t-nt,td.nt{text-align:left}
th{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.04em;font-weight:500}
tr:last-child td{border-bottom:0}
/* One column, two lines: what the CLI calls the session over what the model
   called it. They are different fields and the heading says so, but stacking
   them is what lets both be readable — side by side, neither column was wide
   enough for the values that actually occur. Each line clips on its own. */
td.nt{font-variant-numeric:normal;max-width:340px;line-height:1.35}
td.nt .nm,td.nt .ti{overflow:hidden;text-overflow:ellipsis}
/* A session with no title leaves the line blank rather than printing a dash,
   but the line still has to take up its height or the rows come out ragged. */
td.nt .ti{color:var(--ink-2);font-size:12px;min-height:1.35em}
td.cwd{font-variant-numeric:normal;color:var(--ink-2);font-family:ui-monospace,SFMono-Regular,Menlo,monospace;
 font-size:11.5px;max-width:280px;overflow:hidden;text-overflow:ellipsis}
td.nt a{color:inherit;text-decoration:none;border-bottom:1px solid var(--border);font-weight:550}
td.nt a:hover{border-bottom-color:currentColor}
td.id{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px;color:var(--ink-2)}
.dot{display:inline-block;width:6px;height:6px;border-radius:50%;background:var(--st-critical);margin-right:6px;
 vertical-align:1px}
.note{margin-top:16px;color:var(--ink-2);font-size:12px}
.note p{margin:4px 0}
.problem{background:var(--surface-1);border:1px solid var(--border);border-radius:10px;padding:16px;color:var(--ink-2)}
.problem strong{color:var(--ink);display:block;margin-bottom:4px}
.problem code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px}
</style></head><body><div class="wrap">

<header>
  <div>
    <h1>{{.Agent.Name}} sessions</h1>

  </div>
  <div class="meta">
    <button class="toggle" type="button" id="theme"></button>
  </div>
</header>
<script>window.agtraceTheme.wire(document.getElementById("theme"));</script>

<nav>
  {{- /* An agent with no reader is still a link: clicking it explains why,
         which a hover tooltip cannot do on a touch screen. The dashed, muted
         style is what says it will not list anything. */ -}}
  {{range .Agents}}
    <a href="/?agent={{.ID}}" title="{{.Status}}"
       class="{{if not .Implemented}}off {{end}}{{if eq .ID $.Agent.ID}}on{{end}}">{{.Name}}</a>
  {{end}}
</nav>

{{if .Problem}}
<div class="problem">
  <strong>{{.Agent.Name}}: nothing to list</strong>
  {{.Problem}}
  <p style="margin:10px 0 0">Transcripts would come from <code>{{.Agent.Transcripts}}</code>.</p>
</div>
{{else}}
<div class="tools">
  <input type="search" id="q" placeholder="Search" autocomplete="off" spellcheck="false" aria-label="Search sessions">
  <span class="count" id="count"></span>
</div>
<div class="scroll">
<table id="sessions"><thead><tr>
  <th class="t-nt" title="Two fields, one column: the name is what the CLI calls the session — its agent-name entry — and the title is what the model called it. Often different values, and either can be missing.">Name / title</th><th>Tokens</th><th>Cost</th><th title="The transcript's first timestamp.">Started</th><th>Last touched</th><th>Size</th>
  <th>Working directory</th>
</tr></thead><tbody>
{{range .Sessions}}<tr data-s="{{.Title}}|{{.Name}}|{{.CWD}}|{{.ID}}">
  <td class="nt">
    <div class="nm">{{if live .}}<span class="dot" title="touched in the last two minutes"></span>{{end}}<a href="/s/{{.ID}}?agent={{$.Agent.ID}}">{{with .Name}}{{.}}{{else}}{{short .ID}}{{end}}</a></div>
    <div class="ti">{{with .Title}}{{.}}{{end}}</div>
  </td>
  <td title="{{tokenDetail .}}">{{tokens .}}</td>
  <td>{{cost .CostUSD}}</td>
  <td>{{started .}}</td>
  <td>{{when .ModTime}}</td>
  <td>{{size .Size}}</td>
  <td class="cwd" title="{{with .CWD}}{{.}}{{else}}not recorded{{end}}">{{with .CWD}}{{.}}{{else}}(not recorded){{end}}</td>
</tr>{{end}}
</tbody></table>
</div>
<script>
(function () {
  "use strict";
  // Reveal the full text of a clipped cell on hover, with the browser's own
  // tooltip. Only cells that were actually cut off get one: titling them all
  // put a tooltip on rows that were already fully readable. The directory
  // column carries its title from the template, so it is left out here — a
  // tooltip of ours on top of that one showed two boxes at once.
  function markClipped() {
    document.querySelectorAll("#sessions td.nt .nm, #sessions td.nt .ti")
      .forEach(function (n) {
        if (n.scrollWidth > n.clientWidth + 1) n.title = n.textContent.trim();
        else n.removeAttribute("title");
      });
  }
  markClipped();
  addEventListener("resize", markClipped);

  var q = document.getElementById("q"), count = document.getElementById("count");
  var rows = Array.prototype.slice.call(document.querySelectorAll("#sessions tbody tr"));
  var total = rows.length;
  // Each row's fields, kept apart: title, name, directory, id.
  var hay = rows.map(function (r) {
    return (r.getAttribute("data-s") || "").toLowerCase().split("|");
  });

  // Fuzzy the way a file finder is: the typed characters must appear in order,
  // not necessarily adjacent, so "srvhub" finds "service-hub" and a few letters
  // of a non-ASCII title find it too. Words are matched independently, so "app sep"
  // narrows by directory and by month at once. A plain substring always counts.
  //
  // A word has to land inside one field. Matched against the fields joined
  // together, a five-letter query was a subsequence of almost every row — the
  // letters found one another across a title, a path and a uuid — which is not
  // what anyone means by a search.
  function subseq(needle, text) {
    var i = 0;
    for (var j = 0; j < text.length && i < needle.length; j++) {
      if (text[j] === needle[i]) i++;
    }
    return i === needle.length;
  }
  function wordHits(w, fields) {
    for (var f = 0; f < fields.length; f++) {
      if (fields[f].indexOf(w) >= 0 || subseq(w, fields[f])) return true;
    }
    return false;
  }
  function matches(query, fields) {
    var words = query.split(/\s+/).filter(Boolean);
    for (var k = 0; k < words.length; k++) {
      if (!wordHits(words[k], fields)) return false;
    }
    return true;
  }
  function apply() {
    var query = q.value.trim().toLowerCase();
    var shown = 0;
    rows.forEach(function (r, i) {
      var ok = !query || matches(query, hay[i]);
      r.hidden = !ok;
      if (ok) shown++;
      // Carry the query into the session link, so its back link returns to
      // this filtered list rather than to the full one.
      var a = r.querySelector("td.nt a");
      if (a) {
        var href = new URL(a.getAttribute("href"), location.href);
        if (query) href.searchParams.set("q", q.value.trim()); else href.searchParams.delete("q");
        a.setAttribute("href", href.pathname + href.search);
      }
    });
    count.textContent = query ? shown + " of " + total : total + " sessions";
    // Keep the query in the URL so the browser's back button, and a reload,
    // land on the same list. replaceState, so typing does not pile up history.
    var url = new URL(location.href);
    if (query) url.searchParams.set("q", q.value.trim()); else url.searchParams.delete("q");
    try { history.replaceState(null, "", url); } catch (e) {}
  }
  q.addEventListener("input", apply);
  document.addEventListener("keydown", function (e) {
    // "/" focuses the box from anywhere on the page, as in most list UIs.
    if (e.key === "/" && document.activeElement !== q) { e.preventDefault(); q.focus(); }
    if (e.key === "Escape" && document.activeElement === q) { q.value = ""; apply(); q.blur(); }
  });
  var initial = new URL(location.href).searchParams.get("q");
  if (initial) q.value = initial;
  apply();
})();
</script>
{{end}}

<div class="colophon">agtrace {{.Version}}  ·  <a href="https://github.com/Dongss/agent-trace" target="_blank" rel="noopener noreferrer">GitHub</a></div>
</div></body></html>
`))
