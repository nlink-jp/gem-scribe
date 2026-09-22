package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nlink-jp/gem-scribe/internal/config"
	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
	"github.com/nlink-jp/gem-scribe/internal/mcp/workdir"
)

// The layer these tests observe is the wiring: the Resolver the server hands
// every tool, taken out of the tools.Deps that newToolDeps actually builds.
// The resolver's own denial mechanics are covered a layer below, in
// internal/mcp/workdir. A test that built its own Resolver{Denied: ...} would
// prove the mechanism and keep passing with the wiring deleted, which is the
// defect this file exists to catch.

// wantDenied asserts that dir is refused with work_dir_denied. It reports an
// accepted directory in those words, because "accepted" is the failure the
// reader needs, not "nil is not a structured error".
func wantDenied(t *testing.T, r workdir.Resolver, dir string) {
	t.Helper()
	_, err := r.Validate(dir)
	if err == nil {
		t.Fatalf("Validate(%q) accepted the server's own directory; want %s",
			dir, toolerr.CodeWorkDirDenied)
	}
	var te *toolerr.Error
	if !errors.As(err, &te) {
		t.Fatalf("Validate(%q) = %v, which is not a structured tool error", dir, err)
	}
	if te.Code != toolerr.CodeWorkDirDenied {
		t.Errorf("Validate(%q) = %s, want %s", dir, te.Code, toolerr.CodeWorkDirDenied)
	}
}

// serverWorkDir returns the resolver as the running server configures it,
// with HOME pointed at a directory this test owns so the server's own
// directories can be created and named. Reaching it through newToolDeps is
// deliberate: nothing here restates the denied list.
func serverWorkDir(t *testing.T) (workdir.Resolver, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	dir := configDir()
	if dir == "" {
		t.Fatal("configDir() is empty with HOME set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return newToolDeps(context.Background(), &config.Config{}).WorkDir, dir
}

func TestWorkDirRefusesServerConfigDir(t *testing.T) {
	r, cfgDir := serverWorkDir(t)
	wantDenied(t, r, cfgDir)
}

// A subdirectory is the obvious way around a check that only compares the
// directory itself.
func TestWorkDirRefusesInsideServerConfigDir(t *testing.T) {
	r, cfgDir := serverWorkDir(t)
	inside := filepath.Join(cfgDir, "workspaces")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	wantDenied(t, r, inside)
}

func TestWorkDirAcceptsOrdinaryDir(t *testing.T) {
	r, _ := serverWorkDir(t)
	ordinary := t.TempDir()
	got, err := r.Validate(ordinary)
	if err != nil {
		t.Fatalf("Validate(%q) = %v, want accepted", ordinary, err)
	}
	want, err := filepath.EvalSymlinks(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("Validate = %q, want the symlink-resolved %q", got, want)
	}
}

// The wired workspace manager judges the directory a call actually uses:
// work_dir=~/.config with workspace_id=gh would land in ~/.config/gh.
func TestTheWiredWorkspacesRefuseACredentialDirectoryBeneathAWorkDir(t *testing.T) {
	serverWorkDir(t) // HOME is a directory this test owns, with ~/.config in it
	cfg := filepath.Join(os.Getenv("HOME"), ".config")
	ws := newToolDeps(context.Background(), &config.Config{}).WS
	_, err := ws.EnsureUnder(cfg, "gh")
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeWorkDirDenied {
		t.Errorf("EnsureUnder(~/.config, gh) = %v, want %s", err, toolerr.CodeWorkDirDenied)
	}
}
