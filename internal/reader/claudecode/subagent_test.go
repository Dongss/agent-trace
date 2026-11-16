package claudecode

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dongss/agent-trace/internal/event"
	"github.com/Dongss/agent-trace/internal/timeline"
)

// The shapes below are 2.1.283's, from sessions that spawned subagents through
// the Agent tool: the session's own transcript links to a subagent only through
// the spawning call's result, and the subagent's conversation is a file of its
// own under <session>/subagents/, named for its agentId, with a sidecar naming
// its type and task. The values — ids, times, text — are made up.
var (
	parentLines = []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-01-07T10:04:00.000Z","sessionId":"s1","origin":{"kind":"human"},"message":{"role":"user","content":"survey the repo"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-01-07T10:04:10.000Z","sessionId":"s1","message":{"id":"m1","model":"claude-opus-5-5","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tu_agent","name":"Agent","input":{"description":"Find the parser","prompt":"Find where the parser reads usage.","subagent_type":"Explore"}}],"usage":{"input_tokens":2,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":50}}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-01-07T10:04:11.000Z","sessionId":"s1","toolUseResult":{"agentId":"a0000000000000001","status":"async_launched","isAsync":true},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_agent","content":[{"type":"text","text":"Async agent launched successfully."}]}]}}`,
	}
	// The subagent's opening message is the task the session wrote for it:
	// no origin, no wrapper, the shape of a typed turn. Its first response
	// arrives as two entries whose output rises from a placeholder to the
	// figure, the second as one that never got a stop reason.
	subLines = []string{
		`{"type":"user","uuid":"s1u1","parentUuid":null,"isSidechain":true,"agentId":"a0000000000000001","sessionId":"s1","timestamp":"2026-01-07T10:04:12.000Z","message":{"role":"user","content":"Find where the parser reads usage."}}`,
		`{"type":"assistant","uuid":"s1a1","isSidechain":true,"agentId":"a0000000000000001","sessionId":"s1","timestamp":"2026-01-07T10:04:14.000Z","message":{"id":"sm1","model":"claude-opus-5-5","role":"assistant","stop_reason":null,"content":[{"type":"thinking","thinking":"look first"}],"usage":{"input_tokens":2,"cache_creation_input_tokens":23072,"cache_read_input_tokens":0,"output_tokens":8}}}`,
		`{"type":"assistant","uuid":"s1a2","isSidechain":true,"agentId":"a0000000000000001","sessionId":"s1","timestamp":"2026-01-07T10:04:15.000Z","message":{"id":"sm1","model":"claude-opus-5-5","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"stu1","name":"Read","input":{"file_path":"/tmp/a.go"}}],"usage":{"input_tokens":2,"cache_creation_input_tokens":23072,"cache_read_input_tokens":0,"output_tokens":742}}}`,
		`{"type":"user","uuid":"s1u2","isSidechain":true,"agentId":"a0000000000000001","sessionId":"s1","timestamp":"2026-01-07T10:04:17.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"stu1","content":"package a"}]}}`,
		`{"type":"assistant","uuid":"s1a3","isSidechain":true,"agentId":"a0000000000000001","sessionId":"s1","timestamp":"2026-01-07T10:04:20.000Z","message":{"id":"sm2","model":"claude-opus-5-5","role":"assistant","stop_reason":null,"content":[{"type":"text","text":"Done."}],"usage":{"input_tokens":1,"cache_creation_input_tokens":40,"cache_read_input_tokens":23072,"output_tokens":8}}}`,
	}
	sideLine = `{"agentType":"Explore","description":"Find the parser","toolUseId":"tu_agent","spawnDepth":1,"requestShape":"background","requestNonInteractive":true}`
	// A second subagent with no sidecar, which is not an error: it has no
	// name, and is otherwise read the same way.
	bareLines = []string{
		`{"type":"assistant","uuid":"b1","isSidechain":true,"agentId":"b0","sessionId":"s1","timestamp":"2026-01-07T10:05:00.000Z","message":{"id":"bm1","model":"claude-opus-5-5","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":3}}}`,
	}
)

// writeSession lays a session out on disk the way Claude Code does and
// returns its transcript's path.
func writeSession(t *testing.T) string {
	t.Helper()
	proj := filepath.Join(t.TempDir(), "-tmp-proj")
	subs := filepath.Join(proj, "s1", "subagents")
	if err := os.MkdirAll(subs, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(proj, "s1.jsonl"):                                  strings.Join(parentLines, "\n") + "\n",
		filepath.Join(subs, "agent-a0000000000000001.jsonl"):             strings.Join(subLines, "\n") + "\n",
		filepath.Join(subs, "agent-a0000000000000001.meta.json"):         sideLine,
		filepath.Join(subs, "agent-b0.jsonl"):                            strings.Join(bareLines, "\n") + "\n",
		filepath.Join(subs, "agent-a0000000000000001.forked-skill.json"): `{"skillName":"x"}`, // not a transcript
	}
	for p, body := range files {
		if err := writeFile(p, body); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(proj, "s1.jsonl")
}

// A subagent's tokens belong to the session that spawned it, and live in a
// file the session's own transcript does not contain. Read without it, a
// session that fanned its work out shows a fraction of what it spent: one
// surveyed session showed 7.6M tokens of the 22.8M it put through the model.
func TestSubagentTranscriptsAreReadBesideTheSession(t *testing.T) {
	run, err := ReadFile(writeSession(t), Options{})
	if err != nil {
		t.Fatal(err)
	}

	if len(run.Subagents) != 2 {
		t.Fatalf("subagents %+v, want the two transcripts and not the sidecars", run.Subagents)
	}
	sa := run.Subagents[0]
	if sa.ID != "a0000000000000001" || sa.Type != "Explore" || sa.Description != "Find the parser" {
		t.Errorf("first subagent %+v, want its sidecar's type and task", sa)
	}
	if b := run.Subagents[1]; b.ID != "b0" || b.Type != "" || b.Description != "" {
		t.Errorf("subagent without a sidecar %+v, want it named by nothing", b)
	}

	tot := timeline.Compute(run)
	// The session's one response and the subagents' three, each once.
	if tot.Responses != 4 {
		t.Errorf("responses %d, want 4", tot.Responses)
	}
	want := event.Usage{Input: 2 + 2 + 1 + 5, CacheRead: 1000 + 23072, CacheWrite: 100 + 23072 + 40, Output: 50 + 742 + 8 + 3}
	if tot.All != want {
		t.Errorf("tokens %+v, want %+v", tot.All, want)
	}
	// The task the session wrote for its subagent is not a prompt anybody typed.
	if tot.Prompts != 1 {
		t.Errorf("prompts %d, want the one typed into the session", tot.Prompts)
	}
	if tot.ToolCalls != 2 {
		t.Errorf("tool calls %d, want the session's Agent call and the subagent's Read", tot.ToolCalls)
	}

	a := tot.Subagents["a0000000000000001"]
	if a == nil {
		t.Fatal("no totals for the first subagent")
	}
	if a.Responses != 2 || a.ToolCalls != 1 || a.Usage.Output != 750 {
		t.Errorf("first subagent's totals %+v", a)
	}
	if !a.First.Equal(mustTime("2026-01-07T10:04:12Z")) || !a.Last.Equal(mustTime("2026-01-07T10:04:20Z")) {
		t.Errorf("first subagent ran %v → %v", a.First, a.Last)
	}

	// Every step says whose it is, and Seq stays unique across the files, so
	// the timeline's ordering never has to compare two lines numbered alike.
	seen := map[int]bool{}
	for _, st := range run.Steps {
		if seen[st.Seq] {
			t.Errorf("Seq %d appears twice", st.Seq)
		}
		seen[st.Seq] = true
		if (st.Agent != "") != st.Sidechain {
			t.Errorf("step %d: agent %q but sidechain %v", st.Seq, st.Agent, st.Sidechain)
		}
	}
	// The subagent's call is paired inside its own file.
	for _, st := range run.Steps {
		if st.Tool != nil && st.Tool.ID == "stu1" {
			if st.Agent != "a0000000000000001" || st.Tool.Outcome != event.OutcomeOK || !st.Tool.HasDuration {
				t.Errorf("subagent's Read %+v in agent %q", st.Tool, st.Agent)
			}
		}
	}
	if !run.End.Equal(mustTime("2026-01-07T10:05:00Z")) {
		t.Errorf("run ends %v, want the last subagent's last step", run.End)
	}
}

// The listing's figures and the session page's are two readings of the same
// files, and have to agree for a session with subagents as for one without.
func TestScanAgreesWithTheFullParserAcrossSubagents(t *testing.T) {
	path := writeSession(t)
	got, err := Scan(path)
	if err != nil {
		t.Fatal(err)
	}
	run, err := ReadFile(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	full := timeline.Compute(run)
	if got.Tokens != full.All || got.Responses != full.Responses || got.ToolCalls != full.ToolCalls {
		t.Errorf("scan %+v/%d/%d, full parse %+v/%d/%d",
			got.Tokens, got.Responses, got.ToolCalls, full.All, full.Responses, full.ToolCalls)
	}
}

// A fork starts from the session's context, and its file opens with a copy of
// the entry whose Agent call spawned it: same message id, same usage, same
// tool_use id, a uuid and a time of its own. Two forks from one response each
// copy their own call. The shapes are 2.1.307–2.1.310's; the values are made
// up.
var (
	forkParentLines = []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-02-03T09:00:00.000Z","sessionId":"s2","origin":{"kind":"human"},"message":{"role":"user","content":"try both fixes"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-02-03T09:00:20.000Z","sessionId":"s2","message":{"id":"m1","model":"claude-opus-5-5","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tu_f1","name":"Agent","input":{"description":"Try the first fix","prompt":"Try the first fix.","subagent_type":"fork"}}],"usage":{"input_tokens":3,"cache_creation_input_tokens":400,"cache_read_input_tokens":9000,"output_tokens":120}}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-02-03T09:00:21.000Z","sessionId":"s2","message":{"id":"m1","model":"claude-opus-5-5","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tu_f2","name":"Agent","input":{"description":"Try the second fix","prompt":"Try the second fix.","subagent_type":"fork"}}],"usage":{"input_tokens":3,"cache_creation_input_tokens":400,"cache_read_input_tokens":9000,"output_tokens":120}}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-02-03T09:00:22.000Z","sessionId":"s2","toolUseResult":{"agentId":"f1","status":"async_launched"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_f1","content":"Fork launched."}]}}`,
		`{"type":"user","uuid":"u3","timestamp":"2026-02-03T09:00:22.500Z","sessionId":"s2","toolUseResult":{"agentId":"f2","status":"async_launched"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_f2","content":"Fork launched."}]}}`,
	}
	forkLines = func(agent, use, msg string, out int) []string {
		return []string{
			`{"type":"fork-context-ref","agentId":"` + agent + `","parentSessionId":"s2","parentLastUuid":"u1","contextLength":2}`,
			`{"type":"assistant","uuid":"` + agent + `c","parentUuid":null,"isSidechain":true,"agentId":"` + agent + `","sessionId":"s2","timestamp":"2026-02-03T09:00:23.000Z","message":{"id":"m1","model":"claude-opus-5-5","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"` + use + `","name":"Agent","input":{"description":"Try a fix","prompt":"Try a fix.","subagent_type":"fork"}}],"usage":{"input_tokens":3,"cache_creation_input_tokens":400,"cache_read_input_tokens":9000,"output_tokens":120}}}`,
			`{"type":"user","uuid":"` + agent + `u","isSidechain":true,"agentId":"` + agent + `","sessionId":"s2","timestamp":"2026-02-03T09:00:23.100Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + use + `","content":"Forked."},{"type":"text","text":"<fork-boilerplate>You are a fork.</fork-boilerplate>"}]}}`,
			`{"type":"assistant","uuid":"` + agent + `a","isSidechain":true,"agentId":"` + agent + `","sessionId":"s2","timestamp":"2026-02-03T09:00:40.000Z","message":{"id":"` + msg + `","model":"claude-opus-5-5","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"Done."}],"usage":{"input_tokens":1,"cache_creation_input_tokens":50,"cache_read_input_tokens":9400,"output_tokens":` + strconv.Itoa(out) + `}}}`,
		}
	}
)

func writeForkSession(t *testing.T) string {
	t.Helper()
	proj := filepath.Join(t.TempDir(), "-tmp-proj")
	subs := filepath.Join(proj, "s2", "subagents")
	if err := os.MkdirAll(subs, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(proj, "s2.jsonl"):           strings.Join(forkParentLines, "\n") + "\n",
		filepath.Join(subs, "agent-f1.jsonl"):     strings.Join(forkLines("f1", "tu_f1", "fm1", 30), "\n") + "\n",
		filepath.Join(subs, "agent-f1.meta.json"): `{"agentType":"fork","isFork":true,"description":"Try the first fix","toolUseId":"tu_f1","spawnDepth":1}`,
		filepath.Join(subs, "agent-f2.jsonl"):     strings.Join(forkLines("f2", "tu_f2", "fm2", 40), "\n") + "\n",
		filepath.Join(subs, "agent-f2.meta.json"): `{"agentType":"fork","isFork":true,"description":"Try the second fix","toolUseId":"tu_f2","spawnDepth":1}`,
	}
	for p, body := range files {
		if err := writeFile(p, body); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(proj, "s2.jsonl")
}

// The copied entry is the session's response, already counted where it was
// made. Read again from each fork it counted the response and its Agent call
// once per fork more; in one surveyed session, 9 responses and 9 tool calls.
func TestAForkDoesNotRecountTheResponseThatSpawnedIt(t *testing.T) {
	path := writeForkSession(t)
	run, err := ReadFile(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	tot := timeline.Compute(run)
	if tot.Responses != 3 {
		t.Errorf("responses %d, want the session's one and each fork's own", tot.Responses)
	}
	if tot.ToolCalls != 2 {
		t.Errorf("tool calls %d, want the session's two Agent calls", tot.ToolCalls)
	}
	want := event.Usage{Input: 3 + 1 + 1, CacheRead: 9000 + 9400 + 9400, CacheWrite: 400 + 50 + 50, Output: 120 + 30 + 40}
	if tot.All != want {
		t.Errorf("tokens %+v, want %+v", tot.All, want)
	}
	if n := run.Skipped["assistant(copied)"]; n != 2 {
		t.Errorf("copied entries skipped %d, want one per fork", n)
	}
	for _, st := range run.Steps {
		if st.Tool != nil && (st.Agent != "" || st.Tool.Outcome != event.OutcomeOK) {
			t.Errorf("Agent call %s in agent %q with outcome %q, want each once, in the session, answered", st.Tool.ID, st.Agent, st.Tool.Outcome)
		}
	}
	if tot.Prompts != 1 {
		t.Errorf("prompts %d, want the one typed into the session", tot.Prompts)
	}

	got, err := Scan(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tokens != tot.All || got.Responses != tot.Responses || got.ToolCalls != tot.ToolCalls {
		t.Errorf("scan %+v/%d/%d, full parse %+v/%d/%d",
			got.Tokens, got.Responses, got.ToolCalls, tot.All, tot.Responses, tot.ToolCalls)
	}
}

// Discover carries each session's subagent files, so a listing can tell when
// one has grown while the session's own file has not.
func TestDiscoverListsSubagentTranscripts(t *testing.T) {
	path := writeSession(t)
	sessions, err := Discover(filepath.Dir(filepath.Dir(path)))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions %+v, want the one; a subagent is not a session", sessions)
	}
	var names []string
	for _, f := range sessions[0].Subagents {
		names = append(names, filepath.Base(f.Path))
	}
	if strings.Join(names, ",") != "agent-a0000000000000001.jsonl,agent-b0.jsonl" {
		t.Errorf("subagent files %v", names)
	}
}

// In a session's own transcript every entry of a response repeats the same
// usage, and the first is as good as any. Where the entries differ — as a
// subagent's do, written while the response streams — the output is the
// largest any entry recorded, and the context fields are the first's.
func TestRepeatedEntriesCanOnlyRaiseOutput(t *testing.T) {
	run := read(t,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-09-18T10:00:00.000Z","message":{"id":"m1","model":"m","role":"assistant","stop_reason":null,"content":[{"type":"thinking","thinking":"x"}],"usage":{"input_tokens":2,"cache_read_input_tokens":10,"output_tokens":8}}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-09-18T10:00:01.000Z","message":{"id":"m1","model":"m","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"y"}],"usage":{"input_tokens":2,"cache_read_input_tokens":10,"output_tokens":742}}}`,
		// An entry recording less than one before it does not lower the count.
		`{"type":"assistant","uuid":"a3","timestamp":"2026-09-18T10:00:02.000Z","message":{"id":"m1","model":"m","role":"assistant","stop_reason":null,"content":[{"type":"text","text":"z"}],"usage":{"input_tokens":2,"cache_read_input_tokens":10,"output_tokens":8}}}`,
		`{"type":"assistant","uuid":"a4","timestamp":"2026-09-18T10:00:03.000Z","message":{"id":"m2","model":"m","role":"assistant","stop_reason":null,"content":[{"type":"text","text":"w"}],"usage":{"input_tokens":1,"output_tokens":16}}}`,
	)
	var us []*event.Usage
	for _, st := range run.Steps {
		if st.Usage != nil {
			us = append(us, st.Usage)
		}
	}
	if len(us) != 2 {
		t.Fatalf("%d responses, want 2", len(us))
	}
	if us[0].Output != 742 || us[0].Input != 2 || us[0].CacheRead != 10 {
		t.Errorf("first response %+v, want output 742, context as recorded", *us[0])
	}
	if us[1].Output != 16 {
		t.Errorf("second response %+v, want output 16", *us[1])
	}
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
