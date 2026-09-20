# agent-trace

[![license](https://img.shields.io/github/license/Dongss/agent-trace)](LICENSE)
[![version](https://img.shields.io/github/v/release/Dongss/agent-trace?label=version)](https://github.com/Dongss/agent-trace/releases/latest)
[![CI](https://github.com/Dongss/agent-trace/actions/workflows/ci.yml/badge.svg)](https://github.com/Dongss/agent-trace/actions/workflows/ci.yml)
![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/Dongss/agent-trace/badges/coverage.json)

A local dashboard for agent CLI sessions.

![screenshot-claude-code](/docs/assets/screenshot-claude-code.png)

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

## License

Apache 2.0. See [LICENSE](LICENSE).
