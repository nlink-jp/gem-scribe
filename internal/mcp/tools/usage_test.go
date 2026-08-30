package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-scribe/internal/mcp/mcpserver"
	"github.com/nlink-jp/gem-scribe/internal/mcp/workspace"
)

// The manual is the only thing a client reads before its first call, so it
// drifting from the code is not a documentation problem — it is a client
// following instructions that no longer work. These tests machine-check the
// coherence rather than trusting a reviewer to notice.

func registeredServer(t *testing.T) *mcpserver.Server {
	t.Helper()
	srv := mcpserver.New("gem-scribe", "test", nil, nil)
	Register(srv, &Deps{WS: workspace.NewManager(t.TempDir())})
	return srv
}

func TestUsage_ListsEveryRegisteredTool(t *testing.T) {
	for _, tool := range registeredServer(t).Tools() {
		if !strings.Contains(usageMarkdown, "`"+tool.Name+"`") {
			t.Errorf("usage.md does not document the %q tool", tool.Name)
		}
	}
}

func TestUsage_DocumentsEveryTranscribeArgument(t *testing.T) {
	var transcribe *mcpserver.Tool
	for _, tool := range registeredServer(t).Tools() {
		if tool.Name == "transcribe" {
			transcribe = &tool
			break
		}
	}
	if transcribe == nil {
		t.Fatal("the transcribe tool is not registered")
	}

	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(transcribe.InputSchema, &schema); err != nil {
		t.Fatalf("transcribe input schema is not valid JSON: %v", err)
	}
	if len(schema.Properties) == 0 {
		t.Fatal("transcribe declares no arguments")
	}
	for name := range schema.Properties {
		if !strings.Contains(usageMarkdown, "`"+name+"`") {
			t.Errorf("usage.md does not document the %q argument", name)
		}
	}
}

// Every code the package can actually emit has to appear in the recovery table:
// an agent that meets an undocumented code has nothing to act on.
func TestUsage_DocumentsEveryErrorCodeThePackageEmits(t *testing.T) {
	used := map[string]bool{}
	pattern := regexp.MustCompile(`toolerr\.(Code[A-Za-z]+)`)
	constants := errorCodeValues(t)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range pattern.FindAllStringSubmatch(string(src), -1) {
			if value, ok := constants[m[1]]; ok {
				used[value] = true
			}
		}
	}

	if len(used) == 0 {
		t.Fatal("no error codes were found in the package; the scan is broken")
	}
	for code := range used {
		if !strings.Contains(usageMarkdown, "`"+code+"`") {
			t.Errorf("usage.md's recovery table is missing the %q code", code)
		}
	}
}

// errorCodeValues reads the toolerr constants so the test compares wire values
// ("input_not_found"), not Go identifiers.
func errorCodeValues(t *testing.T) map[string]string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "toolerr", "toolerr.go"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	pattern := regexp.MustCompile(`(Code[A-Za-z]+)\s*=\s*"([a-z_]+)"`)
	for _, m := range pattern.FindAllStringSubmatch(string(src), -1) {
		out[m[1]] = m[2]
	}
	if len(out) == 0 {
		t.Fatal("no error code constants were parsed")
	}
	return out
}

// The manual claims the transcription model is global-only and that vocabulary
// biasing is withheld. Both are measured facts a reader will act on, so they
// must not quietly disappear from the document.
func TestUsage_KeepsTheMeasuredCaveats(t *testing.T) {
	for _, claim := range []string{"global", "30-minute", "Vocabulary biasing"} {
		if !strings.Contains(usageMarkdown, claim) {
			t.Errorf("usage.md no longer mentions %q", claim)
		}
	}
}

func TestInstructions_PointAtGetUsage(t *testing.T) {
	if !strings.Contains(Instructions, "get_usage") {
		t.Error("the initialize-time instructions do not mention get_usage, which is how a client finds the manual")
	}
	// The choice between this server and its local counterpart is the first
	// decision a client makes; making it needs both names.
	if !strings.Contains(Instructions, "voice-scribe") {
		t.Error("the instructions do not mention the local counterpart")
	}
}
