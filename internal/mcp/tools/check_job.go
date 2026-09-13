package tools

import (
	"context"
	"encoding/json"

	"github.com/nlink-jp/gem-scribe/internal/mcp/mcpserver"
	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
)

func registerCheckJob(srv *mcpserver.Server, d *Deps) {
	srv.RegisterTool(mcpserver.Tool{
		Name: "check_job",
		Description: "Poll a transcribe job: state (queued/running/done/error), progress, and — when done — " +
			"the transcript, carried in the result up to max_bytes with anything past the cap counted. " +
			"Jobs do not survive a server restart; if the job_id is unknown, re-submit transcribe " +
			"(it works from the same workspace).",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "required": ["job_id"],
  "properties": {
    "job_id": {"type": "string"}
  },
  "additionalProperties": false
}`),
	}, func(ctx context.Context, args json.RawMessage) (any, error) {
		var in struct {
			JobID string `json:"job_id"`
		}
		if err := unmarshalStrict(args, &in); err != nil {
			return nil, err
		}
		if in.JobID == "" {
			return nil, toolerr.New(toolerr.CodeMissingArgument, "job_id is required")
		}
		return d.Jobs.Get(in.JobID)
	})
}
