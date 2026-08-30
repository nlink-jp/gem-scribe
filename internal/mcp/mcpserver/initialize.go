package mcpserver

import "github.com/nlink-jp/gem-scribe/internal/mcp/jsonrpc"

type initializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      serverInfo     `json:"serverInfo"`
	// Instructions is the MCP hint clients feed to their model; empty is
	// omitted. See Server.SetInstructions.
	Instructions string `json:"instructions,omitempty"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// SetInstructions sets the server-usage hint returned by initialize.
// Must be called before Serve.
func (s *Server) SetInstructions(text string) {
	s.instructions = text
}

func (s *Server) handleInitialize(req jsonrpc.Request) error {
	return s.writeResult(req.ID, initializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities: map[string]any{
			"tools": map[string]any{},
		},
		ServerInfo:   serverInfo{Name: s.name, Version: s.version},
		Instructions: s.instructions,
	})
}
