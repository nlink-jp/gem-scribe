// Package transcript defines gem-scribe's output envelope and renders it into
// the shipping formats.
//
// The envelope is deliberately compatible with voice-scribe, the local
// counterpart of this tool, and with the archived gem-transcribe it succeeds:
// same field names, same shapes, same defaults. That is what lets a downstream
// consumer — the meeting-notes skill, say — parse a cloud transcript and a
// local one with a single parser. The most load-bearing piece of that
// compatibility is Segment.Text, which is a language-code → text map rather
// than a string; see its doc comment.
package transcript

import (
	"errors"
	"fmt"
	"sort"
)

// Segment is one span of transcribed audio.
type Segment struct {
	// Start and End are seconds from the beginning of the audio.
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	// Speaker labels who is talking. With diarization it is the model's own
	// label ("spk:0", "spk:1", ...), or a name attributed to it by the second
	// pass. Without diarization every segment carries SingleSpeaker.
	Speaker string `json:"speaker"`
	// Text maps an ISO 639-1 language code to the text in that language. A
	// plain transcription has exactly one key; a translation pass adds another
	// alongside the original. voice-scribe uses the same shape, which is why
	// this is a map and not a string.
	Text map[string]string `json:"text"`
}

// SingleSpeaker is the label every segment carries when diarization is off. It
// is not the empty string because the envelope requires a non-empty speaker,
// and a consumer that groups by speaker should see one group rather than none.
const SingleSpeaker = "A"

// Metadata describes the run that produced a transcript.
//
// The first six fields are the envelope all three transcription tools share.
// The ones after them are gem-scribe additions; a consumer written against the
// shared envelope ignores them, which is why they are additive rather than a
// reshuffle.
type Metadata struct {
	Source          string   `json:"source"`
	Model           string   `json:"model"`
	DurationSeconds *float64 `json:"duration_seconds"`
	Languages       []string `json:"languages"`
	SpeakerHints    []string `json:"speaker_hints"`
	// DroppedSegments exists for shape compatibility and is normally 0. It
	// counts segments discarded as invalid — the failure mode gem-transcribe
	// added it for, when it asked a general model to emit JSON. The dedicated
	// transcription model returns structured parts instead, so nothing here
	// increments it today.
	DroppedSegments int `json:"dropped_segments"`

	Engine     string `json:"engine,omitempty"`
	Translated bool   `json:"translated,omitempty"`
	Diarized   bool   `json:"diarized,omitempty"`
	// Location is the Vertex AI endpoint the transcription ran against. The
	// Gemini 3 family is served from "global" only, so recording it makes a
	// transcript reproducible without guessing at the caller's configuration.
	Location string `json:"location,omitempty"`
}

// Result is a complete transcript.
type Result struct {
	Metadata Metadata  `json:"metadata"`
	Segments []Segment `json:"segments"`
}

// Languages returns every language code present across the segments, sorted so
// that output file names and rendering order are deterministic.
func (r Result) Languages() []string {
	seen := map[string]bool{}
	for _, s := range r.Segments {
		for lang := range s.Text {
			seen[lang] = true
		}
	}

	langs := make([]string, 0, len(seen))
	for lang := range seen {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	return langs
}

// Speakers returns the distinct speaker labels in first-appearance order.
func (r Result) Speakers() []string {
	var speakers []string
	seen := map[string]bool{}
	for _, s := range r.Segments {
		if !seen[s.Speaker] {
			seen[s.Speaker] = true
			speakers = append(speakers, s.Speaker)
		}
	}
	return speakers
}

// Duration returns the end of the last segment, which is the best lower bound
// on audio length available from the transcript alone.
func (r Result) Duration() float64 {
	var end float64
	for _, s := range r.Segments {
		if s.End > end {
			end = s.End
		}
	}
	return end
}

// ErrEmpty reports a transcript with no usable segments. Callers surface it
// rather than writing an empty file: silence and a decode that produced nothing
// look identical on disk, and only one of them is a success.
var ErrEmpty = errors.New("transcript has no segments")

// Validate checks the invariants the envelope promises to consumers. It is
// applied before rendering so that a malformed transcript fails here, with a
// specific message, rather than downstream in someone else's parser.
func (r Result) Validate() error {
	if len(r.Segments) == 0 {
		return ErrEmpty
	}
	if len(r.Metadata.Languages) == 0 {
		return errors.New("metadata.languages is empty")
	}

	for i, s := range r.Segments {
		if s.End < s.Start {
			return fmt.Errorf("segment %d: end (%g) is before start (%g)", i, s.End, s.Start)
		}
		if s.Speaker == "" {
			return fmt.Errorf("segment %d: speaker is empty", i)
		}
		if len(s.Text) == 0 {
			return fmt.Errorf("segment %d: text has no languages", i)
		}
		for lang, text := range s.Text {
			if lang == "" {
				return fmt.Errorf("segment %d: empty language code", i)
			}
			if text == "" {
				return fmt.Errorf("segment %d: text for language %q is empty", i, lang)
			}
		}
	}
	return nil
}

// Normalize fills in the fields whose zero value would be wrong on the wire.
//
// Empty slices matter: the envelope declares speaker_hints as a list, so it has
// to marshal as [] rather than null, and a nil slice in Go marshals as null.
// Callers get this wrong exactly once, in a place no test covers, so the
// constructor does it instead.
func (r *Result) Normalize() {
	if r.Metadata.SpeakerHints == nil {
		r.Metadata.SpeakerHints = []string{}
	}
	if r.Metadata.Languages == nil {
		r.Metadata.Languages = r.Languages()
	}
	if r.Segments == nil {
		r.Segments = []Segment{}
	}
	for i := range r.Segments {
		if r.Segments[i].Speaker == "" {
			r.Segments[i].Speaker = SingleSpeaker
		}
	}
}
