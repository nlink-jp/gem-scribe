package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-scribe/internal/mcp/job"
	"github.com/nlink-jp/gem-scribe/internal/mcp/mcpserver"
	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
	"github.com/nlink-jp/gem-scribe/internal/mcp/workdir"
	"github.com/nlink-jp/gem-scribe/internal/mcp/workspace"
)

// Whether a file exists is never the difference between two answers to
// transcribe. Each case names one path twice — once while a file is there and
// once after it is removed — and the whole answer (code, message, details) must
// be the same both times; where the path is one the floor refuses, both must be
// that refusal. Otherwise "not found" against "refused" tells the caller which
// secrets exist (knowledge: security.md, "Compare places by identity, not by
// name").
//
// The layer observed is the tool call, the answer a caller receives; the home
// directory is a temporary one, so no real credential directory is touched.
func TestExistenceIsNotRevealed(t *testing.T) {
	base := realDir(t, t.TempDir())
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	// ~/.config links into a dotfiles tree, and ~/.ssh/config into a sync
	// folder, as on many machines.
	dot := filepath.Join(home, "dotfiles", "config")
	sync := filepath.Join(base, "sync")
	work := filepath.Join(base, "work")
	other := filepath.Join(base, "other")
	data := filepath.Join(base, "data")
	for _, d := range []string{
		filepath.Join(dot, "gcloud"), filepath.Join(home, ".aws"), filepath.Join(home, ".docker"),
		filepath.Join(home, ".ssh"), sync, filepath.Join(work, "default"), filepath.Join(work, "sub"), other, data,
	} {
		mkdirAll(t, d)
	}
	symlink(t, dot, filepath.Join(home, ".config"))
	symlink(t, filepath.Join(sync, "ssh_config"), filepath.Join(home, ".ssh", "config"))
	// Links a caller could plant in its own work directory and workspace.
	symlink(t, filepath.Join(home, ".aws", "planted.m4a"), filepath.Join(work, "lnk_file"))
	symlink(t, filepath.Join(home, ".aws"), filepath.Join(work, "lnk_dir"))
	symlink(t, filepath.Join(home, ".aws", "ws_planted.m4a"), filepath.Join(work, "default", "lnk_ws"))
	symlink(t, filepath.Join(home, ".aws"), filepath.Join(work, "default", "lnk_wsdir"))

	h := harnessWithData(t, data)
	answer := func(workDir, audio string) string {
		raw, err := json.Marshal(map[string]any{"audio": audio, "work_dir": workDir})
		if err != nil {
			t.Fatal(err)
		}
		_, err = h.srv.Call(context.Background(), "transcribe", raw)
		if err == nil {
			return "accepted"
		}
		var te *toolerr.Error
		if !errors.As(err, &te) {
			return "untyped: " + err.Error()
		}
		d, _ := json.Marshal(te.Details)
		return fmt.Sprintf("%s | %s | %s", te.Code, te.Message, d)
	}

	for _, c := range []struct {
		name         string
		workDir, arg string
		leaf         string // the file that exists for one answer and not the other
		refused      bool   // the path is on the floor: both answers must refuse it
	}{
		{"absolute, in a credential directory", work, filepath.Join(home, ".aws", "rec.m4a"), filepath.Join(home, ".aws", "rec.m4a"), true},
		{"absolute, through a dotfiles-linked ~/.config", work, filepath.Join(home, ".config", "gcloud", "rec.m4a"), filepath.Join(dot, "gcloud", "rec.m4a"), true},
		{"absolute, a credential file", work, filepath.Join(home, ".docker", "config.json"), filepath.Join(home, ".docker", "config.json"), true},
		{"absolute, a planted link to a credential file", work, filepath.Join(work, "lnk_file"), filepath.Join(home, ".aws", "planted.m4a"), true},
		{"absolute, through a planted link to a credential directory", work, filepath.Join(work, "lnk_dir", "via.m4a"), filepath.Join(home, ".aws", "via.m4a"), true},
		{"absolute, where a link in ~/.ssh leads", work, filepath.Join(sync, "ssh_config"), filepath.Join(sync, "ssh_config"), true},
		{"absolute, a .env file", work, filepath.Join(other, ".env"), filepath.Join(other, ".env"), true},
		{"relative, in a credential directory under work_dir", filepath.Join(home, ".config"), filepath.Join("gcloud", "rel.m4a"), filepath.Join(dot, "gcloud", "rel.m4a"), true},
		{"relative, through a link planted in work_dir", work, filepath.Join("lnk_dir", "rel.m4a"), filepath.Join(home, ".aws", "rel.m4a"), true},
		{"relative, a .env file in work_dir", work, filepath.Join("sub", ".env"), filepath.Join(work, "sub", ".env"), true},
		{"relative, a .env file in the workspace", work, filepath.Join("wsub", ".env"), filepath.Join(work, "default", "wsub", ".env"), true},
		{"relative, a link planted in the workspace", work, "lnk_ws", filepath.Join(home, ".aws", "ws_planted.m4a"), false},
		{"relative, through a directory link planted in the workspace", work, filepath.Join("lnk_wsdir", "x.m4a"), filepath.Join(home, ".aws", "x.m4a"), false},
		{"the did-you-mean hint, a credential file", filepath.Join(home, ".docker"), filepath.Join(other, "nowhere", "config.json"), filepath.Join(home, ".docker", "config.json"), false},
		{"a .env file named where none is, with one in work_dir", work, filepath.Join(other, "nowhere", ".env"), filepath.Join(work, ".env"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			writeFileAt(t, c.leaf)
			e := answer(c.workDir, c.arg)
			if err := os.Remove(c.leaf); err != nil {
				t.Fatal(err)
			}
			m := answer(c.workDir, c.arg)
			if c.refused && !strings.HasPrefix(e, toolerr.CodePathNotAllowed+" ") {
				t.Errorf("existing: %s\n  want path_not_allowed", e)
			}
			if e != m {
				t.Errorf("the answer tells them apart\n  existing: %s\n  missing:  %s", e, m)
			}
		})
	}

	// The control: an ordinary missing recording is still reported missing,
	// named as the caller gave it and relative to the workspace.
	if a := answer(work, filepath.Join(other, "typo.m4a")); !strings.HasPrefix(a, toolerr.CodeInputNotFound+" ") {
		t.Errorf("an ordinary missing absolute recording: %s", a)
	}
	if a := answer(work, "typo.m4a"); !strings.HasPrefix(a, toolerr.CodeInputNotFound+" ") {
		t.Errorf("an ordinary missing relative recording: %s", a)
	}
}

// harnessWithData is newHarness with this server's own directory at data.
func harnessWithData(t *testing.T, data string) *harness {
	t.Helper()
	fake := &fakeTranscriber{result: sampleTranscript()}
	wd := workdir.NewResolver(data)
	srv := mcpserver.New("gem-scribe", "test", nil, nil)
	Register(srv, &Deps{
		WS:         workspace.NewManager(wd.CheckBeneath),
		WorkDir:    wd,
		Transcribe: fake,
		Jobs:       job.NewManager(context.Background()),
	})
	return &harness{srv: srv, fake: fake}
}

func realDir(t *testing.T, d string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mkdirAll(t *testing.T, d string) {
	t.Helper()
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, at string) {
	t.Helper()
	if err := os.Symlink(target, at); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func writeFileAt(t *testing.T, p string) {
	t.Helper()
	mkdirAll(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte("not really audio"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The corners of placing a path. A planted link whose target climbs with ..
// past a component that is a directory, a file or missing gets one answer in
// all three cases: existence is asked at the place, not re-walked from the
// spelling. A chain of links that does not end is refused and named as the
// caller gave it.
func TestPlacementCorners(t *testing.T) {
	base := realDir(t, t.TempDir())
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	work := filepath.Join(base, "work")
	other := filepath.Join(base, "other")
	for _, d := range []string{filepath.Join(home, ".aws"), work, filepath.Join(other, "probe_d")} {
		mkdirAll(t, d)
	}
	writeFileAt(t, filepath.Join(other, "probe_f"))
	h := harnessWithData(t, filepath.Join(base, "data"))
	answer := func(audio string) string {
		raw, _ := json.Marshal(map[string]any{"audio": audio, "work_dir": work})
		_, err := h.srv.Call(context.Background(), "transcribe", raw)
		var te *toolerr.Error
		if errors.As(err, &te) {
			return te.Code + " | " + strings.ReplaceAll(te.Message, "probe_", "probe_?")
		}
		return fmt.Sprint(err)
	}
	for _, target := range []string{
		filepath.Join(home, ".aws", "c.m4a"), // on the floor
		filepath.Join(work, "ordinary.m4a"),  // not
	} {
		writeFileAt(t, target)
		rel, err := filepath.Rel(other, target)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, k := range []string{"d", "f", "m"} {
			// Written as a string: filepath.Join would clean the ".." away.
			link := filepath.Join(other, "probe_"+k) + string(filepath.Separator) + ".." + string(filepath.Separator) + rel
			at := filepath.Join(work, "L_"+k+"_"+filepath.Base(target))
			symlink(t, link, at)
			got[k] = strings.ReplaceAll(answer(at), "L_"+k+"_", "L_?_")
		}
		if got["d"] != got["f"] || got["d"] != got["m"] {
			t.Errorf("a link climbing past a directory / a file / nothing to %s:\n  %s\n  %s\n  %s", target, got["d"], got["f"], got["m"])
		}
	}

	symlink(t, "loopB", filepath.Join(work, "loopA"))
	symlink(t, "loopA", filepath.Join(work, "loopB"))
	loop := filepath.Join(work, "loopA")
	if a := answer(loop); !strings.HasPrefix(a, toolerr.CodePathNotAllowed+" | ") || !strings.Contains(a, fmt.Sprintf("%q", loop)) {
		t.Errorf("a loop: %s; want path_not_allowed naming %s", a, loop)
	}
}
