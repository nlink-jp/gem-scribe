package asr

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestOptions_Validate(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		bad  bool
	}{
		{"verbatim with everything", Options{Diarize: true, WordTimestamps: true}, false},
		{"smart alone", Options{Smart: true}, false},
		{"smart with diarization", Options{Smart: true, Diarize: true}, true},
		{"smart with timestamps", Options{Smart: true, WordTimestamps: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if tt.bad != (err != nil) {
				t.Errorf("Validate() = %v, wanted an error: %v", err, tt.bad)
			}
			if tt.bad && !errors.Is(err, ErrModeConflict) {
				t.Errorf("error is not ErrModeConflict: %v", err)
			}
		})
	}
}

func TestGenerateConfig(t *testing.T) {
	cfg := generateConfig(Options{Languages: []string{"ja-JP"}, Diarize: true, WordTimestamps: true})
	ac := cfg.AudioTranscriptionConfig
	if ac == nil {
		t.Fatal("AudioTranscriptionConfig is nil")
	}
	if ac.Mode != genai.AudioTranscriptionConfigModeVerbatim {
		t.Errorf("mode = %q, want VERBATIM", ac.Mode)
	}
	if ac.Diarization == nil || !*ac.Diarization {
		t.Error("diarization was not requested")
	}
	if ac.WordTimestamp == nil || !*ac.WordTimestamp {
		t.Error("word timestamps were not requested")
	}
	if len(ac.LanguageCodes) != 1 || ac.LanguageCodes[0] != "ja-JP" {
		t.Errorf("language codes = %v", ac.LanguageCodes)
	}

	// Supplying custom vocabulary silently truncates the transcript to its
	// first turn, so nothing in this package may ever set it.
	if len(ac.CustomVocabulary) != 0 {
		t.Errorf("custom vocabulary must never be sent, got %v", ac.CustomVocabulary)
	}
}

// SMART mode must not carry the flags it is incompatible with, even though
// Validate already rejects that combination — the two guards are independent.
func TestGenerateConfig_SmartCarriesNoConflictingFlags(t *testing.T) {
	ac := generateConfig(Options{Smart: true, Diarize: true, WordTimestamps: true}).AudioTranscriptionConfig
	if ac.Mode != genai.AudioTranscriptionConfigModeSmart {
		t.Errorf("mode = %q, want SMART", ac.Mode)
	}
	if ac.Diarization != nil || ac.WordTimestamp != nil {
		t.Error("SMART mode sent diarization or word timestamps")
	}
}

func TestAudioPart(t *testing.T) {
	if _, err := audioPart(Source{}); err == nil {
		t.Error("an empty source should be rejected")
	}
	if _, err := audioPart(Source{Data: []byte("x"), URI: "gs://b/o"}); err == nil {
		t.Error("a source with both inline data and a URI should be rejected")
	}

	p, err := audioPart(Source{URI: "gs://b/o.wav", MIMEType: "audio/wav"})
	if err != nil {
		t.Fatal(err)
	}
	if p.FileData == nil || p.FileData.FileURI != "gs://b/o.wav" {
		t.Errorf("URI source did not become a file part: %+v", p)
	}

	p, err = audioPart(Source{Data: []byte("x"), MIMEType: "audio/wav"})
	if err != nil {
		t.Fatal(err)
	}
	if p.InlineData == nil || string(p.InlineData.Data) != "x" {
		t.Errorf("inline source did not become an inline part: %+v", p)
	}
}

func TestHintGlobalEndpoint(t *testing.T) {
	notFound := errors.New("Error 404, Message: Publisher model `projects/p/locations/us-central1/publishers/google/models/gemini-3.5-transcribe-preview` was not found, Status: NOT_FOUND")
	quota := errors.New("Error 429: quota exceeded")

	tests := []struct {
		name     string
		err      error
		model    string
		location string
		wantHint bool
	}{
		{"regional 404 on a gemini 3 model", notFound, "gemini-3.5-transcribe-preview", "us-central1", true},
		{"google/ prefix still recognized", notFound, "google/gemini-3.5-transcribe-preview", "asia-northeast1", true},
		{"global needs no hint", notFound, "gemini-3.5-transcribe-preview", "global", false},
		{"a gemini 2.5 model 404s for real reasons", notFound, "gemini-2.5-flash", "us-central1", false},
		{"non-404 passes through", quota, "gemini-3.5-transcribe-preview", "us-central1", false},
		{"nil stays nil", nil, "gemini-3.5-transcribe-preview", "us-central1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hintGlobalEndpoint(tt.err, tt.model, tt.location)
			if tt.err == nil {
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, tt.err) {
				t.Errorf("the original error was not wrapped: %v", got)
			}
			if hinted := strings.Contains(got.Error(), "global endpoint"); hinted != tt.wantHint {
				t.Errorf("hint present = %v, want %v (%q)", hinted, tt.wantHint, got)
			}
		})
	}
}

func TestRetryable(t *testing.T) {
	for err, want := range map[string]bool{
		"Error 429: quota exceeded":      true,
		"Error 503: service unavailable": true,
		"Error 500: internal":            true,
		"unexpected EOF":                 true,
		"context deadline exceeded":      true,
		"Error 400: bad request":         false,
		"Error 404: not found":           false,
		"Error 403: permission denied":   false,
	} {
		if got := retryable(errors.New(err)); got != want {
			t.Errorf("retryable(%q) = %v, want %v", err, got, want)
		}
	}
}

func TestBackoff_IsBoundedAndGrows(t *testing.T) {
	prev := backoff(0)
	for i := 1; i < 8; i++ {
		d := backoff(i)
		if d < prev {
			t.Errorf("backoff(%d) = %v is shorter than backoff(%d) = %v", i, d, i-1, prev)
		}
		if d > 30_000_000_000 {
			t.Errorf("backoff(%d) = %v exceeds the 30s cap", i, d)
		}
		prev = d
	}
}
