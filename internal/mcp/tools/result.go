package tools

import (
	"fmt"
	"strings"

	"github.com/nlink-jp/gem-scribe/internal/transcript"
)

// DefaultMaxBytes caps how much of the transcript a result carries when neither
// the call nor the config says otherwise. It is config.DefaultMaxBytes,
// restated here so this package does not import the config for one number.
const DefaultMaxBytes = 65536

// where addresses one transcript: which work directory it landed in, which
// workspace inside it, and the file itself. The work directory is echoed
// because a caller whose runtime supplied it through the request `_meta`
// (org ADR-021) learns the destination from the result and nowhere else.
type where struct {
	WorkDir     string
	WorkspaceID string
	Rel         string
	Abs         string
}

// Result is what transcribe and check_job report for a finished transcription.
type Result struct {
	// WorkDir is the resolved work directory this call wrote into.
	WorkDir string `json:"work_dir"`
	// WorkspaceID is the workspace inside it.
	WorkspaceID string `json:"workspace_id"`
	// Path is the workspace-relative transcript file, always written.
	Path string `json:"path"`
	// AbsolutePath is the same file, for tools that cannot resolve the
	// workspace root themselves.
	AbsolutePath string `json:"absolute_path"`
	Format       string `json:"format"`
	Bytes        int    `json:"bytes"`

	// Text is the transcript, up to max_bytes. It is always present: the
	// result carries as much as the cap allows rather than switching to a
	// preview, because what fits is the caller's judgement, not this
	// server's (ADR-0011).
	Text string `json:"text"`
	// Truncated and OmittedBytes appear only when the cap dropped something.
	// Their presence is the signal; Bytes stays the exact total either way,
	// and Path reaches the rest.
	Truncated    bool   `json:"truncated,omitempty"`
	OmittedBytes int    `json:"omitted_bytes,omitempty"`
	Note         string `json:"note,omitempty"`

	Model    string   `json:"model"`
	Language string   `json:"language"`
	Segments int      `json:"segments"`
	Speakers []string `json:"speakers,omitempty"`
	Duration float64  `json:"duration_seconds"`

	// Warning describes a result that is well-formed but probably wrong —
	// diarization that over-split, most often. Without it an agent has no way
	// to tell ninety-three imaginary speakers from a real cast.
	Warning string `json:"warning,omitempty"`
}

// resultFor builds the result, capping the text it carries.
//
// The cap bounds the response and nothing else. The transcript file is written
// either way — it is this server's product, and a knob that changed whether
// the artifact exists would be a surprising thing to tune. What the cap leaves
// out is counted rather than quietly cut, and Path reaches all of it.
//
// maxBytes: negative means no cap, zero means DefaultMaxBytes. The caller's
// explicit 0 is turned into "no cap" before it gets here.
func resultFor(w where, format, content string, maxBytes int, r transcript.Result) Result {
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}

	out := Result{
		WorkDir:      w.WorkDir,
		WorkspaceID:  w.WorkspaceID,
		Path:         w.Rel,
		AbsolutePath: w.Abs,
		Format:       format,
		Bytes:        len(content),
		Model:        r.Metadata.Model,
		Segments:     len(r.Segments),
		Speakers:     r.Speakers(),
		Duration:     r.Duration(),
	}
	if len(r.Metadata.Languages) > 0 {
		out.Language = strings.Join(r.Metadata.Languages, ",")
	}
	if r.Metadata.DurationSeconds != nil {
		out.Duration = *r.Metadata.DurationSeconds
	}

	// A single speaker is the un-diarized default and carries no information;
	// reporting ["A"] would suggest diarization ran and found one person.
	if !r.Metadata.Diarized {
		out.Speakers = nil
	}

	// One field carries every diagnosis: an agent that reads `warning` at all
	// reads all of it, and a second field would be a second thing to forget.
	if ds := transcript.Diagnose(r); len(ds) > 0 {
		parts := make([]string, 0, len(ds))
		for _, d := range ds {
			parts = append(parts, d.String())
		}
		out.Warning = strings.Join(parts, " ")
	}

	if maxBytes < 0 || len(content) <= maxBytes {
		out.Text = content
		return out
	}
	out.Text = excerpt(content, maxBytes)
	out.Truncated = true
	out.OmittedBytes = len(content) - len(out.Text)
	out.Note = fmt.Sprintf("Transcript capped at max_bytes=%d; %d of %d bytes are not in this result. "+
		"Read them from the transcript file at absolute_path, or raise max_bytes.",
		maxBytes, out.OmittedBytes, len(content))
	return out
}

// excerpt returns the leading n bytes, cut at a rune boundary and, where one is
// nearby, at a line boundary. Cutting mid-rune would corrupt Japanese text into
// replacement characters, which is exactly the audience this tool serves.
func excerpt(s string, n int) string {
	if len(s) <= n {
		return s
	}

	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	if nl := strings.LastIndexByte(s[:cut], '\n'); nl > cut/2 {
		cut = nl
	}
	return strings.TrimRight(s[:cut], "\n") + "\n…"
}

// utf8Start reports whether b begins a UTF-8 rune (i.e. is not a continuation
// byte, which are all 0b10xxxxxx).
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// describeJob renders a job submission acknowledgement. It names the work
// directory the job resolved, so a caller that did not pass one itself can see
// where the transcript will appear before the job finishes.
func describeJob(jobID, workDir, workspaceID, rel string) map[string]any {
	return map[string]any{
		"job_id":       jobID,
		"state":        "queued",
		"work_dir":     workDir,
		"workspace_id": workspaceID,
		"output":       rel,
		"next": fmt.Sprintf(
			"poll check_job with job_id %q; the transcript is written to %q when it reports done",
			jobID, rel),
	}
}
