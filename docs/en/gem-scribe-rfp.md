# RFP: gem-scribe

> Generated: 2026-08-30
> Status: Draft

## 1. Problem Statement

Give both LLM agents that cannot handle audio and shell pipelines an accurate
transcription path through Vertex AI's **dedicated transcription model**
(`gemini-3.5-transcribe`).

The model returns speaker turns, word-level timestamps and language detection as
structured data, so nothing has to ask an LLM to emit JSON. That is the decisive
difference from the existing `gem-transcribe`, and it removes the failure mode
that tool hit on long audio — a broken JSON structure that stopped the run.

gem-scribe is the cloud counterpart of `voice-scribe` (local whisper.cpp).
**Neither one is the default**:

| Priority | Tool | Why |
|----------|------|-----|
| Cost | voice-scribe | No API cost. Audio never leaves the machine |
| Accuracy, speed, speaker count | **gem-scribe** | Up to 8 speakers against voice-scribe's ceiling of 4; no model management |

The target users are the nlink-jp operator (a single person) and agents that need
transcription over MCP. gem-scribe supersedes `gem-transcribe`: it takes over that
tool's CLI use cases, and `gem-transcribe` is then archived.

## 2. Functional Specification

### Commands / API Surface

```
gem-scribe <audio-file|gs://...> [flags]    # transcribe
gem-scribe mcp                              # stdio MCP server (same binary)
gem-scribe --version
```

Main flags, aligned with `gem-transcribe` and `voice-scribe`:

| Flag | Default | Description |
|------|---------|-------------|
| `-o, --output-file` | stdout | Output path; split per language when several are requested |
| `-f, --format` | `json` | `json` / `text` / `md` / `srt` / `vtt` |
| `--lang` | auto-detect | BCP-47 detection hints. Several values produce original + translation (second pass) |
| `--diarize` / `--no-diarize` | on | Speaker diarization |
| `--word-timestamps` | on | Word-level timestamps (drives SRT/VTT accuracy) |
| `--smart` | off | SMART mode (disfluency removal, formatting). Incompatible with timestamps and diarization |
| `--speaker-hint` | none | Candidate speaker names, assigned to `spk:N` in the second pass |
| `-m, --model` / `-c, --config` | — | Override model / config file |

MCP tools, given the same names and shapes as voice-scribe's so an agent drives
both tools the same way:

- `transcribe` — start a transcription, return a `job_id` (asynchronous)
- `check_job` — poll progress and collect the result
- `get_usage` — full tool reference and error-recovery table

`list_models` (present in voice-scribe) is omitted: there are no local models.

### Input / Output

**Input**: a local audio file or a `gs://` URI. Local files go **inline by
default, falling back to GCS staging only when the request size limit would be
exceeded**, with the staged object deleted afterwards. Short audio then works
without configuring a staging bucket at all, which lowers the setup burden.

**Output envelope**: kept **fully compatible** with `gem-transcribe` and
`voice-scribe`. The requirement is that the downstream `meeting-notes` reads all
three tools with one parser.

```json
{
  "metadata": { "source": "...", "model": "...", "duration_seconds": 0.0, "languages": ["ja"] },
  "segments": [
    { "start": 0.1, "end": 4.4, "speaker": "spk:0", "text": { "ja": "..." } }
  ]
}
```

`Segment.text` being a language-code map is exactly what lets translation be added
as a second pass. The mapping from `parts[].audioTranscription` is direct:

| API response | Segment |
|--------------|---------|
| `speakerLabel` | `speaker` |
| `text` | `text[<lang>]` |
| `words[0].startOffset` / `words[-1].endOffset` | `start` / `end` |

### Configuration

`~/.config/gem-scribe/config.toml`, following the org-wide Vertex AI tool schema.

```toml
[gcp]
project  = "your-project-id"
location = "global"          # the Gemini 3 family is global-only

[model]
name = "gemini-3.5-transcribe-preview"

[transcribe]
diarization     = true
word_timestamp  = true

[staging]
bucket = ""                  # empty: fail past the size limit, inline-only operation
```

Priority: CLI flags > environment (`GEMSCRIBE_*` > `GOOGLE_CLOUD_*`) > config file > defaults.

### External Dependencies

- Vertex AI (`gemini-3.5-transcribe-preview`, plus a general Gemini model for the second pass)
- Google Cloud Storage (staging, only for audio past the inline limit)
- Authentication via ADC (`gcloud auth application-default login`)
- `google.golang.org/genai` v1.70.0 or later (the version carrying `AudioTranscriptionConfig`)

## 3. Design Decisions

### Why Go

Because the counterpart tool needs the same distribution as `voice-scribe`:
Homebrew tap, Developer ID signing, notarization, cross-compilation through
`make build-all`. `gem-transcribe` was Python/uv and shipped no binaries. The CLI
interface is the contract, so the successor being written in another language is
not a breaking change for users.

The precondition — SDK support — was verified before committing.
`google.golang.org/genai` v1.70.0 carries `GenerateContentConfig.AudioTranscriptionConfig`
on the request side and `Part.AudioTranscription` (`SpeakerLabel` / `Words`) on the
response side, and 30 lines of Go returned the expected speaker-attributed segments.

### Skeleton source

`voice-scribe`: the CLI-with-an-`mcp`-subcommand layout, the asynchronous job model,
and the output envelope. A skeleton transplant rewrites the domain vocabulary too —
leftover terms about local model management, GGUF or Metal would make a reader
mistake this for a different tool.

### Complements

- `voice-scribe` — the local counterpart, chosen along the axis in §1
- `meeting-notes` — downstream, and the beneficiary of envelope compatibility
- `voice-studio-mcp` — the opposite direction (TTS)

### Explicitly out of scope

- **Real-time / streaming transcription** — `gemini-3.5-transcribe-live-preview` has a
  fundamentally different connection model. A separate tool if it is ever needed
- **Minute structuring, summarization, action-item extraction** — `meeting-notes` territory
- **Persistent speaker profiles** (identifying a speaker across sessions)
- **`customVocabulary`** — measured to be broken; not implemented (see §7)
- Speech synthesis (`voice-studio-mcp`)

## 4. Development Plan

### Phase 1: Core (CLI + MCP)

- Configuration and authentication (config.toml + env + flags)
- Input paths: inline first, GCS staging and cleanup past the limit
- The ASR call and its mapping onto `Segment` (including parsing the `"0.100s"` offset strings)
- Output formatters: JSON / text / Markdown / SRT / VTT
- `mcp` subcommand: `transcribe` / `check_job` / `get_usage`
- Tests, driven mainly by fixtures of real API responses

**Phase 1 verification items** — none measured yet, and each one shapes the design:
- Real accuracy of speaker attribution beyond 2 speakers (documented as experimental)
- The actual inline request size limit, which sets the threshold for switching to GCS
- What actually happens past the documented 30-minute ceiling (31 minutes already
  succeeded; where does it break?)

### Phase 2: Features

- The second pass, taking only the transcript text to a general Gemini model
  - Translation (`--lang=en,ja` for original + translation)
  - Speaker naming (`spk:N` → the candidates given by `--speaker-hint`)
- SMART mode exposed on the CLI

### Phase 3: Release

- README.md / README.ja.md / AGENTS.md / CLAUDE.md / ADRs
- Signing, notarization, `make verify-release`, Homebrew tap
- Submodule registration under util-series, all three catalog surfaces
- **Archive `gem-transcribe`** (after its README names gem-scribe as the successor)
- Feed knowledge back

**Independently reviewable units**: Phase 1 splits into CLI and MCP (the MCP layer stays
thin, calling the same core functions). The Phase 2 second pass touches nothing in
Phase 1 — it is a skippable downstream stage — so it reviews on its own.

## 5. Required API Scopes / Permissions

| Target | Permission | Purpose |
|--------|-----------|---------|
| Vertex AI | `roles/aiplatform.user` | Calling `generateContent` |
| GCS staging bucket | `roles/storage.objectAdmin` | Uploading audio and deleting it afterwards |

Authentication uses Application Default Credentials
(`gcloud auth application-default login`). The GCS permission is only required for
audio past the inline limit.

## 6. Series Placement

Series: **util-series**

Reason: it is a pipe-friendly transformation CLI, and its counterpart `voice-scribe`,
its predecessor `gem-transcribe`, its downstream `meeting-notes` and its opposite
`voice-studio-mcp` all live in util-series.

## 7. External Platform Constraints

Constraints measured on 2026-08-30, all against `gemini-3.5-transcribe-preview` on
the global endpoint.

| Constraint | Detail | Design consequence |
|------------|--------|--------------------|
| **Preview only** | No GA dedicated transcription model exists | Exposed to a retirement schedule. Keep the model name configurable and say so in the README |
| **Global endpoint only** | `us-central1` / `asia-northeast1` return 404 | Default `location` to `global`; add a hint to the 404 when a region is set |
| **30-minute ceiling (documented)** | With diarization or word timestamps. 31 minutes actually succeeded | Treat the documented figure as the contract and warn past it. Do not depend on the success |
| **`customVocabulary` is broken** | Setting it truncates the transcript to the first turn, and does so with `finishReason: STOP` — silently, not as an error. Reproduced twice | Not implemented. Re-evaluate at GA |
| **It does not translate** | Japanese audio with `languageCodes: ["en-US"]` still comes back in Japanese; `languageCodes` is a detection hint | Translation moves to the second pass (Phase 2) |
| **Speakers are only `spk:N`** | Inferring real names is outside ASR's job | Naming moves to the second pass (Phase 2) |
| **3+ speakers is experimental** | The ceiling is 8, but attribution beyond 2 is documented as experimental | Measure it in Phase 1 and state the usable range in the README |
| **Acoustically similar speakers are not separated** | Two macOS TTS voices were judged one speaker; separating their pitch made attribution correct | State the expectation in the README — this one misleads users easily |
| **Request size limit** | A 31-minute mp3 (~7.5MB, ~10MB base64) succeeded inline | Measure the threshold in Phase 1 and route the rest through GCS |
| Cost | ~$0.16 for 31 minutes ≈ **$0.30/hour** | This is the axis that separates gem-scribe from voice-scribe |

---

## Discussion Log

**Origin (2026-08-30)**: the conversation began as a request to migrate
`gem-transcribe` to the new dedicated transcription model. Two motivations — the model
generation change, and **worry about stability, since long audio failed with JSON
structure errors**.

**The central finding**: on Vertex AI, `gemini-3.5-transcribe-preview` runs through
ordinary `:generateContent` and returns a dedicated response part,
`parts[].audioTranscription` (`text` / `speakerLabel` / `words`). **Each speaker turn
is its own part, and the API guarantees the structure.** The layer that asks an LLM to
write JSON disappears entirely, and with it the reported failure mode. A 31-minute
recording came back as 209 parts, consistent across 2 speakers, with timestamps running
to the end of the audio.

**The first proposal (not adopted)**: replacing only `gem-transcribe`'s LLM layer. That
was defensible — the data model and formatters would survive — but the user chose to
**pivot to a new tool, "the Gemini version of voice-scribe"**: starting from the proven
voice-scribe skeleton is faster than carrying the existing design debt (the prompt,
schema and salvage layers, and an RFP written around translation).

**What happens to gem-transcribe**: keeping both would leave two cloud transcription
tools side by side, so it is **archived as superseded** — the same pattern as
`csv-editor`→`grid-edit` and `quick-translate`→`instant-translate`. That is why
gem-scribe must also carry a CLI: no capability may be lost in the handover.

**Relationship to voice-scribe**: an initial framing put gem-scribe first with
voice-scribe as the fallback. The user replaced it with **two equal options** — cost
first means voice-scribe, accuracy, speed or speaker count (voice-scribe caps at 4)
means gem-scribe. The README and the `mcp-tactics` skill follow the same axis.

**What the ASR model does not do**: measurement settled that it neither translates nor
names speakers (Japanese audio given `languageCodes: ["en-US"]` stays Japanese). The
open question was whether to drop `gem-transcribe`'s `--lang=en,ja` and
`--speaker-hint`; both are instead carried over as a **second pass that takes only the
transcript text**. The input changes from hours of audio to bounded text, so even the
part that still uses an LLM becomes sturdier than today's — and it can be skipped entirely.

**Language choice**: Go, because the counterpart tool needs voice-scribe's distribution
model. Before committing, the Go SDK's support for `AudioTranscriptionConfig` was
verified on real hardware, down to 30 lines of Go returning speaker-attributed
segments — the intent being to settle the precondition before Phase 1 starts, not during it.

**Input path**: inline first with GCS only past the limit, rather than always-GCS as
`gem-transcribe` did, because short audio then works without a staging bucket.

**MCP phase**: `voice-scribe` deferred MCP to Phase 2b, but gem-scribe has to be usable
as "the Gemini version of voice-scribe" from the start, so **Phase 1 carries both the
CLI and MCP**.

**Deliberately not implemented**: `customVocabulary`. Vocabulary biasing is attractive,
but measurement showed it truncates the transcript to the first turn and does so
silently, with `finishReason: STOP` rather than an error (reproduced twice). Recorded as
a preview-quality trap, to be re-evaluated at GA.
