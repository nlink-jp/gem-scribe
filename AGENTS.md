# AGENTS.md — gem-scribe

## Project summary

Speech-to-text CLI and MCP server on Vertex AI's dedicated transcription model
(`gemini-3.5-transcribe`). Cloud counterpart of voice-scribe; successor to the
archived gem-transcribe. Part of util-series.

The design premise: the model returns speaker turns and word timings as
structured response parts, so nothing asks a language model to emit JSON. That
is what removed gem-transcribe's failure mode on long audio.

## Build commands

```bash
make build          # → dist/gem-scribe
make test           # go test ./...
make test-linux     # same suite on Linux (container)
make check          # vet → test → build
make build-all      # cross-compile 4 platforms (darwin arm64 only)
make package        # archives + notarize darwin
make verify-release # gate: notarization marker + freshness (run before upload)
make brew           # generate the tap formula from the built darwin zip
```

## Module path

`github.com/nlink-jp/gem-scribe`

## Key structure

```
gem-scribe/
├── main.go
├── cmd/
│   ├── root.go            ← cobra root; the root command IS the transcribe command
│   ├── transcribe.go      ← flags, the CLI pipeline, output writing
│   ├── mcp.go             ← `mcp` subcommand, stdout claim
│   └── mcp_wiring.go      ← adapts the CLI pipeline to the MCP tool interface
├── internal/
│   ├── config/            ← TOML + GEMSCRIBE_* / GOOGLE_CLOUD_* env
│   ├── asr/               ← Vertex AI client (asr.go) + response mapping (parse.go)
│   ├── enrich/            ← second pass: translation and speaker naming, over text
│   ├── staging/           ← inline-vs-GCS decision, upload and cleanup
│   ├── transcript/        ← output envelope, formatters, diagnosis
│   └── mcp/               ← jsonrpc, transport, mcpserver, job, workdir, workspace, tools
└── docs/{en,ja}/          ← RFP, ADRs
```

## Environment variables

- `GEMSCRIBE_PROJECT` (required) — GCP project ID
- `GEMSCRIBE_LOCATION` (default: `global`) — Vertex AI location
- `GEMSCRIBE_MODEL` (default: `gemini-3.5-transcribe-preview`)
- `GEMSCRIBE_SECOND_PASS_MODEL` (default: `gemini-3.7-flash`) — translation and speaker naming
- `GEMSCRIBE_STAGING_BUCKET` — only for audio above 20 MB
- Falls back to `GOOGLE_CLOUD_PROJECT` / `GOOGLE_CLOUD_LOCATION`

## Gotchas

Everything here was measured, not assumed.

- **Containment: the workspace base is verified by real path, because the path
  is handed to code outside any root.** `os.Root` contains operations *within*
  the root but resolves the root path itself normally — measured: `os.OpenRoot`
  on a symlink anchors on the link's target and writes through it without
  error. A link planted at `<work_dir>/<id>` therefore put every read and write
  outside `work_dir` while the call reported success. `workspace.makeBaseDir`
  creates the directory through an `os.Root` on `work_dir` **and** compares
  `filepath.EvalSymlinks` of the result against `<real work_dir>/<id>`; the
  comparison is the part that must stay, because `w.BaseDir` is afterwards
  passed to `os.OpenRoot` and to the staging/upload path.
  `TestEnsureUnderRefusesLinkedWorkspaceDir` pins it.
- **The Gemini 3 family is served from `global` only.** Regional endpoints
  return 404 with a message about the model name. `asr.hintGlobalEndpoint`
  annotates it; do not remove that without a replacement.
- **`customVocabulary` truncates the transcript to its first turn**, silently,
  with `finishReason: STOP`. `generateConfig` must never set it. There is a test.
- **The response never carries `languageCode`**, though the SDK type declares
  it. The envelope's language key comes from the caller's hint and falls back to
  `"und"`.
- **The API does not translate.** `languageCodes` is a detection hint, not an
  output language. Translation is a second pass over the transcript text.
- **Speakers are `spk:N` only.** Real names are a second pass, not ASR.
- **Acoustically similar voices come back as one speaker** with no error.
  `transcript.Diagnose` reports it; the MCP result surfaces it as `warning`.
- **Word timestamps are what give segments their start and end.** Without them
  every segment is zero-length, which is why `srt`/`vtt` are refused.
- **`--smart` excludes diarization and word timestamps.** Passing it alone turns
  both off rather than reporting a conflict the user never chose. The MCP
  `transcribe` tool does the same.
- **Inline requests accept far more than documented** — 87 MB of audio was
  measured to work — but `staging.InlineLimitBytes` stays at 20 MB because that
  headroom is not a contract.
- **The second pass never lets a model author structure either** (ADR-0001
  applies to it). Translation uses a numbered-line protocol: we number the
  slots, the model fills them, and a line that does not come back keeps its
  original. Never replace this with "ask for a JSON array of segments" — that
  is the exact failure the tool was built to remove, reintroduced one layer
  later.
- **A partial second pass must stay visible.** `transcript.Diagnose` derives
  the untranslated count and unresolved labels from the result itself rather
  than trusting a recorded number, so it stays true whichever path produced it.
- **The transcription model is preview.** No GA equivalent exists. The
  second-pass model is deliberately GA so a supporting stage does not inherit a
  preview retirement schedule.

## Testing notes

- `internal/asr/testdata/*.json` are **real recorded API responses**, not
  hand-written fixtures. Regenerate them from a live call if the shape changes.
- `internal/mcp/tools/usage_test.go` machine-checks `usage.md` against the code:
  every registered tool, every `transcribe` argument, and every error code the
  package emits must appear in the manual.
- The MCP tests use a fake transcriber, so the suite needs no GCP project,
  credentials, or network.
