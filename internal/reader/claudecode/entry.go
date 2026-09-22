package claudecode

import "encoding/json"

// The wire types below mirror the JSONL Claude Code writes to
// ~/.claude/projects/<cwd-slug>/<session-uuid>.jsonl. They are deliberately
// partial: the surveyed sessions carry fifteen top-level entry types and a new
// release will add more, so anything not modelled here is counted and skipped
// rather than treated as an error.
//
// Field-level notes worth keeping next to the struct tags:
//
//   - parentUuid is null on the first entry of a conversation and on a
//     compaction boundary. JSON null into a string is a no-op, so "" means
//     absent.
//   - timestamp is absent on nine of the fifteen types, cost-state and
//     compact_boundary among them. Such entries are placed by file order.
//   - toolUseResult is polymorphic: an object for Bash and Read, an array for
//     most MCP tools, a bare string for some error paths. It stays raw and only
//     its size and a few known fields are read.
type entry struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`

	UUID              string `json:"uuid"`
	ParentUUID        string `json:"parentUuid"`
	LogicalParentUUID string `json:"logicalParentUuid"`
	SessionID         string `json:"sessionId"`
	Timestamp         string `json:"timestamp"`
	CWD               string `json:"cwd"`
	GitBranch         string `json:"gitBranch"`
	Version           string `json:"version"`
	Entrypoint        string `json:"entrypoint"`
	RequestID         string `json:"requestId"`
	Effort            string `json:"effort"`

	IsSidechain bool   `json:"isSidechain"`
	AgentName   string `json:"agentName"`
	IsMeta      bool   `json:"isMeta"`

	Message *message `json:"message"`

	// Tool result side.
	ToolUseResult           json.RawMessage `json:"toolUseResult"`
	SourceToolAssistantUUID string          `json:"sourceToolAssistantUUID"`
	SourceToolUseID         string          `json:"sourceToolUseID"`
	ToolDenialKind          string          `json:"toolDenialKind"`

	// system entries.
	Content         json.RawMessage  `json:"content"`
	Level           string           `json:"level"`
	DurationMs      int64            `json:"durationMs"`
	MessageCount    int              `json:"messageCount"`
	CompactMetadata *compactMetadata `json:"compactMetadata"`

	// cost-state entries: a snapshot rewritten as the session goes, so the
	// last one in the file is the one that counts.
	TotalCostUSD        float64               `json:"totalCostUSD"`
	TotalAPIDuration    int64                 `json:"totalAPIDuration"`
	TotalToolDuration   int64                 `json:"totalToolDuration"`
	TotalDuration       int64                 `json:"totalDuration"`
	TotalLinesAdded     int                   `json:"totalLinesAdded"`
	TotalLinesRemoved   int                   `json:"totalLinesRemoved"`
	ModelUsage          map[string]modelUsage `json:"modelUsage"`
	HasUnknownModelCost bool                  `json:"hasUnknownModelCost"`
}

type message struct {
	ID         string          `json:"id"`
	Role       string          `json:"role"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Content    json.RawMessage `json:"content"` // string, or []block
	Usage      *usage          `json:"usage"`
}

type block struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`

	// tool_use
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`

	// tool_result
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// usage is the token cost of one API response. Claude Code repeats it verbatim
// on every content block of the same message, which is why the parser keys
// dedup on message.id.
//
// The iterations array is deliberately not read: it holds the per-API-call
// breakdown of a message whose totals are already in the fields above, and
// adding it to them would double count.
type usage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	OutputTokensDetails      struct {
		ThinkingTokens int `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

type modelUsage struct {
	InputTokens              int     `json:"inputTokens"`
	OutputTokens             int     `json:"outputTokens"`
	ThinkingTokens           int     `json:"thinkingTokens"`
	CacheReadInputTokens     int     `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
	CostUSD                  float64 `json:"costUSD"`
}

type compactMetadata struct {
	Trigger                 string `json:"trigger"`
	PreTokens               int    `json:"preTokens"`
	PostTokens              int    `json:"postTokens"`
	CumulativeDroppedTokens int    `json:"cumulativeDroppedTokens"`
	DurationMs              int64  `json:"durationMs"`
}

// toolResultMeta is the handful of fields worth reading out of the polymorphic
// toolUseResult object. Absent fields decode to their zero value, which is the
// right answer here: a result that does not say it was interrupted was not.
type toolResultMeta struct {
	Interrupted bool   `json:"interrupted"`
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	IsImage     bool   `json:"isImage"`
}
