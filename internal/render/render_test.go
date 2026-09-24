package render

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Dongss/agent-trace/internal/event"
	"github.com/Dongss/agent-trace/internal/timeline"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func sampleRun() *event.Run {
	return &event.Run{
		Source:    event.SourceClaudeCode,
		SessionID: "abcdef12-3456-7890-abcd-ef1234567890",
		Path:      "~/.claude/projects/-~-workspace-app/s.jsonl",
		CWD:       "~/workspace/app",
		GitBranch: "main",
		Versions:  []string{"2.1.275"},
		Start:     at("2026-09-18T10:00:00Z"),
		End:       at("2026-09-18T10:30:00Z"),
		Steps: []event.Step{
			{Seq: 1, Kind: event.KindPrompt, At: at("2026-09-18T10:00:00Z"), HasTime: true, Text: "do it"},
			{Seq: 2, Kind: event.KindAssistant, At: at("2026-09-18T10:00:10Z"), HasTime: true, Model: "claude-opus-5",
				Usage: &event.Usage{Input: 3, CacheRead: 20000, CacheWrite: 500, Output: 120, Thinking: 40}},
			{Seq: 3, Kind: event.KindTool, At: at("2026-09-18T10:00:20Z"), HasTime: true,
				Tool: &event.Tool{ID: "t1", Name: "Bash", Brief: "go test ./...", Outcome: event.OutcomeOK,
					Started: at("2026-09-18T10:00:20Z"), Ended: at("2026-09-18T10:00:25Z"),
					HasDuration: true, Duration: 5 * time.Second, ResultBytes: 2048}},
			{Seq: 4, Kind: event.KindTool, At: at("2026-09-18T10:20:00Z"), HasTime: true,
				Tool: &event.Tool{ID: "t2", Name: "Read", Outcome: event.OutcomeUnpaired, Started: at("2026-09-18T10:20:00Z")}},
			{Seq: 6, Kind: event.KindAssistant, At: at("2026-09-18T10:30:00Z"), HasTime: true, Model: "claude-opus-5",
				Usage: &event.Usage{Input: 1, CacheRead: 45000, CacheWrite: 100, Output: 60}},
		},
		Compacts: []event.Compact{{Seq: 5, Trigger: "auto", Pre: 900000, Post: 40000, Dropped: 860000, Duration: 3 * time.Minute}},
		Stated: &event.Stated{
			CostUSD: 1.25, TotalDur: 30 * time.Minute, APIDur: 2 * time.Minute, ToolDur: 5 * time.Second,
			ByModel: map[string]event.Usage{"claude-opus-5[1m]": {Input: 10, CacheRead: 70000, Output: 200}},
		},
		Skipped: map[string]int{"attachment": 12},
	}
}

func TestPageIsSelfContained(t *testing.T) {
	page, err := Page(sampleRun(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)

	if !strings.HasPrefix(html, "<!doctype html>") {
		t.Error("not a complete document")
	}
	if strings.Contains(html, "__AGTRACE_DATA__") {
		t.Error("the data placeholder survived into the output")
	}

	// The page must not reach the network: a transcript's contents cannot be
	// handed to a third party by the act of looking at them. A link the reader
	// can choose to follow is a different thing from a resource the page loads,
	// so the repository link is allowed and anything that fetches is not.
	for _, bad := range []string{
		`src="http`, `src='http`, "@import", "fetch(", "XMLHttpRequest", "WebSocket",
		"cdnjs", "jsdelivr", "unpkg", "fonts.googleapis",
	} {
		if strings.Contains(html, bad) {
			t.Errorf("page references the network via %q", bad)
		}
	}
	allowed := map[string]bool{
		// A namespace identifier, not something the browser fetches.
		"http://www.w3.org/2000/svg": true,
		RepoURL:                      true,
	}
	urls := regexp.MustCompile(`https?://[^\s"'<>)]+`).FindAllString(html, -1)
	for _, u := range urls {
		if !allowed[u] {
			t.Errorf("unexpected URL in page: %s", u)
		}
	}
	// The repository link opens away from the page and cannot reach back into
	// it through window.opener.
	if strings.Contains(html, `"_blank"`) && !strings.Contains(html, "noopener") {
		t.Error("a link opens a new tab without noopener")
	}
}

func TestPageDataIsValidJSON(t *testing.T) {
	page, err := Page(sampleRun(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	const open = `<script type="application/json" id="agtrace-data">`
	i := strings.Index(html, open)
	if i < 0 {
		t.Fatal("no data script element")
	}
	rest := html[i+len(open):]
	j := strings.Index(rest, "</script>")
	if j < 0 {
		t.Fatal("data script never closes")
	}

	var v map[string]any
	if err := json.Unmarshal([]byte(rest[:j]), &v); err != nil {
		t.Fatalf("embedded data is not valid JSON: %v", err)
	}
	meta, _ := v["meta"].(map[string]any)
	if meta["sessionId"] != "abcdef12-3456-7890-abcd-ef1234567890" {
		t.Errorf("meta.sessionId = %v", meta["sessionId"])
	}
	if _, ok := v["bins"]; !ok {
		t.Error("no bins in the payload")
	}
}

// Content reaches the page only through JSON, which escapes the characters that
// could close the script element early.
func TestScriptInjectionIsEscaped(t *testing.T) {
	run := sampleRun()
	run.Steps[0].Text = `</script><script>alert(1)</script>`
	page, err := Page(run, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(page), "<script>alert(1)") {
		t.Error("transcript text broke out of the data element")
	}
}

func TestEmptyRunRenders(t *testing.T) {
	page, err := Page(&event.Run{Source: event.SourceClaudeCode, Skipped: map[string]int{}}, Options{})
	if err != nil {
		t.Fatalf("a run with nothing in it should still render: %v", err)
	}
	if len(page) == 0 {
		t.Error("empty page")
	}
}

// A level is represented by one real response, a flow by the sum of its slice.
// Averaging a composition would invent a request nobody made.
func TestBinsSumEveryResponseInTheSlice(t *testing.T) {
	// Two responses a second apart, plus a third half an hour later so the
	// axis is long enough for the first two to share a slice.
	run := &event.Run{Steps: []event.Step{
		{Seq: 1, Kind: event.KindAssistant, At: at("2026-09-18T10:00:00Z"), HasTime: true, Model: "small",
			Usage: &event.Usage{Input: 1, CacheRead: 100, CacheWrite: 1, Output: 10}},
		{Seq: 2, Kind: event.KindAssistant, At: at("2026-09-18T10:00:01Z"), HasTime: true, Model: "big",
			Usage: &event.Usage{Input: 2, CacheRead: 900, CacheWrite: 50, Output: 20}},
		{Seq: 3, Kind: event.KindAssistant, At: at("2026-09-18T10:30:00Z"), HasTime: true, Model: "later",
			Usage: &event.Usage{Input: 1, CacheRead: 5, CacheWrite: 0, Output: 1}},
	}}
	var ts []time.Time
	for i := range run.Steps {
		ts = append(ts, run.Steps[i].At)
	}
	clock := timeline.NewClock(ts, time.Hour) // one working stretch, no compression
	got, sc := buildBins(run, clock)

	var shared *bin
	for i := range got {
		if got[i].N == 2 {
			shared = &got[i]
		}
	}
	if shared == nil {
		t.Fatalf("no slice holds both of the responses one second apart: %+v", got)
	}
	// Every field is the column's total. Charting one response per slice and
	// calling it the slice's spend is the mistake this guards.
	if shared.Read != 1000 || shared.Write != 51 || shared.In != 3 || shared.Out != 30 {
		t.Errorf("sums came out as %+v; want read 1000, write 51, in 3, out 30", *shared)
	}
	// The label is when the slice began, not when its biggest response landed.
	if shared.At == "" {
		t.Error("the slice carries no timestamp")
	}
	// Every token in the run, which is where the cumulative curve ends.
	if want := 1 + 100 + 1 + 10 + 2 + 900 + 50 + 20 + 1 + 5 + 0 + 1; sc.MaxTotal != want {
		t.Errorf("MaxTotal = %d, want %d", sc.MaxTotal, want)
	}
}

// The cumulative chart's curve ends at the figure the stat tile shows, or one
// of the two is lying about the same session. They are computed from different
// sides — the stat from the run's totals, the curve from the binned columns —
// so nothing but a test keeps them equal.
func TestCumulativeTotalMatchesTheTotalTokensStat(t *testing.T) {
	run := &event.Run{Steps: []event.Step{
		{Seq: 1, Kind: event.KindAssistant, At: at("2026-09-18T10:00:00Z"), HasTime: true, Model: "m",
			Usage: &event.Usage{Input: 7, CacheRead: 400, CacheWrite: 13, Output: 90}},
		{Seq: 2, Kind: event.KindAssistant, At: at("2026-09-18T10:05:00Z"), HasTime: true, Model: "m",
			Usage: &event.Usage{Input: 3, CacheRead: 1200, CacheWrite: 0, Output: 40}},
		{Seq: 3, Kind: event.KindAssistant, At: at("2026-09-18T11:40:00Z"), HasTime: true, Model: "m",
			Usage: &event.Usage{Input: 11, CacheRead: 60, CacheWrite: 5, Output: 2}},
	}}
	page, err := Page(run, Options{IdleCutoff: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	const open = `<script type="application/json" id="agtrace-data">`
	i := strings.Index(html, open)
	rest := html[i+len(open):]
	var v view
	if err := json.Unmarshal([]byte(rest[:strings.Index(rest, "</script>")]), &v); err != nil {
		t.Fatal(err)
	}
	tot := timeline.Compute(run).All
	want := tot.Input + tot.CacheRead + tot.CacheWrite + tot.Output
	if v.Scales.MaxTotal != want {
		t.Errorf("maxTotal = %d, want %d", v.Scales.MaxTotal, want)
	}
	// And the columns add up to it, which is what the curve actually walks.
	sum := 0
	for _, b := range v.Bins {
		sum += b.In + b.Read + b.Write + b.Out
	}
	if sum != want {
		t.Errorf("the bins add up to %d, want %d", sum, want)
	}
}

// Failures must draw last, or a dense run buries the handful of calls somebody
// opened the timeline to find.
func TestFailuresDrawLast(t *testing.T) {
	run := &event.Run{Steps: []event.Step{
		{Seq: 1, Kind: event.KindTool, At: at("2026-09-18T10:00:00Z"), HasTime: true,
			Tool: &event.Tool{ID: "e", Name: "Bash", Outcome: event.OutcomeError}},
		{Seq: 2, Kind: event.KindTool, At: at("2026-09-18T10:00:01Z"), HasTime: true,
			Tool: &event.Tool{ID: "o", Name: "Bash", Outcome: event.OutcomeOK}},
		{Seq: 3, Kind: event.KindTool, At: at("2026-09-18T10:00:02Z"), HasTime: true,
			Tool: &event.Tool{ID: "d", Name: "Bash", Outcome: event.OutcomeDenied}},
	}}
	clock := timeline.NewClock([]time.Time{run.Steps[0].At, run.Steps[2].At}, time.Minute)
	got := buildTools(run, clock)
	if len(got) != 3 {
		t.Fatalf("got %d marks", len(got))
	}
	if got[0].Outcome != "ok" || got[len(got)-1].Outcome != "error" {
		var order []string
		for _, m := range got {
			order = append(order, m.Outcome)
		}
		t.Errorf("draw order %v; ok should be first and error last", order)
	}
}

// The tool lane names the longest call in each slice. A call that waited
// through an idle stretch draws narrower than it lasted, because the stretch is
// compressed, so width ranks it below a shorter call from a busy stretch; the
// mark has to carry the wall clock beside the width.
func TestToolMarkCarriesWallClockBesideWidth(t *testing.T) {
	run := &event.Run{Steps: []event.Step{
		{Seq: 1, Kind: event.KindTool, At: at("2026-09-18T10:00:00Z"), HasTime: true,
			Tool: &event.Tool{ID: "busy", Name: "Bash", Outcome: event.OutcomeOK,
				Started: at("2026-09-18T10:00:00Z"), Ended: at("2026-09-18T10:01:30Z"),
				HasDuration: true, Duration: 90 * time.Second}},
		{Seq: 2, Kind: event.KindAssistant, At: at("2026-09-18T10:00:45Z"), HasTime: true},
		{Seq: 3, Kind: event.KindTool, At: at("2026-09-18T10:01:30Z"), HasTime: true,
			Tool: &event.Tool{ID: "waited", Name: "Edit", Outcome: event.OutcomeOK,
				Started: at("2026-09-18T10:01:30Z"), Ended: at("2026-09-18T12:00:00Z"),
				HasDuration: true, Duration: 118*time.Minute + 30*time.Second}},
		{Seq: 4, Kind: event.KindAssistant, At: at("2026-09-18T12:00:00Z"), HasTime: true},
	}}
	var times []time.Time
	for _, st := range run.Steps {
		times = append(times, st.At)
	}
	clock := timeline.NewClock(times, time.Minute)
	marks := map[string]toolMark{}
	for _, m := range buildTools(run, clock) {
		marks[m.Name] = m
	}
	busy, waited := marks["Bash"], marks["Edit"]
	if waited.W >= busy.W {
		t.Fatalf("widths %v, %v: the idle stretch was not compressed, so this test proves nothing", busy.W, waited.W)
	}
	if busy.Secs != 90 || waited.Secs != 7110 {
		t.Errorf("secs = %v, %v; want 90, 7110", busy.Secs, waited.Secs)
	}
}

// A call placed by its step's timestamp, having none of its own, still says
// when that was: the tooltip dates a slice by the calls in it.
func TestToolMarkIsDatedWhereItIsPlaced(t *testing.T) {
	run := &event.Run{Steps: []event.Step{
		{Seq: 1, Kind: event.KindTool, At: at("2026-09-18T10:00:00Z"), HasTime: true,
			Tool: &event.Tool{ID: "a", Name: "Bash", Outcome: event.OutcomeUnpaired}},
	}}
	clock := timeline.NewClock([]time.Time{run.Steps[0].At}, time.Minute)
	got := buildTools(run, clock)
	if len(got) != 1 || got[0].At != stampSec(run.Steps[0].At) {
		t.Errorf("marks = %+v; want one dated %q", got, stampSec(run.Steps[0].At))
	}
}

// An untimed compaction still has to land on the axis: it is the most
// important event in the file.
func TestCompactWithoutTimestampIsPlaced(t *testing.T) {
	run := sampleRun()
	v := build(run, Options{})
	if len(v.Compacts) != 1 {
		t.Fatalf("got %d compaction marks, want 1", len(v.Compacts))
	}
	c := v.Compacts[0]
	if c.X <= 0 || c.X >= 1 {
		t.Errorf("compaction placed at x=%v, want it strictly inside the axis", c.X)
	}
	if c.Label != "900k → 40k" {
		t.Errorf("label %q", c.Label)
	}
}

func TestCompactAndDurFormatting(t *testing.T) {
	for in, want := range map[int]string{
		0: "0", 999: "999", 9999: "9999", 10000: "10k", 991610: "992k",
		1_014_866: "1M", 2_514_457: "2.5M", 1_326_072_767: "1.3B",
	} {
		if got := compact(in); got != want {
			t.Errorf("compact(%d) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[time.Duration]string{
		0:                       "0s",
		250 * time.Millisecond:  "250ms",
		2500 * time.Millisecond: "2.5s",
		90 * time.Second:        "1m30s",
		3 * time.Hour:           "3h0m",
		50 * time.Hour:          "2d",
	} {
		if got := dur(in); got != want {
			t.Errorf("dur(%v) = %q, want %q", in, got, want)
		}
	}
}

// A nil slice marshals to null, and the page calls forEach on these. A session
// with no compactions or no tool calls must still render.
func TestPayloadListsAreNeverNull(t *testing.T) {
	page, err := Page(&event.Run{Source: event.SourceClaudeCode}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	const open = `<script type="application/json" id="agtrace-data">`
	i := strings.Index(html, open)
	rest := html[i+len(open):]
	payload := rest[:strings.Index(rest, "</script>")]

	var v map[string]any
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"bins", "tools", "compacts", "gaps", "ticks", "outcomes", "toolUse", "skillUse", "stats"} {
		if v[key] == nil {
			t.Errorf("%s is null; the page would call forEach on it", key)
		}
	}
}

// A back link exists only where there is somewhere to go back to. A file
// written by `render` is opened on its own, and a link to a session index that
// is not there is worse than no link.
func TestBackLinkOnlyWhenGiven(t *testing.T) {
	v := build(sampleRun(), Options{})
	if v.Meta.Back != "" || v.Meta.BackLabel != "" {
		t.Errorf("standalone page got a back link: %q / %q", v.Meta.Back, v.Meta.BackLabel)
	}

	v = build(sampleRun(), Options{Back: "/?agent=claude-code"})
	if v.Meta.Back != "/?agent=claude-code" {
		t.Errorf("back href = %q", v.Meta.Back)
	}
	if v.Meta.BackLabel != "Sessions" {
		t.Errorf("back label = %q, want the default", v.Meta.BackLabel)
	}

	v = build(sampleRun(), Options{Back: "/x", BackLabel: "Claude Code sessions"})
	if v.Meta.BackLabel != "Claude Code sessions" {
		t.Errorf("back label = %q", v.Meta.BackLabel)
	}
}

func TestModelsUsedRanksByOutputAndSharesSumToOne(t *testing.T) {
	run := &event.Run{
		Steps: []event.Step{
			{Kind: event.KindAssistant, Model: "small", Usage: &event.Usage{Output: 100, CacheRead: 9000}},
			{Kind: event.KindAssistant, Model: "big", Usage: &event.Usage{Output: 900, CacheRead: 10}},
			{Kind: event.KindAssistant, Model: "big", Usage: &event.Usage{Output: 0, CacheRead: 10}},
			// A synthetic message is an entry, not a model.
			{Kind: event.KindAssistant, Model: "<synthetic>", Usage: &event.Usage{}},
		},
	}
	used, statedOnly := buildModelsUsed(run, timeline.Compute(run))

	if len(used) != 2 {
		t.Fatalf("got %d models: %+v", len(used), used)
	}
	// Busiest first, by output — the total is dominated by cache reads and
	// would rank "small" first.
	if used[0].Name != "big" || used[1].Name != "small" {
		t.Errorf("order = %s, %s", used[0].Name, used[1].Name)
	}
	if used[0].Responses != 2 {
		t.Errorf("big had %d responses, want 2", used[0].Responses)
	}
	var sum float64
	for _, u := range used {
		sum += u.Share
	}
	if sum < 0.999 || sum > 1.001 {
		t.Errorf("shares sum to %v, want 1", sum)
	}
	if len(statedOnly) != 0 {
		t.Errorf("statedOnly = %v for a run that states nothing", statedOnly)
	}
}

// The cost snapshot and the messages disagree about spelling as well as
// membership: a bracketed variant marker is the same model, a different model
// name is not.
func TestStatedOnlyIgnoresVariantSuffixes(t *testing.T) {
	run := &event.Run{
		Steps: []event.Step{
			{Kind: event.KindAssistant, Model: "claude-opus-5", Usage: &event.Usage{Output: 10}},
		},
		Stated: &event.Stated{ByModel: map[string]event.Usage{
			"claude-opus-5[1m]":         {Output: 12},
			"claude-haiku-4-5-20251001": {Output: 3},
			"zero-usage-model":          {},
		}},
	}
	_, statedOnly := buildModelsUsed(run, timeline.Compute(run))
	if len(statedOnly) != 1 || statedOnly[0] != "claude-haiku-4-5-20251001" {
		t.Errorf("statedOnly = %v, want only the model the transcript never shows", statedOnly)
	}
}

func TestBaseModel(t *testing.T) {
	for in, want := range map[string]string{
		"claude-opus-5":             "claude-opus-5",
		"claude-opus-5[1m]":         "claude-opus-5",
		"claude-haiku-4-5-20251001": "claude-haiku-4-5-20251001",
		"[weird]":                   "[weird]",
		"":                          "",
	} {
		if got := baseModel(in); got != want {
			t.Errorf("baseModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// A skill's name lives only in the arguments of the Skill call that invoked
// it, which the reader has already reduced to the call's brief. Counting the
// tool by name instead would report "Skill ×20" and never say which.
func TestSkillUseCountsByNameAndIgnoresOtherTools(t *testing.T) {
	tool := func(seq int, name, brief string) event.Step {
		return event.Step{
			Seq: seq, Kind: event.KindTool, At: at("2026-09-18T10:00:00Z"), HasTime: true,
			Tool: &event.Tool{ID: fmt.Sprint(seq), Name: name, Brief: brief, Outcome: event.OutcomeOK},
		}
	}
	run := &event.Run{Steps: []event.Step{
		tool(1, "Skill", "dataviz"),
		tool(2, "Bash", "go test ./..."),
		tool(3, "Skill", "artifact-design"),
		tool(4, "Skill", "dataviz"),
		tool(5, "Skill", ""),
	}}
	got := buildSkillUse(run)
	if len(got) != 3 {
		t.Fatalf("got %d skills, want 3: %+v", len(got), got)
	}
	if got[0].Name != "dataviz" || got[0].N != 2 {
		t.Errorf("busiest is %+v, want dataviz ×2", got[0])
	}
	if got[0].Share != 0.5 {
		t.Errorf("share = %v, want 0.5 of the four invocations", got[0].Share)
	}
	// A call whose arguments never named a skill is still an invocation, and
	// dropping it would make the shares add up to less than one.
	var unnamed bool
	for _, s := range got {
		if s.Name == "(unrecorded)" {
			unnamed = true
		}
	}
	if !unnamed {
		t.Error("the Skill call with no name in its arguments went missing")
	}
	if len(buildSkillUse(&event.Run{Steps: []event.Step{tool(1, "Bash", "ls")}})) != 0 {
		t.Error("a run that invoked no skill listed one")
	}
}

// "No compactions" is a fact about a run worth reading. A tile that appears
// only sometimes makes its absence look like a rendering gap rather than an
// answer, so the count is always shown and the note carries the meaning.
func TestCompactionsTileIsAlwaysShown(t *testing.T) {
	find := func(v view) (stat, bool) {
		for _, s := range v.Stats {
			if s.Label == "Compactions" {
				return s, true
			}
		}
		return stat{}, false
	}

	none := build(&event.Run{Steps: []event.Step{
		{Seq: 1, Kind: event.KindAssistant, At: at("2026-09-18T10:00:00Z"), HasTime: true,
			Usage: &event.Usage{Input: 1, Output: 1}},
	}}, Options{})
	s, ok := find(none)
	if !ok {
		t.Fatal("a run with no compaction has no Compactions tile")
	}
	if s.Value != "0" || !strings.Contains(s.Note, "never cut") {
		t.Errorf("with none: %q / %q", s.Value, s.Note)
	}

	some := build(&event.Run{
		Steps:    []event.Step{{Seq: 1, Kind: event.KindAssistant, At: at("2026-09-18T10:00:00Z"), HasTime: true, Usage: &event.Usage{Input: 1, Output: 1}}},
		Compacts: []event.Compact{{Seq: 2, Trigger: "auto", Pre: 180000, Post: 12000, Dropped: 168000}},
	}, Options{})
	s, _ = find(some)
	if s.Value != "1" || !strings.Contains(s.Note, "dropped") {
		t.Errorf("with one: %q / %q", s.Value, s.Note)
	}
}

// The table and the tooltip both name the moment a compaction happened, so
// the mark carries it. A boundary with no timestamp of its own is placed by
// file order, and the label follows it there.
func TestCompactMarkCarriesItsMoment(t *testing.T) {
	run := &event.Run{
		Steps: []event.Step{
			{Seq: 1, Kind: event.KindAssistant, At: at("2026-09-18T10:00:00Z"), HasTime: true, Usage: &event.Usage{Input: 1, Output: 1}},
			{Seq: 3, Kind: event.KindAssistant, At: at("2026-09-18T10:10:00Z"), HasTime: true, Usage: &event.Usage{Input: 1, Output: 1}},
		},
		Compacts: []event.Compact{
			{Seq: 2, Trigger: "auto", Pre: 180000, Post: 12000, Dropped: 168000, Duration: 4 * time.Second},
		},
	}
	v := build(run, Options{})
	if len(v.Compacts) != 1 {
		t.Fatalf("got %d marks, want 1", len(v.Compacts))
	}
	c := v.Compacts[0]
	if c.At == "" {
		t.Error("the mark carries no timestamp, so the table has nothing to print")
	}
	// What this one cut is pre-post; Dropped is the run's running total and is
	// a different number the row must not confuse it with.
	if c.Pre-c.Post != 168000 {
		t.Errorf("pre-post = %d, want 168000", c.Pre-c.Post)
	}
	if c.Trigger != "auto" || c.Dur == "" {
		t.Errorf("mark = %+v", c)
	}
}
