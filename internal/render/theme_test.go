package render

import (
	"strings"
	"testing"

	"github.com/Dongss/agent-trace/internal/event"
	"github.com/Dongss/agent-trace/internal/timeline"
)

// One definition of the palette, shared by every page. Two copies drift, and a
// theme that means something different on the list than on a session page is
// the thing this exists to prevent.
func TestThemeAssetsAreServable(t *testing.T) {
	css, js := ThemeCSS(), ThemeJS()
	if len(css) == 0 || len(js) == 0 {
		t.Fatal("a theme asset is empty")
	}

	// The dark set must be declared under both scopes, or the toggle wins in
	// only one direction.
	for _, want := range []string{
		":root {", "--surface-1:#fcfcfb", "--accent:", "button.toggle",
		"@media (prefers-color-scheme: dark)", `:root:where(:not([data-theme="light"]))`,
		`:root[data-theme="dark"]`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("theme.css is missing %q", want)
		}
	}
	for _, want := range []string{"agtrace-theme", "localStorage", "data-theme", "agtraceTheme", "wire:", "storage"} {
		if !strings.Contains(js, want) {
			t.Errorf("theme.js is missing %q", want)
		}
	}
	// Storage can throw; the page has to render anyway.
	if !strings.Contains(js, "catch") {
		t.Error("theme.js does not guard storage access")
	}
	// A script embedded in a <script> element must not be able to close it.
	if strings.Contains(js, "</script") || strings.Contains(css, "</style") {
		t.Error("a theme asset can break out of its element")
	}
}

func TestPageSubstitutesTheThemeAssets(t *testing.T) {
	page, err := Page(sampleRun(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, marker := range []string{"/*__AGTRACE_THEME_CSS__*/", "/*__AGTRACE_THEME_JS__*/"} {
		if strings.Contains(html, marker) {
			t.Errorf("%s survived into the output", marker)
		}
	}
	if !strings.Contains(html, "--surface-1:#fcfcfb") {
		t.Error("the palette did not reach the page")
	}
	if !strings.Contains(html, "agtraceTheme.wire") {
		t.Error("the page does not wire the toggle")
	}
	// The theme has to be applied before the body paints, or the reader sees a
	// flash of the wrong one.
	iJS := strings.Index(html, "agtrace-theme")
	iBody := strings.Index(html, "<body")
	if iJS < 0 || iBody < 0 || iJS > iBody {
		t.Errorf("the theme script is not in <head> (js at %d, body at %d)", iJS, iBody)
	}
}

// Range controls only where a server can answer for another window.
func TestRangeControlsOnlyWhenOffered(t *testing.T) {
	v := build(sampleRun(), Options{})
	if v.Meta.Interact || len(v.Ranges) != 0 {
		t.Errorf("a standalone page offers ranges: interact=%v, %d ranges", v.Meta.Interact, len(v.Ranges))
	}

	v = build(sampleRun(), Options{Ranges: []Range{
		{Label: "Full run", Href: "/s/x", Active: true},
		{Label: "Fri 4 Sep", Href: "/s/x?from=a&to=b", Note: "2h active"},
	}})
	if !v.Meta.Interact || len(v.Ranges) != 2 {
		t.Errorf("ranges were not passed through: interact=%v, %d", v.Meta.Interact, len(v.Ranges))
	}
	if v.Meta.FromValue == "" || v.Meta.ToValue == "" {
		t.Error("the free-form inputs have no values to start from")
	}
}

// A windowed run says what it is a window into, and drops what it cannot claim.
func TestWindowedPageSaysSoAndDropsStated(t *testing.T) {
	run := sampleRun()
	run.Windowed = true
	run.WindowFrom = at("2026-09-18T10:00:00Z")
	run.WindowTo = at("2026-09-18T10:10:00Z")
	run.FullStart = at("2026-09-18T09:00:00Z")
	run.FullEnd = at("2026-09-18T12:00:00Z")
	run.Stated = nil

	v := build(run, Options{Ranges: []Range{{Label: "Full run", Href: "/x"}}})
	if !v.Meta.Windowed {
		t.Error("meta does not say the view is windowed")
	}
	for _, s := range v.Stats {
		if s.Label == "Cost" {
			t.Error("a cost tile survived into a windowed view")
		}
	}
}

// Times become text in one place, in the reader's zone.
func TestStampsAreLocalAndZoneIsNamed(t *testing.T) {
	utc := at("2026-09-17T03:48:00Z")
	if got := stamp(utc); got != utc.Local().Format("2006-01-02 15:04") {
		t.Errorf("stamp = %q, want the local rendering", got)
	}
	if got := inputStamp(utc); got != utc.Local().Format("2006-01-02T15:04") {
		t.Errorf("inputStamp = %q", got)
	}
	if stamp(event.Run{}.Start) != "" || stampSec(event.Run{}.Start) != "" || inputStamp(event.Run{}.Start) != "" {
		t.Error("a zero time produced text")
	}
}

// A long session crosses many releases; the header shows the range and keeps
// the list in a tooltip.
func TestVersionRange(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"2.1.274"}, "2.1.274"},
		{[]string{"2.1.270", "2.1.274"}, "2.1.270, 2.1.274"},
		{[]string{"2.1.231", "2.1.236", "2.1.274"}, "2.1.231 → 2.1.274"},
	} {
		if got := versionRange(tc.in); got != tc.want {
			t.Errorf("versionRange(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}

	run := sampleRun()
	run.Versions = []string{"2.1.231", "2.1.236", "2.1.258", "2.1.274"}
	v := build(run, Options{})
	if v.Meta.Versions != "2.1.231 → 2.1.274" {
		t.Errorf("pill shows %q", v.Meta.Versions)
	}
	if !strings.Contains(v.Meta.VersionsAll, "2.1.258") {
		t.Errorf("the full list is not available for the tooltip: %q", v.Meta.VersionsAll)
	}
	if v.Meta.VersionCount != 4 {
		t.Errorf("version count = %d", v.Meta.VersionCount)
	}
}

// The heading names both fields. Showing one and hiding the other made readers
// ask which they were looking at, and "agent" is not available as a label: it
// already names the CLI in the session list.
func TestHeadingLabelsBothNameAndTitle(t *testing.T) {
	page, err := Page(sampleRun(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if strings.Contains(html, `"agent: "`) {
		t.Error(`the page still labels the session's name "agent:"`)
	}
	if !strings.Contains(html, `headingPart("name"`) || !strings.Contains(html, `headingPart("title"`) {
		t.Error("the heading does not label both fields")
	}

	// Both reach the page; which of them the heading can draw depends on the run.
	run := sampleRun()
	run.AgentName = "webapp"
	run.Title = "another ünïcödé title"
	v := build(run, Options{})
	if v.Meta.AgentName != "webapp" || v.Meta.Title != "another ünïcödé title" {
		t.Errorf("meta carries name=%q title=%q", v.Meta.AgentName, v.Meta.Title)
	}
}

// "Which tools did this run use" is a question about the whole set: every tool
// is listed, not a top few, because the single call to something unexpected is
// usually the one worth seeing.
func TestToolUseListsEveryToolBusiestFirst(t *testing.T) {
	run := &event.Run{Steps: []event.Step{}}
	add := func(name string, n int) {
		for i := 0; i < n; i++ {
			run.Steps = append(run.Steps, event.Step{
				Kind: event.KindTool,
				Tool: &event.Tool{ID: name + string(rune('a'+i)), Name: name, Outcome: event.OutcomeOK},
			})
		}
	}
	add("Bash", 7)
	add("Read", 2)
	add("mcp__claude-in-chrome__javascript_tool", 1)

	got := buildToolUse(timeline.Compute(run))
	if len(got) != 3 {
		t.Fatalf("got %d tools, want all 3: %+v", len(got), got)
	}
	if got[0].Name != "Bash" || got[0].N != 7 {
		t.Errorf("busiest = %s ×%d", got[0].Name, got[0].N)
	}
	if got[2].N != 1 {
		t.Errorf("the one-call tool was dropped or reordered: %+v", got)
	}
	// Share is of all calls, so the strip and the tool-call tile agree.
	var sum float64
	for _, u := range got {
		sum += u.Share
	}
	if sum < 0.999 || sum > 1.001 {
		t.Errorf("shares sum to %v", sum)
	}
	if got[2].Display != "claude-in-chrome/javascript_tool" {
		t.Errorf("MCP name not shortened: %q", got[2].Display)
	}
}

func TestShortToolName(t *testing.T) {
	for in, want := range map[string]string{
		"Bash":                                   "Bash",
		"mcp__claude-in-chrome__javascript_tool": "claude-in-chrome/javascript_tool",
		"mcp__srv__do":                           "srv/do",
		// A shape the convention does not cover keeps whatever it has.
		"mcp__weird": "weird",
		"":           "",
	} {
		if got := shortToolName(in); got != want {
			t.Errorf("shortToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The strip sits above the first chart, and a run with no tool calls gets none.
func TestToolUseAbsentWhenNoCalls(t *testing.T) {
	v := build(&event.Run{Source: event.SourceClaudeCode}, Options{})
	if len(v.ToolUse) != 0 {
		t.Errorf("a run with no tool calls listed %d tools", len(v.ToolUse))
	}
}

// The page reads as three subjects, each with what it did and when it did it.
// The sections are built in call order by a script, so the order of these
// literals in the source is the order on the page; rewording a title has to
// come here too, and there is no stabler anchor for a section the script
// builds.
func TestSectionsAreInOrder(t *testing.T) {
	page, err := Page(sampleRun(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	want := []string{
		`card("Tokens spent"`,
		`card("Tokens over time"`,
		`usedList("Tools used"`,
		`"Tool calls over time"`,
		`usedList("Skills used"`,
		`card("Skill invocations over time"`,
	}
	at := -1
	for _, w := range want {
		i := strings.Index(html, w)
		if i < 0 {
			t.Fatalf("%s is not on the page at all", w)
		}
		if i < at {
			t.Errorf("%s comes before the section that should precede it", w)
		}
		at = i
	}
}

// The build that produced a page is on it, and so is a way back to the project.
func TestVersionAndRepoReachThePage(t *testing.T) {
	v := build(sampleRun(), Options{Version: "v0.1.0"})
	if v.Meta.Version != "v0.1.0" {
		t.Errorf("version = %q", v.Meta.Version)
	}
	if v.Meta.Repo != RepoURL {
		t.Errorf("repo = %q", v.Meta.Repo)
	}
	// A build that was not told its version says nothing rather than guessing.
	if got := build(sampleRun(), Options{}).Meta.Version; got != "" {
		t.Errorf("version = %q with none supplied", got)
	}
}
