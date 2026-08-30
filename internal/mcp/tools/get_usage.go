package tools

import (
	"context"
	_ "embed"
	"encoding/json"

	"github.com/nlink-jp/gem-scribe/internal/mcp/mcpserver"
)

// usageMarkdown is the client-neutral operating manual returned by get_usage.
// A stateful, async, workspace-scoped server is not something a client should
// have to work out by trial and error. Coherence with the real tools, error
// codes and schema is pinned by usage_test.go.
//
//go:embed usage.md
var usageMarkdown string

// Instructions is the short initialize-time hint that makes get_usage
// discoverable (surfaced via the MCP `instructions` field).
const Instructions = "gem-scribe transcribes recordings with Vertex AI's dedicated transcription model, which " +
	"returns speaker turns and word timings as structured data. It is stateful and async: recordings live in " +
	"a workspace directory you prepare, transcribe returns a job_id which you poll with check_job, and a " +
	"finished transcript comes back inline when it is short and as a file path with an excerpt when it is " +
	"long. It costs money and sends audio to Google; its local counterpart voice-scribe costs nothing and " +
	"keeps audio on the machine but separates at most four speakers. Call the get_usage tool before your " +
	"first transcription to learn the workspace model, the transcribe arguments, the job lifecycle, the " +
	"meaning of the warning field, and the error recovery table."

func registerGetUsage(srv *mcpserver.Server, d *Deps) {
	srv.RegisterTool(mcpserver.Tool{
		Name: "get_usage",
		Description: "Return this server's operating manual (markdown): the workspace model and workspace_root, " +
			"the transcribe arguments, the async job lifecycle (transcribe -> job_id -> check_job), how " +
			"transcripts are returned, what the warning field means, and the error recovery table. " +
			"Call it once before your first transcription.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}, func(ctx context.Context, args json.RawMessage) (any, error) {
		var in struct{}
		if err := unmarshalStrict(args, &in); err != nil {
			return nil, err
		}
		return mcpserver.RawResult{
			Content: []mcpserver.ContentBlock{{Type: "text", Text: usageMarkdown}},
		}, nil
	})
}
