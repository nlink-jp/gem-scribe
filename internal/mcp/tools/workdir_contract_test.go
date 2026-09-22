package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-scribe/internal/mcp/mcpserver"
	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
	"github.com/nlink-jp/gem-scribe/internal/mcp/workdir"
)

// The work-directory contract (organization ADR-021) is a rule about every
// tool, not about one of them. Stated only in prose it gets re-decided by
// whoever adds the next tool, so it is pinned here.

func TestNoToolSchemaCarriesARetiredWorkDirName(t *testing.T) {
	h := newHarness(t)
	for _, tool := range h.srv.Tools() {
		for _, old := range retiredWorkDirNames {
			if strings.Contains(string(tool.InputSchema), `"`+old+`"`) {
				t.Errorf("tool %q declares %q; the name is work_dir", tool.Name, old)
			}
		}
	}
}

func TestWorkDirIsRequiredWhereverItIsDeclared(t *testing.T) {
	h := newHarness(t)
	for _, tool := range h.srv.Tools() {
		var schema struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("%s: input schema is not valid JSON: %v", tool.Name, err)
		}
		if _, ok := schema.Properties["work_dir"]; !ok {
			if _, addresses := schema.Properties["workspace_id"]; addresses {
				t.Errorf("tool %q takes a workspace_id but never says which work_dir it lives in", tool.Name)
			}
			continue
		}
		found := false
		for _, r := range schema.Required {
			if r == "work_dir" {
				found = true
			}
		}
		if !found {
			t.Errorf("tool %q declares work_dir but does not require it", tool.Name)
		}
	}
}

func TestTranscribeRequiresAWorkDir(t *testing.T) {
	h := newHarness(t)
	_, err := h.srv.Call(context.Background(), "transcribe", json.RawMessage(`{"audio":"meeting.m4a"}`))
	if !errors.Is(err, toolerr.New(toolerr.CodeWorkDirRequired, "")) {
		t.Fatalf("err = %v, want work_dir_required", err)
	}
}

// The rename is ours, so a caller working from an older manual recovers in one
// turn rather than guessing what "unknown field" meant.
func TestARetiredSpellingNamesTheNewOne(t *testing.T) {
	h := newHarness(t)
	for _, old := range retiredWorkDirNames {
		args := `{"audio":"meeting.m4a","` + old + `":"/tmp"}`
		_, err := h.srv.Call(context.Background(), "transcribe", json.RawMessage(args))
		if !errors.Is(err, toolerr.New(toolerr.CodeWorkDirRequired, "")) {
			t.Errorf("%s: err = %v, want work_dir_required", old, err)
		}
		if err != nil && !strings.Contains(err.Error(), "work_dir") {
			t.Errorf("%s: error does not name the new argument: %v", old, err)
		}
	}
}

// The second channel: our own runtimes set it on every tools/call, so the model
// does not have to carry an argument it cannot get wrong.
func TestWorkDirComesFromRequestMeta(t *testing.T) {
	h := newHarness(t)
	hint, err := json.Marshal(h.root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := mcpserver.WithRequestMeta(context.Background(),
		map[string]json.RawMessage{workdir.MetaKey: hint})

	out, err := h.srv.Call(ctx, "transcribe", json.RawMessage(`{"audio":"meeting.m4a"}`))
	if err != nil {
		t.Fatalf("transcribe with only a _meta work dir: %v", err)
	}
	ack := toMap(t, out)
	if ack["work_dir"] == nil || ack["work_dir"] == "" {
		t.Errorf("the acknowledgement does not echo work_dir: %v", ack)
	}
	status := h.await(t, ack["job_id"].(string))
	if status["state"] != "done" {
		t.Fatalf("state = %v: %v", status["state"], status["error"])
	}
}

// A schema test catches a renamed argument; it does not catch a sentence. The
// descriptions are the other half of what the model reads, and prose drifts
// silently because nothing compiles it: get_usage shipped v0.3.0 still
// offering to explain "the workspace model and workspace_root".
func TestNoToolDescriptionNamesARetiredWorkDirName(t *testing.T) {
	h := newHarness(t)
	for _, tool := range h.srv.Tools() {
		for _, old := range retiredWorkDirNames {
			if strings.Contains(tool.Description, old) {
				t.Errorf("tool %q describes itself with %q; the name is work_dir", tool.Name, old)
			}
		}
	}
}

// The initialize `instructions` field is the first thing the model reads about
// this server — before any tool list — so the contract has to survive there
// too. It did not: the string described "a workspace directory you prepare"
// and never named the argument, while every tool required it.
func TestInstructionsNameTheWorkDirContract(t *testing.T) {
	for _, want := range []string{"work_dir", "absolute", "required"} {
		if !strings.Contains(Instructions, want) {
			t.Errorf("the initialize instructions do not mention %q; a model that "+
				"reads only this will omit an argument every tool requires", want)
		}
	}
	for _, old := range retiredWorkDirNames {
		if strings.Contains(Instructions, old) {
			t.Errorf("the initialize instructions name %q; the name is work_dir", old)
		}
	}
}

// The delivery-mode switch was withdrawn too, so the words that described it
// must not survive in what the model reads. A model told about an
// `inline_threshold` will send one, and strict decoding refuses it.
func TestModelFacingTextNamesNoWithdrawnDeliveryMode(t *testing.T) {
	withdrawn := []string{"inline_threshold", "excerpt", "inline when it is short"}
	h := newHarness(t)
	texts := map[string]string{"initialize instructions": Instructions, "usage manual": usageMarkdown}
	for _, tool := range h.srv.Tools() {
		texts["tool "+tool.Name+" description"] = tool.Description
		texts["tool "+tool.Name+" schema"] = string(tool.InputSchema)
	}
	for where, text := range texts {
		for _, term := range withdrawn {
			if strings.Contains(text, term) {
				t.Errorf("%s still names %q: the result carries the text up to max_bytes "+
					"and counts what the cap left out", where, term)
			}
		}
	}
}

// TestEveryRequiredNameIsDeclared is the regression for a tool list that a
// strict client refuses outright. Vertex AI validates `required` against
// `properties` and answers a whole tools/list with
// "schema at top-level requires unspecified property 'work_dir'" — one bad
// schema and the session cannot start at all (2026-09-14, gem-agent).
//
// The existing contract test checks the other direction (declared => required)
// and is blind to this one; JSON Schema itself permits it, so nothing else
// catches it either.
func TestEveryRequiredNameIsDeclared(t *testing.T) {
	for _, tool := range newHarness(t).srv.Tools() {
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("%s: input schema is not valid JSON: %v", tool.Name, err)
		}
		for _, name := range schema.Required {
			if _, ok := schema.Properties[name]; !ok {
				t.Errorf("tool %q requires %q but does not declare it in properties: "+
					"a strict client refuses the whole tool list", tool.Name, name)
			}
		}
	}
}

// The directory a call actually uses is <work_dir>/<workspace_id>, and it is
// judged too: work_dir=~/.config with workspace_id=gh would otherwise land the
// workspace in ~/.config/gh, a credential directory, and write into it.
func TestTheWorkspaceDirectoryIsJudgedNotOnlyTheWorkDir(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	cfg := filepath.Join(home, ".config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	args, err := json.Marshal(map[string]any{"work_dir": cfg, "workspace_id": "gh", "audio": "meeting.m4a"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.srv.Call(context.Background(), "transcribe", args)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Code != toolerr.CodeWorkDirDenied {
		t.Fatalf("transcribe into ~/.config/gh = %v, want %s", err, toolerr.CodeWorkDirDenied)
	}
	if _, serr := os.Stat(filepath.Join(cfg, "gh")); !os.IsNotExist(serr) {
		t.Errorf("the refused workspace was created (stat: %v)", serr)
	}
}
