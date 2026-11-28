// Package transport holds the two streams the MCP server is served over.
// Framing, line bounds and writing are the organization's module
// nlink-jp/mcp's (ADR-0005); this type only carries stdin and the stdout the
// caller set aside for JSON-RPC.
package transport

import "io"

// StdioTransport carries the server's input and output streams.
type StdioTransport struct {
	in  io.Reader
	out io.Writer
}

// NewStdioTransport wires a transport around the given Reader/Writer.
func NewStdioTransport(in io.Reader, out io.Writer) *StdioTransport {
	return &StdioTransport{in: in, out: out}
}

// In is the stream requests are read from.
func (t *StdioTransport) In() io.Reader { return t.in }

// Out is the stream answers are written to.
func (t *StdioTransport) Out() io.Writer { return t.out }
