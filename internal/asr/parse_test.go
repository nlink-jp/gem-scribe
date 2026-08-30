package asr

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nlink-jp/gem-scribe/internal/transcript"
	"google.golang.org/genai"
)

// load reads a recorded Vertex AI response. The fixtures are real responses,
// not hand-written ones, so the parser is tested against the shape the API
// actually produces rather than the shape the docs describe.
func load(t *testing.T, name string) *genai.GenerateContentResponse {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var resp genai.GenerateContentResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", name, err)
	}
	return &resp
}

func TestParse_Diarized(t *testing.T) {
	segments, err := Parse(load(t, "diarized.json"), []string{"ja-JP"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(segments) != 3 {
		t.Fatalf("got %d segments, want 3 (one per speaker turn)", len(segments))
	}

	wantSpeakers := []string{"spk:0", "spk:1", "spk:0"}
	for i, s := range segments {
		if s.Speaker != wantSpeakers[i] {
			t.Errorf("segment %d speaker = %q, want %q", i, s.Speaker, wantSpeakers[i])
		}
		if s.End <= s.Start {
			t.Errorf("segment %d has no duration: %g..%g", i, s.Start, s.End)
		}
		if _, ok := s.Text["ja"]; !ok {
			t.Errorf("segment %d text is not keyed by the requested language: %v", i, s.Text)
		}
	}

	// Timestamps come from the words, so they must be seconds, not the raw
	// "0.100s" strings misread as some other unit.
	if segments[0].Start != 0.1 {
		t.Errorf("first start = %g, want 0.1", segments[0].Start)
	}
	// Segments must not overlap or run backwards across turns.
	for i := 1; i < len(segments); i++ {
		if segments[i].Start < segments[i-1].End {
			t.Errorf("segment %d starts (%g) before segment %d ends (%g)",
				i, segments[i].Start, i-1, segments[i-1].End)
		}
	}

	result := transcript.Result{
		Metadata: transcript.Metadata{Source: "x", Model: "m", Languages: []string{"ja"}},
		Segments: segments,
	}
	result.Normalize()
	if err := result.Validate(); err != nil {
		t.Errorf("parsed segments do not satisfy the envelope: %v", err)
	}
}

// Without diarization the API returns one part, no speaker label and no words.
// The envelope still requires a non-empty speaker.
func TestParse_PlainNoDiarization(t *testing.T) {
	segments, err := Parse(load(t, "plain.json"), []string{"ja-JP"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(segments) != 1 {
		t.Fatalf("got %d segments, want 1", len(segments))
	}
	if segments[0].Speaker != transcript.SingleSpeaker {
		t.Errorf("speaker = %q, want %q", segments[0].Speaker, transcript.SingleSpeaker)
	}
	if segments[0].Start != 0 || segments[0].End != 0 {
		t.Errorf("a response without words should report a zero span, got %g..%g",
			segments[0].Start, segments[0].End)
	}
}

// The response has never carried languageCode in practice, so an unhinted run
// must still produce a valid key rather than an empty one.
func TestParse_LanguageFallback(t *testing.T) {
	segments, err := Parse(load(t, "plain.json"), nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := segments[0].Text[UndeterminedLanguage]; !ok {
		t.Errorf("text keys = %v, want %q", keys(segments[0].Text), UndeterminedLanguage)
	}
}

// A response that does carry languageCode must win over the requested hint:
// the model heard what it heard.
func TestParse_ResponseLanguageWins(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{AudioTranscription: &genai.Transcription{Text: "hello", LanguageCode: "en-US"}},
		}}}},
	}
	segments, err := Parse(resp, []string{"ja-JP"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := segments[0].Text["en"]; !ok {
		t.Errorf("text keys = %v, want en", keys(segments[0].Text))
	}
}

func TestParse_Errors(t *testing.T) {
	if _, err := Parse(nil, nil); err == nil {
		t.Error("a nil response should be an error")
	}

	empty := &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{Content: &genai.Content{}}}}
	if _, err := Parse(empty, nil); !errors.Is(err, transcript.ErrEmpty) {
		t.Errorf("a response with no transcription parts should report ErrEmpty, got %v", err)
	}

	blocked := &genai.GenerateContentResponse{Candidates: []*genai.Candidate{
		{FinishReason: genai.FinishReasonSafety, Content: &genai.Content{}},
	}}
	if _, err := Parse(blocked, nil); !errors.Is(err, ErrSafetyBlock) {
		t.Errorf("a safety block should be reported as such, got %v", err)
	}
}

// A part whose text is blank contributes nothing and must not become an empty
// segment, which would fail envelope validation downstream.
func TestParse_SkipsEmptyParts(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{AudioTranscription: &genai.Transcription{Text: "   "}},
			{Text: "not a transcription"},
			{AudioTranscription: &genai.Transcription{Text: "real", SpeakerLabel: "spk:0"}},
		}}}},
	}
	segments, err := Parse(resp, []string{"ja"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(segments) != 1 || segments[0].Text["ja"] != "real" {
		t.Errorf("got %+v, want the one real segment", segments)
	}
}

func TestParseOffset(t *testing.T) {
	tests := []struct {
		in   string
		want float64
		bad  bool
	}{
		{"0.100s", 0.1, false},
		{"1s", 1, false},
		{"1868.700s", 1868.7, false},
		{"", 0, false},
		{"12", 0, true}, // the API always writes a unit; a bare number is a change worth failing on
		{"abc", 0, true},
	}
	for _, tt := range tests {
		got, err := parseOffset(tt.in)
		if tt.bad {
			if err == nil {
				t.Errorf("parseOffset(%q) should have failed", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseOffset(%q): %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("parseOffset(%q) = %g, want %g", tt.in, got, tt.want)
		}
	}
}

// The last word in the list is not necessarily the one that ends last.
func TestSpan_UsesLatestEnd(t *testing.T) {
	words := []*genai.WordInfo{
		{StartOffset: "1s", EndOffset: "5s"},
		{StartOffset: "2s", EndOffset: "3s"},
	}
	start, end, err := span(words)
	if err != nil {
		t.Fatal(err)
	}
	if start != 1 || end != 5 {
		t.Errorf("span = %g..%g, want 1..5", start, end)
	}
}

func TestBaseLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"ja-JP": "ja", "en-US": "en", "ja": "ja", "": "", "EN": "en", "cmn_Hans": "cmn",
	} {
		if got := baseLanguage(in); got != want {
			t.Errorf("baseLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
