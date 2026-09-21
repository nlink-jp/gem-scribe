package cmd

import (
	"context"
	"os"

	"github.com/nlink-jp/gem-scribe/internal/config"
	"github.com/nlink-jp/gem-scribe/internal/mcp/mcpserver"
	"github.com/nlink-jp/gem-scribe/internal/mcp/tools"
	"github.com/nlink-jp/gem-scribe/internal/mcp/transport"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve transcription over MCP (stdio)",
	Long: `mcp speaks the Model Context Protocol over stdin/stdout, giving an agent whose
model cannot process audio a way to read recordings.

Three tools: get_usage, transcribe, check_job. Transcription is asynchronous —
transcribe returns a job_id that check_job polls.`,
	Args: cobra.NoArgs,
	RunE: runMCP,
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}

func runMCP(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	// stdout is the JSON-RPC transport from here on. Take a private duplicate
	// of it and point fd 1 at stderr, so that anything which writes to "stdout"
	// — this program, a dependency, something added in five years — lands in
	// the log rather than corrupting the protocol.
	protocolOut, err := claimStdout()
	if err != nil {
		return err
	}
	// The transport is done with by then; a failure to close it has nobody
	// left to tell.
	defer func() { _ = protocolOut.Close() }()

	deps := newToolDeps(cmd.Context(), cfg)

	srv := mcpserver.New("gem-scribe", Version, transport.NewStdioTransport(os.Stdin, protocolOut), nil)
	srv.SetInstructions(tools.Instructions)
	tools.Register(srv, deps)

	if err := srv.Serve(cmd.Context()); err != nil && !isShutdown(err) {
		return err
	}
	return nil
}

// isShutdown reports whether err is the ordinary end of a session: the client
// closed stdin, or the context was cancelled.
func isShutdown(err error) bool {
	return err == context.Canceled || err == context.DeadlineExceeded
}
