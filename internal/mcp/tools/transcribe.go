package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/nlink-jp/gem-scribe/internal/asr"
	"github.com/nlink-jp/gem-scribe/internal/config"
	"github.com/nlink-jp/gem-scribe/internal/mcp/job"
	"github.com/nlink-jp/gem-scribe/internal/mcp/mcpserver"
	"github.com/nlink-jp/gem-scribe/internal/mcp/toolerr"
	"github.com/nlink-jp/gem-scribe/internal/mcp/workdir"
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
  "required": ["work_dir", "audio"],
  "properties": {
    "work_dir": {"type": "string", "description": "Absolute path to a directory you can read back \u2014 your session or working directory. The workspace is <work_dir>/<workspace_id>/ and the transcript is written there, so a directory you cannot open leaves you holding a path to nothing. It must already exist, and nothing here expands ~ or resolves a relative path."},
    "audio": {"type": "string", "description": "Recording to transcribe: a path relative to the workspace, or an absolute path to a recording anywhere you can read \u2014 it is read in place, never copied. Credential and agent-control locations (~/.ssh, ~/.aws and the like) are refused."},
    "workspace_id": {"type": "string", "description": "Workspace within work_dir; defaults to \"default\""},
    "model": {"type": "string", "description": "Transcription model; omit to use the configured one"},
    "languages": {"type": "array", "items": {"type": "string"}, "description": "BCP-47 hints such as [\"ja-JP\"]; omit to detect"},
    "diarize": {"type": "boolean", "description": "Label each speaker turn (default true). Up to 8 speakers; attribution beyond 2 is experimental"},
    "word_timestamps": {"type": "boolean", "description": "Attach word-level timings (default true). Required for srt and vtt"},
    "smart": {"type": "boolean", "description": "Remove disfluencies and format lightly. Cannot be combined with diarize or word_timestamps"},
    "format": {"type": "string", "enum": ["json", "text", "md", "srt", "vtt"], "description": "Default json"},
    "translate_to": {"type": "string", "description": "Add a translation beside the original, e.g. \"en\". A second pass over the transcript text"},
    "speaker_hints": {"type": "array", "items": {"type": "string"}, "description": "Candidate speaker names, assigned to spk:N in the same second pass"},
    "output": {"type": "string", "description": "Transcript path relative to the workspace; defaults under output/"},
    "max_bytes": {"type": "integer", "minimum": 0, "description": "Cap on transcript bytes carried in the result (default 65536; 0 means no cap). What the cap leaves out is counted in omitted_bytes, and bytes stays the exact total. The transcript file is written either way \u2014 set this to what your context can hold."}
  },
  "additionalProperties": false
}`),
	}, func(ctx context.Context, args json.RawMessage) (any, error) {
		var in struct {
			Audio          string   `json:"audio"`
			WorkDir        string   `json:"work_dir"`
			WorkspaceID    string   `json:"workspace_id"`
			Model          string   `json:"model"`
			Languages      []string `json:"languages"`
			Diarize        *bool    `json:"diarize"`
			WordTimestamps *bool    `json:"word_timestamps"`
			Smart          bool     `json:"smart"`
			TranslateTo    string   `json:"translate_to"`
			SpeakerHints   []string `json:"speaker_hints"`
			Format         string   `json:"format"`
			Output         string   `json:"output"`
			MaxBytes       *int     `json:"max_bytes"`
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

		workDir, err := d.WorkDir.Resolve(ctx, in.WorkDir)
		if err != nil {
			return nil, err
		}

		if in.WorkspaceID == "" {
			in.WorkspaceID = "default"
		}
		ws, err := d.WS.EnsureUnder(workDir, in.WorkspaceID)
		if err != nil {
			return nil, err
		}

		audioAbs, audioName, err := resolveAudio(ws, in.Audio)
		if err != nil {
			return nil, err
		}
		// Refusing an unsupported container here, with the accepted list in
		// hand, beats a job that fails minutes later with an opaque API error.
		if _, err := staging.MIMETypeFor(audioName); err != nil {
			return nil, toolerr.Newf(toolerr.CodeUnsupportedFormat, "%v", err)
		}
		req.Audio = audioAbs

		outRel, err := resolveOutput(ws, in.Output, audioName, string(format))
		if err != nil {
			return nil, err
		}

		// A caller that passes 0 means "no cap"; one that passes nothing gets
		// the configured default. The pointer is what tells the two apart.
		maxBytes := d.MaxBytes
		if in.MaxBytes != nil {
			if maxBytes = *in.MaxBytes; maxBytes == 0 {
				maxBytes = -1
			}
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
			out := resultFor(where{WorkDir: workDir, WorkspaceID: ws.ID, Rel: primary, Abs: ws.Path(primary)},
				string(format), files[0].Content, maxBytes, result)
			if len(extra) == 0 {
				return out, nil
			}
			return map[string]any{"transcript": out, "additional_files": extra}, nil
		})

		return describeJob(jobID, workDir, ws.ID, outRel), nil
	})
}

// boolOr resolves an optional argument against a default.
func boolOr(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

// resolveAudio locates the recording and returns the absolute path the decoder
// reads plus the name the default transcript is derived from.
//
// A relative path is workspace-relative, and os.Root keeps the read inside the
// workspace. An absolute path is read where it lies: the caller could have
// read it itself, and copying an hour of audio into the workspace to transcribe
// it would be pure waste (org ADR-021 §7). What is refused is a credential or
// agent-control location, checked on both spellings of the path — as given and
// symlink-resolved — because either alone has a hole.
func resolveAudio(ws *workspace.Workspace, audio string) (string, string, error) {
	if audio == "" {
		return "", "", toolerr.New(toolerr.CodeMissingArgument, "audio is required")
	}
	if !filepath.IsAbs(audio) {
		rel, err := ws.ResolveInside(audio)
		if err != nil {
			return "", "", err
		}
		// The decoder cannot inherit os.Root, so the containment check happens
		// here, immediately before the absolute path is handed over.
		if err := ws.VerifyRegular(rel); err != nil {
			return "", "", err
		}
		return ws.Path(rel), rel, nil
	}

	resolved, err := filepath.EvalSymlinks(audio)
	if err != nil {
		return "", "", toolerr.Newf(toolerr.CodeInputNotFound,
			"audio %q cannot be read: %v", audio, err)
	}
	if why := workdir.Sensitive(audio, resolved); why != "" {
		return "", "", toolerr.Newf(toolerr.CodePathNotAllowed,
			"audio %q is refused: %s", audio, why)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", "", toolerr.Newf(toolerr.CodeInputNotFound, "audio %q cannot be read: %v", audio, err)
	}
	if !fi.Mode().IsRegular() {
		return "", "", toolerr.Newf(toolerr.CodePathNotAllowed,
			"audio %q is not a regular file (mode %s)", audio, fi.Mode())
	}
	return resolved, filepath.Base(resolved), nil
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
