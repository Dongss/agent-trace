// Command agtrace turns an agent CLI's own session transcripts into a visual
// timeline of tool calls, context changes and token spend, and serves that
// timeline in a browser. Running it is the whole interface: there are no
// subcommands, only the address to listen on.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
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
  agtrace update

flags:
  -h, --help          help for agtrace
      --host string   interface to listen on (default "127.0.0.1", 0.0.0.0 to share)
  -p, --port int      port to listen on (default 7391)
  -v, --version       version for agtrace

commands:
  update              replace this binary with the latest release

The page lists the sessions found on this machine; open one for its timeline.
`
}

func main() {
	fs := flag.NewFlagSet("agtrace", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage()) }
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
	// when nothing else claims the name, printing fs.Usage and returning
	// ErrHelp. Registering them would take that over and print it twice.
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}
	if showVersion {
		fmt.Println("agtrace", version.String())
		return
	}
	// `update` is the one word agtrace takes, and it is maintenance rather
	// than a view of anything: serving the timeline stays the whole interface.
	// Anything else is refused rather than swallowed, so somebody who typed a
	// command this program never had does not get a listing instead.
	if fs.NArg() > 0 {
		if fs.Arg(0) != "update" {
			fmt.Fprintf(os.Stderr, "agtrace: unexpected argument %q\n\n%s", fs.Arg(0), usage())
			os.Exit(2)
		}
		if fs.NArg() > 1 {
			fmt.Fprintf(os.Stderr, "agtrace: update takes no arguments, got %q\n\n%s", fs.Arg(1), usage())
			os.Exit(2)
		}
		if err := update(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "agtrace:", err)
			os.Exit(1)
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
