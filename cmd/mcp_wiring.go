package cmd

import (
	"context"
	"os"
	"path/filepath"

	"github.com/nlink-jp/gem-scribe/internal/asr"
	"github.com/nlink-jp/gem-scribe/internal/config"
	"github.com/nlink-jp/gem-scribe/internal/mcp/tools"
	"github.com/nlink-jp/gem-scribe/internal/transcript"
)

// defaultWorkspaceRoot is where workspaces live when the agent does not prepare
// one of its own. It sits under the data directory rather than the config
// directory: these are working files, and recordings can be large.
func defaultWorkspaceRoot() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "gem-scribe", "mcp-workspaces")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "gem-scribe", "mcp-workspaces")
	}
	return filepath.Join(home, ".local", "share", "gem-scribe", "mcp-workspaces")
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
