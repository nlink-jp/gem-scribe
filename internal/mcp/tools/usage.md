# gem-scribe MCP server

Transcribes recordings with Vertex AI's dedicated transcription model. The model
returns speaker turns and word timings as structured data, so a transcript never
depends on a language model writing valid JSON.

This costs money and sends audio to Google. Its local counterpart,
`voice-scribe`, costs nothing and keeps the audio on the machine but separates
at most four speakers. Pick per recording: cost favours voice-scribe, accuracy,
speed and speaker count favour this server.

## The workspace model

The server works in a directory **you** prepare. One workspace is one
transcription project:

```
<workspace_root>/<workspace_id>/
├── meeting.m4a          ← you put recordings here
└── output/              ← the server writes transcripts here, and nowhere else
```

- `workspace_root` — absolute path of a directory you control. Omit it and the
  server uses its own default root.
- `workspace_id` — `[a-zA-Z0-9_-]{1,64}`, defaults to `default`.
- Every path argument is **relative to the workspace** and cannot escape it.
  Absolute paths and `..` are refused (`path_not_allowed`), and symlinks planted
  in the workspace cannot redirect the server outside it.

## Tools

### `transcribe`

Starts a transcription and returns a `job_id` immediately. It does not wait.

| Argument | Default | Notes |
|---|---|---|
| `audio` (required) | — | Recording path, relative to the workspace |
| `workspace_root` / `workspace_id` | server default / `default` | See above |
| `model` | configured model | Override the transcription model |
| `languages` | detect | BCP-47 hints, e.g. `["ja-JP"]` |
| `diarize` | `true` | Label each speaker turn |
| `word_timestamps` | `true` | Word-level timings; **required** for `srt` and `vtt` |
| `smart` | `false` | Disfluency removal and light formatting |
| `translate_to` | none | Add a translation beside the original, e.g. `"en"` |
| `speaker_hints` | none | Candidate names, assigned to `spk:N`. Needs `diarize` |
| `format` | `json` | `json`, `text`, `md`, `srt`, `vtt` |
| `output` | `output/<name>.<ext>` | Transcript path, relative to the workspace |
| `inline_threshold` | 8192 | Bytes at or below which the transcript comes back inline |

`speaker_hints` needs `diarize`: without it there are no labels to assign names
to, so the combination is refused rather than silently ignored.

`smart` cannot be combined with `diarize` or `word_timestamps`
(`mode_conflict`). Passing `smart` alone turns both off for you rather than
reporting a conflict you did not ask for.

### The second pass — `translate_to` and `speaker_hints`

The transcription model does neither of these: it does not translate (its
language field is a detection hint), and it labels speakers `spk:0`, not by
name. Both are a second call to a general model **over the transcript text**,
run after the transcript exists.

That ordering is the safety property. The segments are already fixed, and the
second pass only fills slots in them — so a failure costs a translation or a
name, never the transcript:

- A segment the model does not return **keeps its original text**, and the
  count of those appears in the result's `warning`.
- A speaker label the model cannot resolve from the transcript **keeps its
  label**. Omitting is the correct answer when the transcript does not
  establish a name; a guess would be worse.
- `translate_to` adds a language key beside the original. Nothing is replaced,
  so `text` ends up with both.

### `check_job`

Poll with the `job_id`. States are `queued`, `running`, `done`, `error`.

Jobs are held in memory and do not survive a server restart: an unknown
`job_id` returns `job_not_found`, and re-submitting `transcribe` is safe because
it works from the same workspace.

### `get_usage`

This document.

## How a finished transcript comes back

The transcript file is always written. In addition:

- **Short transcript** — the whole text is inline in `text`.
- **Long transcript** — `truncated` is `true`, `excerpt` holds the opening, and
  `path` / `absolute_path` name the file to read.

The result also reports `format`, `bytes`, `model`, `language`, `segments`,
`speakers` and `duration_seconds`.

### `warning` — read it

A `warning` field means the transcript is well-formed but probably not what you
wanted. You cannot hear the audio, so this is the only signal that separates a
good transcript from a structurally perfect wrong one:

- **diarization returned one speaker for a conversation** — two voices that are
  acoustically similar are not separated. The transcript is complete; the
  speaker labels are not trustworthy.
- **more than two speakers were labelled** — the model supports up to eight, but
  documents attribution beyond two as experimental. Check the assignments.
- **no timings** — you turned off `word_timestamps`, so nothing can be located
  in the audio.
- **segments left untranslated** — part of the transcript came back without a
  translation and carries only the original. The transcript is complete; the
  translation is not.
- **speakers left unnamed** — `speaker_hints` was given but the transcript did
  not establish who some labels are. Those keep `spk:N`.

## Errors

Every failure carries a stable `code`. Branch on the code, not the prose.

| Code | What happened | What to do |
|---|---|---|
| `missing_argument` | A required argument was absent | Supply it |
| `invalid_arguments` | Unknown field, wrong type, or a bad `format` | Fix the call; unknown fields are rejected rather than ignored |
| `invalid_workspace_id` | `workspace_id` is not `[a-zA-Z0-9_-]{1,64}` | Rename it |
| `path_not_allowed` | A path was absolute or pointed outside the workspace | Use a workspace-relative path |
| `workspace_failed` | The workspace could not be created or read | Check the root exists and is writable |
| `input_not_found` | The recording is not in the workspace | Put the file there first; the server does not fetch |
| `unsupported_format` | Not an audio container the model reads | Convert to wav, mp3, m4a, flac, ogg, opus, aiff or webm |
| `too_large_for_inline` | The file needs a staging bucket that is not configured | Set `[staging].bucket`, or transcribe a smaller file |
| `mode_conflict` | `smart` was combined with `diarize` or `word_timestamps` | Drop one side |
| `project_required` | No GCP project is configured | Operator sets `[gcp].project` or `GEMSCRIBE_PROJECT` |
| `credentials` | Application Default Credentials are missing or expired | Operator runs `gcloud auth application-default login` |
| `safety_blocked` | The safety filter refused the audio | Nothing to retry |
| `empty_transcript` | The call succeeded but found no speech | Check the recording is not silent |
| `transcribe_failed` | The API call failed | Read the message; transient failures are already retried |
| `job_not_found` | Unknown `job_id`, usually a server restart | Re-submit `transcribe` |

`project_required` and `credentials` are operator problems, not agent problems.
Retrying will not fix either — say what is missing and stop.

## Limits worth knowing

- The transcription model is served from the **global** endpoint only. A
  regional location returns a 404 that does not mention the location; the error
  is annotated when that happens.
- The model documents a **30-minute ceiling** with diarization or word
  timestamps. Longer audio has been observed to work, but that is not a promise.
- Vocabulary biasing is deliberately not exposed: supplying it truncated the
  transcript to its first turn, silently, in every measured run.
