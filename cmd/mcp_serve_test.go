package cmd

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/nlink-jp/mcp"
	"github.com/nlink-jp/mcp/client"

	"github.com/nlink-jp/gem-scribe/internal/mcp/tools"
)

// isolateMCP keeps the developer's environment out of the mcp subcommand: a
// home of its own, an empty config file, and stdin and the stdout claim
// standing in for the process's. It returns the server's stdout.
func isolateMCP(t *testing.T, stdin io.Reader) *os.File {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	cfgFile := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	origCfg, origIn, origClaim := configPath, mcpStdin, mcpClaim
	t.Cleanup(func() { configPath, mcpStdin, mcpClaim = origCfg, origIn, origClaim })
	configPath = cfgFile
	mcpStdin = stdin
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	mcpClaim = func() (*os.File, error) { return outW, nil }
	t.Cleanup(func() { _ = outR.Close() })
	return outR
}

// SIGINT and SIGTERM end the server through its ctx, even while stdin is
// open (server-migration §2), and both are the ones registered. Before the
// migration the subcommand handled no signal.
func TestASignalEndsTheMCPServer(t *testing.T) {
	inR, inW := io.Pipe()
	defer func() { _ = inW.Close() }()
	isolateMCP(t, inR)
	orig := notifyContext
	t.Cleanup(func() { notifyContext = orig })
	var registered []os.Signal
	notifyContext = func(parent context.Context, sigs ...os.Signal) (context.Context, context.CancelFunc) {
		registered = sigs
		ctx, cancel := context.WithCancel(parent)
		go func() { time.Sleep(100 * time.Millisecond); cancel() }() // the signal arrives
		return ctx, cancel
	}
	done := make(chan error, 1)
	go func() { done <- runMCP(mcpCmd, nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runMCP: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not end on the signal")
	}
	for _, want := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		if !slices.Contains(registered, want) {
			t.Errorf("%v is not among the signals that end the server: %v", want, registered)
		}
	}
}

// What the mcp subcommand serves opens with nlink-jp/mcp's client in the
// modern era, carries the instructions, lists the tools, and returns nil once
// stdin ends.
func TestMCPOpensWithTheModuleClient(t *testing.T) {
	inR, inW := io.Pipe()
	out := isolateMCP(t, inR)
	done := make(chan error, 1)
	go func() { done <- runMCP(mcpCmd, nil) }()
	conn, err := client.NewLineConn(out, inW, func() { _ = inW.Close() }, client.LineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl, err := client.Open(ctx, conn, client.Options{Client: mcp.Implementation{Name: "test", Version: "0"}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if cl.Era() != mcp.EraModern || cl.Server().Name != "gem-scribe" || cl.Instructions() != tools.Instructions {
		t.Errorf("era %v, server %+v, instructions %q", cl.Era(), cl.Server(), cl.Instructions())
	}
	list, err := cl.ListTools(ctx)
	if err != nil || len(list.Tools) != 3 {
		t.Fatalf("tools/list = %+v, %v", list, err)
	}
	_ = cl.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runMCP after EOF: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not end when stdin closed")
	}
}

// fakeJobs records the order of the cancellation and the wait.
type fakeJobs struct {
	cancelled         bool
	waitedAfterCancel bool
}

func (f *fakeJobs) Wait(time.Duration) bool { f.waitedAfterCancel = f.cancelled; return true }

// On its way out the server cancels the jobs and then waits for them, so a
// cancelled job's cleanup runs before the process exits or what it uses is
// freed.
func TestDrainJobsCancelsThenWaits(t *testing.T) {
	f := &fakeJobs{}
	drainJobs(func() { f.cancelled = true }, f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !f.cancelled || !f.waitedAfterCancel {
		t.Errorf("cancelled %v, waited after the cancellation %v", f.cancelled, f.waitedAfterCancel)
	}
}
