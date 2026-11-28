package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	mcpsrv "github.com/nlink-jp/mcp/server"

	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
)

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

// call runs one tool handler and turns what it returned into the protocol's
// result: a RawResult's blocks verbatim, any other value JSON-marshalled into
// one text block, and an error into an isError result (toolError). A handler
// never makes this return a Go error unless the result cannot be encoded at
// all; the protocol layer answers that -32603.
func call(ctx context.Context, h ToolHandler, _ string, args json.RawMessage, meta map[string]json.RawMessage) (*mcpsrv.Result, error) {
	if len(meta) > 0 {
		ctx = WithRequestMeta(ctx, meta)
	}
	out, err := h(ctx, args)
	if err != nil {
		return result(toolError(err))
	}
	if raw, ok := out.(RawResult); ok {
		return result(toolsCallResult(raw))
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal tool result: %w", err)
	}
	return result(toolsCallResult{Content: []ContentBlock{{Type: "text", Text: string(body)}}})
}

// result copies a tools/call payload into the protocol's shape.
func result(r toolsCallResult) (*mcpsrv.Result, error) {
	out := &mcpsrv.Result{IsError: r.IsError}
	for _, b := range r.Content {
		raw, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		out.Content = append(out.Content, raw)
	}
	return out, nil
}

// toolError is a tool error per MCP convention: a result with
// isError=true and a single text content block. If err is (or wraps) a
// *toolerr.Error, the content carries the structured {code, message, details}
// JSON so LLM clients can branch on the code. Otherwise the plain Error()
// string is used.
func toolError(err error) toolsCallResult {
	var te *toolerr.Error
	if errors.As(err, &te) {
		if body, marshalErr := json.Marshal(te); marshalErr == nil {
			return toolsCallResult{IsError: true, Content: []ContentBlock{{Type: "text", Text: string(body)}}}
		}
		// Fall through to plain text on marshal failure.
	}
	return toolsCallResult{IsError: true, Content: []ContentBlock{{Type: "text", Text: err.Error()}}}
}
