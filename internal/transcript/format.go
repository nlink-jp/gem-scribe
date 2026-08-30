package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Format is an output rendering.
type Format string

const (
	FormatJSON Format = "json"
	FormatText Format = "text"
	FormatMD   Format = "md"
	FormatSRT  Format = "srt"
	FormatVTT  Format = "vtt"
)

// Formats lists every supported format, in the order they appear in help text.
func Formats() []Format {
	return []Format{FormatJSON, FormatText, FormatMD, FormatSRT, FormatVTT}
}

// ParseFormat resolves a user-supplied format name.
func ParseFormat(s string) (Format, error) {
	for _, f := range Formats() {
		if string(f) == s {
			return f, nil
		}
	}
	names := make([]string, 0, len(Formats()))
	for _, f := range Formats() {
		names = append(names, string(f))
	}
	return "", fmt.Errorf("unknown format %q (want one of: %s)", s, strings.Join(names, ", "))
}

// File is one rendered output.
//
// Suffix is inserted before the output file's extension. It is empty for
// formats that produce a single file, and a language tag (".ja", ".en") for the
// subtitle formats when the transcript carries more than one language: a WebVTT
// or SubRip file has no way to express two languages at once, so
// `-o meeting.srt` on a translated transcript yields meeting.ja.srt and
// meeting.en.srt. voice-scribe splits the same way.
type File struct {
	Suffix  string
	Content string
}

// Render turns a transcript into the files a given format produces.
func Render(r Result, f Format) ([]File, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}

	switch f {
	case FormatJSON:
		return renderJSON(r)
	case FormatText:
		return renderText(r)
	case FormatMD:
		return renderMarkdown(r)
	case FormatSRT:
		return renderTimed(r, srtStyle)
	case FormatVTT:
		return renderTimed(r, vttStyle)
	default:
		return nil, fmt.Errorf("unknown format %q", f)
	}
}

func renderJSON(r Result) ([]File, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode transcript: %w", err)
	}
	return []File{{Content: string(b) + "\n"}}, nil
}

// primaryLanguage is the language a single-language rendering uses: the first
// one declared in metadata, falling back to whatever the segments carry.
func primaryLanguage(r Result) string {
	if len(r.Metadata.Languages) > 0 {
		return r.Metadata.Languages[0]
	}
	if langs := r.Languages(); len(langs) > 0 {
		return langs[0]
	}
	return ""
}

// textIn returns a segment's text in the requested language, falling back to
// any language it does have. A segment missing the requested translation should
// still appear — dropping the line would silently shorten the transcript.
func textIn(s Segment, lang string) string {
	if t, ok := s.Text[lang]; ok {
		return t
	}

	// Fall back to the alphabetically first language the segment does have, so
	// the choice is deterministic across runs rather than map-iteration order.
	fallback := ""
	for l := range s.Text {
		if fallback == "" || l < fallback {
			fallback = l
		}
	}
	return s.Text[fallback]
}

func renderText(r Result) ([]File, error) {
	lang := primaryLanguage(r)
	labelled := len(r.Speakers()) > 1

	var b strings.Builder
	for _, s := range r.Segments {
		if labelled {
			fmt.Fprintf(&b, "%s: ", s.Speaker)
		}
		b.WriteString(textIn(s, lang))
		b.WriteString("\n")
	}
	return []File{{Content: b.String()}}, nil
}

func renderMarkdown(r Result) ([]File, error) {
	lang := primaryLanguage(r)
	labelled := len(r.Speakers()) > 1

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", r.Metadata.Source)
	fmt.Fprintf(&b, "- Model: `%s`\n", r.Metadata.Model)
	fmt.Fprintf(&b, "- Language: %s\n", strings.Join(r.Metadata.Languages, ", "))
	if d := r.Metadata.DurationSeconds; d != nil {
		fmt.Fprintf(&b, "- Duration: %s\n", clock(*d))
	}
	if len(r.Metadata.SpeakerHints) > 0 {
		fmt.Fprintf(&b, "- Speaker hints: %s\n", strings.Join(r.Metadata.SpeakerHints, ", "))
	}
	b.WriteString("\n")

	for _, s := range r.Segments {
		if labelled {
			fmt.Fprintf(&b, "**%s** ", s.Speaker)
		}
		fmt.Fprintf(&b, "[%s] %s\n\n", clock(s.Start), textIn(s, lang))
	}
	return []File{{Content: b.String()}}, nil
}

// cueStyle captures everything that differs between SubRip and WebVTT. They are
// the same format with three cosmetic disagreements, so they share one renderer.
type cueStyle struct {
	// header is written once at the top of the file.
	header string
	// separator sits between seconds and milliseconds. SubRip uses a comma and
	// WebVTT a period; players reject a file that uses the wrong one.
	separator string
	// numbered reports whether cues carry a sequence number (SubRip does).
	numbered bool
}

var (
	srtStyle = cueStyle{separator: ",", numbered: true}
	vttStyle = cueStyle{header: "WEBVTT\n\n", separator: "."}
)

func renderTimed(r Result, style cueStyle) ([]File, error) {
	langs := r.Metadata.Languages
	if len(langs) == 0 {
		langs = r.Languages()
	}
	labelled := len(r.Speakers()) > 1
	split := len(langs) > 1

	files := make([]File, 0, len(langs))
	for _, lang := range langs {
		var b strings.Builder
		b.WriteString(style.header)
		for i, s := range r.Segments {
			if style.numbered {
				fmt.Fprintf(&b, "%d\n", i+1)
			}
			fmt.Fprintf(&b, "%s --> %s\n",
				timestamp(s.Start, style.separator), timestamp(s.End, style.separator))
			if labelled {
				fmt.Fprintf(&b, "%s: ", s.Speaker)
			}
			b.WriteString(textIn(s, lang))
			b.WriteString("\n\n")
		}

		suffix := ""
		if split {
			suffix = "." + lang
		}
		files = append(files, File{Suffix: suffix, Content: b.String()})
	}
	return files, nil
}

// timestamp renders seconds as HH:MM:SS<sep>mmm. SubRip separates milliseconds
// with a comma and WebVTT with a period; a mismatch makes players reject the
// whole file, so the separator is a parameter rather than a hardcoded choice.
func timestamp(sec float64, sep string) string {
	if sec < 0 {
		sec = 0
	}
	ms := int64(sec*1000 + 0.5)
	h := ms / 3_600_000
	ms -= h * 3_600_000
	m := ms / 60_000
	ms -= m * 60_000
	s := ms / 1000
	ms -= s * 1000
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", h, m, s, sep, ms)
}

// clock renders seconds as HH:MM:SS for human-facing output.
func clock(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	total := int64(sec + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d", total/3600, (total%3600)/60, total%60)
}
