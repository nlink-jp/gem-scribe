package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/mcp"
	"github.com/nlink-jp/mcp/client"
	"github.com/nlink-jp/mcp/probe"

	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
	"github.com/nlink-jp/gem-scribe/internal/mcp/transport"
)

const serverName = "gem-scribe"

// serveOver runs a server prepared by register over pipes and returns the
// client-side streams (the server's stdout to read, its stdin to write).
func serveOver(t *testing.T, logger *slog.Logger, register func(*Server)) (io.Reader, io.WriteCloser, func()) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := New(serverName, "test", transport.NewStdioTransport(inR, outW), logger)
	if register != nil {
		register(srv)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ctx)
		_ = outW.Close()
	}()
	stop := func() {
		_ = inW.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after stdin closed")
		}
		cancel()
		_ = outR.Close()
	}
	return outR, inW, stop
}

// open serves a server prepared by register and opens it with nlink-jp/mcp's
// client, the way a dual-era MCP client does (server-migration §6). Nothing
// here writes the handshake by hand.
func open(t *testing.T, register func(*Server)) *client.Client {
	t.Helper()
	return openWith(t, nil, register, client.Options{})
}

// openWith is open with a logger of the server's own and the client's
// options (CancelNotice, say).
func openWith(t *testing.T, logger *slog.Logger, register func(*Server), opts client.Options) *client.Client {
	t.Helper()
	out, in, stop := serveOver(t, logger, register)
	conn, err := client.NewLineConn(out, in, func() { _ = in.Close() }, client.LineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opts.Client = mcp.Implementation{Name: "test", Version: "0"}
	cl, err := client.Open(ctx, conn, opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close(); stop() })
	return cl
}

func callTool(t *testing.T, cl *client.Client, tool string, args any, meta map[string]any) (*client.ToolResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return cl.CallTool(ctx, tool, args, meta)
}

func blocks(t *testing.T, res *client.ToolResult) []ContentBlock {
	t.Helper()
	var out []ContentBlock
	for _, raw := range res.Content {
		var b ContentBlock
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatalf("content block: %v", err)
		}
		out = append(out, b)
	}
	return out
}

var objSchema = json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`)

func echo(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		Msg string `json:"msg"`
	}
	_ = json.Unmarshal(args, &in)
	return map[string]string{"echoed": in.Msg}, nil
}

// The registration API end to end: the listing, a value marshalled into one text block, an error as an
// isError result, and a RawResult's blocks verbatim.
func TestToolsOverTheProtocol(t *testing.T) {
	cl := open(t, func(s *Server) {
		s.RegisterTool(Tool{Name: "echo", Description: "Echo the input msg", InputSchema: objSchema}, echo)
		s.RegisterTool(Tool{Name: "coded", InputSchema: objSchema},
			func(ctx context.Context, args json.RawMessage) (any, error) {
				return nil, toolerr.New(toolerr.CodeInvalidArguments, "nope")
			})
		s.RegisterTool(Tool{Name: "plain", InputSchema: objSchema},
			func(ctx context.Context, args json.RawMessage) (any, error) {
				return nil, errors.New("plain failure")
			})
		s.RegisterTool(Tool{Name: "rich", InputSchema: objSchema},
			func(ctx context.Context, args json.RawMessage) (any, error) {
				return RawResult{Content: []ContentBlock{
					{Type: "text", Text: "a picture"},
					{Type: "image", Data: "aGk=", MimeType: "image/png"},
				}}, nil
			})
	})
	if cl.Server().Name != serverName {
		t.Errorf("serverInfo.name = %q", cl.Server().Name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, err := cl.ListTools(ctx)
	if err != nil || len(list.Tools) != 4 || list.Tools[0].Name != "echo" || list.Tools[0].Description != "Echo the input msg" {
		t.Fatalf("tools/list = %+v, %v", list, err)
	}

	res, err := callTool(t, cl, "echo", map[string]string{"msg": "hello"}, nil)
	if err != nil || res.IsError {
		t.Fatalf("echo: %+v, %v", res, err)
	}
	if b := blocks(t, res); len(b) != 1 || b[0].Type != "text" || b[0].Text != `{"echoed":"hello"}` {
		t.Errorf("echo blocks = %+v", b)
	}

	res, _ = callTool(t, cl, "coded", nil, nil)
	if b := blocks(t, res); !res.IsError || len(b) != 1 || !strings.Contains(b[0].Text, `"code":"invalid_arguments"`) {
		t.Errorf("a toolerr is not a structured isError result: %+v", b)
	}
	res, _ = callTool(t, cl, "plain", nil, nil)
	if b := blocks(t, res); !res.IsError || b[0].Text != "plain failure" {
		t.Errorf("a plain error is not its text: %+v", b)
	}
	res, _ = callTool(t, cl, "rich", nil, nil)
	if b := blocks(t, res); len(b) != 2 || b[1].Type != "image" || b[1].Data != "aGk=" || b[1].MimeType != "image/png" {
		t.Errorf("RawResult blocks were not kept: %+v", b)
	}
}

// An unknown tool never reaches a handler: the protocol layer refuses it with
// -32602 (server-migration §4).
func TestUnknownToolIsAProtocolError(t *testing.T) {
	_, err := callTool(t, open(t, nil), "missing", nil, nil)
	var e *mcp.Error
	if !errors.As(err, &e) || e.Code != -32602 {
		t.Errorf("err = %v, want -32602", err)
	}
}

// The request's _meta — the work directory above all — reaches the handler
// through RequestMeta.
func TestRequestMetaReachesTheHandler(t *testing.T) {
	got := make(chan string, 1)
	cl := open(t, func(s *Server) {
		s.RegisterTool(Tool{Name: "where", InputSchema: objSchema}, func(ctx context.Context, args json.RawMessage) (any, error) {
			var dir string
			_ = json.Unmarshal(RequestMeta(ctx)["jp.nlink/work_dir"], &dir)
			got <- dir
			return "ok", nil
		})
	})
	if _, err := callTool(t, cl, "where", nil, map[string]any{"jp.nlink/work_dir": "/session/work"}); err != nil {
		t.Fatal(err)
	}
	if dir := <-got; dir != "/session/work" {
		t.Errorf("RequestMeta work_dir = %q", dir)
	}
}

// What SetInstructions stores is what a client picks up when it opens the
// server; none set means none advertised.
func TestInstructions(t *testing.T) {
	const hint = "call get_usage first"
	if got := open(t, func(s *Server) { s.SetInstructions(hint) }).Instructions(); got != hint {
		t.Errorf("instructions = %q, want %q", got, hint)
	}
	if got := open(t, nil).Instructions(); got != "" {
		t.Errorf("instructions %q although none were set", got)
	}
}

// Tools lists what was registered, in registration order.
func TestToolsListsTheRegistrations(t *testing.T) {
	s := New(serverName, "test", transport.NewStdioTransport(strings.NewReader(""), io.Discard), nil)
	s.RegisterTool(Tool{Name: "b", InputSchema: objSchema}, echo)
	s.RegisterTool(Tool{Name: "a", InputSchema: objSchema}, echo)
	if got := s.Tools(); len(got) != 2 || got[0].Name != "b" || got[1].Name != "a" {
		t.Errorf("Tools() = %+v", got)
	}
}

// Call runs a registered handler without the protocol; an unregistered name
// is an error.
func TestCallRunsTheHandler(t *testing.T) {
	s := New(serverName, "test", transport.NewStdioTransport(strings.NewReader(""), io.Discard), nil)
	s.RegisterTool(Tool{Name: "echo", InputSchema: objSchema}, echo)
	got, err := s.Call(context.Background(), "echo", json.RawMessage(`{"msg":"x"}`))
	if err != nil || fmt.Sprint(got) != fmt.Sprint(map[string]string{"echoed": "x"}) {
		t.Errorf("Call(echo) = %v, %v", got, err)
	}
	if _, err := s.Call(context.Background(), "missing", nil); err == nil {
		t.Error("Call of an unregistered tool succeeded")
	}
}

// ADR-030 §7: the server against the three probe clients — legacy only,
// dual-era, modern only — with the outcomes the specification's
// compatibility matrix gives for a dual-era server.
func TestAgainstTheProbeClients(t *testing.T) {
	for _, kind := range []probe.ClientKind{probe.ClientLegacyOnly, probe.ClientDual, probe.ClientModernOnly} {
		out, in, stop := serveOver(t, nil, func(s *Server) {
			s.RegisterTool(Tool{Name: "echo", InputSchema: objSchema}, echo)
		})
		r := probe.Run(context.Background(), kind, out, in, probe.ClientOptions{StepTimeout: 2 * time.Second})
		stop()
		if want := probe.Expect(kind, probe.ServerDual); r.Outcome != want || len(r.Violations) != 0 || len(r.Warnings) != 0 {
			t.Errorf("%v probe client: outcome %s (want %s), violations %v, warnings %v, error %s",
				kind, r.Outcome, want, r.Violations, r.Warnings, r.Error)
		}
	}
}

// notifications/cancelled reaches the handler: its ctx ends (the module
// cancels the call).
func TestACancelledCallCancelsTheHandler(t *testing.T) {
	ended := make(chan error, 1)
	cl := openWith(t, nil, func(s *Server) {
		s.RegisterTool(Tool{Name: "wait", InputSchema: objSchema}, func(ctx context.Context, _ json.RawMessage) (any, error) {
			select {
			case <-ctx.Done():
				ended <- ctx.Err()
			case <-time.After(5 * time.Second):
				ended <- nil
			}
			return "done", nil
		})
	}, client.Options{CancelNotice: true})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := cl.CallTool(ctx, "wait", nil, nil); err == nil {
		t.Fatal("the cancelled call returned a result")
	}
	select {
	case err := <-ended:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the handler's ctx ended with %v, want context.Canceled", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("the handler never returned")
	}
}

// Calls sent side by side wait behind the running one, as before the module:
// QueueDepth 256, where the module's default of 16 would refuse the 17th as
// "server busy".
func TestCallsSentSideBySideAllWait(t *testing.T) {
	cl := open(t, func(s *Server) {
		s.RegisterTool(Tool{Name: "slow", InputSchema: objSchema}, func(context.Context, json.RawMessage) (any, error) {
			time.Sleep(20 * time.Millisecond)
			return "ok", nil
		})
	})
	const n = 24
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := cl.CallTool(ctx, "slow", nil, nil)
			errs <- err
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("call refused: %v", err)
		}
	}
}

// A handler that panics is answered -32603 by the protocol layer, and the
// server goes on answering (server-migration §4).
func TestAPanicIsAnsweredAndServingGoesOn(t *testing.T) {
	cl := open(t, func(s *Server) {
		s.RegisterTool(Tool{Name: "boom", InputSchema: objSchema}, func(context.Context, json.RawMessage) (any, error) {
			panic("boom")
		})
		s.RegisterTool(Tool{Name: "echo", InputSchema: objSchema}, echo)
	})
	_, err := callTool(t, cl, "boom", nil, nil)
	var e *mcp.Error
	if !errors.As(err, &e) || e.Code != -32603 {
		t.Errorf("panic answered %v, want -32603", err)
	}
	if res, err := callTool(t, cl, "echo", map[string]string{"msg": "after"}, nil); err != nil || res.IsError {
		t.Errorf("the call after the panic: %+v, %v", res, err)
	}
}

type brokenStdin struct{}

func (brokenStdin) Read([]byte) (int, error) { return 0, errors.New("broken stdin") }

// Without a logger of its own, the server writes the protocol layer's
// warnings to stderr (it used to discard them).
func TestTheDefaultLoggerWritesWarningsToStderr(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	srv := New(serverName, "test", transport.NewStdioTransport(brokenStdin{}, io.Discard), nil)
	os.Stderr = orig
	_ = srv.Serve(context.Background())
	_ = w.Close()
	got, _ := io.ReadAll(r)
	if !strings.Contains(string(got), "reading stdin failed") {
		t.Fatalf("stderr = %q, want the protocol layer's warning", got)
	}
}
