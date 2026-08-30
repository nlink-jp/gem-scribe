package tools

import (
	"fmt"
	"strings"

	"github.com/nlink-jp/gem-scribe/internal/transcript"
)

// DefaultInlineThreshold is the transcript size, in bytes, at or below which a
// result comes back inline rather than only as a path.
//
// 8 KB is roughly forty minutes of conversation as plain text, or a few minutes
// as JSON. Below it, an agent reading the transcript costs one round trip;
// above it, the text starts crowding out the context the agent needs to do
// anything with it.
const DefaultInlineThreshold = 8192

// excerptLimit caps the preview attached to a file-mediated result. It exists
// so an agent can tell what it fetched — the language, the speakers, whether it
// is obviously garbage — without reading the file.
const excerptLimit = 600

// Result is what transcribe and check_job report for a finished transcription.
type Result struct {
	// Path is the workspace-relative transcript file, always written.
	Path string `json:"path"`
	// AbsolutePath is the same file, for tools that cannot resolve the
	// workspace root themselves.
	AbsolutePath string `json:"absolute_path"`
	Format       string `json:"format"`
	Bytes        int    `json:"bytes"`

	// Text is the whole transcript, present only when it fits inline.
	Text string `json:"text,omitempty"`
	// Excerpt is the leading fragment, present only when Text is not.
	Excerpt string `json:"excerpt,omitempty"`
	// Truncated reports whether the caller must read Path to see everything.
	Truncated bool `json:"truncated"`

	Model    string   `json:"model"`
	Language string   `json:"language"`
	Segments int      `json:"segments"`
	Speakers []string `json:"speakers,omitempty"`
	Duration float64  `json:"duration_seconds"`

	// Warning describes a result that is well-formed but probably wrong —
	// diarization that returned one speaker for a conversation, most often.
	// An agent cannot hear the audio, so without this a transcript that is
	// structurally perfect and substantively wrong looks like a good one.
	Warning string `json:"warning,omitempty"`
}

// resultFor decides between inline text and a path plus excerpt.
//
// The file is written either way: an agent that decided to keep the transcript
// should not have to ask for it again, and a threshold that changes whether the
// artifact exists would be a surprising thing to tune.
func resultFor(rel, abs, format, content string, threshold int, r transcript.Result) Result {
	if threshold <= 0 {
		threshold = DefaultInlineThreshold
	}

	out := Result{
		Path:         rel,
		AbsolutePath: abs,
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
	// reporting it would suggest diarization ran and found one person.
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

	if len(content) <= threshold {
		out.Text = content
		return out
	}
	out.Truncated = true
	out.Excerpt = excerpt(content, excerptLimit)
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

// describeJob renders a job submission acknowledgement.
func describeJob(jobID, rel string) map[string]any {
	return map[string]any{
		"job_id": jobID,
		"state":  "queued",
		"output": rel,
		"next": fmt.Sprintf(
			"poll check_job with job_id %q; the transcript is written to %q when it reports done",
			jobID, rel),
	}
}
