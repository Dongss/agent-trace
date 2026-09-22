# agent-trace

[![license](https://img.shields.io/github/license/Dongss/agent-trace)](LICENSE)
[![version](https://img.shields.io/github/v/release/Dongss/agent-trace?label=version)](https://github.com/Dongss/agent-trace/releases/latest)
[![CI](https://github.com/Dongss/agent-trace/actions/workflows/ci.yml/badge.svg)](https://github.com/Dongss/agent-trace/actions/workflows/ci.yml)
![coverage](https://img.shields.io/endpoint?url=https://gist.githubusercontent.com/Dongss/c7ef84f8a44585c1b108650219b85664/raw/agent-trace-coverage.json)

A local dashboard for agent CLI sessions.

**Nothing leaves your machine.** Transcripts are read from disk, the pages are
served locally.

## Supported agents

- [x] Claude Code

## Installation

macOS / Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/Dongss/agent-trace/main/scripts/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/Dongss/agent-trace/main/scripts/install.ps1 | iex
```

**Windows is experimental.**

## Quick start

```sh
# quick start
agtrace

# update agtrace to latest version
agtrace update

# help
agtrace --help
```

The UI is served at `http://127.0.0.1:7391`

![screenshot-claude-code](/docs/assets/screenshot-claude-code.png)

## Development

```sh
go test ./...        # passes with no Claude Code and no account present
go test -race ./...
go vet ./...

scripts/build.sh     # -> ./agtrace, stamped from the git tag
```

Tests cover constructed transcript shapes, plus the real ones on this machine
when there are any — so a CLI format change fails loudly.

[AGENTS.md](AGENTS.md) has the layout and the invariants — most of them traps
found by reading real transcripts, with the measurements that justify them.

## Disclaimer

`agent-trace` reads the session transcripts that agent CLI software writes on your own machine. It does not include, redistribute or modify any agent CLI, and each must be installed and used subject to the respective vendor's terms of service. All product names and trademarks are the property of their respective owners.

## License

[Apache License 2.0](LICENSE)
