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

	m := NewManager(allowAll)
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

// allowAll stands for the server's check in tests of the manager's own
// mechanics; the check itself is workdir.Resolver.CheckBeneath's.
func allowAll(string) error { return nil }

// The directory actually used is judged before it is made: the check sees
// <work_dir>/<workspace_id>, and its refusal is returned as is, with nothing
// created. A Manager without a check refuses every workspace.
func TestEnsureUnderJudgesTheWorkspaceDirectoryBeforeMakingIt(t *testing.T) {
	work := t.TempDir()
	refusal := errors.New("refused")
	var seen string
	m := NewManager(func(dir string) error { seen = dir; return refusal })
	if _, err := m.EnsureUnder(work, "gh"); !errors.Is(err, refusal) {
		t.Fatalf("EnsureUnder = %v, want the check's refusal", err)
	}
	if want := filepath.Join(work, "gh"); seen != want {
		t.Errorf("the check saw %q, want %q", seen, want)
	}
	if _, err := os.Stat(filepath.Join(work, "gh")); !os.IsNotExist(err) {
		t.Errorf("a refused workspace was created (stat: %v)", err)
	}
	for name, m := range map[string]*Manager{"no check": NewManager(nil), "zero": {}} {
		if _, err := m.EnsureUnder(work, "ws"); err == nil {
			t.Errorf("%s: EnsureUnder succeeded", name)
		}
	}
}
