package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
)

// TestEnsureUnderRefusesLinkedWorkspaceDir: os.Root contains operations within
// a root but resolves the root path itself normally, so a link pre-planted at
// <work_dir>/<id> makes every later read and write anchor on the link's target
// — outside work_dir, and reported as success. EnsureUnder therefore compares
// real paths after creating the directory. Without that comparison this test
// finds output/ created in the outside directory and no error anywhere.
func TestEnsureUnderRefusesLinkedWorkspaceDir(t *testing.T) {
	// EvalSymlinks first: on macOS t.TempDir() sits under /var, which is
	// itself a link to /private/var, so an unresolved work_dir would differ
	// from the resolved base for reasons unrelated to the attack.
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(work, "proj")); err != nil {
		t.Fatal(err)
	}

	m := NewManager()
	w, err := m.EnsureUnder(work, "proj")
	if err == nil {
		t.Fatalf("EnsureUnder on a linked workspace dir succeeded: base=%q", w.BaseDir)
	}
	if !errors.Is(err, toolerr.New(toolerr.CodePathNotAllowed, "")) {
		t.Errorf("err = %v, want path_not_allowed", err)
	}
	if !strings.Contains(err.Error(), "proj") {
		t.Errorf("err %q does not name the workspace id", err)
	}
	// Nothing may have been created in the link's target.
	entries, rerr := os.ReadDir(outside)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("link target was written into: %v", names)
	}
}
