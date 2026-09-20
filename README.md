# agent-trace

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
agtrace
# or
agtrace --host 0.0.0.0 --port 7391
```

`agtrace --help` lists the flags; `agtrace --version` prints the build.

## Updating

```sh
agtrace update
```

Downloads the latest release for this platform, checks it against the published
SHA-256 sums, and swaps it in place. Nothing on disk changes until the download
has been verified, so a failure leaves the version you have exactly as it was.

## Development

```sh
go test ./...        # passes with no Claude Code and no account present
go test -race ./...
go vet ./...

scripts/build.sh     # -> ./agtrace, stamped from the git tag
```

A release is a tag: `git tag v0.1.0 && git push origin v0.1.0`.
[`.github/workflows/release.yml`](.github/workflows/release.yml) runs the tests
on macOS, Linux and Windows and then lets GoReleaser build and publish the
archives. `goreleaser release --snapshot --clean` tries it without tagging.

Tests cover constructed transcript shapes, plus the real ones on this machine
when there are any — so a CLI format change fails loudly.

[AGENTS.md](AGENTS.md) has the layout and the invariants — most of them traps
found by reading real transcripts, with the measurements that justify them.

## License

Apache 2.0. See [LICENSE](LICENSE).
