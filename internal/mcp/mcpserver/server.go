// Package mcpserver is the thin layer between the tools (internal/mcp/tools) and
// the MCP protocol. The protocol itself — both eras (the legacy initialize
// handshake and the 2026-07-28 revision), framing, versions, cancellation — is
// the organization's module nlink-jp/mcp (organization ADR-030, ADR-0005);
// this package keeps the registration API the tools use, the request `_meta`
// they read, and the way their results become MCP content.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/nlink-jp/mcp"
	mcpsrv "github.com/nlink-jp/mcp/server"

	"github.com/nlink-jp/gem-scribe/internal/mcp/transport"
)

// Tool is a single MCP tool descriptor returned by tools/list.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolHandler is invoked when tools/call is received for a registered tool.
// The return value is JSON-serialized into a single text content block.
// Returning an error produces a result with isError=true per MCP convention.
type ToolHandler func(ctx context.Context, args json.RawMessage) (any, error)

// Server is the MCP stdio server.
type Server struct {
	name      string
	version   string
	tools     []Tool
	handlers  map[string]ToolHandler
	transport *transport.StdioTransport
	logger    *slog.Logger
	// instructions is returned when a client opens the server; see
	// SetInstructions.
	instructions string
}

// New creates a server bound to the given transport.
func New(name, version string, t *transport.StdioTransport, logger *slog.Logger) *Server {
	if logger == nil {
		// Warnings and above to stderr, as every migrated server logs them
		// (server-migration §2); stdout belongs to the protocol.
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	return &Server{
		name:      name,
		version:   version,
		transport: t,
		handlers:  make(map[string]ToolHandler),
		logger:    logger,
	}
}

// RegisterTool registers a tool descriptor and its handler.
// Must be called before Serve.
func (s *Server) RegisterTool(t Tool, h ToolHandler) {
	s.tools = append(s.tools, t)
	s.handlers[t.Name] = h
}

// Tools returns the registered tool descriptors. It exists so that tests can
// check the tools against the documentation and the schemas without going
// through the JSON-RPC framing.
func (s *Server) Tools() []Tool {
	out := make([]Tool, len(s.tools))
	copy(out, s.tools)
	return out
}

// Call invokes a registered tool by name in-process, returning the handler's
// raw value and error. It bypasses the JSON-RPC framing and is used for
// in-process invocation and tests.
func (s *Server) Call(ctx context.Context, name string, args json.RawMessage) (any, error) {
	h, ok := s.handlers[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
	return h(ctx, args)
}

// SetInstructions sets the server-usage hint returned when a client opens the
// server (initialize, or server/discover in the modern era). Must be called
// before Serve.
func (s *Server) SetInstructions(text string) {
	s.instructions = text
}

// Serve registers the tools on a fresh protocol server and serves the
// transport's streams until stdin reaches EOF (answering every request that
// arrived), ctx is cancelled, or a write fails. It returns nil for EOF and
// cancellation.
func (s *Server) Serve(ctx context.Context) error {
	ts, err := mcpsrv.New(mcpsrv.Info{
		Server:       mcp.Implementation{Name: s.name, Version: s.version},
		Instructions: s.instructions,
	}, mcpsrv.Options{
		// One call at a time, as before the module: the tools' shared state
		// assumes it (server-migration §2).
		Workers: 1,
		// Calls sent side by side wait, as they did before the module: the
		// default of 16 would refuse the 17th as "server busy".
		QueueDepth: 256,
		Logger:     s.logger,
	})
	if err != nil {
		return err
	}
	for _, t := range s.tools {
		h := s.handlers[t.Name]
		if err := ts.AddTool(mcpsrv.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema},
			func(ctx context.Context, req *mcpsrv.Request) (*mcpsrv.Result, error) {
				return call(ctx, h, req.Name, req.Arguments, req.Meta)
			}); err != nil {
			return err
		}
	}
	return ts.Serve(ctx, s.transport.In(), s.transport.Out())
}
