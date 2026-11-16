package claudecode

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dongss/agent-trace/internal/event"
	"github.com/Dongss/agent-trace/internal/textfmt"
)

// A subagent's conversation is not in the session's transcript. Claude Code
// writes it to a file of its own in a directory named for the session, beside
// the session's file:
//
//	<cwd-slug>/<session-uuid>.jsonl
//	<cwd-slug>/<session-uuid>/subagents/agent-<agentId>.jsonl
//	<cwd-slug>/<session-uuid>/subagents/agent-<agentId>.meta.json
//
// Every entry in it carries isSidechain, the agentId and the session's own
// sessionId; the session links to it only through the spawning call's result
// (toolUseResult.agentId), on an Agent call or on a Skill that forks one.
// Apart from the one entry a fork copies (see readSubagents), no message id or
// tool call id appeared in both a session and its subagents, so adding them up
// counts each response once. A subagent's own subagents (spawnDepth 2, naming
// a parentAgentId) are written to the same directory, and read the same way.
// Left out, a subagent's tokens simply vanish from the session:
// in one surveyed session its three subagents put 15.2M tokens through the
// model against the session's own 7.6M.

// File is one transcript on disk, as a listing needs it: enough to notice
// that it has changed.
type File struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// subagentDir is where the transcripts of the agents a session spawned live.
func subagentDir(sessionPath string) string {
	return filepath.Join(strings.TrimSuffix(sessionPath, ".jsonl"), "subagents")
}

// subagentFiles lists a session's subagent transcripts in name order. A
// session that spawned none has no directory, which is the common case and not
// an error.
func subagentFiles(sessionPath string) []File {
	dir := subagentDir(sessionPath)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []File
	for _, d := range ents {
		name := d.Name()
		if d.IsDir() || !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		info, err := d.Info()
		if err != nil {
			continue
		}
		out = append(out, File{Path: filepath.Join(dir, name), Size: info.Size(), ModTime: info.ModTime()})
	}
	return out
}

// agentID is the agentId a subagent's file is named for.
func agentID(path string) string {
	return strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "agent-"), ".jsonl")
}

// sidecar is the small file Claude Code writes beside a subagent's
// transcript. It names the agent and its task, which the transcript itself
// never does; it also carries fields this reader has no use for (toolUseId,
// spawnDepth, requestShape) and, for a forked skill, the skill's name.
type sidecar struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
}

func readSidecar(transcript string) sidecar {
	var s sidecar
	b, err := os.ReadFile(strings.TrimSuffix(transcript, ".jsonl") + ".meta.json")
	if err == nil {
		_ = json.Unmarshal(b, &s) // a sidecar that does not parse names nothing
	}
	return s
}

// readSubagents merges the session's subagent transcripts into run.
//
// Each is parsed on its own, as a sidechain: the pairing of tool calls with
// their results and the deduplication of usage are per file, and none of a
// subagent's user-role entries is a prompt. Its steps then join the session's
// with Seq moved past everything already read, so Seq stays unique across the
// session; Step.Agent says whose file order it is.
//
// A forked subagent (agentType "fork", surveyed on 2.1.307–2.1.310) starts
// from the session's context, and its file opens with a copy of the entry
// whose Agent call spawned it: the same message id, usage and tool_use id as
// in the session's file, under a uuid and a time of its own. All 9 forks
// surveyed did — four of them from one response, four from another — and so
// 9 of the session's responses and Agent calls were counted twice. An
// assistant entry whose message id an earlier file carried is skipped: seen
// holds the session's ids, and each subagent's are added once it is read.
//
// A subagent's compactions are its own context being cut, not the session's,
// and are counted as skipped rather than put among the session's. None was
// surveyed.
func readSubagents(run *event.Run, sessionPath string, opt Options, seen map[string]bool) error {
	files := subagentFiles(sessionPath)
	if len(files) == 0 {
		return nil
	}
	base := maxSeq(run)
	for _, f := range files {
		sub, ids, err := readSubagent(f.Path, opt, seen)
		if errors.Is(err, fs.ErrNotExist) {
			continue // gone between listing the directory and opening it
		}
		if err != nil {
			return err
		}
		for id := range ids {
			seen[id] = true
		}
		id := agentID(f.Path)
		side := readSidecar(f.Path)
		run.Subagents = append(run.Subagents, event.Subagent{
			ID:          id,
			Type:        side.AgentType,
			Description: textfmt.Clip(side.Description, opt.PreviewRunes),
			Path:        textfmt.Path(f.Path),
		})
		for _, st := range sub.Steps {
			st.Seq += base
			st.Agent = id
			st.Sidechain = true
			run.Steps = append(run.Steps, st)
		}
		base += maxSeq(sub)

		run.Malformed += sub.Malformed
		run.Truncated = run.Truncated || sub.Truncated
		for k, n := range sub.Skipped {
			run.Skipped[k] += n
		}
		if n := len(sub.Compacts); n > 0 {
			run.Skipped["compact_boundary(subagent)"] += n
		}
	}
	span(run)
	return nil
}

// readSubagent parses one subagent's file, skipping what earlier files said,
// and returns the message ids it carried.
func readSubagent(path string, opt Options, earlier map[string]bool) (*event.Run, map[string]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	sub := &event.Run{Skipped: map[string]int{}}
	p := newParser(sub, opt, earlier)
	p.sidechain = true
	if err := p.read(f, path); err != nil {
		return nil, nil, err
	}
	p.finish()
	return sub, p.messages, nil
}

// maxSeq is the highest Seq anything in run was given, which is at least the
// line number of the last line that produced a step or a compaction.
func maxSeq(run *event.Run) int {
	n := 0
	for i := range run.Steps {
		n = max(n, run.Steps[i].Seq)
	}
	for i := range run.Compacts {
		n = max(n, run.Compacts[i].Seq)
	}
	return n
}
