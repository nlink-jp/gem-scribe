# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

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
