package claudecode

import (
	"os"
	"strings"
	"testing"

	"github.com/Dongss/agent-trace/internal/event"
)

const (
	aiTitleEntry     = `{"type":"ai-title","sessionId":"s","aiTitle":"a title with ünïcödé"}`
	customTitleEntry = `{"type":"custom-title","sessionId":"s","customTitle":"demo-app"}`
	agentNameEntry   = `{"type":"agent-name","sessionId":"s","agentName":"demo-app"}`
	oneResponse      = `{"type":"assistant","uuid":"a1","timestamp":"2026-09-18T10:00:00.000Z","message":{"id":"m1","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":3,"cache_creation_input_tokens":500,"cache_read_input_tokens":20000,"output_tokens":120,"output_tokens_details":{"thinking_tokens":40}}}}`
)

// Title is the model-written one and nothing else. It used to fall back to
// custom-title, which made a listing's Title and Name columns repeat each other
// on every session without a title of its own.
func TestTitleIsTheModelWrittenOneOnly(t *testing.T) {
	for _, tc := range []struct {
		name        string
		lines       []string
		title       string
		sessionName string
	}{
		{"both", []string{customTitleEntry, aiTitleEntry}, "a title with ünïcödé", "demo-app"},
		{"both, other order", []string{aiTitleEntry, customTitleEntry}, "a title with ünïcödé", "demo-app"},
		{"only custom", []string{customTitleEntry}, "", "demo-app"},
		{"only ai", []string{aiTitleEntry}, "a title with ünïcödé", ""},
		{"neither", []string{oneResponse}, "", ""},
		// agent-name wins over custom-title, which is only its fallback.
		{"agent name", []string{customTitleEntry, agentNameEntry}, "", "demo-app"},
	} {
		run := read(t, tc.lines...)
		if run.Title != tc.title {
			t.Errorf("%s: title %q, want %q", tc.name, run.Title, tc.title)
		}
		if run.AgentName != tc.sessionName {
			t.Errorf("%s: name %q, want %q", tc.name, run.AgentName, tc.sessionName)
		}
	}
}

// A listing shows them in separate columns, so neither may stand in for the
// other.
func TestScanSplitsTitleFromName(t *testing.T) {
	path := t.TempDir() + "/s.jsonl"
	if err := writeFile(path, customTitleEntry+"\n"); err != nil {
		t.Fatal(err)
	}
	got, err := Scan(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "" {
		t.Errorf("scan invented a title from custom-title: %q", got.Title)
	}
	if got.AgentName != "demo-app" {
		t.Errorf("scan name = %q", got.AgentName)
	}
	s := Session{ID: "abcdef1234", Totals: got}
	if s.Title() != "" || s.Name() != "demo-app" {
		t.Errorf("Session.Title()=%q Name()=%q", s.Title(), s.Name())
	}
}

// Scan is what a listing uses, and must agree with the full parser rather than
// being a second, drifting implementation.
func TestScanAgreesWithTheFullParser(t *testing.T) {
	lines := []string{
		aiTitleEntry, customTitleEntry, agentNameEntry, oneResponse,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-09-18T10:00:01.000Z","message":{"id":"m1","model":"claude-opus-5","role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":3,"cache_creation_input_tokens":500,"cache_read_input_tokens":20000,"output_tokens":120,"output_tokens_details":{"thinking_tokens":40}}}}`,
		`{"type":"cost-state","sessionId":"s","totalCostUSD":2.5,"modelUsage":{}}`,
	}
	body := strings.Join(lines, "\n") + "\n"

	path := t.TempDir() + "/s.jsonl"
	if err := writeFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := Scan(path)
	if err != nil {
		t.Fatal(err)
	}

	if got.Title != "a title with ünïcödé" {
		t.Errorf("title %q", got.Title)
	}
	if got.AgentName != "demo-app" {
		t.Errorf("agent name %q", got.AgentName)
	}
	// One response, counted once despite arriving as two entries.
	if got.Responses != 1 {
		t.Errorf("responses %d, want 1", got.Responses)
	}
	if got.ToolCalls != 1 {
		t.Errorf("tool calls %d, want 1", got.ToolCalls)
	}
	want := event.Usage{Input: 3, CacheRead: 20000, CacheWrite: 500, Output: 120, Thinking: 40}
	if got.Tokens != want {
		t.Errorf("tokens %+v, want %+v", got.Tokens, want)
	}
	// The four fields added: volume, and the only figure Total claims to be.
	if got.Total() != 3+20000+500+120 {
		t.Errorf("Total() = %d", got.Total())
	}
	if got.Cost == nil || *got.Cost != 2.5 {
		t.Errorf("cost %v", got.Cost)
	}
}

// A listing shows the working directory in its own column, so Title does not
// fall back to it: a derived label must not look like one the CLI wrote.
func TestSessionTitleDoesNotInventOne(t *testing.T) {
	s := Session{ID: "abcdef1234", CWD: "~/workspace/app"}
	if got := s.Title(); got != "" {
		t.Errorf("Title without a scan = %q, want empty", got)
	}
	s.Totals = &Totals{}
	if got := s.Title(); got != "" {
		t.Errorf("Title with an untitled scan = %q, want empty", got)
	}
	s.Totals = &Totals{Title: "real title"}
	if got := s.Title(); got != "real title" {
		t.Errorf("Title = %q", got)
	}
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

// Versions sort by value, not as strings: a plain sort puts 2.1.98 after
// 2.1.231 and the "first → last" range a page shows would then be backwards.
func TestVersionsSortNumerically(t *testing.T) {
	line := func(uuid, ver string) string {
		return `{"type":"user","uuid":"` + uuid + `","timestamp":"2026-09-18T10:00:00.000Z","version":"` + ver +
			`","message":{"role":"user","content":"hi"}}`
	}
	run := read(t, line("u1", "2.1.231"), line("u2", "2.1.98"), line("u3", "2.2.0"), line("u4", "2.1.231"))
	if got := strings.Join(run.Versions, ","); got != "2.1.98,2.1.231,2.2.0" {
		t.Errorf("versions = %q", got)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"2.1.98", "2.1.231", -1},
		{"2.1.231", "2.1.98", 1},
		{"2.1.274", "2.1.274", 0},
		{"2.1", "2.1.1", -1},
		{"3.0.0", "2.9.9", 1},
		// A component that is not a number still orders deterministically.
		{"2.1.0-rc1", "2.1.0", 1},
		{"2.1.0", "2.1.0-rc1", -1},
	} {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// The title and the session's name are different fields and are allowed to
// disagree — which is exactly what confused a reader of the page.
func TestTitleAndAgentNameAreSeparate(t *testing.T) {
	run := read(t,
		`{"type":"ai-title","sessionId":"s","aiTitle":"another ünïcödé title"}`,
		`{"type":"agent-name","sessionId":"s","agentName":"webapp"}`,
		`{"type":"custom-title","sessionId":"s","customTitle":"webapp"}`,
	)
	if run.Title != "another ünïcödé title" {
		t.Errorf("title = %q, want the model-written one", run.Title)
	}
	if run.AgentName != "webapp" {
		t.Errorf("agent name = %q", run.AgentName)
	}
}
