package claudecode

import (
	"strings"
	"testing"
	"time"

	"github.com/Dongss/agent-trace/internal/event"
)

// The transcripts in this file are constructed, not recorded. A real Claude
// Code transcript is the user's own prompts, file contents and shell history,
// so unlike a CLI's own fixtures, one cannot be committed to a public
// repository at all.
//
// What is committed instead is the *shapes*, reproduced from a survey of real
// sessions written by Claude Code 2.1.231 through 2.1.275 on 2026-09-18:
// per-block assistant entries repeating one usage object, a polymorphic
// toolUseResult, a compaction boundary with no timestamp, and the nine entry
// types that carry no timestamp at all. TestRealTranscripts at the end of this
// file runs the parser over whatever real sessions the machine actually has,
// and is skipped when there are none.
//
// If a future release changes a shape, the fix is to re-survey and update these
// literals — not to relax the parser until the old literals pass again.

func read(t *testing.T, lines ...string) *event.Run {
	t.Helper()
	run, err := Read(strings.NewReader(strings.Join(lines, "\n")+"\n"), "/tmp/t.jsonl", Options{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return run
}

func usageSum(run *event.Run) (in, read, write, out int) {
	for i := range run.Steps {
		if u := run.Steps[i].Usage; u != nil {
			in += u.Input
			read += u.CacheRead
			write += u.CacheWrite
			out += u.Output
		}
	}
	return
}

// One API response is written as one entry per content block, each repeating
// the same usage. In the surveyed session that was 4,558 entries for 2,670
// responses; counting per entry overstated the run by about 70%.
func TestUsageCountedOncePerResponse(t *testing.T) {
	const usage = `"usage":{"input_tokens":2,"cache_creation_input_tokens":300,"cache_read_input_tokens":1000,"output_tokens":50,"output_tokens_details":{"thinking_tokens":20}}`
	run := read(t,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-09-18T10:00:00.000Z","message":{"id":"msg_1","model":"m","role":"assistant","content":[{"type":"thinking","thinking":"hm"}],`+usage+`}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-09-18T10:00:01.000Z","message":{"id":"msg_1","model":"m","role":"assistant","content":[{"type":"text","text":"hello"}],`+usage+`}}`,
		`{"type":"assistant","uuid":"a3","timestamp":"2026-09-18T10:00:02.000Z","message":{"id":"msg_1","model":"m","role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls"}}],`+usage+`}}`,
	)

	in, cr, cw, out := usageSum(run)
	if in != 2 || cr != 1000 || cw != 300 || out != 50 {
		t.Fatalf("usage counted %d/%d/%d/%d, want one response's worth 2/1000/300/50", in, cr, cw, out)
	}
	if got := len(run.Steps); got != 3 {
		t.Fatalf("got %d steps, want one per content block", got)
	}
	var carried int
	for i := range run.Steps {
		if run.Steps[i].Usage != nil {
			carried++
		}
	}
	if carried != 1 {
		t.Fatalf("%d steps carry usage, want exactly 1", carried)
	}
}

// A second response with the same content must still be counted: dedup is on
// message.id, not on the content.
func TestSeparateResponsesBothCounted(t *testing.T) {
	line := func(uuid, id string) string {
		return `{"type":"assistant","uuid":"` + uuid + `","timestamp":"2026-09-18T10:00:00.000Z","message":{"id":"` + id + `","model":"m","role":"assistant","content":[{"type":"text","text":"x"}],"usage":{"output_tokens":10}}}`
	}
	run := read(t, line("a1", "msg_1"), line("a2", "msg_2"))
	if _, _, _, out := usageSum(run); out != 20 {
		t.Fatalf("output %d, want 20", out)
	}
}

func toolOf(t *testing.T, run *event.Run, id string) *event.Tool {
	t.Helper()
	for i := range run.Steps {
		if tl := run.Steps[i].Tool; tl != nil && tl.ID == id {
			return tl
		}
	}
	t.Fatalf("no tool step with id %q", id)
	return nil
}

func TestToolPairingAndOutcomes(t *testing.T) {
	call := func(uuid, id, name, args string, ts string) string {
		return `{"type":"assistant","uuid":"` + uuid + `","timestamp":"` + ts + `","message":{"id":"msg_` + id + `","model":"m","role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + args + `}]}}`
	}
	run := read(t,
		call("a1", "tu_ok", "Bash", `{"command":"echo hi"}`, "2026-09-18T10:00:00.000Z"),
		`{"type":"user","uuid":"u1","timestamp":"2026-09-18T10:00:02.500Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_ok","content":"hi"}]},"toolUseResult":{"stdout":"hi","stderr":"","interrupted":false,"isImage":false}}`,

		call("a2", "tu_err", "Read", `{"file_path":"/Users/someone/x.go"}`, "2026-09-18T10:01:00.000Z"),
		`{"type":"user","uuid":"u2","timestamp":"2026-09-18T10:01:01.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_err","content":"File does not exist","is_error":true}]},"toolUseResult":"File does not exist"}`,

		call("a3", "tu_den", "Bash", `{"command":"rm -rf /"}`, "2026-09-18T10:02:00.000Z"),
		`{"type":"user","uuid":"u3","timestamp":"2026-09-18T10:02:00.500Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_den","content":"denied"}]},"toolDenialKind":"user-rejected"}`,

		call("a4", "tu_int", "Bash", `{"command":"sleep 100"}`, "2026-09-18T10:03:00.000Z"),
		`{"type":"user","uuid":"u4","timestamp":"2026-09-18T10:03:10.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_int","content":""}]},"toolUseResult":{"interrupted":true,"stdout":"","stderr":""}}`,

		// An MCP tool: toolUseResult is an array, not an object.
		call("a5", "tu_mcp", "mcp__srv__do", `{"x":1}`, "2026-09-18T10:04:00.000Z"),
		`{"type":"user","uuid":"u5","timestamp":"2026-09-18T10:04:01.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_mcp","content":[{"type":"text","text":"ok"}]}]},"toolUseResult":[{"type":"text","text":"ok"}]}`,

		// No result at all: the run ended, or the call is still running.
		call("a6", "tu_open", "Bash", `{"command":"still going"}`, "2026-09-18T10:05:00.000Z"),
	)

	for _, tc := range []struct {
		id   string
		want event.Outcome
	}{
		{"tu_ok", event.OutcomeOK},
		{"tu_err", event.OutcomeError},
		{"tu_den", event.OutcomeDenied},
		{"tu_int", event.OutcomeInterrupted},
		{"tu_mcp", event.OutcomeOK},
		{"tu_open", event.OutcomeUnpaired},
	} {
		if got := toolOf(t, run, tc.id).Outcome; got != tc.want {
			t.Errorf("%s: outcome %q, want %q", tc.id, got, tc.want)
		}
	}

	if d := toolOf(t, run, "tu_ok"); !d.HasDuration || d.Duration != 2500*time.Millisecond {
		t.Errorf("tu_ok duration %v (has=%v), want 2.5s", d.Duration, d.HasDuration)
	}
	// An unpaired call has no duration. Zero would read as an instant call.
	if d := toolOf(t, run, "tu_open"); d.HasDuration {
		t.Errorf("tu_open reports a duration; an unpaired call has none")
	}
	if got := toolOf(t, run, "tu_mcp").MCPServer; got != "srv" {
		t.Errorf("MCP server %q, want srv", got)
	}
	if got := toolOf(t, run, "tu_ok").Brief; got != "echo hi" {
		t.Errorf("brief %q, want the command", got)
	}
	// A path in an argument is structure, but the account name in it is not.
	if got := toolOf(t, run, "tu_err").Brief; strings.Contains(got, "someone") {
		t.Errorf("brief %q still carries the account name", got)
	}
}

// Timestamps in a real transcript are not monotonic — 248 out-of-order pairs in
// the surveyed session. A result that predates its call yields no duration
// rather than a negative one.
func TestOutOfOrderTimestampsGiveNoDuration(t *testing.T) {
	run := read(t,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-09-18T10:00:05.000Z","message":{"id":"m1","model":"m","role":"assistant","content":[{"type":"tool_use","id":"tu","name":"Bash","input":{"command":"x"}}]}}`,
		`{"type":"user","uuid":"u1","timestamp":"2026-09-18T10:00:01.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu","content":"y"}]}}`,
	)
	if tl := toolOf(t, run, "tu"); tl.HasDuration {
		t.Fatalf("negative duration %v reported as usable", tl.Duration)
	}
}

// The compaction boundary is the most important entry in the file and carries
// no timestamp.
func TestCompactBoundary(t *testing.T) {
	run := read(t,
		`{"type":"user","uuid":"u1","timestamp":"2026-09-18T10:00:00.000Z","message":{"role":"user","content":"go"}}`,
		`{"type":"system","subtype":"compact_boundary","parentUuid":null,"logicalParentUuid":"x","content":"Conversation compacted","compactMetadata":{"trigger":"auto","preTokens":1014866,"postTokens":44793,"cumulativeDroppedTokens":970073,"durationMs":219788}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-09-18T10:10:00.000Z","message":{"role":"user","content":"on"}}`,
	)
	if len(run.Compacts) != 1 {
		t.Fatalf("got %d compactions, want 1", len(run.Compacts))
	}
	c := run.Compacts[0]
	if c.Pre != 1014866 || c.Post != 44793 || c.Dropped != 970073 || c.Trigger != "auto" {
		t.Fatalf("compaction parsed as %+v", c)
	}
	if c.HasTime {
		t.Fatalf("compaction claims a timestamp; the CLI writes none")
	}
	if c.Duration != 219788*time.Millisecond {
		t.Fatalf("duration %v", c.Duration)
	}
}

// The CLI may be appending to the transcript while it is read. A half-written
// final line is the CLI mid-write, not corruption.
func TestTruncatedFinalLine(t *testing.T) {
	body := `{"type":"user","uuid":"u1","timestamp":"2026-09-18T10:00:00.000Z","message":{"role":"user","content":"hi"}}` + "\n" +
		`{"type":"assistant","uuid":"a1","timestamp":"2026-09-18T10:00:01.000Z","message":{"id":"m1","mod`
	run, err := Read(strings.NewReader(body), "t.jsonl", Options{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !run.Truncated {
		t.Error("Truncated is false for a half-written final line")
	}
	if run.Malformed != 0 {
		t.Errorf("Malformed = %d; a mid-write tail is not a parse failure", run.Malformed)
	}
	if len(run.Steps) != 1 {
		t.Errorf("got %d steps, want the one complete entry", len(run.Steps))
	}
}

// A corrupt line in the middle of a file is a parse failure, and reading
// continues past it.
func TestMalformedLineMidFile(t *testing.T) {
	run := read(t,
		`{"type":"user","uuid":"u1","timestamp":"2026-09-18T10:00:00.000Z","message":{"role":"user","content":"hi"}}`,
		`{not json`,
		`{"type":"user","uuid":"u2","timestamp":"2026-09-18T10:00:02.000Z","message":{"role":"user","content":"again"}}`,
	)
	if run.Malformed != 1 {
		t.Errorf("Malformed = %d, want 1", run.Malformed)
	}
	if run.Truncated {
		t.Error("a mid-file failure must not be reported as a live tail")
	}
	if len(run.Steps) != 2 {
		t.Errorf("got %d steps, want the 2 that parsed", len(run.Steps))
	}
}

// Unmodelled entry types are counted, not treated as errors: the surveyed
// sessions carry fifteen top-level types and a release will add more.
func TestUnknownTypesAreCountedNotFatal(t *testing.T) {
	run := read(t,
		`{"type":"atis-latch","atis":{}}`,
		`{"type":"file-history-snapshot","snapshot":{}}`,
		`{"type":"atis-latch","atis":{}}`,
		`{"type":"system","subtype":"away_summary","timestamp":"2026-09-18T10:00:00.000Z"}`,
	)
	if run.Malformed != 0 {
		t.Errorf("Malformed = %d for well-formed unmodelled entries", run.Malformed)
	}
	if run.Skipped["atis-latch"] != 2 || run.Skipped["file-history-snapshot"] != 1 {
		t.Errorf("skipped counts = %v", run.Skipped)
	}
	if run.Skipped["system/away_summary"] != 1 {
		t.Errorf("system subtypes should be counted by subtype: %v", run.Skipped)
	}
	if len(run.Steps) != 0 {
		t.Errorf("got %d steps from entries with nothing to show", len(run.Steps))
	}
}

// cost-state is a snapshot rewritten as the session runs, so the last one wins.
func TestCostStateLastSnapshotWins(t *testing.T) {
	run := read(t,
		`{"type":"cost-state","sessionId":"s","totalCostUSD":1.5,"totalToolDuration":1000,"modelUsage":{"m":{"inputTokens":1,"outputTokens":2,"cacheReadInputTokens":3,"cacheCreationInputTokens":4,"thinkingTokens":5,"costUSD":1.5}}}`,
		`{"type":"cost-state","sessionId":"s","totalCostUSD":9.25,"totalToolDuration":5000,"modelUsage":{"m":{"inputTokens":10,"outputTokens":20,"cacheReadInputTokens":30,"cacheCreationInputTokens":40,"thinkingTokens":50,"costUSD":9.25}}}`,
	)
	if run.Stated == nil {
		t.Fatal("no stated totals")
	}
	if run.Stated.CostUSD != 9.25 {
		t.Errorf("cost %v, want the last snapshot 9.25", run.Stated.CostUSD)
	}
	if got := run.Stated.ByModel["m"].Output; got != 20 {
		t.Errorf("stated output %d, want 20", got)
	}
	if run.Stated.ToolDur != 5*time.Second {
		t.Errorf("tool duration %v", run.Stated.ToolDur)
	}
}

// A human turn is a string content. Tool plumbing and meta notices are also
// user-role entries and are not prompts.
func TestOnlyRealTurnsCountAsPrompts(t *testing.T) {
	run := read(t,
		`{"type":"user","uuid":"u1","timestamp":"2026-09-18T10:00:00.000Z","message":{"role":"user","content":"do the thing"}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-09-18T10:00:01.000Z","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"system reminder"}]}}`,
		`{"type":"user","uuid":"u3","timestamp":"2026-09-18T10:00:02.000Z","sourceToolUseID":"tu_1","message":{"role":"user","content":[{"type":"text","text":"tool companion"}]}}`,
	)
	var prompts int
	for i := range run.Steps {
		if run.Steps[i].Kind == event.KindPrompt {
			prompts++
		}
	}
	if prompts != 1 {
		t.Fatalf("counted %d prompts, want 1", prompts)
	}
}

// Paths are shortened for display wherever they appear, including inside a
// tool's arguments: a column should not spend a third of its width on a prefix
// every row shares.
func TestHomeDirectoryIsCollapsedInArguments(t *testing.T) {
	run := read(t,
		`{"type":"user","uuid":"u1","timestamp":"2026-09-18T10:00:00.000Z","cwd":"/Users/someone/work","message":{"role":"user","content":"go"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-09-18T10:00:01.000Z","message":{"id":"m1","model":"m","role":"assistant","content":[{"type":"tool_use","id":"tu","name":"Read","input":{"file_path":"/Users/someone/work/x.go"}}]}}`,
	)
	if got := toolOf(t, run, "tu").Brief; got != "~/work/x.go" {
		t.Errorf("brief = %q, want the home directory collapsed", got)
	}
	if run.CWD != "~/work" {
		t.Errorf("cwd = %q", run.CWD)
	}
}

// A tool's arguments reach the page as the transcript recorded them.
func TestArgumentsAreKeptAsRecorded(t *testing.T) {
	run := read(t,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-09-18T10:00:01.000Z","message":{"id":"m1","model":"m","role":"assistant","content":[{"type":"tool_use","id":"tu","name":"Bash","input":{"command":"go test ./... -run TestFoo"}}]}}`,
	)
	if got := toolOf(t, run, "tu").Brief; got != "go test ./... -run TestFoo" {
		t.Errorf("brief = %q", got)
	}
}

func TestSessionMetadata(t *testing.T) {
	run := read(t,
		`{"type":"user","uuid":"u1","timestamp":"2026-09-18T10:00:00.000Z","sessionId":"sess-1","cwd":"/w/a","version":"2.1.270","gitBranch":"main","message":{"role":"user","content":"hi"}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-09-18T11:00:00.000Z","sessionId":"sess-1","cwd":"/w/b","version":"2.1.231","gitBranch":"main","message":{"role":"user","content":"hi"}}`,
		`{"type":"user","uuid":"u3","timestamp":"2026-09-18T12:00:00.000Z","sessionId":"sess-1","cwd":"/w/b","version":"2.1.270","gitBranch":"main","message":{"role":"user","content":"hi"}}`,
	)
	if run.SessionID != "sess-1" {
		t.Errorf("session %q", run.SessionID)
	}
	if run.CWD != "/w/b" {
		t.Errorf("cwd %q, want the most frequent one", run.CWD)
	}
	// A long session spans releases; all of them are reported, in order.
	if strings.Join(run.Versions, ",") != "2.1.231,2.1.270" {
		t.Errorf("versions %v", run.Versions)
	}
	if !run.Start.Equal(time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)) || !run.End.Equal(time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("span %s → %s", run.Start, run.End)
	}
}

// A result whose call is not in the file happens after a compaction or a
// resume. It is counted, not dropped silently.
func TestResultWithoutCallIsCounted(t *testing.T) {
	run := read(t,
		`{"type":"user","uuid":"u1","timestamp":"2026-09-18T10:00:00.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"gone","content":"x"}]}}`,
	)
	if run.Skipped["tool_result(no call)"] != 1 {
		t.Errorf("skipped = %v", run.Skipped)
	}
}
