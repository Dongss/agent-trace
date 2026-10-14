package claudecode

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

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

	// Activity is when those responses were made: their tokens and count in
	// quarter-hour slots, keyed by the slot's start in Unix seconds. A quarter
	// hour rather than an hour because time zones sit a whole number of
	// quarter hours from UTC but not always of hours (+5:30, +5:45), and the
	// listing draws each slot in the reader's local hour. A response with no
	// timestamp is in the totals above and not here.
	Activity map[int64]*Slot

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

// Slot is the responses made in one quarter hour of Totals.Activity.
type Slot struct {
	Tokens    event.Usage
	Responses int
}

// SlotSeconds is the width of a Totals.Activity slot.
const SlotSeconds = 15 * 60

// SlotOf is the Totals.Activity key a response made at t is counted under.
func SlotOf(t time.Time) int64 {
	u := t.Unix()
	return u - u%SlotSeconds
}

// scanKeys are the markers that make a line worth decoding. A line matching
// none of them cannot carry anything Scan reports.
var scanKeys = []string{`"usage"`, `"aiTitle"`, `"customTitle"`, `"agentName"`, `"cost-state"`}

// scanState is what deduplication needs to remember across the lines of a
// session and its subagents: which responses have been counted, at what
// output and in which slot, and which tool calls.
type scanState struct {
	resp    map[string]*scanResp
	seenUse map[string]bool
	// session is false while reading a subagent's transcript, whose titles and
	// cost snapshots, had it any, would not be the session's.
	session bool
}

// scanResp is one counted response. A later entry of it can raise its output,
// and the rise belongs to the slot of the entry that was counted, as the full
// parser attaches usage to the first entry of a message.id.
type scanResp struct {
	u    event.Usage
	slot *Slot // nil when the first entry carried no timestamp
}

// Scan reads one session for listing purposes: its transcript and those of
// the subagents it spawned, as ReadFile does.
func Scan(path string) (*Totals, error) {
	out := Totals{Activity: map[int64]*Slot{}}
	st := scanState{resp: map[string]*scanResp{}, seenUse: map[string]bool{}, session: true}
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
				out0, think0 := prev.u.Output, prev.u.Thinking
				repeatUsage(&prev.u, e.Message)
				dOut, dThink := prev.u.Output-out0, prev.u.Thinking-think0
				out.Tokens.Output += dOut
				out.Tokens.Thinking += dThink
				if prev.slot != nil {
					prev.slot.Tokens.Output += dOut
					prev.slot.Tokens.Thinking += dThink
				}
			} else {
				r := &scanResp{u: firstUsage(e.Message)}
				st.resp[e.Message.ID] = r
				out.Responses++
				addUsage(&out.Tokens, r.u)
				if at, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil {
					k := SlotOf(at)
					if out.Activity[k] == nil {
						out.Activity[k] = &Slot{}
					}
					r.slot = out.Activity[k]
					r.slot.Responses++
					addUsage(&r.slot.Tokens, r.u)
				}
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

func addUsage(to *event.Usage, u event.Usage) {
	to.Input += u.Input
	to.CacheRead += u.CacheRead
	to.CacheWrite += u.CacheWrite
	to.Output += u.Output
	to.Thinking += u.Thinking
}
