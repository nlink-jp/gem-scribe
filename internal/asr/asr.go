// Package asr calls Vertex AI's dedicated transcription model and maps its
// response onto gem-scribe's output envelope.
//
// The model returns speaker turns as separate response parts, each with its own
// label and word timings, so this package never asks a language model to
// produce JSON and never has to repair what one produced. That is the whole
// reason the tool exists in this shape.
package asr

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/nlink-jp/gem-scribe/internal/config"
	"github.com/nlink-jp/gem-scribe/internal/transcript"
	"google.golang.org/genai"
)

const maxRetries = 3

var (
	// ErrSafetyBlock reports a request the safety filter refused.
	ErrSafetyBlock = errors.New("request blocked by safety filter")
	// ErrModeConflict reports the SMART/timestamps incompatibility.
	ErrModeConflict = errors.New("smart mode cannot be combined with diarization or word timestamps")
)

// Source is the audio to transcribe: either bytes to send inline or a GCS URI
// the service reads for itself. Exactly one of Data and URI is set.
type Source struct {
	Data     []byte
	URI      string
	MIMEType string
}

// Options are the per-call transcription settings.
type Options struct {
	// Languages are BCP-47 hints. Empty means automatic detection.
	Languages []string
	// Diarize splits the response into per-speaker turns.
	Diarize bool
	// WordTimestamps attaches word-level timings, which is what gives segments
	// their start and end.
	WordTimestamps bool
	// Smart asks for disfluency removal and light formatting. The API
	// documents it as incompatible with the two flags above.
	Smart bool
}

// Validate rejects option combinations the API documents as unsupported.
//
// The combination is checked here rather than trusted to the API: SMART with
// diarization was observed to be accepted and answered normally, so relying on
// a server-side rejection would make the tool's behaviour depend on an
// undocumented leniency that can be withdrawn without notice.
func (o Options) Validate() error {
	if o.Smart && (o.Diarize || o.WordTimestamps) {
		return ErrModeConflict
	}
	return nil
}

// Client talks to one transcription model at one endpoint.
type Client struct {
	inner    *genai.Client
	model    string
	location string
}

// New builds a client for the configured project, location and model.
func New(ctx context.Context, cfg *config.Config) (*Client, error) {
	if err := cfg.RequireProject(); err != nil {
		return nil, err
	}
	inner, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  cfg.GCP.Project,
		Location: cfg.GCP.Location,
	})
	if err != nil {
		return nil, fmt.Errorf("create Vertex AI client: %w", err)
	}
	return &Client{inner: inner, model: cfg.Model.Name, location: cfg.GCP.Location}, nil
}

// Model returns the model this client transcribes with.
func (c *Client) Model() string { return c.model }

// Location returns the endpoint this client uses.
func (c *Client) Location() string { return c.location }

// Transcribe sends one audio source and returns its segments.
func (c *Client) Transcribe(ctx context.Context, src Source, opts Options) ([]transcript.Segment, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	part, err := audioPart(src)
	if err != nil {
		return nil, err
	}
	contents := []*genai.Content{genai.NewContentFromParts([]*genai.Part{part}, genai.RoleUser)}
	cfg := generateConfig(opts)

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		resp, err := c.inner.Models.GenerateContent(ctx, c.model, contents, cfg)
		if err == nil {
			return Parse(resp, opts.Languages)
		}

		lastErr = err
		if !retryable(err) || attempt == maxRetries {
			return nil, fmt.Errorf("transcribe: %w", hintGlobalEndpoint(err, c.model, c.location))
		}

		wait := backoff(attempt)
		log.Printf("transcription call failed (attempt %d/%d), retrying in %v: %v",
			attempt+1, maxRetries+1, wait, err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil, fmt.Errorf("transcribe after %d retries: %w", maxRetries,
		hintGlobalEndpoint(lastErr, c.model, c.location))
}

func audioPart(src Source) (*genai.Part, error) {
	switch {
	case src.URI != "" && len(src.Data) > 0:
		return nil, errors.New("audio source has both inline data and a URI")
	case src.URI != "":
		return genai.NewPartFromURI(src.URI, src.MIMEType), nil
	case len(src.Data) > 0:
		return genai.NewPartFromBytes(src.Data, src.MIMEType), nil
	default:
		return nil, errors.New("audio source is empty")
	}
}

func generateConfig(opts Options) *genai.GenerateContentConfig {
	ac := &genai.AudioTranscriptionConfig{LanguageCodes: opts.Languages}
	if opts.Smart {
		ac.Mode = genai.AudioTranscriptionConfigModeSmart
	} else {
		ac.Mode = genai.AudioTranscriptionConfigModeVerbatim
		if opts.Diarize {
			ac.Diarization = boolPtr(true)
		}
		if opts.WordTimestamps {
			ac.WordTimestamp = boolPtr(true)
		}
	}
	// CustomVocabulary is deliberately never set. Supplying it truncated the
	// transcript to its first turn in every measured run, and did so with
	// finishReason STOP rather than an error — a silent, total loss of content.
	return &genai.GenerateContentConfig{AudioTranscriptionConfig: ac}
}

func retryable(err error) bool {
	s := strings.ToLower(err.Error())
	for _, k := range []string{"429", "500", "503", "unavailable", "deadline", "timeout", "connection refused", "eof"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

func backoff(attempt int) time.Duration {
	d := 2 * time.Second << attempt
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// hintGlobalEndpoint explains a 404 that a regional endpoint produces for a
// Gemini 3 model. Vertex AI serves that family from the global endpoint only,
// and the error it returns talks about the model name being invalid — it never
// mentions the location, so without this the reader looks in the wrong place.
func hintGlobalEndpoint(err error, model, location string) error {
	if err == nil || location == "global" {
		return err
	}
	if !strings.HasPrefix(strings.TrimPrefix(model, "google/"), "gemini-3") {
		return err
	}
	s := strings.ToLower(err.Error())
	if !strings.Contains(s, "not_found") && !strings.Contains(s, "404") {
		return err
	}
	return fmt.Errorf("%w (hint: %s is served only from the global endpoint — set location = \"global\")", err, model)
}

func boolPtr(b bool) *bool { return &b }
