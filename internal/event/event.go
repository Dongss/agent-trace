// Package event is the CLI-neutral middle of agent-trace. A reader turns one
// vendor's transcript into these values; a renderer only ever sees these.
//
// Two rules shape the types here, both of them from reading real transcripts:
//
// Usage is a per-step delta, never a running total. Vendors disagree on which
// they report, and a model that could hold either would eventually be summed
// twice. Readers normalise to deltas and a consumer that wants a total adds
// them up itself.
//
// Absent is not zero. A quantity a transcript does not record is represented
// by a nil pointer or an explicit Has* flag, so a renderer can leave a gap
// instead of drawing a measurement nobody made.
package event

import "time"

// Source identifies the agent CLI a Run was read from.
type Source string

const SourceClaudeCode Source = "claude-code"

// Run is one agent session.
type Run struct {
	Source    Source
	SessionID string
	Path      string // transcript the run was read from
	CWD       string // most frequent working directory in the run
	GitBranch string

	// Title is the session title the CLI wrote for itself, when it wrote one.
	// It is model-generated from the conversation.
	Title string
	// AgentName is the agent the session ran under, when the CLI names one.
	// It is not a subagent's name.
	AgentName string
	Versions  []string // CLI versions seen, ascending; a long session spans several
	// Surfaces is what drove the run, in the order each first appeared, with
	// the versions seen under each. See Surface.
	Surfaces []Surface

	Start, End time.Time // first and last timestamped entry

	Steps    []Step
	Compacts []Compact

	// Stated is what the transcript itself claims the run cost, when it says
	// so. It is authoritative over anything recomputed from Steps: see
	// Totals.Disagrees.
	Stated *Stated

	Skipped   map[string]int // entry types the reader does not model, counted
	Malformed int            // lines that did not parse
	Truncated bool           // last line was incomplete: the CLI is still writing

	// Windowed is set when the run has been narrowed to a slice of itself, and
	// WindowFrom/WindowTo are the bounds that were asked for. Stated is nil in
	// that case: the transcript states its cost and durations for the whole
	// session, and nothing in it can attribute them to a slice.
	Windowed             bool
	WindowFrom, WindowTo time.Time
	// FullStart and FullEnd are the whole run's span, kept so a windowed view
	// can say what it is a window into. The page stopped saying so on
	// 2026-09-20; the fields stay because the window is still a slice of
	// something and only these remember what.
	FullStart, FullEnd time.Time
}

// A Surface is one thing that drove the run: a terminal, an editor extension,
// an SDK. The name is whatever the CLI calls it, carried through unmapped —
// the set is open, a release can add to it, and a name nobody here recognises
// is still the true answer to "what ran this".
//
// Versions are grouped per surface rather than pooled, because the surfaces
// do not share a build. A session resumed from an editor extension carries
// that extension's bundled CLI, which can be an older release than the
// terminal's: one surveyed session went 2.1.270 → 2.1.263 on the switch, and
// a single range over the run would have reported it backwards.
type Surface struct {
	Name     string   // the CLI's own string; "" where it records none
	Versions []string // ascending by value
}

// StepKind is what a step represents on the timeline.
type StepKind string

const (
	KindPrompt    StepKind = "prompt"    // a message somebody typed to the model
	KindAssistant StepKind = "assistant" // model output: text or thinking
	KindTool      StepKind = "tool"      // one tool call and its result
	// KindNote is everything else the CLI wrote on the clock: turn boundaries,
	// hooks, slash and shell commands, task notifications. Some of it arrives
	// user-role, shaped like a prompt, and is not one.
	KindNote StepKind = "note"
)

// Step is one thing that happened, in transcript order.
type Step struct {
	Seq        int // position in the transcript; the only total order that always exists
	UUID       string
	ParentUUID string

	Kind StepKind
	At   time.Time
	// HasTime is false for entry types the CLI writes without a timestamp
	// (compaction boundaries and cost snapshots, among others). Such a step is
	// placed by Seq between its neighbours, not at the zero time.
	HasTime bool

	Model  string
	Effort string
	Text   string // short preview, already truncated; never the full content

	// Usage is set on the first step of an API response only. Claude Code
	// repeats one message's usage on every content block it writes out, so a
	// reader that does not deduplicate inflates the run's tokens by ~70%.
	Usage *Usage

	Tool *Tool

	Sidechain bool   // step belongs to a subagent's own conversation
	AgentName string // agent the step ran under, when the CLI records one
}

// Usage is the token cost of one API response.
//
// The five fields are priced differently and are not addends: a cache read is
// most of the context at a fraction of the price, and a cache write is paid
// once so later turns can read it. Summing them yields a number that means
// nothing.
type Usage struct {
	Input      int // fresh input tokens
	CacheRead  int // context re-read from cache
	CacheWrite int // context written to cache for later reuse
	Output     int
	Thinking   int // part of Output, not additional to it
}

// ContextTokens is the size of the context window this response was served
// with: what the model had to read, however it was billed. It is a level that
// rises and falls, unlike Output, which is spent and gone.
func (u Usage) ContextTokens() int { return u.Input + u.CacheRead + u.CacheWrite }

// Compact is a context window compaction: the discontinuity that matters most
// on the timeline. Claude Code writes it without a timestamp, so Seq is how it
// is placed.
type Compact struct {
	Seq      int
	Trigger  string // "auto" or "manual"
	Pre      int    // context tokens before
	Post     int    // context tokens after
	Dropped  int    // cumulative tokens dropped over the run, not this event's
	Duration time.Duration
	At       time.Time
	HasTime  bool
}

// Outcome is how a tool call ended. A call with no result did not take zero
// time, and the distinction is the point: OutcomeUnpaired and OutcomeInterrupted
// are rendered as such rather than as a fast success.
type Outcome string

const (
	OutcomeOK          Outcome = "ok"
	OutcomeError       Outcome = "error"
	OutcomeInterrupted Outcome = "interrupted"
	OutcomeDenied      Outcome = "denied"
	OutcomeUnpaired    Outcome = "unpaired" // no result in the transcript at all
)

// Tool is one tool call paired with its result.
type Tool struct {
	ID    string
	Name  string
	Brief string // one line describing the call, from its arguments

	Outcome Outcome
	Detail  string // why, when Outcome is not OK

	Started time.Time
	Ended   time.Time
	// HasDuration is false when the call has no result, or when either side
	// carries no timestamp. Duration is meaningless then, and is not drawn.
	HasDuration bool
	Duration    time.Duration

	ResultBytes int // size of the result as the transcript stored it
	MCPServer   string
}

// Stated is what the transcript claims for the whole run.
type Stated struct {
	CostUSD      float64
	TotalDur     time.Duration // wall clock the CLI attributes to the session
	APIDur       time.Duration
	ToolDur      time.Duration
	LinesAdded   int
	LinesRemoved int
	ByModel      map[string]Usage
	// UnknownModelCost is the CLI saying its own cost figure is incomplete.
	UnknownModelCost bool
}
