# Working on agent-trace

`agtrace` turns an agent CLI's own session transcript into a visual timeline:
every token spent, every tool call, every skill invoked. It reads transcripts
that already exist on the machine — nothing is instrumented and no agent is
modified — normalises them into one event model, and renders that model into a
self-contained HTML page, served locally. Running `agtrace` is the whole
interface: `--host`, `--port`, `--version`, `--help`, and `update`, which is
maintenance rather than a view of anything.

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

Most of these are traps, and every number quoted below was measured on real
transcripts written by Claude Code 2.1.231–2.1.278. Code that ignores one will
produce a timeline that looks right and is wrong.

- **Read-only, always.** A transcript is the user's own record of their work.
  Open it read-only, never write, move, truncate or "repair" one, and never hold
  a lock that would block the CLI still writing it. A transcript being appended
  to right now must parse: the last line can be half-written, and the reader
  reports that as `Truncated` rather than as corruption.

- **One API response is written as several entries, each repeating the same
  usage.** Claude Code emits one entry per content block — thinking, text,
  each tool_use — and copies the response's `usage` object onto all of them. In
  a 16,385-line session that was 4,558 assistant entries for 2,670 responses.
  Adding usage up per entry overstated the run's tokens by about 70%. Usage is
  attached to the first entry of a `message.id` and dropped on the rest; the
  `usage.iterations` array is deliberately not read, because it breaks down
  totals that are already in the fields beside it.

- **A compaction boundary has no timestamp**, which is awkward because it is the
  single most important entry in the file. It is placed by interpolating between
  the nearest timestamped entries in file order. Dropping it for want of a
  timestamp would lose the run's largest event.

- **Nine entry types carry no timestamp at all** — `cost-state`,
  `file-history-snapshot`, `mode`, `permission-mode`, `ai-title`,
  `custom-title`, `agent-name`, `last-prompt`, `atis-latch`. File order is the
  only total order that always exists, which is what `Step.Seq` records.

- **Timestamps are not monotonic** — 248 out-of-order pairs in the surveyed
  session. Two consequences. A result that predates its call yields *no*
  duration rather than a negative one. And "by time, else by file order" is
  **not a valid ordering**: with A(t=5,seq=1), B(t=1,seq=2), C(untimed,seq=3),
  D(t=3,seq=4) it asserts both A<C and C<D<A, and `sort.Slice` given a cycle
  returns an arbitrary permutation. `timeline.Order` therefore gives every step
  an effective time first, then sorts lexicographically by (time, seq).

- **A tool call is a pair, and an unpaired one is information.** The `tool_use`
  block pairs with a later `tool_result` by id; duration is the gap between
  them. A call with no result did not take zero time — the run ended, the user
  interrupted it, or it was denied (`toolDenialKind`, seen as
  `user-rejected`, `permission-rule`, `automode-blocked`,
  `automode-unavailable`). `HasDuration` is false and the mark is drawn with no
  width. A zero-duration bar for a call somebody killed is a bug report waiting
  to happen.

- **Call-to-result is not execution time.** The pair measures wall clock, which
  includes a permission prompt sitting there waiting for a human: 2h56m
  call-to-result against the CLI's own stated 59m24s of execution on the same
  session. Both are reported, labelled as what they are.

- **`toolUseResult` is polymorphic** — an object for Bash and Read, an array for
  most MCP tools, a bare string on some error paths. It stays `json.RawMessage`
  and only its size and a few known fields are read. A struct-typed decode would
  drop two thirds of the tool calls in a browser-heavy session.

- **An unknown entry type is not an error.** The surveyed sessions carry fifteen
  top-level types, most irrelevant here, and a release will add more. The reader
  ignores what it does not model and counts the skips on `event.Run`. Only a
  malformed entry of a type it *does* model is worth a complaint.

- **A skill's name is in the call, not in the tool name.** Claude Code records a
  skill invocation as a `Skill` tool call whose arguments carry the name, so
  counting by tool name reports "Skill ×20" and never says which.
  `buildSkillUse` counts the call's `Brief`, which is where the reader already
  put that argument.

- **The session title comes from `ai-title`, and `custom-title` is not a
  rename.** `aiTitle` is a model-written summary of the session and is what the
  title is. `customTitle` is byte-identical to `agentName` on every surveyed
  session where both appear, and the two appear and vanish together (15 sessions
  each), so it reads as the agent's label rather than as something somebody
  typed — one surveyed session carries a value for both that matches neither its
  working directory nor anything else in the file, which rules out the working
  directory as its source but not much else. It is kept as a fallback and ranked
  second. Do not claim it is a user rename without a transcript that separates
  the two.

- **Title and name are two fields, so neither may stand in for the other.**
  `Run.Title` is the model-written `ai-title` alone, with no fallback: a field
  that falls back shows the same value twice wherever both are displayed. A
  fallback belongs to whoever needs a heading — the session page's h1 — not to
  the field.

- **A missing title stays missing.** 29 of the 95 surveyed sessions have no
  title at all, mostly short ones where no response was ever produced to
  summarise. `Session.Title()` returns "" for those and the listing leaves the
  line blank, because the working directory is already its own column:
  repeating its last element under "Title" filled the space without adding
  anything and made a derived label indistinguishable from one the CLI wrote.

- **Sidechains are modelled but unverified.** `Step.Sidechain` and `AgentName`
  are parsed, but no transcript on the survey machine contained a subagent
  (`isSidechain` and `Task` calls: zero across all 95 transcripts on it), so the
  lane that would nest them is not built and the behaviour is untested.
  `agentName` there is the session's own agent, not a subagent's — do not read
  it as one without checking against a transcript that actually has sidechains.

- **Transcript shapes drift with CLI releases**, and a real recording cannot be
  committed here: a transcript is the user's prompts, file contents and shell
  history. So the tests carry constructed transcripts reproducing surveyed
  shapes, with the versions they were surveyed from named in a comment, plus
  `TestRealTranscripts`, which reads whatever sessions the machine actually has
  and skips itself when there are none. When a release changes a shape,
  re-survey and update the literals — do not relax the parser until the old ones
  pass.

## Counting

- **Token fields are not addends.** `input_tokens`,
  `cache_creation_input_tokens`, `cache_read_input_tokens` and `output_tokens`
  are priced differently: a cache read is most of the context at a fraction of
  the price, a cache write is paid once so later turns can read it. Summing the
  four yields a number that means nothing as a price. `thinking_tokens` is
  *part of* output, not additional to it — stacking the two would double count.

- **A total token figure is a volume, not a price.** `Totals.Total()` adds all
  four fields and says so everywhere it is shown, because the sum is dominated
  by cache reads: a session can total 1.4B tokens and cost $771, or 2.9B and
  cost $431. It answers "how much did this chew through", and nothing else.

- **Context size is a level; tokens spent is a flow.** The window a response was
  served with is `input + cache_read + cache_write`, a quantity that rises and
  falls. What was spent is gone. They do not share an axis, and a dual-axis
  chart pairing them would invent a relationship. Every series the page draws
  today is a flow, which is why every field of `bin` is a column sum. Anything
  that puts a level back needs its own chart and its own aggregate — and that
  aggregate is a real member, never an average: a column would be the single
  largest response in the slice with its own components, because an averaged
  composition is a request nobody made.

- **A running total of flows is one quantity, and may be stacked.** The four
  fields are billed differently and their sum is not money, but it is still a
  volume — which is what the Total tokens tile shows. The cumulative chart
  stacks all four over the clock and its curve ends exactly on that tile's
  figure; a test asserts the two agree, because they are computed from
  different sides. The per-slice chart is the same four numbers without the
  running sum.

- **Tokens are recomputed; cost never is.** The transcript's `cost-state`
  snapshot exists in only 24 of the 95 surveyed sessions, so a listing that took
  its token counts from there would leave three quarters of its rows empty —
  hence `Scan`, which recomputes them. Cost is the opposite: it is shown only
  where the transcript states it, because recomputing it needs a per-model price
  table that is not in the transcript and would go stale in this repository.

- **Stated and recomputed figures are never reconciled by editing one.** The
  transcript's own `cost-state` snapshot names models the assistant messages
  never mention (a short haiku model, presumably a background task) and spells
  the main one differently — `claude-opus-5[1m]` against the messages'
  `claude-opus-5` — so summing messages cannot reproduce it. Whatever displays
  both must label which is which. `run.Stated` keeps everything per model for
  whoever needs it; only the cost is on the page today.

- **`cost-state` is a periodic snapshot, so the last one wins — and it lags.**
  In a live session it can be far behind: a session mid-run showed a stated
  $0.68 and 2,890 output tokens against 61,710 recomputed. When the transcript
  is truncated, say that rather than blaming the model coverage.

- **The two sources disagree about model names as well as membership.** A
  trailing bracketed marker is a variant of the same model, so it is stripped —
  but only to decide whether a stated model is one the transcript already
  showed, and nothing is merged: each list keeps the names its own source used.
  A model only the snapshot names is listed apart, because nothing in the
  transcript can say what it did.

- **Share is of output, not of the total.** The models strip ranks the models a
  run used by output tokens and shows each one's share of them. Ranking by the
  grand total would put whichever model happened to re-read the most context
  first and report every model in a long session as about the same size: on the
  surveyed run `claude-opus-5` has 93% of the output, which is the number that
  says who did the work.

- **Absent is not zero.** Claude Code's transcript never states the model's
  context window size (checked: no such field anywhere in it), so "percent of
  context used" cannot be computed from it and must not be invented from a
  constant. Codex states `model_context_window` and could. Where a reader has no
  value, the model carries a nil pointer or a `Has*` flag and the renderer
  leaves a gap.

- **`Scan` must agree with the full parser, and a test says so.** It is a second
  implementation of the same dedup, which is exactly how two answers to one
  question start drifting apart. `TestScanMatchesFullParseAndIsFaster` runs both
  over the machine's real transcripts and compares tokens, responses and tool
  calls. It also records why the second implementation exists: 397ms against
  1.8s over the newest twenty sessions (221 MB), because a line is only handed
  to the JSON decoder when a substring check says it could matter, which is 16%
  of the bytes.

- **A listing scans a transcript once, and only when it must.** `Discover` is
  cheap metadata off the ends of each file; titles and token counts need `Scan`,
  which reads the whole thing. The server keeps those scans in memory keyed by
  path, size and mtime — 1.9s on the first index load of 911 MB, 45ms after, and
  a session that has grown is rescanned while a finished one never is.

## The clock

- **A window recomputes; it never rescales.** `timeline.Window` filters the
  steps and everything downstream is computed from what is left, so a day view
  is as exact as the full run. Two things deliberately do not survive it.
  `Stated` is dropped, because the transcript states cost and durations for the
  session and nothing in it attributes them to a slice — a windowed page shows
  no cost tile and says why. And compactions are placed from the *original* run
  before filtering: they carry no timestamp, are positioned between their
  neighbours in file order, and the window is about to remove the neighbours.
  A step is in or out by its own timestamp, tool calls included; keeping
  anything that merely overlaps would put work done outside the window into its
  tool time.

- **Days are the offered unit, in the reader's zone.** `ActiveDays` buckets by
  local calendar day: a bounded, predictable list a person already thinks in,
  where working stretches split on every coffee break. Each day's bounds are its
  first and last timestamp rather than midnight to midnight, so selecting one
  cannot pull in a neighbour through a rounding edge — a test asserts that a
  day's window holds exactly the steps the day counted.

- **A preset that would select nothing is not offered.** Today, Yesterday and
  Past week are relative to now, not to the run, so most sessions in a listing
  qualify for none of them. `buildQuick` asks `ActiveDays` whether anything
  happened inside each window and leaves out the ones where nothing did: a
  control that lands the reader on an empty timeline is worse than a control
  that is not there. Presets sit beside the day list rather than inside it —
  "Yesterday" and "Tue 8 Sep" are not the same kind of thing, and a dropdown
  mixing them says they are. Any window that matches no entry in that list shows
  as "Custom window", because a list reading "Full run" under a filtered page
  says the opposite of what the page is showing.

- **Everything converts to the reader's zone.** Every timestamp becomes text in
  `stamp`, `stampSec` or `inputStamp`, all of which go local first. The
  transcript stores UTC and file mtimes are local, and showing one of each on
  the same screen is a real bug: the list said a session was last touched at
  13:58 while its own page said the run ended at 04:58. The listing has both
  kinds in adjacent columns — Started is the transcript's first timestamp, Last
  touched is the file's mtime — so its `when` helper converts too, and
  converting a time that is already local costs nothing. The page does not name
  the zone anywhere, which is a known gap: a copy read on a machine in another
  one has no way to tell. Naming it again is one line beside the range controls.

## The page

- **The rendered page makes no network requests, and carries everything.** No
  CDN, no fonts, no analytics — enforced by a test. A transcript holds prompts,
  replies, command lines and paths, and the page carries all of it, so a copy
  saved out of the browser is a copy of those contents and travels as one. There
  is no redaction setting. `internal/textfmt` collapses a home directory and
  clips a preview, which is layout, not privacy — nothing there hides anything.

- **A link is not a request.** The page carries one outbound URL, the
  repository, and the self-contained test allows exactly that one while still
  refusing every `src=`, `@import`, `fetch(` and CDN host. The property being
  protected is that looking at a transcript hands its contents to nobody — a
  link the reader chooses to follow does not.

- **Each subject gets a pair: what it reached for, then when.** Tokens, tools
  and skills each have a total or a ranked list followed by a lane on the same
  clock, in that order, and `TestSectionsAreInOrder` pins the sequence by the
  literals the script builds them from. The skills lane is one row per skill
  rather than one row for all of them: there are a handful at most, and a row
  labelled with its own name identifies itself without spending a colour or
  needing a hover. Its marks come out of `D.tools` — a skill invocation is a
  `Skill` tool call, so filtering that list by name costs nothing and cannot
  drift from what the tool lane draws. A list with nothing in it is left out
  entirely; an empty card on every session says less than no card.

- **Every tool, not the top few.** "Tools used" lists each tool the run called
  with its count and share. "Which tools did this use" is a question about the
  whole set, and the single call to something unexpected is usually the one
  worth seeing — which a top-N list is exactly where it disappears. MCP names
  are shortened from `mcp__server__tool` to `server/tool`, with the raw name on
  hover: the prefix and doubled underscores are wire format and a strip of them
  is unreadable.

- **A ranked list is measured against its longest bar, not against the total.**
  Each bar is a share of the busiest one. Against the total, a session that is
  86% one tool leaves every other bar an identical invisible sliver, which is
  the opposite of what a ranked list is for; the percentage of the total is
  printed beside the count instead. One series, so it wears the ink token and
  spends no categorical slot.

- **Chart copy explains the chart, not the model behind it.** Titles are the
  plainest thing that is still true — "Tokens spent", "Tokens over time", "Tool
  calls over time" — and the sentence under each, where there is one, says what
  a bar is and what makes it move. The level/flow distinction and the argument
  about averaging a composition are why the page is built this way; they belong
  here, not over every reader's head. Rewording a title is a code change in two
  places: the card call, and the order test that finds the section by it.

- **A legend entry is a filter, and a filtered chart rescales.** Clicking one
  shows that series alone; clicking it again brings the rest back. The y axis is
  then computed from what is showing, because a band that is 1% of a stack left
  pinned to the old axis answers nothing — which is the whole reason somebody
  clicked it. A series keeps its own colour whatever else is hidden: colour
  follows the entity, never its rank in what happens to be on screen. Each chart
  has one definition of its series, shared by legend, marks and tooltip — three
  copies of the same four labels is how a filtered chart ends up naming a band
  one thing in the key and another on hover.

- **Render nothing that fills nothing.** An area chart between adjacent columns
  draws an empty path when a column stands alone, and an early version silently
  dropped every isolated response that way — a 20-response session showed two
  spikes. Isolated samples are bars. Where a form stops working at a given
  density, switch forms and say so on the page: past ~250 calls the tool lane
  becomes counts per slice, because individual marks merge into a wall that says
  only "there were many". Where a layout question depends on how text actually
  renders, measure it: axis labels are thinned by `getBBox()` after the SVG is
  in the document, because a fixed minimum gap guessed the font width wrong in
  both directions — the gaps on the surveyed run are 93px and up, and the labels
  are wider than that.

- **Three categorical colours, and status colours are reserved.** The chart
  palette's first three slots are the ones that clear the colourblind and
  normal-vision separation gates across all pairs in both light and dark; a
  fourth cannot, in any ordering. So a stack gets at most those three, a
  single-series chart gets an ink token rather than spending a slot, and tool
  outcomes use the fixed status palette with labels beside them, never colour
  alone. The token stacks need a fourth band for output and use that same ink
  token, which the validator confirms is the safe way to get one: adding
  `--s-out` to the three leaves the worst adjacent pair where it already was
  (green↔orange, ΔE 9.2 deutan light, 9.4 dark) and the grey is the *most*
  separated of the four (ΔE 22.6 normal, its nearest neighbour). It fails the
  chroma floor, as a grey must — that check asks whether a colour is a
  categorical hue, and this one is deliberately not. Run the palette validator
  rather than reasoning about it, and re-run it for dark mode: dark is its own
  set of steps, not an automatic flip. One WARN is outstanding: `--s-in` is
  2.74:1 against the light surface, below the 3:1 floor, which obliges visible
  labels or a table view. There is no table view, so the legend and the hover
  tooltip carry it. Darkening the green is the fix if that is not enough —
  re-run the validator on the whole set, not on the one step.

- **The footer says how the transcript was read, and nothing else.** One line
  for the steps and the path, plus the two ways reading can have gone wrong — a
  half-written last line, and lines that did not parse. A list of unmodelled
  entry types under every session is a note to the author, not to the reader;
  the reader still counts them on `event.Run`, so a note that wants saying again
  has the data behind it.

- **A control only appears where it leads somewhere.** The back link is set by
  the server, which has a list to return to, and left empty when there is none —
  a page saved out of the browser is opened on its own, and a back link to an
  index that is not there is worse than none. The same applies to every control
  that needs a server to answer: `render.Options` carries it, the page checks
  it, and nothing in the page is hard-coded to assume one is there.

- **The theme is the reader's, and there is one copy of it.** `theme.css` and
  `theme.js` are embedded once and served to both the session page and the list;
  `render.ThemeCSS`/`ThemeJS` are exported for that reason alone. The choice is
  stored per browser, applied in `<head>` before the first paint so there is no
  flash of the wrong theme, and mirrored across tabs through the `storage`
  event. It cycles system → light → dark, because a two-state toggle cannot
  return to the OS setting and the OS setting is a real choice. Every storage
  access is wrapped: it throws in a private window and the page still has to
  render.

- **The header chrome is one design in two files, and nothing enforces it.**
  `theme.css` is shared, but the listing's header styles live in `serve.go` and
  the session page's in `page.html`, so the same row of controls is written
  twice and drifts. The theme toggle has to be in the same corner on both — a
  toggle that moves when you follow a link reads as a different page — so the
  session page carries a top bar with the back link opposite it. The build and
  the repository link are a `.colophon` line at the foot of both pages, and that
  line is the pair's newest copy-and-paste: keep the two written the same way.

## The listing

- **Only agents with a reader are listed.** `internal/agent.List` filters to
  entries that have both a `discover` and a `load`, so the picker cannot show an
  option that returns nothing. `Status()` still separates "no reader" from
  "reader, but this CLI was never run here", because an empty list needs to say
  which. The survey is worth having when a second reader gets written: Codex
  keeps `~/.codex/sessions/<yyyy>/<mm>/<dd>/rollout-*.jsonl`, typed
  `session_meta` / `event_msg` / `response_item` / `turn_context`, tool calls as
  `custom_tool_call`, and tokens in a `token_count` event carrying both a
  running total and the last turn plus `model_context_window`, which Claude Code
  never states. Cursor keeps per-project directories under `~/.cursor` in which
  no per-session transcript was identified; Qwen Code under `~/.qwen` was not
  surveyed.

- **"Agent" names the CLI, and nothing else.** The session list picks between
  Claude Code and whatever else gains a reader, so the word is taken. A
  transcript's own `agent-name` is a different thing — what the CLI calls that
  session, identical to `custom-title` on every surveyed session — and labelling
  it "agent:" on a page whose list said "Claude Code" produced exactly the
  question it should have answered. It reads "name:" instead. The same rule
  holds anywhere else the word is tempting.

- **A heading names both fields.** `name` and `title` are different things and
  the page labels each, because a heading that showed one and hid the other
  produced exactly the question it should have answered. The listing carries
  them stacked in one "Name / title" column, which is what makes both readable:
  side by side neither column was wide enough for the values that occur, and the
  two lines are told apart by weight and colour rather than by position alone —
  the name is the link, the title is muted under it. A session with no title
  leaves that line blank, but the line keeps its height: without a `min-height`
  the rows without one come out shorter and the table reads as broken.

- **The identity block wraps inside itself.** A working directory 90 characters
  long must not push the controls onto their own line. It is two sub-lines for
  the same reason — where the session is, then when it ran and what ran it. The
  CLI and its release range live on the second of them rather than among the
  page's own controls, because they describe the run.

- **A long session crosses many releases.** Eleven in the surveyed run, which
  filled the header line and pushed everything else off it. What is shown is the
  first and the last with the full list in a tooltip — "2.1.231 → 2.1.274
  (7 releases)". The range is only meaningful because versions are sorted
  numerically per component: a string sort puts 2.1.98 after 2.1.231 and the
  range would come out backwards.

- **Only a clipped cell reveals itself, and one tooltip does it.** Names, titles
  and working directories are all longer than a column that also has to hold the
  rest of the table, so the listing gives a `title` to what layout actually cut
  off (`scrollWidth > clientWidth`, recomputed on resize) and to nothing else:
  titling every cell put a tooltip on rows that were already readable. 31 of the
  95 rows are clipped at a typical width. The measured thing is the element that
  clips, which is each of the two lines inside the name/title cell rather than
  the cell — a `<td>` holding two block children never overflows, so measuring
  it finds nothing to reveal. The mechanism is the browser's own tooltip for the
  whole table: the working-directory cell carries a `title` from the template —
  it is the one cell that must say something ("not recorded") even when it is
  not clipped — so a second, styled box of our own showed two tooltips at once.

- **The list is searched fuzzily, in the browser, and the filter survives the
  round trip.** Matching is subsequence-in-order per word, the way a file finder
  works, so a few letters of a non-ASCII title or `srvhub` for `service-hub`
  both hit; a substring always counts. The query lives in `?q=` via
  `replaceState`, each session link carries it, and the session page's back link
  is built from it — so back means back to the rows you were looking at, not to
  all of them.

## Shipping

- **Print what to open, not what was bound.**
  `net.Listen("tcp", "0.0.0.0:7391")` on a dual-stack machine hands back a
  socket whose `Addr()` reads `[::]:7391`, which is not an address anybody can
  type. Sharing is the only reason to pass a wildcard, so the announcement is
  localhost — for the person at the keyboard — followed by every IPv4 the
  machine can be reached at from elsewhere. Loopback, link-local and down
  interfaces are left out because none of them is an address to hand to
  somebody, and IPv6 is left out because it would double the list on most
  machines and be the wrong half of it on the rest. A host that was named is
  repeated back as typed: it is what the reader chose.

- **The `badges` branch is not debris, and it is deletable.** The coverage
  badge reads a shields.io endpoint document from an orphan `badges` branch of
  this repository, so publishing it needs no gist and no personal access token
  — the built-in `GITHUB_TOKEN` can write there, and `ci.yml` does not watch
  that branch, so the push cannot loop. The cost is that it looks like leftover
  debris in a branch list, and it was swept up in a cleanup once, which is what
  "resource not found" on the badge means. The branch carries a README saying
  so, and the job recreates it when it is missing rather than failing, so the
  badge heals on the next push to main.

- **The version is stamped, not hard-coded twice.** `internal/version` holds
  `Version` and a release overrides it with
  `-X github.com/Dongss/agent-trace/internal/version.Version=<tag>` — the
  conventional variable name and shape, so the install and update scripts need
  no new plumbing. It defaults to the release being worked toward rather than to
  "", because an empty default reports a Go pseudo-version in a repository with
  no tags yet, which is worse than a slightly early number. A build from a
  modified tree gets a `-dev` suffix, so only a clean tag calls itself the
  release.

- **One archive name, written out in four places.** `.goreleaser.yaml` names the
  release assets `agtrace_<tag>_<os>_<arch>.tar.gz` (`.zip` on Windows), and
  `scripts/install.sh`, `scripts/install.ps1` and `internal/selfupdate` each
  rebuild that string to know what to download. Nothing checks that the four
  agree, so a change to `name_template` is a change to all of them. The same
  goes for `checksums.txt`: all three consumers parse `<sha256>  <name>`, with
  the leading `*` that binary-mode hashing adds stripped.

- **The updater fails closed where the installer warns.** Both verify the
  download against `checksums.txt`, but a missing or mismatched sum is fatal in
  `internal/selfupdate` and only a warning in the install scripts. The installer
  is creating a file that did not exist; `update` overwrites a binary the user
  already trusts. Nothing on disk is touched until the download is complete and
  verified, and the swap is a rename inside the destination directory so an
  update killed halfway leaves the old binary or the new one, never a truncated
  file. Windows cannot rename over a running executable, so
  `replace_windows.go` moves the old one aside first and puts it back if the
  second rename fails.

## Conventions

- Comments say *why*, not what. The reader can see what the code does.
- An error a user can act on names the thing to change: the file, the flag, the
  session id, the command to run.
- Say what a number is. A timeline of tokens and costs invites a reader to trust
  it, so a figure that is estimated, recomputed or partial is labelled where it
  is shown, not explained in a doc they will not read.
- Prefer showing less to showing something plausible. A gap prompts a question;
  a fabricated value ends one.
