// Package claudecode reads a Claude Code session transcript into the neutral
// event model.
//
// The transcript is append-only JSONL and the CLI may still be writing it, so
// reading is strictly read-only, streams rather than slurps, and tolerates a
// half-written final line.
//
// Three properties of the real format drive most of the code here, all three
// found by surveying sessions on disk rather than from documentation:
//
//   - One API response is written out as several entries, one per content
//     block, each repeating the same usage object. In a 16,385-line session
//     that was 4,558 assistant entries for 2,670 responses; adding usage up
//     per entry overstated the run's tokens by about 70%. Usage is attached to
//     the first entry of a message.id and dropped on the rest.
//
//   - Timestamps are not monotonic (248 out-of-order pairs in the same
//     session) and nine entry types carry none at all. File order is the only
//     total order that always exists, so it is what Seq records.
//
//   - A compaction boundary may carry no timestamp, which would be awkward
//     because it is the single most important event in the file. Every
//     surveyed one had a timestamp; HasTime says so per entry, and Seq places
//     the ones that do not.
package claudecode

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dongss/agent-trace/internal/event"
	"github.com/Dongss/agent-trace/internal/textfmt"
)

// Options controls how a transcript is read.
type Options struct {
	// PreviewRunes caps the length of any text preview. Previews exist so a
	// reader can recognise a step, not so the content can be read back out.
	PreviewRunes int
}

func (o Options) withDefaults() Options {
	if o.PreviewRunes <= 0 {
		o.PreviewRunes = 160
	}
	return o
}

// ReadFile parses one transcript.
func ReadFile(path string, opt Options) (*event.Run, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Read(f, path, opt)
}

// Read parses a transcript from r. path is recorded on the Run and used for
// nothing else.
func Read(r io.Reader, path string, opt Options) (*event.Run, error) {
	opt = opt.withDefaults()

	run := &event.Run{
		Source:  event.SourceClaudeCode,
		Path:    textfmt.Path(path),
		Skipped: map[string]int{},
	}

	p := &parser{
		opt:      opt,
		run:      run,
		pending:  map[string]*pendingTool{},
		seenMsg:  map[string]bool{},
		cwdCount: map[string]int{},
		versions: map[string]bool{},
	}

	// A transcript line can be megabytes long — a Read of a large file, a
	// screenshot as base64 — so lines are read without an upper bound instead
	// of through bufio.Scanner, whose token limit would turn a big line into a
	// spurious parse failure.
	br := bufio.NewReaderSize(r, 1<<20)
	seq := 0
	for {
		line, err := br.ReadString('\n')
		complete := err == nil
		if line != "" || complete {
			if s := strings.TrimSpace(line); s != "" {
				seq++
				if perr := p.line(seq, s); perr != nil {
					run.Malformed++
					// A parse failure on the very last line of a file that does
					// not end in a newline is the CLI mid-write, not corruption.
					if !complete && errors.Is(err, io.EOF) {
						run.Malformed--
						run.Truncated = true
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read %s: %w", textfmt.Path(path), err)
		}
	}

	p.finish()
	return run, nil
}

type pendingTool struct {
	step int // index into run.Steps
}

type parser struct {
	opt     Options
	run     *event.Run
	pending map[string]*pendingTool
	// seenMsg deduplicates usage across the several entries one API response is
	// written as.
	seenMsg     map[string]bool
	cwdCount    map[string]int
	versions    map[string]bool
	stated      *event.Stated
	title       string
	customTitle string
}

func (p *parser) line(seq int, s string) error {
	var e entry
	if err := json.Unmarshal([]byte(s), &e); err != nil {
		return err
	}

	if e.SessionID != "" && p.run.SessionID == "" {
		p.run.SessionID = e.SessionID
	}
	if e.CWD != "" {
		p.cwdCount[e.CWD]++
	}
	if e.Version != "" {
		p.versions[e.Version] = true
	}
	if e.GitBranch != "" {
		p.run.GitBranch = e.GitBranch
	}

	switch e.Type {
	case "assistant":
		p.assistant(seq, &e)
	case "user":
		p.user(seq, &e)
	case "system":
		p.system(seq, &e)
	case "cost-state":
		p.costState(&e)
	case "ai-title":
		// Rewritten as the session goes, with the same value each time on the
		// surveyed sessions; the last one is the current title either way.
		var t struct {
			AITitle string `json:"aiTitle"`
		}
		if json.Unmarshal([]byte(s), &t) == nil && t.AITitle != "" {
			p.title = t.AITitle
		}
	case "custom-title":
		// A second name the session carries. On every surveyed session where
		// both exist it is byte-identical to agent-name, and the two appear
		// and disappear together, so it reads as the agent's label rather
		// than as a title somebody typed. It is kept as a fallback because it
		// is still a human-meaningful name, and ranked below the model-written
		// title because that one describes the session rather than the project.
		var t struct {
			CustomTitle string `json:"customTitle"`
		}
		if json.Unmarshal([]byte(s), &t) == nil && t.CustomTitle != "" {
			p.customTitle = t.CustomTitle
		}
	case "agent-name":
		if e.AgentName != "" {
			p.run.AgentName = e.AgentName
		}
	default:
		p.run.Skipped[e.Type]++
	}
	return nil
}

func (p *parser) at(e *entry) (time.Time, bool) {
	if e.Timestamp == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, e.Timestamp)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func (p *parser) base(seq int, e *entry, kind event.StepKind) event.Step {
	at, has := p.at(e)
	return event.Step{
		Seq:        seq,
		UUID:       e.UUID,
		ParentUUID: e.ParentUUID,
		Kind:       kind,
		At:         at,
		HasTime:    has,
		Sidechain:  e.IsSidechain,
		AgentName:  e.AgentName,
	}
}

func (p *parser) assistant(seq int, e *entry) {
	if e.Message == nil {
		p.run.Skipped["assistant(no message)"]++
		return
	}
	blocks := decodeBlocks(e.Message.Content)

	// Usage belongs to the response, not to the block, and Claude Code repeats
	// it on every block of the same response.
	var u *event.Usage
	if e.Message.Usage != nil && e.Message.ID != "" && !p.seenMsg[e.Message.ID] {
		p.seenMsg[e.Message.ID] = true
		w := e.Message.Usage
		u = &event.Usage{
			Input:      w.InputTokens,
			CacheRead:  w.CacheReadInputTokens,
			CacheWrite: w.CacheCreationInputTokens,
			Output:     w.OutputTokens,
			Thinking:   w.OutputTokensDetails.ThinkingTokens,
		}
	}

	for _, b := range blocks {
		switch b.Type {
		case "text", "thinking":
			st := p.base(seq, e, event.KindAssistant)
			st.Model = e.Message.Model
			st.Effort = e.Effort
			txt := b.Text
			if b.Type == "thinking" {
				txt = b.Thinking
			}
			st.Text = textfmt.Clip(txt, p.opt.PreviewRunes)
			st.Usage = u
			u = nil // charge the response once, to its first step
			p.run.Steps = append(p.run.Steps, st)

		case "tool_use":
			st := p.base(seq, e, event.KindTool)
			st.Model = e.Message.Model
			st.Effort = e.Effort
			at, has := p.at(e)
			tool := &event.Tool{
				ID:        b.ID,
				Name:      b.Name,
				Brief:     p.brief(b.Name, b.Input),
				Outcome:   event.OutcomeUnpaired,
				MCPServer: mcpServer(b.Name),
			}
			if has {
				tool.Started = at
			}
			st.Tool = tool
			st.Usage = u
			u = nil
			p.run.Steps = append(p.run.Steps, st)
			if b.ID != "" {
				p.pending[b.ID] = &pendingTool{step: len(p.run.Steps) - 1}
			}
		}
	}

	// A response whose blocks are all of kinds not modelled above still cost
	// tokens; record it so the run's totals stay honest.
	if u != nil {
		st := p.base(seq, e, event.KindAssistant)
		st.Model = e.Message.Model
		st.Usage = u
		p.run.Steps = append(p.run.Steps, st)
	}
}

func (p *parser) user(seq int, e *entry) {
	if e.Message == nil {
		p.run.Skipped["user(no message)"]++
		return
	}

	// A plain string content is a human turn. Everything else is the tool
	// plumbing the CLI models as user-role messages.
	var text string
	if err := json.Unmarshal(e.Message.Content, &text); err == nil {
		st := p.base(seq, e, event.KindPrompt)
		st.Text = textfmt.Clip(text, p.opt.PreviewRunes)
		p.run.Steps = append(p.run.Steps, st)
		return
	}

	for _, b := range decodeBlocks(e.Message.Content) {
		switch b.Type {
		case "tool_result":
			p.pairResult(e, b)
		case "text":
			// Tool companions and meta notices are not human turns; a genuine
			// multi-block user message is.
			if e.IsMeta || e.SourceToolUseID != "" {
				continue
			}
			st := p.base(seq, e, event.KindPrompt)
			st.Text = textfmt.Clip(b.Text, p.opt.PreviewRunes)
			p.run.Steps = append(p.run.Steps, st)
		}
	}
}

// pairResult closes the tool call a result belongs to. The pairing is by id
// rather than by position: results do not always follow their call directly.
func (p *parser) pairResult(e *entry, b block) {
	pt, ok := p.pending[b.ToolUseID]
	if !ok {
		// A result whose call is not in this file: the transcript was
		// compacted, or resumed from another session.
		p.run.Skipped["tool_result(no call)"]++
		return
	}
	delete(p.pending, b.ToolUseID)
	tool := p.run.Steps[pt.step].Tool

	if at, has := p.at(e); has {
		tool.Ended = at
		if !tool.Started.IsZero() {
			tool.Duration = at.Sub(tool.Started)
			// Out-of-order timestamps are common enough that a negative
			// duration is a data property, not an impossibility. Report it as
			// unknown rather than as a negative bar.
			tool.HasDuration = tool.Duration >= 0
		}
	}

	tool.ResultBytes = resultSize(b.Content, e.ToolUseResult)

	var meta toolResultMeta
	if len(e.ToolUseResult) > 0 {
		_ = json.Unmarshal(e.ToolUseResult, &meta) // non-object shapes leave it zero
	}

	switch {
	case e.ToolDenialKind != "":
		tool.Outcome = event.OutcomeDenied
		tool.Detail = e.ToolDenialKind
	case meta.Interrupted:
		tool.Outcome = event.OutcomeInterrupted
		tool.Detail = "interrupted"
	case b.IsError:
		tool.Outcome = event.OutcomeError
		tool.Detail = textfmt.Clip(firstText(b.Content), 120)
	default:
		tool.Outcome = event.OutcomeOK
	}
}

func (p *parser) system(seq int, e *entry) {
	switch e.Subtype {
	case "compact_boundary":
		if e.CompactMetadata == nil {
			p.run.Skipped["compact_boundary(no metadata)"]++
			return
		}
		at, has := p.at(e)
		p.run.Compacts = append(p.run.Compacts, event.Compact{
			Seq:      seq,
			Trigger:  e.CompactMetadata.Trigger,
			Pre:      e.CompactMetadata.PreTokens,
			Post:     e.CompactMetadata.PostTokens,
			Dropped:  e.CompactMetadata.CumulativeDroppedTokens,
			Duration: time.Duration(e.CompactMetadata.DurationMs) * time.Millisecond,
			At:       at,
			HasTime:  has,
		})
	case "turn_duration", "local_command", "stop_hook_summary":
		st := p.base(seq, e, event.KindNote)
		st.Text = textfmt.Clip(noteText(e), p.opt.PreviewRunes)
		p.run.Steps = append(p.run.Steps, st)
	default:
		p.run.Skipped["system/"+e.Subtype]++
	}
}

func (p *parser) costState(e *entry) {
	s := &event.Stated{
		CostUSD:          e.TotalCostUSD,
		TotalDur:         time.Duration(e.TotalDuration) * time.Millisecond,
		APIDur:           time.Duration(e.TotalAPIDuration) * time.Millisecond,
		ToolDur:          time.Duration(e.TotalToolDuration) * time.Millisecond,
		LinesAdded:       e.TotalLinesAdded,
		LinesRemoved:     e.TotalLinesRemoved,
		UnknownModelCost: e.HasUnknownModelCost,
		ByModel:          map[string]event.Usage{},
	}
	for model, mu := range e.ModelUsage {
		s.ByModel[model] = event.Usage{
			Input:      mu.InputTokens,
			CacheRead:  mu.CacheReadInputTokens,
			CacheWrite: mu.CacheCreationInputTokens,
			Output:     mu.OutputTokens,
			Thinking:   mu.ThinkingTokens,
		}
	}
	p.stated = s // a snapshot: the last one in the file wins
}

func (p *parser) finish() {
	p.run.Stated = p.stated
	// The title is model-written from the conversation, so it follows the same
	// rule as any other text the page shows.
	// Title is the model-written one and nothing else. It used to fall back to
	// custom-title, which made a listing's Title and Name columns repeat each
	// other on every session that had no title of its own — two columns saying
	// one thing. The fallback belongs to whoever needs a heading, not here.
	p.run.Title = textfmt.Clip(p.title, 120)
	if p.run.AgentName == "" {
		// Identical to agent-name on every surveyed session that has both.
		p.run.AgentName = p.customTitle
	}

	// Anything still pending never got a result. That is information, and the
	// zero value already says so.
	p.pending = nil

	best := 0
	for cwd, n := range p.cwdCount {
		if n > best {
			best, p.run.CWD = n, textfmt.Path(cwd)
		}
	}
	for v := range p.versions {
		p.run.Versions = append(p.run.Versions, v)
	}
	// Ascending by version, not by string: a plain sort puts 2.1.98 after
	// 2.1.231, which would make the oldest release look like the newest to
	// anything that reports the range.
	sort.Slice(p.run.Versions, func(i, j int) bool {
		return compareVersions(p.run.Versions[i], p.run.Versions[j]) < 0
	})

	for _, st := range p.run.Steps {
		if !st.HasTime {
			continue
		}
		if p.run.Start.IsZero() || st.At.Before(p.run.Start) {
			p.run.Start = st.At
		}
		if st.At.After(p.run.End) {
			p.run.End = st.At
		}
	}
}

// brief turns a tool call's arguments into one line. Which argument matters is
// per tool: a Bash call is its command, a Read is its path. Falling back to the
// argument names keeps an unknown tool legible without dumping its payload.
func (p *parser) brief(name string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal(input, &args); err != nil {
		return ""
	}

	pick := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := args[k].(string); ok && s != "" {
				return s
			}
		}
		return ""
	}

	var out string
	switch name {
	case "Bash", "BashOutput":
		out = pick("command", "description")
	case "Read", "Write", "NotebookEdit":
		out = textfmt.Path(pick("file_path", "notebook_path"))
	case "Edit":
		out = textfmt.Path(pick("file_path"))
	case "Glob", "Grep":
		out = pick("pattern")
		if dir := pick("path"); dir != "" {
			out += " in " + textfmt.Path(dir)
		}
	case "Task", "Agent":
		out = pick("description", "prompt")
	case "WebFetch", "WebSearch":
		out = pick("url", "query")
	case "Skill":
		out = pick("skill")
	default:
		out = pick("command", "description", "query", "pattern", "url", "prompt", "file_path")
		if out == "" {
			keys := make([]string, 0, len(args))
			for k := range args {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			out = strings.Join(keys, ", ")
		}
	}

	return textfmt.Clip(out, p.opt.PreviewRunes)
}

func decodeBlocks(raw json.RawMessage) []block {
	if len(raw) == 0 {
		return nil
	}
	var bs []block
	if err := json.Unmarshal(raw, &bs); err != nil {
		return nil
	}
	return bs
}

// resultSize reports how big a tool's result was, preferring the block content
// the model actually saw over the richer object the CLI stored beside it.
func resultSize(content, stored json.RawMessage) int {
	if n := len(content); n > 0 {
		return n
	}
	return len(stored)
}

// firstText pulls a human-readable line out of a tool_result's content, which
// is a string for some tools and a block array for others.
func firstText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	for _, b := range decodeBlocks(raw) {
		if b.Text != "" {
			return b.Text
		}
	}
	return ""
}

func noteText(e *entry) string {
	var s string
	if len(e.Content) > 0 {
		if err := json.Unmarshal(e.Content, &s); err == nil && s != "" {
			return s
		}
	}
	switch e.Subtype {
	case "turn_duration":
		return fmt.Sprintf("turn took %s over %d messages",
			(time.Duration(e.DurationMs) * time.Millisecond).Round(time.Millisecond), e.MessageCount)
	case "stop_hook_summary":
		return "stop hooks ran"
	}
	return e.Subtype
}

// compareVersions orders dotted versions by each component's numeric value,
// falling back to a string comparison for a component that is not a number so
// that an unexpected shape still sorts deterministically.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var ac, bc string
		if i < len(as) {
			ac = as[i]
		}
		if i < len(bs) {
			bc = bs[i]
		}
		an, aerr := strconv.Atoi(ac)
		bn, berr := strconv.Atoi(bc)
		if aerr == nil && berr == nil {
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
			continue
		}
		if ac != bc {
			if ac < bc {
				return -1
			}
			return 1
		}
	}
	return 0
}

// mcpServer names the MCP server behind a tool, from the mcp__<server>__<tool>
// convention the CLI uses.
func mcpServer(tool string) string {
	if !strings.HasPrefix(tool, "mcp__") {
		return ""
	}
	rest := strings.TrimPrefix(tool, "mcp__")
	if i := strings.Index(rest, "__"); i > 0 {
		return rest[:i]
	}
	return rest
}
