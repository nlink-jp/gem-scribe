// Package config loads gem-scribe's settings from a TOML file, the
// environment, and CLI flags, in that order of increasing precedence.
//
// The schema and the environment precedence are the org-wide convention for
// Vertex AI tools: a [gcp] section, a [model] section, and
// <TOOL>_<FIELD> > GOOGLE_CLOUD_<FIELD> > file > built-in default. The path is
// per-tool on purpose — tools are allowed to point at different GCP projects.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Built-in defaults.
//
// DefaultLocation is "global" because Vertex AI serves the Gemini 3 family
// from the global endpoint only; a regional location returns 404 NOT_FOUND for
// these models, and the error text does not say the location is the reason.
// DefaultSecondPassModel is a general Gemini model, not a transcription one:
// the dedicated transcription model neither translates nor names speakers, so
// anything beyond a verbatim transcript is a separate call over the transcript
// text. It is GA rather than preview because a supporting stage must not
// inherit a preview model's retirement schedule.
const (
	DefaultModel           = "gemini-3.5-transcribe-preview"
	DefaultLocation        = "global"
	DefaultSecondPassModel = "gemini-3.7-flash"
)

// Config is the fully resolved configuration.
type Config struct {
	GCP        GCPConfig        `toml:"gcp"`
	Model      ModelConfig      `toml:"model"`
	Transcribe TranscribeConfig `toml:"transcribe"`
	SecondPass SecondPassConfig `toml:"second_pass"`
	Staging    StagingConfig    `toml:"staging"`
}

// GCPConfig holds the Vertex AI endpoint coordinates.
type GCPConfig struct {
	Project  string `toml:"project"`
	Location string `toml:"location"`
}

// ModelConfig names the transcription model.
type ModelConfig struct {
	Name string `toml:"name"`
}

// TranscribeConfig holds the transcription defaults.
//
// The two booleans are pointers so that "absent from the file" is
// distinguishable from "explicitly false": a file that omits diarization must
// get the built-in default (on), while `diarization = false` must survive.
type TranscribeConfig struct {
	Diarization   *bool `toml:"diarization"`
	WordTimestamp *bool `toml:"word_timestamp"`
}

// SecondPassConfig names the general model used for the work the transcription
// model cannot do: translating the transcript and attributing real names to the
// speaker labels.
//
// Google also ships a translation-specialised model, Translation LLM, which
// would plausibly beat a general model at the translation half. It is not used
// here: it belongs to the Cloud Translation API rather than Vertex AI, so it
// would add a second service, a second client, a second IAM role and a regional
// endpoint to a tool that otherwise talks to one global one — and it still
// could not do the speaker-naming half.
type SecondPassConfig struct {
	// Model is the general Gemini model. Empty falls back to
	// DefaultSecondPassModel.
	Model string `toml:"model"`
	// Location is where that model is served. Empty means the same location as
	// the transcription call, which is the right default while both are global.
	Location string `toml:"location"`
}

// StagingConfig holds the GCS bucket used for audio too large to inline.
//
// An empty Bucket is a valid, common configuration: audio under the inline
// limit never needs a bucket, so requiring one up front would put a Cloud
// Storage setup between a new user and their first transcription.
type StagingConfig struct {
	Bucket string `toml:"bucket"`
	// KeepStaged leaves the uploaded object in place instead of deleting it
	// after the transcription. Useful when debugging what was actually sent.
	KeepStaged bool `toml:"keep_staged"`
}

// DefaultPath is the config file consulted when none is given.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "gem-scribe", "config.toml")
}

// Load reads the config file at path (or the default location when path is
// empty) and applies environment overrides.
//
// A missing file is not an error: every field has a default and the project can
// come from the environment. Load also does not require a project — `gem-scribe
// mcp` has to start before it knows whether any tool call will need one, so the
// check lives in RequireProject, at the point of use.
func Load(path string) (*Config, error) {
	diarization, wordTimestamp := true, true
	cfg := &Config{
		GCP:        GCPConfig{Location: DefaultLocation},
		Model:      ModelConfig{Name: DefaultModel},
		Transcribe: TranscribeConfig{Diarization: &diarization, WordTimestamp: &wordTimestamp},
	}

	explicit := path != ""
	if path == "" {
		path = DefaultPath()
	}
	if path != "" {
		switch _, err := os.Stat(path); {
		case err == nil:
			if _, err := toml.DecodeFile(path, cfg); err != nil {
				return nil, fmt.Errorf("parse config %s: %w", path, err)
			}
		case explicit:
			// A path the user typed and that does not exist is a mistake worth
			// reporting; the default path being absent is not.
			return nil, fmt.Errorf("config file not found: %s", path)
		}
	}

	applyEnv(cfg)
	cfg.normalize()
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v := firstEnv("GEMSCRIBE_PROJECT", "GOOGLE_CLOUD_PROJECT"); v != "" {
		cfg.GCP.Project = v
	}
	if v := firstEnv("GEMSCRIBE_LOCATION", "GOOGLE_CLOUD_LOCATION"); v != "" {
		cfg.GCP.Location = v
	}
	if v := firstEnv("GEMSCRIBE_MODEL"); v != "" {
		cfg.Model.Name = v
	}
	if v := firstEnv("GEMSCRIBE_STAGING_BUCKET"); v != "" {
		cfg.Staging.Bucket = v
	}
	if v := firstEnv("GEMSCRIBE_SECOND_PASS_MODEL"); v != "" {
		cfg.SecondPass.Model = v
	}
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// normalize repairs a file that set a field to the empty string. Decoding an
// empty value overwrites the default with "", which would then be sent to the
// API as a blank location or model name.
func (c *Config) normalize() {
	if c.GCP.Location == "" {
		c.GCP.Location = DefaultLocation
	}
	if c.Model.Name == "" {
		c.Model.Name = DefaultModel
	}
	if c.SecondPass.Model == "" {
		c.SecondPass.Model = DefaultSecondPassModel
	}
	if c.Transcribe.Diarization == nil {
		c.Transcribe.Diarization = boolPtr(true)
	}
	if c.Transcribe.WordTimestamp == nil {
		c.Transcribe.WordTimestamp = boolPtr(true)
	}
}

// RequireProject reports the missing GCP project as an actionable error. It is
// called at the point an API call is about to happen, not at load time.
func (c *Config) RequireProject() error {
	if c.GCP.Project == "" {
		return fmt.Errorf("GCP project is required: set [gcp].project in %s, or the GEMSCRIBE_PROJECT / GOOGLE_CLOUD_PROJECT environment variable", DefaultPath())
	}
	return nil
}

// SecondPassLocation returns the endpoint for the second pass, defaulting to
// the transcription endpoint.
func (c *Config) SecondPassLocation() string {
	if c.SecondPass.Location != "" {
		return c.SecondPass.Location
	}
	return c.GCP.Location
}

// Diarize reports whether speaker diarization is on.
func (c *Config) Diarize() bool { return c.Transcribe.Diarization != nil && *c.Transcribe.Diarization }

// WordTimestamps reports whether word-level timestamps are on.
func (c *Config) WordTimestamps() bool {
	return c.Transcribe.WordTimestamp != nil && *c.Transcribe.WordTimestamp
}

// ApplyFlags overlays the CLI flags that were actually set. Each argument is
// nil when the user did not pass the flag, which is what keeps a config file's
// `diarization = false` from being silently overwritten by a flag default.
func (c *Config) ApplyFlags(model, location string, diarize, wordTimestamps *bool) {
	if model != "" {
		c.Model.Name = model
	}
	if location != "" {
		c.GCP.Location = location
	}
	if diarize != nil {
		c.Transcribe.Diarization = boolPtr(*diarize)
	}
	if wordTimestamps != nil {
		c.Transcribe.WordTimestamp = boolPtr(*wordTimestamps)
	}
}

func boolPtr(b bool) *bool { return &b }
