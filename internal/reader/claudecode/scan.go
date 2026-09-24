package claudecode

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
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

// Scan reads one transcript for listing purposes.
func Scan(path string) (*Totals, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var (
		out     Totals
		seenMsg = map[string]bool{}
		seenUse = map[string]bool{}
	)

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
				scanLine(s, &out, seenMsg, seenUse)
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return nil, rerr
		}
	}

	if out.AgentName == "" {
		out.AgentName = out.customTitle
	}
	out.Title = textfmt.Clip(out.Title, 120)
	out.AgentName = textfmt.Clip(out.AgentName, 80)
	return &out, nil
}

func scanLine(s string, out *Totals, seenMsg, seenUse map[string]bool) {
	var e entry
	if json.Unmarshal([]byte(s), &e) != nil {
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
		// The same usage object is repeated on every content block of a
		// response, so it is counted once per message.id.
		if isResponse(&e) && !seenMsg[e.Message.ID] {
			u := e.Message.Usage
			seenMsg[e.Message.ID] = true
			out.Responses++
			out.Tokens.Input += u.InputTokens
			out.Tokens.CacheRead += u.CacheReadInputTokens
			out.Tokens.CacheWrite += u.CacheCreationInputTokens
			out.Tokens.Output += u.OutputTokens
			out.Tokens.Thinking += u.OutputTokensDetails.ThinkingTokens
		}
		for _, b := range decodeBlocks(e.Message.Content) {
			if b.Type == "tool_use" && b.ID != "" && !seenUse[b.ID] {
				seenUse[b.ID] = true
				out.ToolCalls++
			}
		}
	}
}
