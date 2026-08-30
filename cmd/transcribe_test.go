package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-scribe/internal/asr"
	"github.com/nlink-jp/gem-scribe/internal/transcript"
)

func TestRequireTimingsFor(t *testing.T) {
	tests := []struct {
		name   string
		format transcript.Format
		opts   asr.Options
		bad    bool
	}{
		{"srt with timings", transcript.FormatSRT, asr.Options{WordTimestamps: true}, false},
		{"srt without timings", transcript.FormatSRT, asr.Options{}, true},
		{"vtt without timings", transcript.FormatVTT, asr.Options{}, true},
		{"json without timings is fine", transcript.FormatJSON, asr.Options{}, false},
		{"text without timings is fine", transcript.FormatText, asr.Options{}, false},
		{"md without timings is fine", transcript.FormatMD, asr.Options{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := requireTimingsFor(tt.format, tt.opts)
			if tt.bad != (err != nil) {
				t.Errorf("requireTimingsFor = %v, wanted an error: %v", err, tt.bad)
			}
		})
	}
}

func TestWriteFiles_Stdout(t *testing.T) {
	var out bytes.Buffer
	files := []transcript.File{{Content: "hello"}}
	if err := writeFiles(&out, files, ""); err != nil {
		t.Fatalf("writeFiles: %v", err)
	}
	if out.String() != "hello" {
		t.Errorf("stdout = %q", out.String())
	}
}

// Several files cannot share one stream: concatenating two subtitle tracks
// produces a file that parses as neither.
func TestWriteFiles_RefusesMultipleOnStdout(t *testing.T) {
	files := []transcript.File{{Suffix: ".ja", Content: "a"}, {Suffix: ".en", Content: "b"}}
	err := writeFiles(&bytes.Buffer{}, files, "")
	if err == nil || !strings.Contains(err.Error(), "-o") {
		t.Errorf("expected an error pointing at -o, got %v", err)
	}
}

// A per-language suffix goes before the extension, so the file still opens in
// a subtitle player.
func TestWriteFiles_SuffixGoesBeforeTheExtension(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "meeting.srt")
	files := []transcript.File{{Suffix: ".ja", Content: "ja"}, {Suffix: ".en", Content: "en"}}

	if err := writeFiles(&bytes.Buffer{}, files, out); err != nil {
		t.Fatalf("writeFiles: %v", err)
	}
	for _, name := range []string{"meeting.ja.srt", "meeting.en.srt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was not written: %v", name, err)
		}
	}
}

func TestWriteFiles_SingleFileKeepsTheGivenName(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "meeting.json")
	if err := writeFiles(&bytes.Buffer{}, []transcript.File{{Content: "{}"}}, out); err != nil {
		t.Fatalf("writeFiles: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{}" {
		t.Errorf("content = %q", data)
	}
}

func TestFormatNames_CoversEveryFormat(t *testing.T) {
	if len(formatNames()) != len(transcript.Formats()) {
		t.Errorf("help text lists %d formats but %d exist", len(formatNames()), len(transcript.Formats()))
	}
}
