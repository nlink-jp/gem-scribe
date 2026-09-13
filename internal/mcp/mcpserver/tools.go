package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nlink-jp/gem-scribe/internal/mcp/jsonrpc"
	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
)

type toolsListResult struct {
	Tools []Tool `json:"tools"`
}

func (s *Server) handleToolsList(req jsonrpc.Request) error {
	// Always return a non-nil slice so the JSON has [] not null.
	tools := s.tools
	if tools == nil {
		tools = []Tool{}
	}
	return s.writeResult(req.ID, toolsListResult{Tools: tools})
}

type toolsCallParams struct {
	Name      string                     `json:"name"`
	Arguments json.RawMessage            `json:"arguments"`
	Meta      map[string]json.RawMessage `json:"_meta"`
}

// metaCtxKey carries the request's `_meta` object to tool handlers.
type metaCtxKey struct{}

// RequestMeta returns the `_meta` object of the tools/call being handled, or
// nil. It is how a calling runtime hands a server per-session facts — the
// work directory, above all — without every tool declaring them in its
// schema. Values are raw JSON: the protocol layer does not know what any
// key means.
func RequestMeta(ctx context.Context) map[string]json.RawMessage {
	m, _ := ctx.Value(metaCtxKey{}).(map[string]json.RawMessage)
	return m
}

// WithRequestMeta attaches a `_meta` object to ctx. Exported for tests and
// for hosts that drive tool handlers without the stdio transport.
func WithRequestMeta(ctx context.Context, meta map[string]json.RawMessage) context.Context {
	return context.WithValue(ctx, metaCtxKey{}, meta)
}

// ContentBlock is one block in the tools/call result.content array.
//
// Text content uses Type="text" + Text.
// Per MCP spec 2024-11-05.
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// RawResult is a sentinel a tool handler can return to bypass the default
// "JSON-marshal into a single text block" path. Use this when a tool needs
// to return specific content blocks verbatim (e.g. the usage markdown).
type RawResult struct {
	Content []ContentBlock
	IsError bool
}

type toolsCallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

func (s *Server) handleToolsCall(ctx context.Context, req jsonrpc.Request) error {
	var p toolsCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return s.writeError(req.ID, jsonrpc.CodeInvalidParams, "invalid params: "+err.Error())
	}
	h, ok := s.handlers[p.Name]
	if !ok {
		return s.writeError(req.ID, jsonrpc.CodeMethodNotFound, "unknown tool: "+p.Name)
	}
	if len(p.Meta) > 0 {
		ctx = WithRequestMeta(ctx, p.Meta)
	}
	out, err := h(ctx, p.Arguments)
	if err != nil {
		return s.writeToolError(req, err)
	}
	// Rich-content path: handler returned a RawResult, take its content blocks
	// verbatim.
	if raw, ok := out.(RawResult); ok {
		return s.writeResult(req.ID, toolsCallResult{
			Content: raw.Content,
			IsError: raw.IsError,
		})
	}
	// Default path: JSON-marshal the return value into a single text block.
	body, err := json.Marshal(out)
	if err != nil {
		return s.writeError(req.ID, jsonrpc.CodeInternalError, fmt.Sprintf("marshal tool result: %v", err))
	}
	return s.writeResult(req.ID, toolsCallResult{
		Content: []ContentBlock{{Type: "text", Text: string(body)}},
	})
}

// writeToolError emits a tool error per MCP convention: result with
// isError=true and a single text content block. If err is (or wraps) a
// *toolerr.Error, the content carries the structured {code, message, details}
// JSON so LLM clients can branch on the code. Otherwise the plain Error()
// string is used.
func (s *Server) writeToolError(req jsonrpc.Request, err error) error {
	var te *toolerr.Error
	if errors.As(err, &te) {
		body, marshalErr := json.Marshal(te)
		if marshalErr == nil {
			return s.writeResult(req.ID, toolsCallResult{
				IsError: true,
				Content: []ContentBlock{{Type: "text", Text: string(body)}},
			})
		}
		// Fall through to plain text on marshal failure.
	}
	return s.writeResult(req.ID, toolsCallResult{
		IsError: true,
		Content: []ContentBlock{{Type: "text", Text: err.Error()}},
	})
}
