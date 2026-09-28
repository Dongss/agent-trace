package claudecode

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/Dongss/agent-trace/internal/event"
	"github.com/Dongss/agent-trace/internal/textfmt"
)

// Totals is what a session listing wants to show — a title and a token count —
// and cannot get cheaply any other way.
//
// Neither can be had from the ends of the file. The title is written by an
// `ai-title` entry repeated throughout the transcript, and on a 123 MB session
// a bounded tail window holds only a line or two of it. The tokens have to be
// recomputed, because the transcript's own `cost-state` snapshot exists in only
// 24 of the 95 sessions on the survey machine — so a listing that relied on it
// would leave three quarters of its rows empty.
//
// So Scan reads the whole file, but parses almost none of it: a line is only
// handed to the JSON decoder when a substring check says it could matter. On
// the survey corpus that is 16% of the bytes.
type Totals struct {
	// Title is the model-written session title, when the CLI wrote one — 66 of
	// the 95 sessions on the survey machine.
	Title string
	// Name is what the CLI calls the session, from its agent-name entries and
	// falling back to custom-title, which is identical on every surveyed
	// session carrying both. It is not the agent CLI: that word names Claude
	// Code and its peers everywhere else in agtrace.
	AgentName string

	Tokens    event.Usage
	Responses int
	ToolCalls int

	// Cost is what the transcript states, when it states anything. It is not
	// recomputed from the tokens: that would need a price table which is not
	// in the transcript and would go stale here.
	Cost *float64

	// customTitle is the weaker of the two names a session can carry; Title
	// falls back to it. See the note in the full parser.
	customTitle string
}

// Total is every token the session put through a model — fresh input, cache
// read, cache write and output added together.
//
// The four are priced very differently and this sum is therefore a measure of
// volume, not of money: cache reads dominate it, and a session can have a huge
// total and a small bill. Cost is reported separately and only when the
// transcript states it.
func (t Totals) Total() int {
	return t.Tokens.Input + t.Tokens.CacheRead + t.Tokens.CacheWrite + t.Tokens.Output
}

// scanKeys are the markers that make a line worth decoding. A line matching
// none of them cannot carry anything Scan reports.
var scanKeys = []string{`"usage"`, `"aiTitle"`, `"customTitle"`, `"agentName"`, `"cost-state"`}

// scanState is what deduplication needs to remember across the lines of a
// session and its subagents: which responses have been counted, and at what
// output, and which tool calls.
type scanState struct {
	resp    map[string]*event.Usage
	seenUse map[string]bool
	// session is false while reading a subagent's transcript, whose titles and
	// cost snapshots, had it any, would not be the session's.
	session bool
}

// Scan reads one session for listing purposes: its transcript and those of
// the subagents it spawned, as ReadFile does.
func Scan(path string) (*Totals, error) {
	var out Totals
	st := scanState{resp: map[string]*event.Usage{}, seenUse: map[string]bool{}, session: true}
	if err := scanFile(path, &out, &st); err != nil {
		return nil, err
	}
	st.session = false
	for _, f := range subagentFiles(path) {
		err := scanFile(f.Path, &out, &st)
		if errors.Is(err, fs.ErrNotExist) {
			continue // as in readSubagents
		}
		if err != nil {
			return nil, err
		}
	}

	if out.AgentName == "" {
		out.AgentName = out.customTitle
	}
	out.Title = textfmt.Clip(out.Title, 120)
	out.AgentName = textfmt.Clip(out.AgentName, 80)
	return &out, nil
}

func scanFile(path string, out *Totals, st *scanState) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 1<<20)
	for {
		line, rerr := br.ReadString('\n')
		if s := strings.TrimSpace(line); s != "" {
			interesting := false
			for _, k := range scanKeys {
				if strings.Contains(s, k) {
					interesting = true
					break
				}
			}
			if interesting {
				scanLine(s, out, st)
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			return rerr
		}
	}
}

func scanLine(s string, out *Totals, st *scanState) {
	var e entry
	if json.Unmarshal([]byte(s), &e) != nil {
		return
	}
	if !st.session && e.Type != "assistant" {
		return
	}
	switch e.Type {
	case "ai-title":
		// Rewritten as the session goes; the last one is the current title.
		var t struct {
			AITitle string `json:"aiTitle"`
		}
		if json.Unmarshal([]byte(s), &t) == nil && t.AITitle != "" {
			out.Title = t.AITitle
		}
	case "custom-title":
		var t struct {
			CustomTitle string `json:"customTitle"`
		}
		if json.Unmarshal([]byte(s), &t) == nil && t.CustomTitle != "" {
			out.customTitle = t.CustomTitle
		}
	case "agent-name":
		if e.AgentName != "" {
			out.AgentName = e.AgentName
		}
	case "cost-state":
		c := e.TotalCostUSD
		out.Cost = &c
	case "assistant":
		if e.Message == nil {
			return
		}
		// Usage is written on every content block of a response, so it is
		// counted once per message.id, and a later entry can only raise the
		// output — the same rule as the full parser's, through the same code.
		if isResponse(&e) {
			if prev, seen := st.resp[e.Message.ID]; seen {
				out0, think0 := prev.Output, prev.Thinking
				repeatUsage(prev, e.Message)
				out.Tokens.Output += prev.Output - out0
				out.Tokens.Thinking += prev.Thinking - think0
			} else {
				u := firstUsage(e.Message)
				st.resp[e.Message.ID] = &u
				out.Responses++
				out.Tokens.Input += u.Input
				out.Tokens.CacheRead += u.CacheRead
				out.Tokens.CacheWrite += u.CacheWrite
				out.Tokens.Output += u.Output
				out.Tokens.Thinking += u.Thinking
			}
		}
		for _, b := range decodeBlocks(e.Message.Content) {
			if b.Type == "tool_use" && b.ID != "" && !st.seenUse[b.ID] {
				st.seenUse[b.ID] = true
				out.ToolCalls++
			}
		}
	}
}
