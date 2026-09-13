package tools

import (
	"context"
	"encoding/json"
	"errors"
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
