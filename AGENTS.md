# Working on agent-trace

`agtrace` turns an agent CLI's own session transcript into a visual timeline:
every token spent, every tool call, every skill invoked. It reads transcripts
that already exist on the machine — nothing is instrumented and no agent is
modified — normalises them into one event model, and serves that model as a
self-contained HTML page. Running `agtrace` is the whole interface: `--host`,
`--port`, `--version`, `--help`, and `update`.

```sh
go test ./...          # must pass with no agent CLI and no account present
go test -race ./...
go build -o agtrace ./cmd/agtrace
```

Claude Code is the only reader so far. Codex, Cursor and Qwen Code have been
surveyed but not implemented.

## Layout

```
cmd/agtrace/                the command line, the local server, the updater
internal/selfupdate/        replacing this binary with the latest release
internal/agent/             which agent CLIs exist, and which of them can be read
internal/reader/claudecode/ the JSONL parser, the listing scan, session discovery
internal/event/             the CLI-neutral middle: Run, Step, Usage, Tool, Compact
internal/timeline/          totals, a stable order, the gap-compressing clock
internal/render/            the page: view model in Go, chart drawing in page.html
internal/textfmt/           shortening paths and previews for display
```

No reader imports a renderer and no renderer imports a reader; everything meets
at `event`. A reader's only job is to turn one vendor's bytes into `event`
values and to be honest about what that vendor does not record.

## Reading a transcript

Traps, measured on real transcripts written by Claude Code 2.1.231–2.1.278.
Code that ignores one produces a timeline that looks right and is wrong.

- **Read-only, always.** A transcript is the user's own record of their work.
  Never write, move or "repair" one, and never hold a lock that would block the
  CLI still writing it. One being appended to right now must parse: the last
  line can be half-written, and that is `Truncated`, not corruption.

- **One API response is written as several entries, each repeating the same
  usage.** One entry per content block — thinking, text, each tool_use — with
  the response's `usage` copied onto all of them. 4,558 assistant entries for
  2,670 responses in one session; adding usage up per entry overstated its
  tokens by 70%. Usage attaches to the first entry of a `message.id` and is
  dropped on the rest. `usage.iterations` is deliberately not read: it breaks
  down totals that are already in the fields beside it.

- **Timestamps are not monotonic** — 248 out-of-order pairs in one session. A
  result that predates its call yields *no* duration rather than a negative
  one. And "by time, else by file order" is **not a valid ordering**: with
  A(t=5,seq=1), B(t=1,seq=2), C(untimed,seq=3), D(t=3,seq=4) it asserts both
  A<C and C<D<A, and `sort.Slice` given a cycle returns an arbitrary
  permutation. `timeline.Order` gives every step an effective time first, then
  sorts by (time, seq).

- **A compaction boundary may carry no timestamp**, and it is the most
  important entry in the file. Every one of the 17 surveyed did carry one, so
  `PlaceBySeq` is a fallback rather than the usual path — but it is the
  fallback that matters, because dropping the boundary for want of a timestamp
  would lose the run's largest event. `Compact.HasTime` says which case a given
  one is.

- **Nine entry types carry no timestamp at all**, `cost-state` among them. File
  order is the only total order that always exists, which is what `Step.Seq`
  records.

- **A tool call is a pair, and an unpaired one is information.** `tool_use`
  pairs with a later `tool_result` by id. A call with no result did not take
  zero time — the run ended, the user interrupted, or it was denied
  (`toolDenialKind`: `user-rejected`, `permission-rule`, `automode-blocked`,
  `automode-unavailable`). `HasDuration` is false and the mark is drawn with no
  width.

- **Call-to-result is not execution time.** The pair measures wall clock,
  including a permission prompt waiting for a human: 2h56m call-to-result
  against the CLI's stated 59m24s of execution, same session.

- **`toolUseResult` is polymorphic** — an object for Bash and Read, an array
  for most MCP tools, a bare string on some error paths. It stays
  `json.RawMessage`; a struct-typed decode dropped two thirds of the tool calls
  in a browser-heavy session.

- **An unknown entry type is not an error.** Fifteen top-level types surveyed,
  most irrelevant, and a release will add more. Skips are counted, not
  complained about; only a malformed entry of a modelled type is worth a
  complaint.

- **A skill's name is in the call, not in the tool name.** A skill invocation
  is a `Skill` tool call whose arguments carry the name, so counting by tool
  name reports "Skill ×20" and never says which.

- **`ai-title` is the title; `custom-title` is not a rename.** `customTitle` is
  byte-identical to `agentName` wherever both appear, and the two appear and
  vanish together, so it reads as the agent's label rather than something
  somebody typed. Don't claim it is a user rename without a transcript that
  separates the two. `Run.Title` is `ai-title` alone, with no fallback — a
  field that falls back shows the same value twice wherever both are displayed.

- **`entrypoint` says what drove the session, and it can change mid-file.**
  `cli`, `sdk-cli`, `claude-vscode` on the survey machine; the set is open, so
  the name is carried through unmapped — a guess at a surface this build has
  never seen would read as fact. Versions are grouped per entrypoint in
  `event.Surface`, not pooled, because an editor extension bundles its own CLI:
  one surveyed session went `cli` 2.1.270 → `claude-vscode` 2.1.263, which a
  single range reports as the run going backwards. `Run.Versions` stays the
  flat union beside it.

- **Sidechains are modelled but unverified.** `Step.Sidechain` and `AgentName`
  are parsed, but no surveyed transcript contained a subagent (zero
  `isSidechain` and zero `Task` calls), so the nesting is untested. `agentName`
  there is the session's own agent, not a subagent's.

- **Transcript shapes drift with releases**, and a real recording cannot be
  committed here — it is the user's prompts, file contents and shell history.
  The tests carry constructed transcripts reproducing surveyed shapes, plus
  `TestRealTranscripts`, which reads whatever the machine has and skips itself
  when there is nothing. When a release changes a shape, re-survey and update
  the literals; do not relax the parser until the old ones still pass.

## Counting

- **The four token fields are not addends, as a price.** `input`,
  `cache_creation`, `cache_read` and `output` are billed differently. Their sum
  is still a *volume*, which is what the Total tokens tile shows and what the
  charts stack — a session can total 1.4B tokens and cost $771, or 2.9B and
  cost $431. `thinking_tokens` is *part of* output; stacking the two double
  counts.

- **Context size is a level; tokens spent is a flow.** The window a response
  was served with rises and falls; what was spent is gone. They never share an
  axis. Every series on the page today is a flow, which is why every field of
  `bin` is a column sum. Putting a level back needs its own chart and its own
  aggregate — and that aggregate is a real member, never an average: an
  averaged composition is a request nobody made.

- **Tokens are recomputed; cost never is.** A `cost-state` snapshot exists in
  roughly a third of sessions — none before 2.1.258, none from `sdk-cli` at any
  release — so a listing taking token counts from there would leave most of its
  rows empty, hence `Scan`. Cost needs a per-model price table that is not in
  the transcript and would go stale here, so it is shown only where the
  transcript states it.

- **Stated and recomputed figures are never reconciled by editing one.** The
  snapshot names models the messages never mention and spells the main one
  differently (`claude-opus-5[1m]` against `claude-opus-5`), so summing
  messages cannot reproduce it. A trailing bracketed marker is stripped only to
  decide whether a stated model is one the transcript already showed; nothing
  is merged. The snapshot is also periodic and lags — mid-run it showed $0.68
  and 2,890 output tokens against 61,710 recomputed.

- **Share is of output, not of the total.** Ranking models by the grand total
  puts whichever re-read the most context first and makes every model in a long
  session look the same size.

- **Absent is not zero.** Claude Code never states the model's context window
  size, so "percent of context used" cannot be computed and must not be
  invented from a constant. Where a reader has no value the model carries a nil
  pointer or a `Has*` flag and the renderer leaves a gap. The same rule sorts
  and filters the listing: a missing figure is not a zero.

- **`Scan` must agree with the full parser, and a test says so.** It is a
  second implementation of the same dedup, which is how two answers to one
  question start drifting. It exists because it is 397ms against 1.8s over
  221 MB: a line only reaches the JSON decoder when a substring check says it
  could matter. The server caches scans by path, size and mtime — 1.9s for the
  first index load of 911 MB, 45ms after.

## The clock

- **A window recomputes; it never rescales.** `timeline.Window` filters the
  steps and everything downstream is computed from what is left. Two things
  deliberately do not survive it. `Stated` is dropped, because the transcript
  states cost and durations for the whole session. And compactions are placed
  from the *original* run, because they carry no timestamp and are positioned
  between neighbours the window is about to remove. A step is in or out by its
  own timestamp, tool calls included.

- **Days are the offered unit, in the reader's zone.** `ActiveDays` buckets by
  local calendar day, bounded by its first and last timestamp rather than
  midnight to midnight, so selecting one cannot pull in a neighbour through a
  rounding edge.

- **Everything converts to the reader's zone.** The transcript stores UTC and
  file mtimes are local; showing one of each on the same screen is a real bug
  the listing invites, with Started and Last touched in adjacent columns.

## The page

- **The page makes no network requests, and carries everything.** No CDN, no
  fonts, no analytics — enforced by a test that allows exactly one outbound
  URL, the repository. A transcript holds prompts, replies, command lines and
  paths, and the page carries all of it, so a copy saved out of the browser
  travels with all of it. There is no redaction setting; `internal/textfmt`
  does layout, not privacy.

- **Render nothing that fills nothing.** An area chart between adjacent columns
  draws an empty path when a column stands alone — an early version silently
  dropped every isolated response that way. Isolated samples are bars. Where
  layout depends on how text renders, measure it: axis labels are thinned by
  `getBBox()` after the SVG is in the document, because a fixed gap guessed the
  font width wrong in both directions.

- **Three categorical colours, and status colours are reserved.** The palette's
  first three slots clear the colourblind and normal-vision gates across all
  pairs in both modes; a fourth cannot, in any ordering. A single-series chart
  gets the ink token rather than spending a slot, and tool outcomes use the
  status palette with labels beside them, never colour alone. The token stacks
  need a fourth band and use that ink token. Run the validator rather than
  reasoning about it, and re-run it for dark. One WARN stands: `--s-in` is
  2.74:1 on the light surface, below the 3:1 floor, which obliges visible
  labels — the legend and the tooltip carry it.

- **A legend entry is a filter, and a filtered chart rescales.** The y axis is
  computed from what is showing, because a band that is 1% of a stack pinned to
  the old axis answers nothing. A series keeps its own colour whatever is
  hidden: colour follows the entity, never its rank on screen. Each chart has
  one definition of its series, shared by legend, marks and tooltip.

- **Every session renders the same sections, in the same order, in one form
  each.** A block that disappears with its data reads as a rendering fault
  rather than as an answer, so an empty list, table or lane renders and says so
  in words. A chart that changes shape between sessions cannot be compared
  across them, which is why the tool lane is counts per slice at every density.

- **Storage can throw.** Every access to `localStorage` is wrapped: it throws
  in a private window and the page still has to render. The theme cycles
  system → light → dark, because a two-state toggle cannot return to the OS
  setting.

- **The header chrome is one design in two files, and nothing enforces it.**
  The listing's lives in `serve.go`, the session page's in `page.html`. The
  theme toggle must be in the same corner on both, and the `.colophon` line at
  the foot is written twice. Change one, change the other.

## The listing

- **A sortable column sorts the value, not the cell.** `1.4B`, `$771.71`,
  `117.3M` are rendered for reading; ordering that text puts 1.4B below 336k.
  Rows carry the raw numbers in `data-n` beside the search fields in `data-s`.
  A missing value sorts last in *both* directions — most sessions state no
  cost, so an em dash read as zero buries every row that has one. Ties keep the
  server's order.

- **The filter and the order are one view, and it survives the round trip.**
  Matching is subsequence-in-order per word, the way a file finder works, so
  `srvhub` finds `service-hub`; a substring always counts. `?q=`, `?sort=`,
  `?dir=` and `?empty=` go on the address bar and on every session link through
  one writer, and the session page's back link is built from them by name
  rather than from a parameter per control — back means back to the rows you
  were looking at, in the order you were reading them.

- **Only agents with a reader are listed.** `internal/agent.List` filters to
  entries with both a `discover` and a `load`. `Status()` separates "no reader"
  from "reader, but this CLI was never run here". The survey is worth having
  when a second reader gets written: Codex keeps
  `~/.codex/sessions/<yyyy>/<mm>/<dd>/rollout-*.jsonl`, typed `session_meta` /
  `event_msg` / `response_item` / `turn_context`, tool calls as
  `custom_tool_call`, and tokens in a `token_count` event carrying a running
  total, the last turn, and `model_context_window` — which Claude Code never
  states. Cursor keeps per-project directories under `~/.cursor` with no
  per-session transcript identified; Qwen Code under `~/.qwen` was not
  surveyed.

- **Versions sort numerically per component.** A string sort puts 2.1.98 after
  2.1.231, and a "2.1.231 → 2.1.274" range comes out backwards.

## Shipping

- **The version is stamped, not hard-coded twice.** `internal/version` holds
  `Version`; a release overrides it with `-X …/internal/version.Version=<tag>`.
  It defaults to the release being worked toward rather than "", because an
  empty default reports a Go pseudo-version. A modified tree gets `-dev`, so
  only a clean tag calls itself the release.

- **One archive name, written out in four places.** `.goreleaser.yaml` names
  the assets `agtrace_<tag>_<os>_<arch>.tar.gz` (`.zip` on Windows), and
  `scripts/install.sh`, `scripts/install.ps1` and `internal/selfupdate` each
  rebuild that string. Nothing checks that they agree. Same for
  `checksums.txt`: all three parse `<sha256>  <name>`, stripping the leading
  `*` that binary-mode hashing adds.

- **The updater fails closed where the installer warns.** A missing or
  mismatched checksum is fatal in `internal/selfupdate` and a warning in the
  install scripts: the installer creates a file that did not exist, `update`
  overwrites a binary the user already trusts. Nothing on disk is touched
  before the download is verified, and the swap is a rename inside the
  destination directory. Windows cannot rename over a running executable, so
  `replace_windows.go` moves the old one aside and puts it back if the second
  rename fails.

- **The coverage badge job is never fatal.** It writes a gist, which needs a
  PAT with `gist` scope in `GIST_TOKEN` because the built-in `GITHUB_TOKEN`
  cannot write one. That secret can be missing, absent on a fork or
  rate-limited, so the job exits 0 on all of those: a stale badge must not turn
  main red.

## Tests

- **The suite must pass with no agent CLI and no account present.** That is
  what a fresh runner is, and what somebody cloning this gets.
  `TestRealTranscripts` and the scan benchmark read whatever the machine has
  and skip themselves when there is nothing, which is why the runner's
  coverage figure is lower than a developer's — and the runner's is the one
  worth publishing, being the one anybody can reproduce.

- **The handlers are tested without a socket.** `newMux` exists apart from
  `serve` for that: it takes an agent whose `Root` a test points at a
  directory of its own making.

## Conventions

- Comments say *why*, not what. The reader can see what the code does.
- An error a user can act on names the thing to change: the file, the flag, the
  session id, the command to run.
- Say what a number is. A timeline of tokens and costs invites a reader to
  trust it, so a figure that is estimated, recomputed or partial is labelled
  where it is shown.
- Prefer showing less to showing something plausible. A gap prompts a question;
  a fabricated value ends one.
