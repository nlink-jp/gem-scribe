package asr

import (
	"fmt"
	"strings"
	"time"

	"github.com/nlink-jp/gem-scribe/internal/transcript"
	"google.golang.org/genai"
)

// UndeterminedLanguage keys a segment whose language nothing has established.
//
// The response type carries a LanguageCode field, but Vertex AI leaves it empty
// on every response measured — with and without language hints — so the key
// comes from what the caller asked for instead. When the caller asked for
// nothing, this is the honest answer: "und" is the ISO 639-2 code for an
// undetermined language, so a consumer sees a valid code rather than a guess.
const UndeterminedLanguage = "und"

// Parse maps a transcription response onto the output envelope.
//
// It is separate from the API call so that the mapping — where the interesting
// mistakes live — is testable against recorded responses without a network or a
// GCP project.
//
// requestedLanguages are the BCP-47 codes the caller asked the model to expect;
// the first one supplies the envelope's language key when the response does not.
func Parse(resp *genai.GenerateContentResponse, requestedLanguages []string) ([]transcript.Segment, error) {
	if resp == nil || len(resp.Candidates) == 0 {
		return nil, fmt.Errorf("response carried no candidates")
	}

	candidate := resp.Candidates[0]
	if candidate.FinishReason == genai.FinishReasonSafety {
		return nil, ErrSafetyBlock
	}
	if candidate.Content == nil {
		return nil, fmt.Errorf("response carried no content")
	}

	fallbackLang := baseLanguage(first(requestedLanguages))

	var segments []transcript.Segment
	for i, part := range candidate.Content.Parts {
		t := part.AudioTranscription
		if t == nil || strings.TrimSpace(t.Text) == "" {
			// Parts without a transcription are not an error: a response may
			// carry other part kinds, and an empty one contributes nothing.
			continue
		}

		lang := baseLanguage(t.LanguageCode)
		if lang == "" {
			lang = fallbackLang
		}
		if lang == "" {
			lang = UndeterminedLanguage
		}

		start, end, err := span(t.Words)
		if err != nil {
			return nil, fmt.Errorf("part %d: %w", i, err)
		}

		speaker := t.SpeakerLabel
		if speaker == "" {
			speaker = transcript.SingleSpeaker
		}

		segments = append(segments, transcript.Segment{
			Start:   start,
			End:     end,
			Speaker: speaker,
			Text:    map[string]string{lang: strings.TrimSpace(t.Text)},
		})
	}

	if len(segments) == 0 {
		return nil, transcript.ErrEmpty
	}
	return segments, nil
}

// span derives a segment's bounds from its words. With word timestamps off the
// response carries no words at all, and the segment is reported as zero-length:
// the caller decides whether that is acceptable, because it only matters for
// the formats that render time.
func span(words []*genai.WordInfo) (start, end float64, err error) {
	if len(words) == 0 {
		return 0, 0, nil
	}

	start, err = parseOffset(words[0].StartOffset)
	if err != nil {
		return 0, 0, fmt.Errorf("start offset: %w", err)
	}
	// The last word is not necessarily the one that ends last, and a segment
	// whose end precedes its start fails envelope validation downstream.
	for _, w := range words {
		e, err := parseOffset(w.EndOffset)
		if err != nil {
			return 0, 0, fmt.Errorf("end offset: %w", err)
		}
		if e > end {
			end = e
		}
	}
	if end < start {
		end = start
	}
	return start, end, nil
}

// parseOffset reads the API's duration strings — "0.100s", "1s", "1868.700s" —
// as seconds. An empty offset is zero rather than an error: the field is
// documented as optional.
func parseOffset(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("cannot read %q as a duration", s)
	}
	return d.Seconds(), nil
}

// baseLanguage reduces a BCP-47 tag to its ISO 639-1 subtag ("ja-JP" → "ja"),
// which is what the output envelope keys text by.
func baseLanguage(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return ""
	}
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	return strings.ToLower(tag)
}

func first(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}
