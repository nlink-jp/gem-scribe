package tools

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"

	"github.com/nlink-jp/gem-scribe/internal/asr"
	"github.com/nlink-jp/gem-scribe/internal/config"
	"github.com/nlink-jp/gem-scribe/internal/mcp/job"
	"github.com/nlink-jp/gem-scribe/internal/mcp/mcpserver"
	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
	"github.com/nlink-jp/gem-scribe/internal/mcp/workspace"
	"github.com/nlink-jp/gem-scribe/internal/staging"
	"github.com/nlink-jp/gem-scribe/internal/transcript"
)

func registerTranscribe(srv *mcpserver.Server, d *Deps) {
	srv.RegisterTool(mcpserver.Tool{
		Name: "transcribe",
		Description: "Transcribe a recording that is already in the workspace, using Vertex AI's dedicated " +
			"transcription model. Returns a job_id immediately; poll it with check_job. When the job " +
			"finishes, a short transcript comes back inline and a long one comes back as a path with an " +
			"excerpt — the file is written either way. Labels who is speaking by default.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "required": ["audio"],
  "properties": {
    "audio": {"type": "string", "description": "Recording path, relative to the workspace"},
    "workspace_root": {"type": "string", "description": "Absolute path to a workspace root you prepared and can read back. Pass your own session or working directory when you have one: results come back as paths, so a workspace you cannot open leaves you holding a path to nothing."},
    "workspace_id": {"type": "string", "description": "Workspace within the root; defaults to \"default\""},
    "model": {"type": "string", "description": "Transcription model; omit to use the configured one"},
    "languages": {"type": "array", "items": {"type": "string"}, "description": "BCP-47 hints such as [\"ja-JP\"]; omit to detect"},
    "diarize": {"type": "boolean", "description": "Label each speaker turn (default true). Up to 8 speakers; attribution beyond 2 is experimental"},
    "word_timestamps": {"type": "boolean", "description": "Attach word-level timings (default true). Required for srt and vtt"},
    "smart": {"type": "boolean", "description": "Remove disfluencies and format lightly. Cannot be combined with diarize or word_timestamps"},
    "format": {"type": "string", "enum": ["json", "text", "md", "srt", "vtt"], "description": "Default json"},
    "translate_to": {"type": "string", "description": "Add a translation beside the original, e.g. \"en\". A second pass over the transcript text"},
    "speaker_hints": {"type": "array", "items": {"type": "string"}, "description": "Candidate speaker names, assigned to spk:N in the same second pass"},
    "output": {"type": "string", "description": "Transcript path relative to the workspace; defaults under output/"},
    "inline_threshold": {"type": "integer", "minimum": 0, "description": "Bytes at or below which the transcript is returned inline"}
  },
  "additionalProperties": false
}`),
	}, func(ctx context.Context, args json.RawMessage) (any, error) {
		var in struct {
			Audio           string   `json:"audio"`
			WorkspaceRoot   string   `json:"workspace_root"`
			WorkspaceID     string   `json:"workspace_id"`
			Model           string   `json:"model"`
			Languages       []string `json:"languages"`
			Diarize         *bool    `json:"diarize"`
			WordTimestamps  *bool    `json:"word_timestamps"`
			Smart           bool     `json:"smart"`
			TranslateTo     string   `json:"translate_to"`
			SpeakerHints    []string `json:"speaker_hints"`
			Format          string   `json:"format"`
			Output          string   `json:"output"`
			InlineThreshold int      `json:"inline_threshold"`
		}
		if err := unmarshalStrict(args, &in); err != nil {
			return nil, err
		}
		if in.Audio == "" {
			return nil, toolerr.New(toolerr.CodeMissingArgument, "audio is required")
		}

		format := transcript.FormatJSON
		if in.Format != "" {
			f, err := transcript.ParseFormat(in.Format)
			if err != nil {
				return nil, toolerr.Newf(toolerr.CodeInvalidArguments, "%v", err)
			}
			format = f
		}

		// Diarization and timestamps default on, but SMART mode excludes both.
		// Turning them off for a caller that only asked for smart is the same
		// courtesy the CLI extends: reporting a conflict the caller never chose
		// would be a puzzle, not an error.
		req := Request{
			Model:          in.Model,
			Languages:      in.Languages,
			Diarize:        boolOr(in.Diarize, !in.Smart),
			WordTimestamps: boolOr(in.WordTimestamps, !in.Smart),
			Smart:          in.Smart,
			TranslateTo:    in.TranslateTo,
			SpeakerHints:   in.SpeakerHints,
		}
		if err := (asr.Options{
			Diarize: req.Diarize, WordTimestamps: req.WordTimestamps, Smart: req.Smart,
		}).Validate(); err != nil {
			return nil, toolerr.Newf(toolerr.CodeModeConflict, "%v", err)
		}
		if len(req.SpeakerHints) > 0 && !req.Diarize {
			return nil, toolerr.New(toolerr.CodeInvalidArguments,
				"speaker_hints needs diarization: without it there are no speaker labels to assign names to")
		}
		if (format == transcript.FormatSRT || format == transcript.FormatVTT) && !req.WordTimestamps {
			return nil, toolerr.Newf(toolerr.CodeInvalidArguments,
				"%s output needs word_timestamps; every cue would otherwise be written at 00:00:00", format)
		}

		if in.WorkspaceID == "" {
			in.WorkspaceID = "default"
		}
		ws, err := d.WS.EnsureIn(in.WorkspaceRoot, in.WorkspaceID)
		if err != nil {
			return nil, err
		}

		audioRel, err := ws.ResolveInside(in.Audio)
		if err != nil {
			return nil, err
		}
		// The API client cannot inherit os.Root, so the containment check
		// happens here, immediately before the absolute path is handed over.
		if err := ws.VerifyRegular(audioRel); err != nil {
			return nil, err
		}
		// Refusing an unsupported container here, with the accepted list in
		// hand, beats a job that fails minutes later with an opaque API error.
		if _, err := staging.MIMETypeFor(audioRel); err != nil {
			return nil, toolerr.Newf(toolerr.CodeUnsupportedFormat, "%v", err)
		}
		req.Audio = ws.Path(audioRel)

		outRel, err := resolveOutput(ws, in.Output, audioRel, string(format))
		if err != nil {
			return nil, err
		}

		threshold := in.InlineThreshold
		if threshold == 0 {
			threshold = d.InlineThreshold
		}

		jobID := d.Jobs.Submit(func(ctx context.Context, report func(job.Progress)) (any, error) {
			report(job.Progress{Fraction: 0.1, Message: "sending audio to Vertex AI"})
			result, err := d.Transcribe.Transcribe(ctx, req)
			if err != nil {
				return nil, classify(err)
			}
			report(job.Progress{Fraction: 0.9, Message: "writing transcript"})

			files, err := transcript.Render(result, format)
			if err != nil {
				if errors.Is(err, transcript.ErrEmpty) {
					return nil, toolerr.New(toolerr.CodeEmptyTranscript,
						"the recording produced no speech; it may be silent, or in a language the model does not handle")
				}
				return nil, toolerr.Newf(toolerr.CodeTranscribeFailed, "render transcript: %v", err)
			}

			// Subtitle formats split per language when a transcript carries
			// more than one. The first file is the primary; the rest are
			// written beside it and named in the result.
			var extra []string
			for i, f := range files {
				rel := withSuffix(outRel, f.Suffix)
				if err := ws.WriteFileAtomic(rel, []byte(f.Content)); err != nil {
					return nil, err
				}
				if i > 0 {
					extra = append(extra, rel)
				}
			}

			primary := withSuffix(outRel, files[0].Suffix)
			out := resultFor(primary, ws.Path(primary), string(format), files[0].Content, threshold, result)
			if len(extra) == 0 {
				return out, nil
			}
			return map[string]any{"transcript": out, "additional_files": extra}, nil
		})

		return describeJob(jobID, outRel), nil
	})
}

// boolOr resolves an optional argument against a default.
func boolOr(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

// resolveOutput picks where the transcript is written: the caller's choice, or
// output/<recording>.<ext> beside it.
func resolveOutput(ws *workspace.Workspace, requested, audioRel, format string) (string, error) {
	if requested != "" {
		return ws.ResolveInside(requested)
	}
	base := path.Base(audioRel)
	if ext := path.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return ws.ResolveInside(path.Join(workspace.DirOutput, base+"."+format))
}

// withSuffix inserts a language tag before the extension, matching how the CLI
// names split subtitle files.
func withSuffix(rel, suffix string) string {
	if suffix == "" {
		return rel
	}
	ext := path.Ext(rel)
	return strings.TrimSuffix(rel, ext) + suffix + ext
}

// classify maps failures onto stable tool-error codes so an agent can branch on
// the cause rather than parse prose. The codes an agent can actually act on are
// the interesting ones: missing credentials and a missing project are fixed by
// the operator, not by retrying.
func classify(err error) error {
	var te *toolerr.Error
	if errors.As(err, &te) {
		return te
	}
	switch {
	case errors.Is(err, asr.ErrSafetyBlock):
		return toolerr.New(toolerr.CodeSafetyBlocked, err.Error())
	case errors.Is(err, asr.ErrModeConflict):
		return toolerr.New(toolerr.CodeModeConflict, err.Error())
	case errors.Is(err, transcript.ErrEmpty):
		return toolerr.New(toolerr.CodeEmptyTranscript, err.Error())
	case errors.Is(err, staging.ErrTooLargeForInline):
		return toolerr.New(toolerr.CodeTooLargeForInline, err.Error())
	}

	msg := err.Error()
	switch {
	case strings.Contains(msg, config.DefaultPath()), strings.Contains(msg, "GCP project is required"):
		return toolerr.New(toolerr.CodeProjectRequired, msg)
	case strings.Contains(msg, "default credentials"), strings.Contains(msg, "could not find default credentials"):
		return toolerr.New(toolerr.CodeCredentials, msg)
	case strings.Contains(msg, "unsupported audio format"):
		return toolerr.New(toolerr.CodeUnsupportedFormat, msg)
	default:
		return toolerr.New(toolerr.CodeTranscribeFailed, msg)
	}
}
