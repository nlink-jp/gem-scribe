// Package tools implements the MCP tools exposed by `gem-scribe mcp`.
//
// The server is stateful and async: transcribe enqueues a job and returns a
// job_id, which check_job polls. Recordings live in a workspace the agent
// prepares, and transcripts are written under its output/ subdirectory.
//
// Results are not strictly file-mediated. A transcript is text, and making an
// agent read a file to see three lines of it wastes a round trip. Short
// transcripts come back inline; long ones come back as a path plus an excerpt.
// See resultFor.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"

	"github.com/nlink-jp/gem-scribe/internal/mcp/job"
	"github.com/nlink-jp/gem-scribe/internal/mcp/mcpserver"
	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
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
}

// Deps carries the shared dependencies of all tools.
type Deps struct {
	// WS manages workspaces (default root + agent-prepared roots).
	WS *workspace.Manager
	// Transcribe performs the actual work (real client or a test fake).
	Transcribe Transcriber
	// Jobs tracks background transcriptions via a single FIFO worker.
	Jobs *job.Manager
	// InlineThreshold is the transcript size in bytes at or below which the
	// text is returned inline instead of only as a path. Zero uses
	// DefaultInlineThreshold.
	InlineThreshold int
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

// unmarshalStrict decodes tool arguments, rejecting unknown fields so agent
// typos surface as invalid_arguments instead of being silently ignored.
func unmarshalStrict(args json.RawMessage, into any) error {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return toolerr.Newf(toolerr.CodeInvalidArguments, "invalid arguments: %v", err)
	}
	return nil
}
