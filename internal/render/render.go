// Package render turns a Run into a timeline page.
//
// The page is one self-contained HTML file: no CDN, no fonts to fetch, no
// network at all. That is a privacy property as much as a packaging one — a
// transcript's contents must not be handed to a third party by the act of
// looking at them — and it is what makes a page saved out of the browser
// safe to attach to an issue.
//
// Two encoding decisions are load-bearing and both come from the same rule:
// context size is a level and output is a flow, so they never share an axis.
// The page draws them as two charts over one shared clock instead. Anything
// binned for the level chart carries one real response's composition (the
// largest in that bin), never an average of several, so the stack a reader
// hovers is a measurement that actually happened.
package render

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Dongss/agent-trace/internal/event"
	"github.com/Dongss/agent-trace/internal/timeline"
)

//go:embed page.html theme.css theme.js
var assets embed.FS

// ThemeCSS and ThemeJS are the palette and the theme toggle, exported so every
// page agtrace serves shares one definition of them rather than each carrying
// its own drifting copy.
func ThemeCSS() string { return mustAsset("theme.css") }
func ThemeJS() string  { return mustAsset("theme.js") }

func mustAsset(name string) string {
	b, err := assets.ReadFile(name)
	if err != nil {
		// Embedded at build time; a failure here is a broken binary, not input.
		panic("render: missing embedded asset " + name + ": " + err.Error())
	}
	return string(b)
}

// RepoURL is where the project lives. It is a link for a reader to follow, not
// a resource the page loads: nothing here is fetched over the network.
const RepoURL = "https://github.com/Dongss/agent-trace"

// bins is how many columns the two token charts are aggregated into. It is a
// display budget, not a property of the data: a session with thousands of
// responses still has to fit on a screen.
const bins = 160

// Options controls the page.
type Options struct {
	// IdleCutoff is how long a stretch of nothing has to be before the axis
	// compresses it.
	IdleCutoff time.Duration

	// Back is where the page's back link points, and is set only when there is
	// somewhere to go back to. A page saved to a file is opened on its own, so
	// it gets no link rather than one that 404s.
	Back string
	// BackLabel names the destination; "Sessions" when empty.
	BackLabel string

	// Version is the agtrace build that produced the page, shown on it.
	Version string

	// Ranges are the time windows the page offers, each with the URL that
	// selects it. Empty means no range controls at all, which is right for a
	// page saved to a file: there is no server to ask for another window.
	Ranges []Range
	// Quick are presets relative to now — today, yesterday, the last seven
	// days — offered beside Ranges, which are the run's own days. A preset
	// that would select nothing is not in here at all.
	Quick []Range
}

// Range is one selectable time window.
type Range struct {
	Label  string
	Href   string
	Active bool
	Note   string // steps, responses and tool calls in it
}

// Page renders run to a complete HTML document.
func Page(run *event.Run, opt Options) ([]byte, error) {
	tmpl, err := assets.ReadFile("page.html")
	if err != nil {
		return nil, err
	}
	v := build(run, opt)
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	page := strings.Replace(string(tmpl), "/*__AGTRACE_THEME_CSS__*/", ThemeCSS(), 1)
	page = strings.Replace(page, "/*__AGTRACE_THEME_JS__*/", ThemeJS(), 1)
	for _, marker := range []string{"/*__AGTRACE_THEME_CSS__*/", "/*__AGTRACE_THEME_JS__*/"} {
		if strings.Contains(page, marker) {
			return nil, fmt.Errorf("render: %s was not substituted", marker)
		}
	}
	tmpl = []byte(page)
	// json.Marshal escapes <, > and & by default, so the payload cannot break
	// out of the script element it is embedded in.
	out := strings.Replace(string(tmpl), "/*__AGTRACE_DATA__*/null", string(data), 1)
	if !strings.Contains(out, string(data)) {
		return nil, fmt.Errorf("render: data placeholder not found in page template")
	}
	return []byte(out), nil
}

type view struct {
	Meta       meta          `json:"meta"`
	Stats      []stat        `json:"stats"`
	Bins       []bin         `json:"bins"`
	Tools      []toolMark    `json:"tools"`
	Compacts   []compactMark `json:"compacts"`
	Gaps       []gapMark     `json:"gaps"`
	Ticks      []tick        `json:"ticks"`
	Ranges     []Range       `json:"ranges"`
	Quick      []Range       `json:"quick"`
	ModelsUsed []modelUse    `json:"modelsUsed"`
	// StatedOnly are models the cost snapshot names that no response in the
	// transcript does — background work, typically. They are listed apart
	// because nothing here can say what they did.
	StatedOnly []string   `json:"statedOnly"`
	Outcomes   []countRow `json:"outcomes"`
	ToolUse    []toolUse  `json:"toolUse"`
	SkillUse   []toolUse  `json:"skillUse"`
	Reading    reading    `json:"reading"`
	Scales     scales     `json:"scales"`
}

type meta struct {
	SessionID string    `json:"sessionId"`
	Short     string    `json:"short"`
	Title     string    `json:"title"`
	AgentName string    `json:"agentName"`
	Surfaces  []surface `json:"surfaces"` // what ran the session, and under which releases
	CWD       string    `json:"cwd"`
	Branch    string    `json:"branch"`
	Path      string    `json:"path"`
	Start     string    `json:"start"`
	End       string    `json:"end"`
	SpanText  string    `json:"spanText"`
	Active    string    `json:"activeText"`
	Generated string    `json:"generated"`
	Live      bool      `json:"live"`
	Back      string    `json:"back"`
	BackLabel string    `json:"backLabel"`
	Version   string    `json:"version"`
	Repo      string    `json:"repo"`

	// Windowed and the spans below describe a view of part of a run.
	Windowed  bool   `json:"windowed"`
	Interact  bool   `json:"interact"`
	FromValue string `json:"fromValue"` // for the datetime-local inputs
	ToValue   string `json:"toValue"`
}

// modelUse is one model the session actually ran on, for the strip at the top
// of the page.
//
// Share is of output tokens rather than of the total, because the total is
// dominated by cache reads and would report every model in a long session as
// "about the same". Output is the part that tracks what a model did.
type modelUse struct {
	Name      string  `json:"name"`
	Out       int     `json:"out"`
	Ctx       int     `json:"ctx"`
	Thinking  int     `json:"think"`
	Responses int     `json:"responses"`
	Share     float64 `json:"share"`
}

type stat struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Note  string `json:"note"`
}

// bin is one column of the token charts. Every field is summed over the
// column, which is what a flow supports: both charts draw the same four
// numbers, one as a running total and one slice at a time. Sampling a single
// response instead — the context chart used to, taking the largest — is a
// different kind of quantity, and it went out with that chart on 2026-09-20.
type bin struct {
	X        int    `json:"x"` // column index
	In       int    `json:"in"`
	Read     int    `json:"read"`
	Write    int    `json:"write"`
	Out      int    `json:"out"`
	Thinking int    `json:"think"`
	N        int    `json:"n"`
	At       string `json:"at"`
}

type toolMark struct {
	X float64 `json:"x"`
	// W is the call's width on the gap-compressing clock, which is how wide it
	// draws, not how long it took: a call spanning a compressed idle stretch
	// draws narrower than one that did not. Ranking calls by duration uses
	// Secs, the wall clock.
	W       float64 `json:"w"`
	Secs    float64 `json:"secs"`
	Name    string  `json:"name"`
	Brief   string  `json:"brief"`
	Outcome string  `json:"outcome"`
	Detail  string  `json:"detail"`
	Dur     string  `json:"dur"`
	HasDur  bool    `json:"hasDur"`
	Bytes   int     `json:"bytes"`
	At      string  `json:"at"`
}

type compactMark struct {
	X       float64 `json:"x"`
	At      string  `json:"at"`
	Trigger string  `json:"trigger"`
	Pre     int     `json:"pre"`
	Post    int     `json:"post"`
	// Dropped is cumulative over the run, not this event's own figure. What
	// this compaction cut is Pre-Post; the table says so and this one is only
	// worth showing as a running total.
	Dropped int    `json:"dropped"`
	Dur     string `json:"dur"`
	Label   string `json:"label"`
}

type gapMark struct {
	X     float64 `json:"x"`
	Label string  `json:"label"`
}

type tick struct {
	X     float64 `json:"x"`
	Label string  `json:"label"`
}

// toolUse is one tool, or one skill, and how often the run reached for it.
type toolUse struct {
	Name    string  `json:"name"`
	Display string  `json:"display"` // an MCP tool shortened to server/tool
	N       int     `json:"n"`
	Share   float64 `json:"share"`
}

type countRow struct {
	Label string `json:"label"`
	N     int    `json:"n"`
}

// reading is the footer: where the transcript came from, and the two ways
// reading it can have gone wrong. It carried explanatory notes and a count of
// unmodelled entry types until 2026-09-20; the page stopped rendering them,
// so they stopped being built.
type reading struct {
	Steps     int  `json:"steps"`
	Malformed int  `json:"malformed"`
	Truncated bool `json:"truncated"`
}

type scales struct {
	// MaxTotal is every token in the window added together, which is where the
	// cumulative chart's curve ends. It is a volume, not a price. The per-slice
	// chart's maximum is not here: it depends on which series are showing, so
	// the page computes it from the columns it is about to draw.
	MaxTotal int `json:"maxTotal"`
	Bins     int `json:"bins"`
}

func build(run *event.Run, opt Options) view {
	t := timeline.Compute(run)

	times := make([]time.Time, 0, len(run.Steps))
	for i := range run.Steps {
		if run.Steps[i].HasTime {
			times = append(times, run.Steps[i].At)
		}
	}
	clock := timeline.NewClock(times, opt.IdleCutoff)

	v := view{
		Meta: meta{
			SessionID: run.SessionID,
			Short:     short(run.SessionID),
			Title:     run.Title,
			AgentName: run.AgentName,
			Surfaces:  surfaces(run.Surfaces),
			CWD:       run.CWD,
			Branch:    run.GitBranch,
			Path:      run.Path,
			Generated: stamp(time.Now()),
			Live:      run.Truncated,
			Back:      opt.Back,
			BackLabel: backLabel(opt),
		},
	}
	if !run.Start.IsZero() {
		v.Meta.Start = stamp(run.Start)
		v.Meta.End = stamp(run.End)
		v.Meta.SpanText = dur(clock.Span())
		v.Meta.Active = dur(clock.Active())
		v.Meta.FromValue = inputStamp(run.Start)
		v.Meta.ToValue = inputStamp(run.End)
	}
	v.Meta.Version = opt.Version
	v.Meta.Repo = RepoURL
	v.Meta.Interact = len(opt.Ranges) > 0
	v.Ranges = opt.Ranges
	v.Quick = opt.Quick
	if run.Windowed {
		v.Meta.Windowed = true
		if !run.WindowFrom.IsZero() {
			v.Meta.FromValue = inputStamp(run.WindowFrom)
		}
		if !run.WindowTo.IsZero() {
			v.Meta.ToValue = inputStamp(run.WindowTo)
		}
	}

	v.SkillUse = buildSkillUse(run)
	v.Stats = buildStats(run, t, clock, v.SkillUse)
	v.Bins, v.Scales = buildBins(run, clock)
	v.Tools = buildTools(run, clock)
	v.Compacts = buildCompacts(run, clock)

	for _, g := range clock.Gaps() {
		v.Gaps = append(v.Gaps, gapMark{X: g.At, Label: "idle " + dur(g.Dur)})
	}
	for _, tk := range clock.Ticks(7) {
		v.Ticks = append(v.Ticks, tick{X: tk.At, Label: tk.T.Local().Format("Jan 2 15:04")})
	}

	v.ModelsUsed, v.StatedOnly = buildModelsUsed(run, t)
	v.ToolUse = buildToolUse(t)
	for _, o := range []event.Outcome{event.OutcomeOK, event.OutcomeError, event.OutcomeDenied, event.OutcomeInterrupted, event.OutcomeUnpaired} {
		if n := t.Outcomes[o]; n > 0 {
			v.Outcomes = append(v.Outcomes, countRow{Label: string(o), N: n})
		}
	}
	v.Reading = buildReading(run)

	// A nil slice marshals to JSON null, and the page would then call forEach
	// on it. Every list goes out as a list, empty or not, so a run with no
	// compactions or no tool calls renders instead of throwing.
	if v.Bins == nil {
		v.Bins = []bin{}
	}
	if v.Tools == nil {
		v.Tools = []toolMark{}
	}
	if v.Compacts == nil {
		v.Compacts = []compactMark{}
	}
	if v.Gaps == nil {
		v.Gaps = []gapMark{}
	}
	if v.Ticks == nil {
		v.Ticks = []tick{}
	}
	if v.Ranges == nil {
		v.Ranges = []Range{}
	}
	if v.Quick == nil {
		v.Quick = []Range{}
	}
	if v.ModelsUsed == nil {
		v.ModelsUsed = []modelUse{}
	}
	if v.StatedOnly == nil {
		v.StatedOnly = []string{}
	}
	if v.Outcomes == nil {
		v.Outcomes = []countRow{}
	}
	if v.ToolUse == nil {
		v.ToolUse = []toolUse{}
	}
	if v.SkillUse == nil {
		v.SkillUse = []toolUse{}
	}
	return v
}

func buildStats(run *event.Run, t timeline.Totals, clock *timeline.Clock, skills []toolUse) []stat {
	out := []stat{}

	// Every tile is drawn, whatever the run had. A tile that appears only
	// sometimes makes its absence read as a rendering gap rather than as an
	// answer, and two sessions cannot be compared when one of them is short a
	// figure. Missing and zero stay apart: a value nobody recorded is an em
	// dash, a value that is nothing is 0.

	// Active time leads: how long the run actually worked is the first thing
	// to know about it, and the cost beside it reads as the price of that. A
	// run with no timestamped entry has no span to measure, which is not the
	// same as one that worked for no time.
	active := stat{"Active time", "—",
		fmt.Sprintf("%d responses, %d prompts", t.Responses, t.Prompts)}
	if clock.Span() > 0 {
		active.Value = dur(clock.Active())
	}
	out = append(out, active)

	// The note carries the scope of the figure, which is the thing about it a
	// reader can act on: it covers the session and every model in it,
	// including ones no message here mentions. A windowed view therefore has
	// no figure to show, and says that rather than looking like a transcript
	// that stated none.
	cost := stat{"Cost", "—", "the transcript states none"}
	switch {
	case run.Stated != nil:
		cost.Value = fmt.Sprintf("$%.2f", run.Stated.CostUSD)
		switch {
		case run.Stated.UnknownModelCost:
			cost.Note = "the CLI calls this figure incomplete"
		case run.Truncated:
			// The snapshot is written when a sitting ends, so in a session
			// still being written the figure stops at the last exit.
			cost.Note = "as of the last exit, not this sitting"
		default:
			cost.Note = "the whole session"
		}
	case run.Windowed:
		cost.Note = "stated for the session, not for a window"
	}
	out = append(out, cost)
	out = append(out,
		stat{"Total tokens", compact(t.All.Input + t.All.CacheRead + t.All.CacheWrite + t.All.Output),
			"input, cache and output"},
		stat{"Input tokens", compact(t.All.Input), "sent fresh, not from cache"},
		stat{"Output tokens", compact(t.All.Output), fmt.Sprintf("%s of it thinking", compact(t.All.Thinking))},
		stat{"Cache read", compact(t.All.CacheRead), "context re-read"},
		stat{"Cache write", compact(t.All.CacheWrite), "paid once, read back later"},
	)
	// Compactions closes the run of context figures above rather than opening
	// the run of activity below: what it reports is the context being cut, not
	// something the agent did.
	compactions := stat{"Compactions", fmt.Sprintf("%d", len(run.Compacts)), "the context was never cut"}
	if len(run.Compacts) > 0 {
		compactions.Note = compact(t.DroppedByCompaction) + " tokens dropped"
	}
	out = append(out, compactions, stat{"Tool calls", compact(t.ToolCalls), toolNote(t)})
	calls := 0
	for _, sk := range skills {
		calls += sk.N
	}
	used := stat{"Skills", compact(calls), "no skills were invoked"}
	if n := len(skills); n > 0 {
		used.Note = fmt.Sprintf("across %d %s", n, plural(n, "skill"))
	}
	out = append(out, used)
	return out
}

// buildBins aggregates responses into columns, summing each column's usage.
// At is the first response in the column, which is when the slice began.
func buildBins(run *event.Run, clock *timeline.Clock) ([]bin, scales) {
	type acc struct {
		bin
		set bool
	}
	cols := make([]acc, bins)

	for i := range run.Steps {
		st := &run.Steps[i]
		if st.Usage == nil || !st.HasTime {
			continue
		}
		x := int(clock.X(st.At) * float64(bins-1))
		if x < 0 {
			x = 0
		}
		if x >= bins {
			x = bins - 1
		}
		c := &cols[x]
		c.X = x
		c.N++
		c.Out += st.Usage.Output
		c.Thinking += st.Usage.Thinking
		c.In += st.Usage.Input
		c.Read += st.Usage.CacheRead
		c.Write += st.Usage.CacheWrite
		if !c.set {
			c.set = true
			c.At = stampSec(st.At)
		}
	}

	var out []bin
	sc := scales{Bins: bins}
	for i := range cols {
		if cols[i].N == 0 {
			continue
		}
		out = append(out, cols[i].bin)
		sc.MaxTotal += cols[i].In + cols[i].Read + cols[i].Write + cols[i].Out
	}
	return out, sc
}

// buildTools orders calls so failures draw last. In a dense session successful
// calls would otherwise bury the handful that went wrong, which are the reason
// somebody opened the timeline.
func buildTools(run *event.Run, clock *timeline.Clock) []toolMark {
	var out []toolMark
	for i := range run.Steps {
		st := &run.Steps[i]
		if st.Kind != event.KindTool || st.Tool == nil {
			continue
		}
		tl := st.Tool
		// At is the instant X was placed from, so the time a tooltip prints is
		// the one the mark sits at.
		var x, w float64
		var placed time.Time
		switch {
		case !tl.Started.IsZero():
			placed = tl.Started
			x = clock.X(tl.Started)
			if tl.HasDuration && !tl.Ended.IsZero() {
				w = clock.X(tl.Ended) - x
			}
		case st.HasTime:
			placed = st.At
			x = clock.X(st.At)
		}
		m := toolMark{
			X: x, W: w,
			Name: tl.Name, Brief: tl.Brief,
			Outcome: string(tl.Outcome), Detail: tl.Detail,
			HasDur: tl.HasDuration, Bytes: tl.ResultBytes,
			At: stampSec(placed),
		}
		if tl.HasDuration {
			m.Dur = dur(tl.Duration)
			m.Secs = tl.Duration.Seconds()
		}
		out = append(out, m)
	}
	rank := map[string]int{
		string(event.OutcomeOK): 0, string(event.OutcomeUnpaired): 1,
		string(event.OutcomeInterrupted): 2, string(event.OutcomeDenied): 3,
		string(event.OutcomeError): 4,
	}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Outcome] < rank[out[j].Outcome] })
	return out
}

func buildCompacts(run *event.Run, clock *timeline.Clock) []compactMark {
	var out []compactMark
	for _, c := range run.Compacts {
		at, ok := c.At, c.HasTime
		if !ok {
			// Every surveyed boundary carried a timestamp, but a transcript
			// that omits one still has to plot: placing it by file order
			// beats leaving the run's largest event off the chart.
			at, ok = timeline.PlaceBySeq(run, c.Seq)
		}
		if !ok {
			continue
		}
		out = append(out, compactMark{
			X: clock.X(at), At: stampSec(at), Trigger: c.Trigger,
			Pre: c.Pre, Post: c.Post, Dropped: c.Dropped,
			Dur:   dur(c.Duration),
			Label: compact(c.Pre) + " → " + compact(c.Post),
		})
	}
	return out
}

func backLabel(opt Options) string {
	if opt.Back == "" {
		return ""
	}
	if opt.BackLabel != "" {
		return opt.BackLabel
	}
	return "Sessions"
}

// buildModelsUsed lists the models the run's own responses name, busiest
// first, and separately the ones only the cost snapshot mentions.
//
// The two lists disagree about spelling as well as membership: the messages of
// the surveyed session say "claude-opus-5" where its cost snapshot says
// "claude-opus-5[1m]". A bracketed suffix marks a variant of the same model, so
// it is stripped before comparing — narrowly, and only for deciding whether a
// stated model is one the transcript already showed. Nothing is merged: each
// list keeps the names its own source used.
func buildModelsUsed(run *event.Run, t timeline.Totals) ([]modelUse, []string) {
	var used []modelUse
	for name, u := range t.ByModel {
		if u == (event.Usage{}) {
			continue // a synthetic message is not a model
		}
		used = append(used, modelUse{
			Name:      name,
			Out:       u.Output,
			Ctx:       u.ContextTokens(),
			Thinking:  u.Thinking,
			Responses: t.ResponsesByModel[name],
		})
	}
	sort.Slice(used, func(i, j int) bool {
		if used[i].Out != used[j].Out {
			return used[i].Out > used[j].Out
		}
		return used[i].Name < used[j].Name
	})
	if total := t.All.Output; total > 0 {
		for i := range used {
			used[i].Share = float64(used[i].Out) / float64(total)
		}
	}

	var statedOnly []string
	if run.Stated != nil {
		seen := map[string]bool{}
		for name := range t.ByModel {
			seen[baseModel(name)] = true
		}
		for name, u := range run.Stated.ByModel {
			if u == (event.Usage{}) || seen[baseModel(name)] {
				continue
			}
			statedOnly = append(statedOnly, name)
		}
		sort.Strings(statedOnly)
	}
	return used, statedOnly
}

// baseModel drops a trailing bracketed variant marker: claude-opus-5[1m] and
// claude-opus-5 are the same model asked for a different context window.
func baseModel(name string) string {
	if i := strings.IndexByte(name, '['); i > 0 {
		return name[:i]
	}
	return name
}

func buildReading(run *event.Run) reading {
	return reading{Steps: len(run.Steps), Malformed: run.Malformed, Truncated: run.Truncated}
}

// toolNote: how long the calls took, and whether any of them failed — the one
// thing the tool lane colours but never counts. Both fit on one line only
// because they are this short; a longer pair wraps the tile and leaves it
// taller than the ones beside it. Only errors count as failures. A denial or
// an interruption is somebody's decision, and an unpaired call is the
// transcript missing a result rather than a call going wrong.
func toolNote(t timeline.Totals) string {
	if n := t.Outcomes[event.OutcomeError]; n > 0 {
		return fmt.Sprintf("%s, %d failed", dur(t.ToolTime), n)
	}
	return dur(t.ToolTime) + ", none failed"
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// buildSkillUse lists the skills the run invoked, busiest first. A skill is a
// Skill tool call, and the skill's name is the call's own brief — the reader
// already pulls it out of the arguments, which is the only place it appears.
// A run that invoked none gets an empty list and the page leaves the section
// out: most runs invoke none, and an empty card would be on every one of them.
func buildSkillUse(run *event.Run) []toolUse {
	by := map[string]int{}
	total := 0
	for i := range run.Steps {
		st := &run.Steps[i]
		if st.Kind != event.KindTool || st.Tool == nil || st.Tool.Name != "Skill" {
			continue
		}
		name := st.Tool.Brief
		if name == "" {
			name = "(unrecorded)"
		}
		by[name]++
		total++
	}
	out := make([]toolUse, 0, len(by))
	for name, n := range by {
		out = append(out, toolUse{Name: name, Display: name, N: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Name < out[j].Name
	})
	if total > 0 {
		for i := range out {
			out[i].Share = float64(out[i].N) / float64(total)
		}
	}
	return out
}

// buildToolUse lists every tool the run called, busiest first. Every one, not a
// top few: "which tools did this run use" is a question about the whole set,
// and the single call to something unexpected is usually the interesting one.
func buildToolUse(t timeline.Totals) []toolUse {
	out := make([]toolUse, 0, len(t.ToolsByName))
	for name, n := range t.ToolsByName {
		out = append(out, toolUse{Name: name, Display: shortToolName(name), N: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Name < out[j].Name
	})
	if t.ToolCalls > 0 {
		for i := range out {
			out[i].Share = float64(out[i].N) / float64(t.ToolCalls)
		}
	}
	return out
}

// shortToolName turns mcp__claude-in-chrome__javascript_tool into
// claude-in-chrome/javascript_tool. The prefix and the doubled underscores are
// wire format, and a strip of these names is unreadable with them left in.
func shortToolName(name string) string {
	rest, ok := strings.CutPrefix(name, "mcp__")
	if !ok {
		return name
	}
	if server, tool, found := strings.Cut(rest, "__"); found {
		return server + "/" + tool
	}
	return rest
}

// stamp, stampSec and inputStamp are the only places a time becomes text.
//
// They convert to the reader's zone first. The transcript stores UTC while file
// mtimes are local, and an earlier version showed one of each on the same
// screen: the session list said one was last touched at 13:58 and its own page said
// the run ended at 04:58. A timeline that shows somebody else's clock is a bug,
// so every label goes local and the zone is named on the page.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}

func stampSec(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("Jan 2 15:04:05")
}

// inputStamp is the format <input type="datetime-local"> requires.
func inputStamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02T15:04")
}

// A surface is one entrypoint on the meta line: what drove the session, and
// the releases seen under it. The name is printed as the transcript wrote it
// — nothing is mapped to a friendlier word, because the set is open and a
// guess at a name this build has never seen reads as fact.
type surface struct {
	Name     string `json:"name"`
	Versions string `json:"versions"` // compact: one, two, or first → last
	All      string `json:"all"`      // every release under this name
	Count    int    `json:"count"`
}

func surfaces(in []event.Surface) []surface {
	out := make([]surface, 0, len(in))
	for _, s := range in {
		out = append(out, surface{
			Name:     s.Name,
			Versions: versionRange(s.Versions),
			All:      strings.Join(s.Versions, ", "),
			Count:    len(s.Versions),
		})
	}
	return out
}

// versionRange compacts the CLI versions a run spans. A long session crosses a
// lot of releases — eleven in the surveyed one — and the full list pushed
// everything else off the header line. The first and last say what matters;
// the rest live in the tooltip.
func versionRange(v []string) string {
	switch len(v) {
	case 0:
		return ""
	case 1:
		return v[0]
	case 2:
		return v[0] + ", " + v[1]
	}
	return v[0] + " → " + v[len(v)-1]
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// compact formats a token count the way a reader scans it: 1.3M, not
// 1,326,072,767. Exact figures live in the tables.
func compact(n int) string {
	f := float64(n)
	switch {
	case n >= 1_000_000_000:
		return trim(f/1e9) + "B"
	case n >= 1_000_000:
		return trim(f/1e6) + "M"
	case n >= 10_000:
		return trim(f/1e3) + "k"
	}
	return fmt.Sprintf("%d", n)
}

func trim(f float64) string {
	if f >= 100 || f == math.Trunc(f) {
		return fmt.Sprintf("%.0f", f)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", f), ".0")
}

func dur(d time.Duration) string {
	switch {
	case d <= 0:
		return "0s"
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	case d >= time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}
