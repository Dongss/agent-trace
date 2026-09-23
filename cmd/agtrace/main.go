// Command agtrace turns an agent CLI's own session transcripts into a visual
// timeline of tool calls, context changes and token spend, and serves that
// timeline in a browser. Running it is the whole interface: an address to
// listen on, and three words that serve nothing — help, version, update.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/Dongss/agent-trace/internal/agent"
	"github.com/Dongss/agent-trace/internal/event"
	"github.com/Dongss/agent-trace/internal/textfmt"
	"github.com/Dongss/agent-trace/internal/version"
)

// usage is written out rather than left to flag.PrintDefaults: that prints one
// leading dash, and has no way to show a short spelling and a long one as the
// single flag they are. Both spellings of each parse — the flag package treats
// -port and --port alike, and a short alias is the same variable registered
// twice — so the help is the only place the pairing is stated.
func usage() string {
	return "agtrace " + version.String() + `

A local dashboard for agent CLI sessions

usage:
  agtrace [flags]
  agtrace <command>

flags:
  -h, --help          help for agtrace
      --host string   interface to listen on (default "127.0.0.1")
  -p, --port int      port to listen on (default 7391)
  -v, --version       version for agtrace

commands:
  help                help for agtrace
  update              update to the latest release
  version             version for agtrace

The page lists the sessions found on this machine; open one for its timeline.
`
}

// commands are the words agtrace takes, in the order the help lists them.
// Parsing reads this; the help spells them out again because the alignment is
// part of the text. A test holds the two together — a command that works and
// is not in the help, or a line in the help that is refused, is the pair that
// drifts.
var commands = []string{"help", "update", "version"}

func main() {
	fs := flag.NewFlagSet("agtrace", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	// Silent, and the help is printed after Parse instead: the flag package
	// calls Usage both for -h and beside a bad flag, and the two want
	// different streams.
	fs.Usage = func() {}
	var (
		host        string
		port        int
		showVersion bool
	)
	fs.StringVar(&host, "host", "127.0.0.1", "interface to listen on")
	fs.IntVar(&port, "port", 7391, "port to listen on")
	fs.IntVar(&port, "p", 7391, "port to listen on")
	fs.BoolVar(&showVersion, "version", false, "print the version and exit")
	fs.BoolVar(&showVersion, "v", false, "print the version and exit")
	// -h and --help are not registered: the flag package handles them itself
	// when nothing else claims the name and return ErrHelp.
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Print(usage())
			return
		}
		// The flag package has already named the bad flag on stderr.
		fmt.Fprint(os.Stderr, usage())
		os.Exit(2)
	}
	if showVersion {
		fmt.Println("agtrace", version.String())
		return
	}
	// The three words agtrace takes. None of them serves anything: help and
	// version describe the binary, update replaces it, and showing the
	// timeline stays the whole interface. `help` and `version` are here as
	// well as being flags because both spellings are the first thing somebody
	// tries. Anything else is refused rather than swallowed, so somebody who
	// typed a command this program never had does not get a listing instead.
	if fs.NArg() > 0 {
		cmd := fs.Arg(0)
		if !slices.Contains(commands, cmd) {
			fmt.Fprintf(os.Stderr, "agtrace: unexpected argument %q\n\n%s", cmd, usage())
			os.Exit(2)
		}
		if fs.NArg() > 1 {
			fmt.Fprintf(os.Stderr, "agtrace: %s takes no arguments, got %q\n\n%s", cmd, fs.Arg(1), usage())
			os.Exit(2)
		}
		switch cmd {
		case "help":
			// Asked for, so it goes to stdout where it can be piped or paged,
			// the same as -h. The same text printed beside an error is a
			// diagnostic and stays on stderr.
			fmt.Print(usage())
		case "version":
			fmt.Println("agtrace", version.String())
		case "update":
			if err := update(os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, "agtrace:", err)
				os.Exit(1)
			}
		default:
			// The list above accepted a word this switch cannot run.
			fmt.Fprintf(os.Stderr, "agtrace: %s is not implemented\n", cmd)
			os.Exit(2)
		}
		return
	}
	if port < 0 || port > 65535 {
		fmt.Fprintf(os.Stderr, "agtrace: port %d is out of range\n", port)
		os.Exit(2)
	}

	if err := serve(host, port); err != nil {
		fmt.Fprintln(os.Stderr, "agtrace:", err)
		os.Exit(1)
	}
}

func loadRun(a agent.Agent, ref string) (*event.Run, error) {
	run, err := a.Load(ref)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no %s session matching %q under %s",
				a.Name, ref, textfmt.Path(a.Root))
		}
		return nil, err
	}
	return run, nil
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

// compactInt is the short form a scanning eye wants in a table: 1.4B, not
// 1,357,365,806. The exact figure is in the cell's tooltip.
func compactInt(n int) string {
	f := float64(n)
	switch {
	case n >= 1_000_000_000:
		return trimZero(f/1e9) + "B"
	case n >= 1_000_000:
		return trimZero(f/1e6) + "M"
	case n >= 10_000:
		return trimZero(f/1e3) + "k"
	}
	return fmt.Sprintf("%d", n)
}

func trimZero(f float64) string {
	if f >= 100 {
		return fmt.Sprintf("%.0f", f)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", f), ".0")
}
