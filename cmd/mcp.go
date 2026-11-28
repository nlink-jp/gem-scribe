package cmd

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nlink-jp/gem-scribe/internal/config"
	"github.com/nlink-jp/gem-scribe/internal/mcp/mcpserver"
	"github.com/nlink-jp/gem-scribe/internal/mcp/tools"
	"github.com/nlink-jp/gem-scribe/internal/mcp/transport"
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
	// SIGINT and SIGTERM cancel the server's ctx: running calls and jobs are
	// cancelled and Serve returns within the protocol layer's shutdown grace.
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, stop := notifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	// stdout is the JSON-RPC transport from here on. Take a private duplicate
	// of it and point fd 1 at stderr, so that anything which writes to "stdout"
	// — this program, a dependency, something added in five years — lands in
	// the log rather than corrupting the protocol.
	protocolOut, err := mcpClaim()
	if err != nil {
		return err
	}
	// The transport is done with by then; a failure to close it has nobody
	// left to tell.
	defer func() { _ = protocolOut.Close() }()

	// Jobs run under a ctx of their own beneath the server's, cancelled and
	// drained once Serve returns (drainJobs).
	jobCtx, cancelJobs := context.WithCancel(ctx)
	deps := newToolDeps(jobCtx, cfg)

	srv := mcpserver.New("gem-scribe", Version, transport.NewStdioTransport(mcpStdin, protocolOut), nil)
	srv.SetInstructions(tools.Instructions)
	tools.Register(srv, deps)

	// Serve returns nil for EOF and for a signal's cancellation.
	err = srv.Serve(ctx)
	drainJobs(cancelJobs, deps.Jobs, slog.Default())
	return err
}

// jobDrain bounds how long the server waits, on its way out, for a cancelled
// job to finish its own cleanup.
const jobDrain = 30 * time.Second

// drainJobs cancels the jobs and waits for the one running to unwind, so it
// finishes its cleanup before the process exits or what it uses is freed. A
// client that closed stdin can no longer collect a job's result, so EOF
// cancels them as a signal does.
func drainJobs(cancel context.CancelFunc, jobs interface{ Wait(time.Duration) bool }, logger *slog.Logger) {
	cancel()
	if !jobs.Wait(jobDrain) {
		logger.Warn("a cancelled job did not finish within the drain bound", "bound", jobDrain)
	}
}

// notifyContext is signal.NotifyContext, mcpStdin the process's stdin and
// mcpClaim the stdout claim; tests stand in for them.
var (
	notifyContext           = signal.NotifyContext
	mcpStdin      io.Reader = os.Stdin
	mcpClaim                = claimStdout
)
