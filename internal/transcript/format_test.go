package transcript

import (
	"encoding/json"
	"strings"
	"testing"
)

func sample() Result {
	r := Result{
		Metadata: Metadata{
			Source:    "meeting.m4a",
			Model:     "kotoba-whisper-v2.2-q5_0",
			Languages: []string{"ja"},
		},
		Segments: []Segment{
			{Start: 0, End: 4.12, Speaker: "A", Text: map[string]string{"ja": "それでは始めます。"}},
			{Start: 4.12, End: 9.5, Speaker: "B", Text: map[string]string{"ja": "お願いします。"}},
		},
	}
	r.Normalize()
	return r
}

func bilingual() Result {
	r := sample()
	r.Metadata.Languages = []string{"ja", "en"}
	r.Segments[0].Text["en"] = "Let's begin."
	r.Segments[1].Text["en"] = "Please go ahead."
	return r
}

func render(t *testing.T, r Result, f Format) []File {
	t.Helper()
	files, err := Render(r, f)
	if err != nil {
		t.Fatalf("Render(%s): %v", f, err)
	}
	return files
}

// TestJSONMatchesGemTranscribeEnvelope is the compatibility contract: a
// downstream consumer written against gem-transcribe must be able to parse this
// output. It checks the field names and, crucially, that `text` is an object
// keyed by language rather than a string.
func TestJSONMatchesGemTranscribeEnvelope(t *testing.T) {
	files := render(t, sample(), FormatJSON)
	if len(files) != 1 {
		t.Fatalf("json produced %d files, want 1", len(files))
	}

	var decoded struct {
		Metadata struct {
			Source          string    `json:"source"`
			Model           string    `json:"model"`
			DurationSeconds *float64  `json:"duration_seconds"`
			Languages       []string  `json:"languages"`
			SpeakerHints    *[]string `json:"speaker_hints"`
			DroppedSegments *int      `json:"dropped_segments"`
		} `json:"metadata"`
		Segments []struct {
			Start   *float64          `json:"start"`
			End     *float64          `json:"end"`
			Speaker string            `json:"speaker"`
			Text    map[string]string `json:"text"`
		} `json:"segments"`
	}
	if err := json.Unmarshal([]byte(files[0].Content), &decoded); err != nil {
		t.Fatalf("output is not parseable as the gem-transcribe envelope: %v", err)
	}

	if decoded.Metadata.SpeakerHints == nil {
		t.Error("speaker_hints marshalled as null; gem-transcribe declares it a list, so it must be []")
	}
	if decoded.Metadata.DroppedSegments == nil {
		t.Error("dropped_segments is absent; it is part of the shared shape even when always 0")
	}
	if len(decoded.Segments) != 2 {
		t.Fatalf("got %d segments, want 2", len(decoded.Segments))
	}
	if got := decoded.Segments[0].Text["ja"]; got != "それでは始めます。" {
		t.Errorf("segments[0].text.ja = %q, want the Japanese text", got)
	}
	for i, s := range decoded.Segments {
		if s.Start == nil || s.End == nil {
			t.Errorf("segment %d: start/end must be present even at 0", i)
		}
	}
}

// TestListFieldsNeverMarshalAsNull guards the trap Normalize exists for: a nil
// Go slice becomes JSON null, which a consumer expecting a list rejects.
//
// duration_seconds is deliberately excluded — gem-transcribe declares it
// `float | None`, so null is a valid value there rather than a defect.
func TestListFieldsNeverMarshalAsNull(t *testing.T) {
	r := Result{
		Metadata: Metadata{Source: "a.wav", Model: "m", Languages: []string{"ja"}},
		Segments: []Segment{{End: 1, Text: map[string]string{"ja": "x"}}},
	}
	r.Normalize()

	if r.Segments[0].Speaker != SingleSpeaker {
		t.Errorf("Normalize left speaker %q, want %q", r.Segments[0].Speaker, SingleSpeaker)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"languages", "speaker_hints", "segments"} {
		if strings.Contains(string(b), `"`+field+`":null`) {
			t.Errorf("%s marshalled as null: %s", field, b)
		}
	}
}

// TestNormalizeDerivesLanguagesFromSegments keeps the caller from having to
// restate what the segments already say.
func TestNormalizeDerivesLanguagesFromSegments(t *testing.T) {
	r := Result{Segments: []Segment{
		{End: 1, Text: map[string]string{"ja": "x", "en": "y"}},
	}}
	r.Normalize()

	if got := strings.Join(r.Metadata.Languages, ","); got != "en,ja" {
		t.Errorf("Languages = %q, want the sorted set derived from segments", got)
	}
}

func TestSRTAndVTTUseTheirOwnSeparators(t *testing.T) {
	srt := render(t, sample(), FormatSRT)[0].Content
	vtt := render(t, sample(), FormatVTT)[0].Content

	if !strings.Contains(srt, "00:00:00,000 --> 00:00:04,120") {
		t.Errorf("SubRip cue missing or malformed:\n%s", srt)
	}
	if !strings.HasPrefix(srt, "1\n") {
		t.Errorf("SubRip must number its cues, got:\n%s", srt)
	}
	if !strings.HasPrefix(vtt, "WEBVTT\n\n") {
		t.Errorf("WebVTT must start with the WEBVTT header, got:\n%s", vtt)
	}
	if !strings.Contains(vtt, "00:00:00.000 --> 00:00:04.120") {
		t.Errorf("WebVTT cue missing or malformed:\n%s", vtt)
	}
	if strings.Contains(vtt, ",120") {
		t.Error("WebVTT used a comma separator; players reject that")
	}
}

// TestSubtitlesSplitPerLanguage pins the behaviour gem-transcribe has: a
// subtitle file cannot hold two languages, so a translated transcript becomes
// one file per language, tagged for the caller to insert before the extension.
func TestSubtitlesSplitPerLanguage(t *testing.T) {
	files := render(t, bilingual(), FormatSRT)
	if len(files) != 2 {
		t.Fatalf("got %d files for a two-language transcript, want 2", len(files))
	}

	bySuffix := map[string]string{}
	for _, f := range files {
		bySuffix[f.Suffix] = f.Content
	}
	if _, ok := bySuffix[".ja"]; !ok {
		t.Errorf("no .ja file; got suffixes %v", keysOf(bySuffix))
	}
	if !strings.Contains(bySuffix[".en"], "Let's begin.") {
		t.Errorf(".en file does not carry the English text:\n%s", bySuffix[".en"])
	}
	if strings.Contains(bySuffix[".ja"], "Let's begin.") {
		t.Error(".ja file leaked English text")
	}
}

func TestSingleLanguageSubtitlesAreNotSuffixed(t *testing.T) {
	for _, f := range []Format{FormatSRT, FormatVTT} {
		files := render(t, sample(), f)
		if len(files) != 1 || files[0].Suffix != "" {
			t.Errorf("%s: got %d files with suffix %q, want 1 unsuffixed file",
				f, len(files), files[0].Suffix)
		}
	}
}

// TestSpeakerLabelsAppearOnlyWhenThereIsMoreThanOne keeps the common case
// clean: prefixing every line with "A: " when nothing was diarized is noise.
func TestSpeakerLabelsAppearOnlyWhenThereIsMoreThanOne(t *testing.T) {
	multi := render(t, sample(), FormatText)[0].Content
	if !strings.Contains(multi, "A: ") || !strings.Contains(multi, "B: ") {
		t.Errorf("two speakers should be labelled, got:\n%s", multi)
	}

	single := sample()
	single.Segments[1].Speaker = "A"
	plain := render(t, single, FormatText)[0].Content
	if strings.Contains(plain, "A: ") {
		t.Errorf("a single-speaker transcript should not be labelled, got:\n%s", plain)
	}
}

func TestTextFallsBackWhenATranslationIsMissing(t *testing.T) {
	r := bilingual()
	delete(r.Segments[1].Text, "en")

	// Ask for the English rendering: the second segment has no English text, so
	// this is the case where a naive renderer would emit an empty line.
	r.Metadata.Languages = []string{"en", "ja"}
	en := render(t, r, FormatText)

	if !strings.Contains(en[0].Content, "お願いします。") {
		t.Errorf("segment missing an English translation was dropped instead of falling back:\n%s", en[0].Content)
	}
}

func TestValidateRejectsMalformedTranscripts(t *testing.T) {
	tests := map[string]func(*Result){
		"end before start":  func(r *Result) { r.Segments[0].End = r.Segments[0].Start - 1 },
		"empty speaker":     func(r *Result) { r.Segments[0].Speaker = "" },
		"no languages":      func(r *Result) { r.Segments[0].Text = map[string]string{} },
		"empty text":        func(r *Result) { r.Segments[0].Text["ja"] = "" },
		"empty lang code":   func(r *Result) { r.Segments[0].Text[""] = "x" },
		"no segments":       func(r *Result) { r.Segments = nil },
		"metadata langless": func(r *Result) { r.Metadata.Languages = nil },
	}

	for name, corrupt := range tests {
		t.Run(name, func(t *testing.T) {
			r := sample()
			corrupt(&r)
			if err := r.Validate(); err == nil {
				t.Error("Validate accepted a malformed transcript")
			}
			if _, err := Render(r, FormatJSON); err == nil {
				t.Error("Render accepted a malformed transcript")
			}
		})
	}
}

func TestParseFormat(t *testing.T) {
	for _, f := range Formats() {
		got, err := ParseFormat(string(f))
		if err != nil || got != f {
			t.Errorf("ParseFormat(%q) = %v, %v", f, got, err)
		}
	}
	if _, err := ParseFormat("docx"); err == nil {
		t.Error("ParseFormat accepted an unknown format")
	} else if !strings.Contains(err.Error(), "json") {
		t.Errorf("error should list the valid formats, got %q", err)
	}
}

func TestTimestampRounding(t *testing.T) {
	for _, tc := range []struct {
		sec  float64
		want string
	}{
		{0, "00:00:00,000"},
		{1.5, "00:00:01,500"},
		{59.9995, "00:01:00,000"},
		{3661.234, "01:01:01,234"},
		{-1, "00:00:00,000"},
	} {
		if got := timestamp(tc.sec, ","); got != tc.want {
			t.Errorf("timestamp(%g) = %q, want %q", tc.sec, got, tc.want)
		}
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
