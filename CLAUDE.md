# CLAUDE.md — gem-scribe

**Organization rules (mandatory): https://github.com/nlink-jp/.github/blob/main/CONVENTIONS.md**

## Project overview

Speech-to-text CLI and MCP server on Vertex AI's dedicated transcription model.
Cloud counterpart of voice-scribe, successor to gem-transcribe. Part of
util-series. See [AGENTS.md](AGENTS.md) for structure and gotchas — they are not
duplicated here.

## Non-negotiable rules

- **Tests are mandatory** — write them with the implementation.
- **Never `go build` directly** — always `make build` (outputs to `dist/`).
- **Docs in sync** — `README.md` and `README.ja.md` change together.
- **`usage.md` is part of the code** — a new tool, argument or error code is not
  done until the manual documents it. The tests enforce this.
- **Measured facts carry their measurement** — the gotchas in AGENTS.md exist
  because someone ran the call. Do not soften them into guesses, and do not
  delete one without measuring again.
- **Small, typed commits** — `feat:`, `fix:`, `test:`, `chore:`, `docs:`.

## Build & test

```bash
make check          # vet → test → build
make test           # go test ./...
```

The suite runs without a GCP project, credentials or a network: the MCP tests
use a fake transcriber and the parser tests use recorded responses.

## Key dependencies

- `google.golang.org/genai` — Vertex AI SDK (needs ≥ v1.70.0 for `AudioTranscriptionConfig`)
- `cloud.google.com/go/storage` — staging bucket
- `github.com/spf13/cobra` — CLI framework
- `github.com/BurntSushi/toml` — config file
