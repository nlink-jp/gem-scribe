package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearEnv blanks every variable Load consults so a developer's own shell does
// not leak into the assertions.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GEMSCRIBE_PROJECT", "GOOGLE_CLOUD_PROJECT",
		"GEMSCRIBE_LOCATION", "GOOGLE_CLOUD_LOCATION",
		"GEMSCRIBE_MODEL", "GEMSCRIBE_STAGING_BUCKET", "GEMSCRIBE_SECOND_PASS_MODEL",
	} {
		t.Setenv(k, "")
	}
}

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err == nil {
		t.Fatal("an explicitly named missing file should be an error")
	}

	// The default path is allowed to be absent, so load through it instead.
	t.Setenv("HOME", t.TempDir())
	cfg, err = Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model.Name != DefaultModel {
		t.Errorf("model = %q, want %q", cfg.Model.Name, DefaultModel)
	}
	if cfg.GCP.Location != "global" {
		t.Errorf("location = %q, want global (Gemini 3 is global-only)", cfg.GCP.Location)
	}
	if !cfg.Diarize() || !cfg.WordTimestamps() {
		t.Error("diarization and word timestamps should default to on")
	}
	if cfg.SecondPass.Model != DefaultSecondPassModel {
		t.Errorf("second pass model = %q, want %q", cfg.SecondPass.Model, DefaultSecondPassModel)
	}
	// A supporting stage must not inherit a preview model's retirement schedule.
	if strings.Contains(DefaultSecondPassModel, "preview") {
		t.Errorf("the second pass model must be GA, got %q", DefaultSecondPassModel)
	}
	if cfg.SecondPassLocation() != cfg.GCP.Location {
		t.Error("the second pass should default to the transcription location")
	}
}

func TestSecondPassLocationOverride(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(write(t, "[gcp]\nlocation = \"global\"\n\n[second_pass]\nlocation = \"us-central1\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SecondPassLocation() != "us-central1" {
		t.Errorf("second pass location = %q, want us-central1", cfg.SecondPassLocation())
	}
	if cfg.GCP.Location != "global" {
		t.Errorf("the transcription location was changed to %q", cfg.GCP.Location)
	}
}

func TestLoad_EnvPrecedence(t *testing.T) {
	clearEnv(t)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "generic")
	t.Setenv("GEMSCRIBE_PROJECT", "specific")
	t.Setenv("GEMSCRIBE_LOCATION", "us-central1")
	t.Setenv("GEMSCRIBE_MODEL", "some-other-model")

	cfg, err := Load(write(t, "[gcp]\nproject = \"from-file\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GCP.Project != "specific" {
		t.Errorf("project = %q, want specific (tool-specific env beats generic and file)", cfg.GCP.Project)
	}
	if cfg.GCP.Location != "us-central1" {
		t.Errorf("location = %q, want us-central1", cfg.GCP.Location)
	}
	if cfg.Model.Name != "some-other-model" {
		t.Errorf("model = %q", cfg.Model.Name)
	}
}

func TestLoad_GenericEnvUsedWhenSpecificAbsent(t *testing.T) {
	clearEnv(t)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "generic")

	cfg, err := Load(write(t, ""))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GCP.Project != "generic" {
		t.Errorf("project = %q, want generic", cfg.GCP.Project)
	}
}

// A file that turns diarization off must keep it off. Representing the two
// booleans as pointers is the only reason this can work, so it gets a test.
func TestLoad_ExplicitFalseSurvives(t *testing.T) {
	clearEnv(t)

	cfg, err := Load(write(t, "[transcribe]\ndiarization = false\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Diarize() {
		t.Error("diarization = false in the file was overwritten by the default")
	}
	if !cfg.WordTimestamps() {
		t.Error("word_timestamp was not set in the file and should keep its default")
	}
}

// An empty string in the file must not reach the API as a blank model name.
func TestLoad_EmptyValuesFallBackToDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load(write(t, "[gcp]\nlocation = \"\"\n\n[model]\nname = \"\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GCP.Location != DefaultLocation || cfg.Model.Name != DefaultModel {
		t.Errorf("empty values were not repaired: location=%q model=%q", cfg.GCP.Location, cfg.Model.Name)
	}
}

func TestLoad_MalformedFile(t *testing.T) {
	clearEnv(t)

	if _, err := Load(write(t, "this is not toml =\n")); err == nil {
		t.Error("expected a parse error")
	}
}

func TestRequireProject(t *testing.T) {
	clearEnv(t)

	cfg, err := Load(write(t, ""))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = cfg.RequireProject()
	if err == nil {
		t.Fatal("expected an error when no project is configured")
	}
	// The message has to tell the user where to put it, not just that it is missing.
	for _, want := range []string{"GEMSCRIBE_PROJECT", "config.toml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message does not mention %q: %v", want, err)
		}
	}

	cfg.GCP.Project = "p"
	if err := cfg.RequireProject(); err != nil {
		t.Errorf("RequireProject with a project set: %v", err)
	}
}

func TestApplyFlags(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(write(t, "[transcribe]\ndiarization = false\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Flags the user did not pass are nil and must change nothing.
	cfg.ApplyFlags("", "", nil, nil)
	if cfg.Model.Name != DefaultModel || cfg.Diarize() {
		t.Error("unset flags modified the config")
	}

	on := true
	cfg.ApplyFlags("flag-model", "europe-west1", &on, nil)
	if cfg.Model.Name != "flag-model" || cfg.GCP.Location != "europe-west1" || !cfg.Diarize() {
		t.Errorf("flags were not applied: %+v", cfg)
	}
}
