package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
)

func TestValidateID(t *testing.T) {
	ok := []string{"a", "deck", "my-project_1", "ABC123"}
	for _, id := range ok {
		if err := ValidateID(id); err != nil {
			t.Errorf("ValidateID(%q) = %v, want nil", id, err)
		}
	}
	bad := []string{"", "has space", "dot.name", "slash/name", "..", string(make([]byte, 65))}
	for _, id := range bad {
		if err := ValidateID(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("ValidateID(%q) = %v, want invalid_workspace_id", id, err)
		}
	}
}

func TestResolveInsideRejectsEscape(t *testing.T) {
	w := &Workspace{ID: "x", BaseDir: t.TempDir()}
	for _, rel := range []string{"../secret", "..", "/etc/passwd", ""} {
		if _, err := w.ResolveInside(rel); !errors.Is(err, toolerr.New(toolerr.CodePathNotAllowed, "")) {
			t.Errorf("ResolveInside(%q) = %v, want path_not_allowed", rel, err)
		}
	}
	got, err := w.ResolveInside("images/p01.png")
	if err != nil {
		t.Fatalf("ResolveInside good path: %v", err)
	}
	if got != filepath.Join("images", "p01.png") {
		t.Errorf("cleaned = %q", got)
	}
}

func TestEnsureUnderWorkDir(t *testing.T) {
	m := NewManager()
	work := t.TempDir()
	w, err := m.EnsureUnder(work, "proj")
	if err != nil {
		t.Fatalf("EnsureUnder: %v", err)
	}
	if w.BaseDir != filepath.Join(work, "proj") {
		t.Errorf("base = %q", w.BaseDir)
	}
	// output/ must exist.
	if fi, err := os.Stat(filepath.Join(w.BaseDir, DirOutput)); err != nil || !fi.IsDir() {
		t.Errorf("output dir missing: %v", err)
	}
	// Idempotent: a second call reuses the same tree.
	if again, err := m.EnsureUnder(work, "proj"); err != nil || again.BaseDir != w.BaseDir {
		t.Errorf("second EnsureUnder = %v, %v", again, err)
	}
}

func TestEnsureUnderRejectsRelativeWorkDir(t *testing.T) {
	m := NewManager()
	if _, err := m.EnsureUnder("relative/dir", "proj"); !errors.Is(err, toolerr.New(toolerr.CodeWorkDirInvalid, "")) {
		t.Errorf("relative work_dir: %v, want work_dir_invalid", err)
	}
}

// TestEnsureUnderDoesNotConjureTheWorkDir: the work directory is the caller's
// and always exists, so a missing one is a typo. Creating it would put the
// transcript somewhere the caller is not looking — the failure this contract
// exists to remove (ADR-0002).
func TestEnsureUnderDoesNotConjureTheWorkDir(t *testing.T) {
	m := NewManager()
	missing := filepath.Join(t.TempDir(), "not-there")
	if _, err := m.EnsureUnder(missing, "proj"); err == nil {
		t.Fatal("EnsureUnder under a missing work_dir succeeded")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("work_dir was created: %v", err)
	}
}

func TestVerifyRegularSymlinkRejected(t *testing.T) {
	m := NewManager()
	w, err := m.EnsureUnder(t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	// A real file inside the workspace verifies OK.
	if err := w.WriteFileAtomic("init.png", []byte("img")); err != nil {
		t.Fatal(err)
	}
	if err := w.VerifyRegular("init.png"); err != nil {
		t.Errorf("regular file: %v", err)
	}
	// A missing file is input_not_found (not path_not_allowed).
	if err := w.VerifyRegular("nope.png"); !errors.Is(err, toolerr.New(toolerr.CodeInputNotFound, "")) {
		t.Errorf("missing input: %v, want input_not_found", err)
	}
	// A symlink pointing outside the workspace is refused.
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(w.BaseDir, "evil.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := w.VerifyRegular("evil.png"); !errors.Is(err, toolerr.New(toolerr.CodePathNotAllowed, "")) {
		t.Errorf("symlink input: %v, want path_not_allowed", err)
	}
}

func TestReadFileSymlinkEscapeRejected(t *testing.T) {
	m := NewManager()
	w, err := m.EnsureUnder(t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(w.BaseDir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ReadFile("link.txt"); !errors.Is(err, toolerr.New(toolerr.CodePathNotAllowed, "")) {
		t.Errorf("read through escaping symlink: %v, want path_not_allowed", err)
	}
}
