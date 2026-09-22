# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Security

- **The workspace directory is judged, not only `work_dir`.** `work_dir=~/.config`
  with `workspace_id=gh` made the workspace `~/.config/gh`, a credential
  directory, and transcripts were written into it. `<work_dir>/<workspace_id>`
  is now refused with `work_dir_denied` wherever `work_dir` itself would be.
  The hole was present since the work-directory contract (ADR-0002).
- A path holding a NUL byte is refused (pathguard v0.2.0).

## [0.5.0] - 2026-09-22

### Changed

- **Path judgement moved to [nlink-jp/pathguard](https://github.com/nlink-jp/pathguard)**
  (ADR-0004). `internal/mcp/workdir` is now an adapter onto it; the resolver is
  built with `workdir.NewResolver(serverOwnedDirs()...)`. Places are compared by
  file identity and by names folded the way the disk folds them, instead of by
  name.
- `transcribe` now **refuses** the real places under your home from the list
  gem-agent and lagent use — newly `~/.kube`, `~/.config/gh`, `~/.azure`,
  `~/.terraform.d`, `~/.gemini`, `~/.config/mcp-bridge`, `~/.netrc`, `~/.npmrc`,
  `~/.pypirc`, `~/.git-credentials`, `~/.vault-token`, `~/.docker/config.json`,
  `~/.claude.json`, `~/.bash_history`, `~/.zsh_history` — and every spelling of
  any refused place (another case, a link, a firmlink), and wherever a link
  directly inside one of those directories points (a `~/.ssh/config` that links
  into a sync folder protects the file it points at). When `$HOME` names another
  directory than the account's home, both are protected. Linux `/etc` is refused
  as a `work_dir`.
- `transcribe` now **accepts** `.env.example`, `.env.sample`, `.env.template` and
  `.env.dist` as audio paths (templates, not secrets).
- When the home directory cannot be determined, audio paths and every
  `work_dir` are **refused**; they used to pass unchecked.
- `work_dir_denied` carries `reason` in its `details`.

### Fixed

- **`make verify-release` now fails closed.** Its last block chained unzip, the
  packaged binary's `--version` and `spctl` with `&&` and ended the whole chain
  in `|| true`, so a zip that did not unpack or a binary that did not run exited
  0 and the upload proceeded. Each step is now judged on its own, the packaged
  binary's `--version` must contain the tag being released, and only the
  informational `spctl` line may be ignored. Matches the org template
  (CONVENTIONS.md §Code Signing → Verifying a release).

### Tests

- The per-tool contract tests fail when no tool is registered. They loop over
  the registered tools, and with an empty list every one of them passed without
  examining anything.

## [0.4.5] - 2026-09-21

### Security

- **`work_dir` may no longer be this server's own config directory.**
  Organization ADR-021 §4 closes the work-directory checks with "not a system
  location … and not the server's own config or state directory" →
  `work_dir_denied`, and the resolver has carried a `Denied` list for exactly
  that — but nothing populated it here, so it ran as its zero value. A caller
  could pass `work_dir = ~/.config/gem-scribe` and have the server write
  transcripts in among the file that names the Vertex project, region and
  staging bucket — and read that file back out as workspace contents — on a
  model's say-so. `~/.config/gem-scribe` and everything under it is now
  refused. The path is derived from `config.DefaultPath`, the one place that
  names the config file, so the denial cannot drift away from the location it
  protects, and the resolver is built in one place (`workDirResolver()`) that
  every tool reaches through `tools.Deps`.

## [0.4.4] - 2026-09-21

### Security

- **A symlink planted at `<work_dir>/<workspace_id>` no longer redirects the
  whole workspace.** `os.Root` confines operations inside a root but resolves
  the root path itself normally, so if anything else with write access to your
  work directory (another tool, sandboxed code) left a link at the workspace's
  name, every read and write anchored on the link's target: transcripts were
  written outside the directory you named and the call reported success. The
  workspace directory is now created through an `os.Root` on `work_dir` and
  then verified by real path; a workspace whose name resolves elsewhere is
  refused with `path_not_allowed`, naming the id and what it resolved to.

## [0.4.3] - 2026-09-14

### Added

- `TestEveryRequiredNameIsDeclared` — a schema that lists a name in `required`
  without declaring it in `properties` makes a strict client refuse the whole
  tool list (Vertex AI: "schema at top-level requires unspecified property").
  data-toolbox-mcp shipped exactly that and broke a session outright; the
  existing contract test checked declared ⇒ required only, so the fleet is
  pinned in both directions now.

## [0.4.2] - 2026-09-14

### Changed

- **A relative `audio` is now looked for in `work_dir` itself as well as in the
  workspace** (the workspace wins if both hold the name). The workspace is a
  level below the work directory the caller named, which is not where an agent
  that has just written a file puts it: two real sessions lost rounds to exactly
  that. An absolute path anywhere readable was already accepted, so resolving a
  relative name one level up costs no containment.
- **An absolute `audio` that is not there now says where the file actually is**,
  when a file of that name sits in the workspace or the work directory. A
  session passed `<work_dir>/x.aiff` for a file at `<work_dir>/<id>/x.aiff` and
  spent a round discovering the level. The search is those two directories
  only — never a tree walk.

## [0.4.1] - 2026-09-14

### Fixed

- **`input_not_found` named nothing.** "input %q is not in the workspace —
  place it there first" does not say where the workspace is. The error now names
  the absolute path it looked at and offers the escape — a recording may be an
  absolute path to wherever it already is, read in place. (Found on voice-scribe
  with a real agent, which spent four rounds recovering from that one sentence;
  the two servers share this code.)
- The `audio` argument's description says the workspace is
  `<work_dir>/<workspace_id>/`, **a level below `work_dir` itself** — the
  confusion the agent actually had.

## [0.4.0] - 2026-09-14

### Changed

- **Breaking: `inline_threshold` is now `max_bytes`, and the result always
  carries the transcript.** The old threshold switched the *delivery mode*: at
  or below 8 KB you got the whole text, above it you got an `excerpt` and a path
  and no text at all. That is a judgement about your context window, which this
  server cannot make — the same reason splunk-mcp and pcap-analyzer-mcp dropped
  their spills. `max_bytes` (default 65536, `0` means no cap) bounds the
  response and nothing else: the result carries as much text as the cap allows,
  `truncated` and `omitted_bytes` say exactly what it left out, `bytes` stays
  the full size, and `path` / `absolute_path` reach all of it. See
  [ADR-0003](docs/en/adr/0003-response-cap-not-delivery-mode.md); voice-scribe
  carries the same change so the two result types stay identical.
- **The transcript file is written either way, as before.** It is this server's
  product, so `work_dir` stays required (ADR-0002). The cap never decides
  whether the artifact exists.

### Added

- `TestModelFacingTextNamesNoWithdrawnDeliveryMode` — walks the initialize
  instructions, the usage manual and every tool's description and schema for the
  words that described the withdrawn switch. It found two tool descriptions the
  rename had missed.

### Removed

- The `excerpt` field. A preview standing in for text that was withheld has
  nothing to stand in for any more.

## [0.3.2] - 2026-09-14

### Fixed

- **The initialize `instructions` field never mentioned `work_dir`.** It is the
  first thing the model reads about this server — before any tool list — and it
  still described "a workspace directory you prepare" while every tool required
  an argument it did not name. It now states the contract: `work_dir` is the
  absolute path of a directory you can read back, required, with no default.

### Added

- `TestInstructionsNameTheWorkDirContract` — the schema and description tests
  walked `tools/list`; nothing walked what `initialize` returns (ADR-0002).

## [0.3.1] - 2026-09-13

### Fixed

- `get_usage`'s own description still offered to explain "the workspace model
  and workspace_root" — a name 0.3.0 retired and the server now refuses.

### Added

- `TestNoToolDescriptionNamesARetiredWorkDirName` — the schema test caught the
  renamed argument but not the sentence beside it.

## [0.3.0] - 2026-09-13

### Changed

- **Breaking: `workspace_root` is now `work_dir`, and `transcribe` requires it.**
  It means the absolute path of a directory the caller can read back, and there
  is no server-owned default any more — omitting it used to write under
  `~/.local/share/gem-scribe/mcp-workspaces`, which no calling agent can open, so
  the job succeeded and the path it returned did not. A call still sending
  `workspace_root` (or `workspaceRoot` / `workspace_dir`) is refused with
  `work_dir_required` naming the replacement. See
  [ADR-0002](docs/en/adr/0002-work-dir-contract.md); organization ADR-021.
- `audio` may now be an absolute path to a recording anywhere readable, read in
  place and never copied. Credential and agent-control locations (`~/.ssh`,
  `~/.aws`, `~/.gnupg`, `~/.config/gcloud`, `~/Library/Keychains`, `~/.claude`,
  `~/.codex`, any `.env`) are refused, checked on the path as given and on its
  symlink-resolved form.
- A runtime may supply the directory instead of the model: the server reads
  `_meta["jp.nlink/work_dir"]` when the argument is absent. The argument wins.
- Results echo the resolved `work_dir` and `workspace_id`.

### Added

- `work_dir_required`, `work_dir_invalid`, `work_dir_not_found`,
  `work_dir_not_writable`, `work_dir_denied` — five codes that say which part of
  the contract failed. The work directory must already exist (the server does not
  create it), be writable, and not be a system or credential location.

## [0.2.1] - 2026-08-31

### Changed

- The `workspace_root` argument now says plainly that the caller should pass a
  root it can read back: every result is returned as a path under that root, so
  a workspace the caller cannot open leaves it holding a path to nothing. Text
  only — the behaviour is unchanged.

## [0.2.0] - 2026-08-30

### Added

- **Translation and speaker naming** — `--translate en` adds a translation
  beside the original, and `--speaker-hint 田中,佐藤` assigns real names to the
  model's `spk:N` labels. Both are exposed on the MCP `transcribe` tool as
  `translate_to` and `speaker_hints`. This is the capability gem-transcribe had
  and this tool did not.
- Both run as a second pass **over the transcript text**, after the transcript
  exists, and neither lets a model author structure: translation numbers the
  slots and reads back the numbers, so a line that does not return keeps its
  original text rather than costing the transcript. A speaker label the
  transcript does not identify keeps its label — omitting is correct where
  guessing is not.
- Two diagnoses for the failure that looks most like success: a partially
  translated transcript and speaker labels a naming pass left unresolved. Both
  are derived from the result rather than from a recorded count, and surface as
  `warning` in MCP results and on stderr in the CLI.
- `speaker_hints` without diarization is refused rather than silently ignored —
  there are no labels to assign names to.

## [0.1.0] - 2026-08-30

### Added

- Initial implementation: transcription CLI and stdio MCP server on Vertex AI's
  dedicated transcription model.
- CLI: JSON / text / Markdown / SRT / VTT output, speaker diarization, word-level
  timestamps, SMART mode, BCP-47 language hints, local files and `gs://` URIs.
- MCP server (`gem-scribe mcp`): `get_usage`, `transcribe`, `check_job` — the
  same tool names and shapes as voice-scribe, so an agent drives either the same
  way. Asynchronous jobs, workspace containment enforced with `os.Root`.
- Output envelope compatible with voice-scribe and the archived gem-transcribe,
  so one downstream parser reads all three.
- Inline audio by default, with Cloud Storage staging above 20 MB: a short
  recording works before any bucket exists.
- Actionable hint on the 404 a regional endpoint returns for a Gemini 3 model,
  which otherwise says nothing about the location being the cause.
- Diagnosis of transcripts that are well-formed but probably wrong — diarization
  that found one speaker in a conversation, an experimental speaker count, or a
  transcript with no timings — surfaced as `warning` in MCP results.
