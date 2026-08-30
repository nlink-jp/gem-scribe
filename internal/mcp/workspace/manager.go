// Package workspace manages per-project state directories.
//
// One workspace = one transcription project. The agent places the recordings it
// wants transcribed inside it; the server writes only under the output/
// subdirectory. Layout:
//
//	<workspace_root>/<id>/
//	├── <recordings>         agent-placed inputs (any relative layout)
//	└── output/              transcripts (server-written)
//
// The os.Root design and the agent-prepared-root convention are the org's
// standard for MCP servers that touch agent-writable directories.
//
// The workspace root is either the server-configured default
// (~/.local/share/gem-scribe/mcp-workspaces) or an agent-prepared directory
// passed per call as workspace_root ("the server works in the workplace the
// agent prepared"). Because agent-prepared roots are agent-writable, every
// server I/O inside a workspace goes through os.Root so symlinks planted in the
// workspace cannot make the server read or write outside it (kernel-enforced
// containment).
package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
)

// DirOutput is the server-written subdirectory holding transcripts.
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
	defer r.Close()
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
	defer r.Close()
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
	defer r.Close()
	fi, err := r.Stat(rel)
	return fi, mapRootErr("stat", rel, err)
}

// MkdirAll creates a workspace-relative directory tree.
func (w *Workspace) MkdirAll(rel string) error {
	r, err := w.openRoot()
	if err != nil {
		return err
	}
	defer r.Close()
	return mapRootErr("mkdir", rel, r.MkdirAll(rel, 0o755))
}

// RemoveAll removes a workspace-relative tree.
func (w *Workspace) RemoveAll(rel string) error {
	r, err := w.openRoot()
	if err != nil {
		return err
	}
	defer r.Close()
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
	defer r.Close()
	fi, err := r.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return toolerr.Newf(toolerr.CodeInputNotFound,
				"input %q is not in the workspace — place it there first", rel)
		}
		return mapRootErr("lstat", rel, err)
	}
	if !fi.Mode().IsRegular() {
		return toolerr.Newf(toolerr.CodePathNotAllowed,
			"%q is not a regular file (mode %s)", rel, fi.Mode())
	}
	return nil
}

// Manager creates, lists, and deletes workspaces under the server's default
// root directory, and materializes workspaces under agent-prepared roots.
type Manager struct {
	root string
}

// NewManager returns a Manager whose default root is dir.
func NewManager(dir string) *Manager {
	return &Manager{root: filepath.Clean(dir)}
}

// Root returns the default workspace root directory.
func (m *Manager) Root() string { return m.root }

// Ensure validates id and creates the workspace directory tree under the
// default root (idempotent).
func (m *Manager) Ensure(id string) (*Workspace, error) {
	return m.ensureUnder(m.root, id, true)
}

// EnsureIn materializes a workspace under an agent-prepared root. rootDir must
// be an absolute path to an existing directory — "prepared" simply means the
// agent created it in a location it can write. An empty rootDir falls back to
// the default root.
func (m *Manager) EnsureIn(rootDir, id string) (*Workspace, error) {
	if rootDir == "" {
		return m.Ensure(id)
	}
	if !filepath.IsAbs(rootDir) {
		return nil, toolerr.Newf(toolerr.CodePathNotAllowed,
			"workspace_root %q must be an absolute path", rootDir)
	}
	fi, err := os.Stat(rootDir)
	if err != nil {
		return nil, toolerr.Newf(toolerr.CodePathNotAllowed,
			"workspace_root %q does not exist — create it first (the agent prepares the workplace)", rootDir)
	}
	if !fi.IsDir() {
		return nil, toolerr.Newf(toolerr.CodePathNotAllowed,
			"workspace_root %q is not a directory", rootDir)
	}
	return m.ensureUnder(filepath.Clean(rootDir), id, false)
}

func (m *Manager) ensureUnder(root, id string, createRoot bool) (*Workspace, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	base := filepath.Join(root, id)
	if createRoot {
		if err := os.MkdirAll(base, 0o755); err != nil {
			return nil, toolerr.Newf(toolerr.CodeWorkspaceFailed, "create workspace dir: %v", err)
		}
	} else if err := os.Mkdir(base, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, toolerr.Newf(toolerr.CodeWorkspaceFailed, "create workspace dir: %v", err)
	}
	w := &Workspace{ID: id, BaseDir: base}
	if err := w.MkdirAll(DirOutput); err != nil {
		return nil, err
	}
	return w, nil
}

// List returns the IDs of existing workspaces under the default root (sorted).
func (m *Manager) List() ([]string, error) {
	entries, err := os.ReadDir(m.root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, toolerr.Newf(toolerr.CodeWorkspaceFailed, "list workspaces: %v", err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && ValidateID(e.Name()) == nil {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// Delete removes a workspace under the default root. Refuses to remove
// anything that is not a direct child of the root (defense in depth on top of
// ValidateID).
func (m *Manager) Delete(id string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	cleaned := filepath.Clean(filepath.Join(m.root, id))
	if filepath.Dir(cleaned) != m.root {
		return fmt.Errorf("refused to delete: %s is not a direct child of %s", cleaned, m.root)
	}
	if err := os.RemoveAll(cleaned); err != nil {
		return toolerr.Newf(toolerr.CodeWorkspaceFailed, "remove workspace: %v", err)
	}
	return nil
}
