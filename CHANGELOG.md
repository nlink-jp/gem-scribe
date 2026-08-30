# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

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
