package cmd

import (
	"context"
	"fmt"
	"os"
	"syscall"

	"github.com/nlink-jp/gem-scribe/internal/config"
	"github.com/nlink-jp/gem-scribe/internal/mcp/job"
	"github.com/nlink-jp/gem-scribe/internal/mcp/mcpserver"
	"github.com/nlink-jp/gem-scribe/internal/mcp/tools"
	"github.com/nlink-jp/gem-scribe/internal/mcp/transport"
	"github.com/nlink-jp/gem-scribe/internal/mcp/workspace"
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
	defer protocolOut.Close()

	deps := &tools.Deps{
		WS:         workspace.NewManager(defaultWorkspaceRoot()),
		Transcribe: newMCPTranscriber(cfg),
		Jobs:       job.NewManager(cmd.Context()),
	}

	srv := mcpserver.New("gem-scribe", Version, transport.NewStdioTransport(os.Stdin, protocolOut), nil)
	srv.SetInstructions(tools.Instructions)
	tools.Register(srv, deps)

	if err := srv.Serve(cmd.Context()); err != nil && !isShutdown(err) {
		return err
	}
	return nil
}

// claimStdout duplicates the real stdout for exclusive use by the transport and
// redirects fd 1 to stderr.
//
// Returning an *os.File rather than writing through fd 1 is the point: after
// this call there is no way to reach the protocol stream except through the
// returned handle.
func claimStdout() (*os.File, error) {
	fd, err := syscall.Dup(int(os.Stdout.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate stdout for the MCP transport: %w", err)
	}
	if err := syscall.Dup2(int(os.Stderr.Fd()), int(os.Stdout.Fd())); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("redirect stdout to stderr: %w", err)
	}
	return os.NewFile(uintptr(fd), "mcp-stdout"), nil
}

// isShutdown reports whether err is the ordinary end of a session: the client
// closed stdin, or the context was cancelled.
func isShutdown(err error) bool {
	return err == context.Canceled || err == context.DeadlineExceeded
}
