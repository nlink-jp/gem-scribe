package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
	"github.com/nlink-jp/gem-scribe/internal/mcp/transport"
)

// drive feeds requestLines through a server over the stdio transport and returns
// the response lines it wrote.
func drive(t *testing.T, register func(*Server), requestLines ...string) []string {
	t.Helper()
	in := strings.NewReader(strings.Join(requestLines, "\n") + "\n")
	var out strings.Builder
	srv := New("voice-scribe-mcp", "test", transport.NewStdioTransport(in, &out), nil)
	if register != nil {
		register(srv)
	}
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		if s := sc.Text(); s != "" {
			lines = append(lines, s)
		}
	}
	return lines
}

func TestInitializeHandshake(t *testing.T) {
	lines := drive(t, func(s *Server) { s.SetInstructions("call get_usage first") },
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if len(lines) != 1 {
		t.Fatalf("want 1 response, got %d: %v", len(lines), lines)
	}
	var resp struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Result.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocol version = %q", resp.Result.ProtocolVersion)
	}
	if resp.Result.ServerInfo.Name != "voice-scribe-mcp" {
		t.Errorf("server name = %q", resp.Result.ServerInfo.Name)
	}
	if resp.Result.Instructions != "call get_usage first" {
		t.Errorf("instructions = %q", resp.Result.Instructions)
	}
}

func TestNotificationProducesNoResponse(t *testing.T) {
	lines := drive(t, nil, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if len(lines) != 0 {
		t.Errorf("notification must produce no response, got %v", lines)
	}
}

func TestToolsListAndCall(t *testing.T) {
	register := func(s *Server) {
		s.RegisterTool(Tool{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)},
			func(ctx context.Context, args json.RawMessage) (any, error) {
				return map[string]any{"got": json.RawMessage(args)}, nil
			})
	}
	lines := drive(t, register,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"x":1}}}`,
	)
	if len(lines) != 2 {
		t.Fatalf("want 2 responses, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], `"name":"echo"`) {
		t.Errorf("tools/list missing echo: %s", lines[0])
	}
	// tools/call wraps the return value in a text content block.
	if !strings.Contains(lines[1], `"content"`) || !strings.Contains(lines[1], `"type":"text"`) {
		t.Errorf("tools/call result shape: %s", lines[1])
	}
}

func TestToolCallStructuredError(t *testing.T) {
	register := func(s *Server) {
		s.RegisterTool(Tool{Name: "boom"},
			func(ctx context.Context, args json.RawMessage) (any, error) {
				return nil, toolerr.New(toolerr.CodeInputNotFound, "no such recording")
			})
	}
	lines := drive(t, register,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"boom","arguments":{}}}`)
	if len(lines) != 1 {
		t.Fatalf("want 1 response, got %v", lines)
	}
	if !strings.Contains(lines[0], `"isError":true`) {
		t.Errorf("expected isError=true: %s", lines[0])
	}
	if !strings.Contains(lines[0], `input_not_found`) {
		t.Errorf("structured error code missing: %s", lines[0])
	}
}

func TestUnknownMethod(t *testing.T) {
	lines := drive(t, nil, `{"jsonrpc":"2.0","id":1,"method":"does/not/exist"}`)
	if len(lines) != 1 || !strings.Contains(lines[0], "method not found") {
		t.Fatalf("want method-not-found error, got %v", lines)
	}
}

func TestParseErrorAndBadVersion(t *testing.T) {
	lines := drive(t, nil,
		`not json`,
		`{"jsonrpc":"1.0","id":1,"method":"initialize"}`,
	)
	if len(lines) != 2 {
		t.Fatalf("want 2 error responses, got %v", lines)
	}
	if !strings.Contains(lines[0], "parse error") {
		t.Errorf("parse error missing: %s", lines[0])
	}
	if !strings.Contains(lines[1], "invalid jsonrpc version") {
		t.Errorf("version error missing: %s", lines[1])
	}
}

// TestCallUnknownTool exercises the in-process Call path.
func TestCallUnknownTool(t *testing.T) {
	srv := New("x", "test", transport.NewStdioTransport(strings.NewReader(""), &strings.Builder{}), nil)
	_, err := srv.Call(context.Background(), "nope", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("want unknown tool error, got %v", err)
	}
}
