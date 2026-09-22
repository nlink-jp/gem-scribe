# gem-scribe

Speech-to-text CLI and MCP server built on Vertex AI's dedicated transcription
model (`gemini-3.5-transcribe`).

The model returns speaker turns and word-level timings as structured data, so
nothing has to ask a language model to emit JSON and nothing has to repair what
one produced. Transcripts come out in an envelope shared with
[voice-scribe](https://github.com/nlink-jp/voice-scribe), so downstream tools
parse cloud and local transcripts with one parser.

## gem-scribe or voice-scribe?

Neither is the default. Pick per recording:

| Priority | Tool | Why |
|----------|------|-----|
| Cost, or audio that must not leave the machine | [voice-scribe](https://github.com/nlink-jp/voice-scribe) | Runs whisper.cpp locally. No API cost |
| Accuracy, speed, more than four speakers | **gem-scribe** | Separates up to 8 speakers; no models to download. About $0.30 per hour of audio |

## Prerequisites

- A **Google Cloud project** with the Vertex AI API enabled
- **Application Default Credentials** — `gcloud auth application-default login`

## Installation

```bash
brew install nlink-jp/tap/gem-scribe
```

Or from source:

```bash
git clone https://github.com/nlink-jp/gem-scribe.git
cd gem-scribe
make build          # → dist/gem-scribe
```

## Usage

```bash
# Transcribe to JSON on stdout
gem-scribe meeting.m4a

# Japanese, as subtitles
gem-scribe meeting.m4a --lang ja-JP -f srt -o meeting.srt

# Audio already in Cloud Storage
gem-scribe gs://my-bucket/interview.flac -f md -o interview.md

# Clean prose instead of a verbatim transcript (no speakers, no timings)
gem-scribe talk.wav --smart -f text

# Name the speakers and add an English translation beside the original
gem-scribe meeting.m4a --lang ja-JP --speaker-hint 田中,佐藤 --translate en
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-o, --output-file` | stdout | Write to a file; per-language files when a transcript carries several |
| `-f, --format` | `json` | `json`, `text`, `md`, `srt`, `vtt` |
| `--lang` | detect | BCP-47 hints, e.g. `--lang ja-JP` |
| `--diarize` | `true` | Label each speaker turn |
| `--word-timestamps` | `true` | Word-level timings; required for `srt`/`vtt` |
| `--smart` | `false` | Remove disfluencies and format lightly. Excludes the two flags above |
| `-m, --model` | config | Transcription model |
| `--location` | `global` | Vertex AI location |
| `-c, --config` | — | Config file path |
| `--translate` | — | Add a translation beside the original, e.g. `--translate en` |
| `--speaker-hint` | — | Candidate names, assigned to `spk:N` (repeatable) |
| `-q, --quiet` | `false` | No progress on stderr |

### Output

```json
{
  "metadata": {
    "source": "meeting.m4a",
    "model": "gemini-3.5-transcribe-preview",
    "duration_seconds": 13.9,
    "languages": ["ja"],
    "diarized": true
  },
  "segments": [
    { "start": 0.1, "end": 4.4, "speaker": "spk:0", "text": { "ja": "…" } }
  ]
}
```

`text` is a language-code map rather than a string, which is what lets a
translation sit beside the original. voice-scribe uses the same shape.

### The second pass

The transcription model does not translate, and it labels speakers `spk:0`
rather than by name. `--translate` and `--speaker-hint` are a second call to a
general model **over the transcript text**, run once the transcript exists.

That ordering is the safety property, and it is why this does not reintroduce
the fragility the tool was built to remove. The segments are already fixed; the
second pass only fills slots in them. A line the model does not return keeps
its original text, a speaker it cannot identify keeps its label, and either way
you are told how much was left behind. A failure costs an enrichment, never the
transcript.

## MCP server

```bash
gem-scribe mcp
```

Speaks MCP over stdin/stdout, giving an agent whose model cannot process audio a
way to read recordings. Three tools — `get_usage`, `transcribe`, `check_job` —
with the same names and shapes as voice-scribe's, so an agent drives either the
same way.

Transcription is asynchronous: `transcribe` returns a `job_id` that `check_job`
polls. Every call names `work_dir` — the absolute path of a directory the agent
can read back — and the transcript is written under it; a recording may sit
there or be named by an absolute path anywhere readable, read in place and never
copied (credential and agent-control locations such as `~/.ssh` are refused
under any spelling and whether or not a file is there, with the same answer
either way; [nlink-jp/pathguard](https://github.com/nlink-jp/pathguard)
makes that judgement). A `work_dir`
naming a system location, your home directory itself, a credential or
agent-control location, or **this server's own config directory
(`~/.config/gem-scribe`)** is refused with `work_dir_denied`, subdirectories
included: the work directory is yours, and ours is not a workspace. The server
writes only under its `output/` subdirectory, and paths cannot escape it. Call
`get_usage` first — it returns the full manual, including the error-recovery
table.
Two spellings still get past it — a name in another Unicode normalisation and a hard link; the limits are listed in [ADR-0004](docs/en/adr/0004-pathguard.md).

Register it with your client, for example:

```json
{ "mcpServers": { "gem-scribe": { "command": "gem-scribe", "args": ["mcp"] } } }
```

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `GEMSCRIBE_PROJECT` | — | GCP project ID (required) |
| `GEMSCRIBE_LOCATION` | `global` | Vertex AI location |
| `GEMSCRIBE_MODEL` | `gemini-3.5-transcribe-preview` | Transcription model |
| `GEMSCRIBE_SECOND_PASS_MODEL` | `gemini-3.7-flash` | General model for translation and speaker naming |
| `GEMSCRIBE_STAGING_BUCKET` | — | Bucket for audio above the inline limit |

Falls back to `GOOGLE_CLOUD_PROJECT` / `GOOGLE_CLOUD_LOCATION`. A config file at
`~/.config/gem-scribe/config.toml` works too — see
[config.example.toml](config.example.toml).

Audio is sent inline by default; only files above 20 MB need a staging bucket,
so a short recording works before any Cloud Storage setup exists. Staged objects
are deleted after the transcription.

## Limits worth knowing

- **The model is served from the `global` endpoint only.** A regional
  `location` returns a 404 that does not mention the location; gem-scribe
  annotates that error when it happens.
- **The transcription model is preview**, and no GA equivalent exists. The model
  name is configurable so a replacement can be dropped in.
- **Google documents a 30-minute ceiling** when diarization or word timestamps
  are on. Longer audio has been observed to work; that is not a promise.
- **Speaker attribution beyond two people is documented as experimental**, and
  voices that are acoustically similar are returned as one speaker with no
  error. Transcripts that look like this carry a warning in the MCP result.
- **Vocabulary biasing is deliberately not exposed.** Supplying it truncated the
  transcript to its first turn — silently, with a normal finish reason — in
  every measured run.

## Security

- **Path containment** — every workspace path is validated and enforced with
  `os.Root`, so a symlink planted in an agent-writable workspace cannot make the
  server read or write outside it. The workspace directory itself is verified by
  real path: a symlink planted at `<work_dir>/<workspace_id>` is refused rather
  than followed, because that path is handed to code outside any root
- **stdout is claimed by the MCP transport** — fd 1 is redirected to stderr, so
  a stray write from any dependency lands in the log instead of corrupting the
  protocol
- **No secrets in output** — project IDs and tokens are never logged
- Authentication is ADC; gem-scribe never handles a credential itself

## License

MIT
