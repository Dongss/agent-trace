// Package agent is the list of agent CLIs agtrace can read, and the one place
// that says which one is the default.
//
// Only agents with a working reader are listed. Ones whose transcripts have
// been surveyed but not implemented are deliberately absent, so the page never
// shows an option that returns nothing; the survey notes live in AGENTS.md
// until a reader exists to attach them to.
package agent

import (
	"fmt"
	"os"

	"github.com/Dongss/agent-trace/internal/event"
	"github.com/Dongss/agent-trace/internal/reader/claudecode"
	"github.com/Dongss/agent-trace/internal/textfmt"
)

// Session is one transcript as a listing shows it. It is Claude Code's type
// today because Claude Code is the only reader; when a second one lands this
// becomes a struct of its own and the readers convert into it.
type Session = claudecode.Session

// Agent is one agent CLI.
type Agent struct {
	ID   string // stable id, used in flags and URLs
	Name string // what to call it on screen

	// Root is where its transcripts live.
	Root string
	// Transcripts describes the path pattern for a reader, in a form a person
	// can check against their own disk.
	Transcripts string

	// Implemented is whether agtrace has a reader for this agent. Everything
	// List returns is implemented; the field exists so a caller can still ask,
	// and so an entry cannot be added here by mistake without a reader.
	Implemented bool
	Why         string // when not implemented, why

	discover func(root string) ([]Session, error)
	load     func(root, ref string) (*event.Run, error)
}

// all is in the order the list shows them. Add an agent here only with its
// reader; an entry without one is filtered out of List and is a bug.
func all() []Agent {
	return []Agent{
		{
			ID:          "claude-code",
			Name:        "Claude Code",
			Root:        claudecode.DefaultRoot(),
			Transcripts: "~/.claude/projects/<cwd-slug>/<session-uuid>.jsonl",
			Implemented: true,
			discover: func(root string) ([]Session, error) {
				return claudecode.Discover(root)
			},
			load: func(root, ref string) (*event.Run, error) {
				path, err := claudecode.Locate(root, ref)
				if err != nil {
					return nil, err
				}
				return claudecode.ReadFile(path, claudecode.Options{})
			},
		},
	}
}

// List returns the agents agtrace can read.
func List() []Agent {
	var out []Agent
	for _, a := range all() {
		if a.Implemented && a.discover != nil && a.load != nil {
			out = append(out, a)
		}
	}
	return out
}

// Default is the agent to use when none was named.
func Default() Agent { return List()[0] }

// Lookup finds an agent by id.
func Lookup(id string) (Agent, error) {
	if id == "" {
		return Default(), nil
	}
	for _, a := range List() {
		if a.ID == id {
			return a, nil
		}
	}
	var names []string
	for _, a := range List() {
		names = append(names, a.ID)
	}
	return Agent{}, fmt.Errorf("unknown agent %q: readable agents are %v", id, names)
}

// HasRoot reports whether the directory this agent's transcripts live in exists.
// An implemented reader with no directory means the CLI was never run here,
// which is a different thing from having no reader.
func (a Agent) HasRoot() bool {
	if a.Root == "" {
		return false
	}
	st, err := os.Stat(a.Root)
	return err == nil && st.IsDir()
}

// Status is a one-line explanation of whether this agent can be listed, for a
// person looking at a disabled entry and wondering why.
func (a Agent) Status() string {
	switch {
	case !a.Implemented:
		return a.Why
	case !a.HasRoot():
		return "no transcripts on this machine: nothing at " + textfmt.Path(a.Root)
	default:
		return "ready"
	}
}

// Discover lists this agent's sessions, newest first.
func (a Agent) Discover() ([]Session, error) {
	if a.discover == nil {
		return nil, fmt.Errorf("%s cannot be read: %s", a.Name, a.Why)
	}
	return a.discover(a.Root)
}

// Load reads one session in full.
func (a Agent) Load(ref string) (*event.Run, error) {
	if a.load == nil {
		return nil, fmt.Errorf("%s cannot be read: %s", a.Name, a.Why)
	}
	return a.load(a.Root, ref)
}
