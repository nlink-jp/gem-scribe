// Package tools implements the MCP tools exposed by `gem-scribe mcp`.
//
// The server is stateful and async: transcribe enqueues a job and returns a
// job_id, which check_job polls. Recordings live in a workspace the agent
// prepares, and transcripts are written under its output/ subdirectory.
//
// Results are not strictly file-mediated. A transcript is text, and making an
// agent read a file to see three lines of it wastes a round trip. The result
// carries the text up to max_bytes and counts what the cap left out; the file
// is written either way, because it is the product. See resultFor.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/nlink-jp/gem-scribe/internal/mcp/job"
	"github.com/nlink-jp/gem-scribe/internal/mcp/mcpserver"
	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
	"github.com/nlink-jp/gem-scribe/internal/mcp/workdir"
	"github.com/nlink-jp/gem-scribe/internal/mcp/workspace"
	"github.com/nlink-jp/gem-scribe/internal/transcript"
)

// Transcriber turns one recording into a transcript. It is an interface so the
// protocol tests can run against a fake without a GCP project, credentials, or
// a network.
type Transcriber interface {
	Transcribe(ctx context.Context, req Request) (transcript.Result, error)
}

// Request is the transcription request, with the audio path already verified to
// be a regular file inside the workspace.
type Request struct {
	// Audio is the absolute path of the recording.
	Audio string
	// Model overrides the configured transcription model.
	Model string
	// Languages are BCP-47 hints; empty means automatic detection.
	Languages []string
	// Diarize labels who is speaking.
	Diarize bool
	// WordTimestamps attaches word-level timings, which is what gives segments
	// their start and end.
	WordTimestamps bool
	// Smart asks for disfluency removal and light formatting. It cannot be
	// combined with the two flags above.
	Smart bool
	// TranslateTo adds a translation beside the original, in a second pass
	// over the transcript text. Empty means no translation.
	TranslateTo string
	// SpeakerHints are candidate names assigned to spk:N in that same second
	// pass. Empty means the labels stay as the model produced them.
	SpeakerHints []string
}

// Deps carries the shared dependencies of all tools.
type Deps struct {
	// WS manages workspaces (default root + agent-prepared roots).
	WS *workspace.Manager
	// WorkDir resolves and validates the per-call work directory: the
	// argument, then the request's _meta, then an error (organization
	// ADR-021). Build it with workdir.NewResolver; the zero value refuses
	// every call.
	WorkDir workdir.Resolver
	// Transcribe performs the actual work (real client or a test fake).
	Transcribe Transcriber
	// Jobs tracks background transcriptions via a single FIFO worker.
	Jobs *job.Manager
	// MaxBytes caps how much of the transcript a result carries. It bounds
	// the response and nothing else — the transcript file is written either
	// way. Zero uses DefaultMaxBytes; a negative value means no cap.
	MaxBytes int
	// Logger is optional.
	Logger *slog.Logger
}

// Register attaches all tools to the MCP server.
func Register(srv *mcpserver.Server, d *Deps) {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Jobs == nil {
		d.Jobs = job.NewManager(context.Background())
	}
	registerGetUsage(srv, d)
	registerTranscribe(srv, d)
	registerCheckJob(srv, d)
}

// retiredWorkDirNames are the spellings the work directory argument carried
// across the fleet before org ADR-021 settled on work_dir.
var retiredWorkDirNames = []string{"workspace_root", "workspaceRoot", "workspace_dir"}

// unmarshalStrict decodes tool arguments, rejecting unknown fields so agent
// typos surface as invalid_arguments instead of being silently ignored.
//
// A caller sending one of the retired work-directory spellings is told the new
// name rather than left to guess from "unknown field": the rename is ours, and
// a caller working from an older manual should need one turn to recover, not a
// schema re-read.
func unmarshalStrict(args json.RawMessage, into any) error {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		msg := err.Error()
		for _, old := range retiredWorkDirNames {
			if strings.Contains(msg, `unknown field "`+old+`"`) {
				return toolerr.Newf(toolerr.CodeWorkDirRequired,
					"%q was renamed to work_dir: pass the absolute path of a directory you can read back", old)
			}
		}
		return toolerr.Newf(toolerr.CodeInvalidArguments, "invalid arguments: %v", err)
	}
	return nil
}
