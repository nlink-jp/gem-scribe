// Package workspace manages per-project state directories.
//
// One workspace = one transcription project. The agent places the recordings it
// wants transcribed inside it; the server writes only under the output/
// subdirectory. Layout:
//
//	<work_dir>/<id>/
//	├── <recordings>         agent-placed inputs (any relative layout)
//	└── output/              transcripts (server-written)
//
// Ported from image-forge's MCP server, which is where the os.Root design and
// the agent-prepared-root convention come from.
//
// Workspaces exist only under the caller's work directory, which arrives per
// call and is validated by internal/mcp/workdir (org ADR-021; project
// ADR-0002). There is no server-owned default root: a directory the caller
// cannot read back turns a successful call into a path to nothing. Because the
// work directory is agent-writable, every server I/O inside a workspace goes
// through os.Root so symlinks planted in the workspace cannot make the server
// read or write outside it (kernel-enforced containment).
package workspace

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
)

// DirOutput is the server-written subdirectory holding rendered images.
const DirOutput = "output"

// Workspace is a validated, materialized per-project directory.
type Workspace struct {
	ID      string
	BaseDir string
}

// Path joins parts under the workspace base directory. Use it for DISPLAY and
// for handing paths to external processes after VerifyRegular; all server-side
// file I/O must go through the os.Root-backed helpers below.
func (w *Workspace) Path(parts ...string) string {
	return filepath.Join(append([]string{w.BaseDir}, parts...)...)
}

// ResolveInside lexically validates an agent-supplied relative path and
// returns it cleaned (workspace-relative). It exists for early, friendly
// path_not_allowed errors; the enforcement boundary is os.Root.
func (w *Workspace) ResolveInside(rel string) (string, error) {
	if rel == "" {
		return "", toolerr.New(toolerr.CodePathNotAllowed, "path must not be empty")
	}
	if filepath.IsAbs(rel) {
		return "", toolerr.Newf(toolerr.CodePathNotAllowed,
			"path %q must be relative to the workspace root", rel)
	}
	cleaned := filepath.Clean(rel)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", toolerr.Newf(toolerr.CodePathNotAllowed,
			"path %q escapes the workspace root", rel)
	}
	return cleaned, nil
}

// openRoot opens the kernel-enforced containment anchor for this workspace.
func (w *Workspace) openRoot() (*os.Root, error) {
	r, err := os.OpenRoot(w.BaseDir)
	if err != nil {
		return nil, toolerr.Newf(toolerr.CodeWorkspaceFailed, "open workspace root: %v", err)
	}
	return r, nil
}

// mapRootErr converts os.Root escape errors into path_not_allowed so agents
// get the same stable code as the lexical pre-check.
func mapRootErr(op, rel string, err error) error {
	if err == nil {
		return nil
	}
	var pe *fs.PathError
	if errors.As(err, &pe) && strings.Contains(pe.Err.Error(), "escapes") {
		return toolerr.Newf(toolerr.CodePathNotAllowed,
			"%s %q: path escapes the workspace root (symlink?)", op, rel)
	}
	return err
}

// ReadFile reads a workspace-relative file with symlink containment.
func (w *Workspace) ReadFile(rel string) ([]byte, error) {
	r, err := w.openRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	b, err := r.ReadFile(rel)
	return b, mapRootErr("read", rel, err)
}

// WriteFileAtomic writes a workspace-relative file via temp+rename, fully
// inside the containment root.
func (w *Workspace) WriteFileAtomic(rel string, data []byte) error {
	r, err := w.openRoot()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if dir := filepath.Dir(rel); dir != "." {
		if err := r.MkdirAll(dir, 0o755); err != nil {
			// A path component replaced by a symlink (or file) surfaces as
			// ErrExist here because os.Root refuses to traverse it.
			if errors.Is(err, fs.ErrExist) {
				return toolerr.Newf(toolerr.CodePathNotAllowed,
					"mkdir %q: a path component is not a real directory (symlink?)", dir)
			}
			return mapRootErr("mkdir", dir, err)
		}
	}
	tmp := rel + ".tmp"
	if err := r.WriteFile(tmp, data, 0o644); err != nil {
		return mapRootErr("write", tmp, err)
	}
	if err := r.Rename(tmp, rel); err != nil {
		return mapRootErr("rename", rel, err)
	}
	return nil
}

// Stat stats a workspace-relative path with symlink containment.
func (w *Workspace) Stat(rel string) (fs.FileInfo, error) {
	r, err := w.openRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	fi, err := r.Stat(rel)
	return fi, mapRootErr("stat", rel, err)
}

// MkdirAll creates a workspace-relative directory tree.
func (w *Workspace) MkdirAll(rel string) error {
	r, err := w.openRoot()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return mapRootErr("mkdir", rel, r.MkdirAll(rel, 0o755))
}

// RemoveAll removes a workspace-relative tree.
func (w *Workspace) RemoveAll(rel string) error {
	r, err := w.openRoot()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return mapRootErr("remove", rel, r.RemoveAll(rel))
}

// VerifyRegular confirms rel is a regular file (not a symlink) inside the
// workspace. Call it immediately before handing w.Path(rel) to the decoder
// (which cannot inherit os.Root) as a recording to transcribe. The remaining
// check-to-use race is accepted under the local single-user threat model.
//
// A missing file returns input_not_found; a symlink or other non-regular entry
// returns path_not_allowed so callers can distinguish "not there" from "refused
// for safety".
func (w *Workspace) VerifyRegular(rel string) error {
	r, err := w.openRoot()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	fi, err := r.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Name the path that was actually looked at, and the escape.
			// "Place it in the workspace" without saying where sent a real
			// agent off inventing ~/sessions/current_session/work and cost it
			// four rounds (2026-09-14): a relative name is workspace-relative,
			// and the workspace is a level below the work directory the caller
			// named, which is not where an agent naturally puts a file.
			return toolerr.Newf(toolerr.CodeInputNotFound,
				"input %q is not in the workspace: looked for %s. A relative name is "+
					"resolved inside the workspace, so either put the file there, or pass "+
					"the absolute path of where it already is — a recording is read in "+
					"place, from anywhere you can read", rel, w.Path(rel))
		}
		return mapRootErr("lstat", rel, err)
	}
	if !fi.Mode().IsRegular() {
		return toolerr.Newf(toolerr.CodePathNotAllowed,
			"%q is not a regular file (mode %s)", rel, fi.Mode())
	}
	return nil
}

// Manager materializes workspaces under the work directory a call names.
// It holds no default root of its own — see the package comment.
type Manager struct{}

// NewManager returns a Manager.
func NewManager() *Manager { return &Manager{} }

// EnsureUnder materializes <workDir>/<id> and its output/ subdirectory
// (idempotent). workDir must be an absolute path to an existing directory the
// caller can read back; workdir.Resolver.Resolve is what establishes that, and
// this method assumes it has already run.
func (m *Manager) EnsureUnder(workDir, id string) (*Workspace, error) {
	if !filepath.IsAbs(workDir) {
		return nil, toolerr.Newf(toolerr.CodeWorkDirInvalid,
			"work_dir %q must be an absolute path", workDir)
	}
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	workDir = filepath.Clean(workDir)
	if err := makeBaseDir(workDir, id); err != nil {
		return nil, err
	}
	w := &Workspace{ID: id, BaseDir: filepath.Join(workDir, id)}
	if err := w.MkdirAll(DirOutput); err != nil {
		return nil, err
	}
	return w, nil
}

// makeBaseDir creates <workDir>/<id> and refuses a workspace whose directory
// is not really there. work_dir is the one path the caller vouched for; <id>
// beneath it may be a link, planted by any other tool with write access to the
// work directory. The directory is made through an os.Root on work_dir, which
// refuses a path that leaves it, and what was made is then compared with what
// was asked for, because the base directory is afterwards handed to code
// outside any root: os.Root contains operations *within* the root but resolves
// the root path itself normally, so os.OpenRoot on a planted link anchors on
// the link's target and every subsequent read and write lands outside work_dir
// while reporting success.
func makeBaseDir(workDir, id string) error {
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return toolerr.Newf(toolerr.CodeWorkspaceFailed, "open work_dir: %v", err)
	}
	defer func() { _ = root.Close() }()
	// Mkdir, not MkdirAll: the work directory itself is the caller's and must
	// already exist, so a missing parent here is a caller mistake worth
	// hearing about rather than a tree to conjure up.
	if err := root.Mkdir(id, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return toolerr.Newf(toolerr.CodeWorkspaceFailed, "create workspace dir: %v", err)
	}
	realBase, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return toolerr.Newf(toolerr.CodeWorkspaceFailed, "resolve work_dir: %v", err)
	}
	want := filepath.Join(realBase, id)
	got, err := filepath.EvalSymlinks(filepath.Join(workDir, id))
	if err != nil {
		return toolerr.Newf(toolerr.CodeWorkspaceFailed, "resolve workspace dir: %v", err)
	}
	if got != want {
		return toolerr.Newf(toolerr.CodePathNotAllowed,
			"refused: workspace %q is a link to %s, not a directory under work_dir", id, got)
	}
	return nil
}
