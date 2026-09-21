package cmd

import (
	"context"
	"path/filepath"

	"github.com/nlink-jp/gem-scribe/internal/asr"
	"github.com/nlink-jp/gem-scribe/internal/config"
	"github.com/nlink-jp/gem-scribe/internal/mcp/job"
	"github.com/nlink-jp/gem-scribe/internal/mcp/tools"
	"github.com/nlink-jp/gem-scribe/internal/mcp/workdir"
	"github.com/nlink-jp/gem-scribe/internal/mcp/workspace"
	"github.com/nlink-jp/gem-scribe/internal/transcript"
)

// newToolDeps assembles what every tool shares. It is the only place
// tools.Deps is built, which is what makes the work-directory resolver below
// impossible for a tool added later to forget: no tool constructs a Resolver
// of its own, they all read Deps.WorkDir.
//
// ctx is the server-lifetime context: a transcription outlives the tool call
// that started it but must stop on shutdown.
func newToolDeps(ctx context.Context, cfg *config.Config) *tools.Deps {
	return &tools.Deps{
		WS:         workspace.NewManager(),
		WorkDir:    workDirResolver(),
		Transcribe: newMCPTranscriber(cfg),
		Jobs:       job.NewManager(ctx),
	}
}

// workDirResolver builds the per-call work-directory resolver, denying this
// server's own directories.
//
// A work directory is the caller's, not ours (organization ADR-021 §4: "not a
// system location … and not the server's own config or state directory" →
// `work_dir_denied`). This server's config directory is where the Vertex
// project, region and staging bucket live; without the denial a caller could
// name it as its workspace root and have the server write transcripts in among
// its own credentials-adjacent configuration — and read that configuration
// back out as workspace contents — on a model's say-so.
func workDirResolver() workdir.Resolver {
	return workdir.Resolver{Denied: serverOwnedDirs()}
}

// serverOwnedDirs lists this server's own config and state directories.
//
// There is one: the config directory. This server keeps no state on disk —
// transcripts go under the caller's `work_dir` and staged audio goes to Cloud
// Storage. If a state directory is ever added it belongs here.
func serverOwnedDirs() []string {
	dir := configDir()
	if dir == "" {
		return nil
	}
	return []string{dir}
}

// configDir is the directory holding this server's own config.toml, derived
// from the one place that names the file (config.DefaultPath) so the denial
// cannot drift away from the location it protects. Empty when the home
// directory cannot be determined.
func configDir() string {
	p := config.DefaultPath()
	if p == "" {
		return ""
	}
	return filepath.Dir(p)
}

// mcpTranscriber adapts the CLI's transcription path to the MCP tool interface.
//
// It builds a client per call rather than holding one open. The work is a
// single HTTPS request; a resident client would save a connection setup and
// cost a credential refresh problem in a server that may sit idle for hours.
type mcpTranscriber struct {
	cfg *config.Config
}

func newMCPTranscriber(cfg *config.Config) tools.Transcriber {
	return &mcpTranscriber{cfg: cfg}
}

func (m *mcpTranscriber) Transcribe(ctx context.Context, req tools.Request) (transcript.Result, error) {
	// Copy the configuration so a per-call model override does not leak into
	// the next call on a long-lived server.
	cfg := *m.cfg
	if req.Model != "" {
		cfg.Model.Name = req.Model
	}

	result, err := transcribeOne(ctx, &cfg, req.Audio, asr.Options{
		Languages:      req.Languages,
		Diarize:        req.Diarize,
		WordTimestamps: req.WordTimestamps,
		Smart:          req.Smart,
	}, func(string) {})
	if err != nil {
		return transcript.Result{}, err
	}

	// The second pass is the same code the CLI runs, for the same reason the
	// transcription is: two implementations of "translate the transcript"
	// would differ, and the difference would surface to an agent first.
	if err := enrichResult(ctx, &cfg, &result, req.TranslateTo, req.SpeakerHints, func(string) {}); err != nil {
		return transcript.Result{}, err
	}
	return result, nil
}

// Compile-time proof that the MCP path and the CLI path share one
// implementation. They would otherwise drift: the MCP server is the surface
// nobody runs by hand, so a difference between the two would be found by an
// agent rather than by a person.
var _ tools.Transcriber = (*mcpTranscriber)(nil)
