package staging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeUploader struct {
	uploaded string
	removed  bool
	err      error
}

func (f *fakeUploader) Upload(_ context.Context, localPath string) (string, func() error, error) {
	if f.err != nil {
		return "", nil, f.err
	}
	f.uploaded = localPath
	return "gs://bucket/staged.wav", func() error { f.removed = true; return nil }, nil
}

func writeFile(t *testing.T, name string, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolve_SmallFileGoesInline(t *testing.T) {
	up := &fakeUploader{}
	src, err := Resolve(context.Background(), writeFile(t, "a.wav", 1024), up)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(src.Data) != 1024 || src.URI != "" {
		t.Errorf("small file was not inlined: len=%d uri=%q", len(src.Data), src.URI)
	}
	if src.MIMEType != "audio/wav" {
		t.Errorf("mime = %q", src.MIMEType)
	}
	if up.uploaded != "" {
		t.Error("a small file must not touch the bucket")
	}
	if err := src.Cleanup(); err != nil {
		t.Errorf("Cleanup: %v", err)
	}
}

func TestResolve_LargeFileIsStaged(t *testing.T) {
	up := &fakeUploader{}
	path := writeFile(t, "big.mp3", InlineLimitBytes+1)

	src, err := Resolve(context.Background(), path, up)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src.URI != "gs://bucket/staged.wav" || len(src.Data) != 0 {
		t.Errorf("large file was not staged: %+v", src)
	}
	if src.MIMEType != "audio/mpeg" {
		t.Errorf("mime = %q, want audio/mpeg", src.MIMEType)
	}
	if up.uploaded != path {
		t.Errorf("uploaded %q, want %q", up.uploaded, path)
	}
	if err := src.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if !up.removed {
		t.Error("Cleanup did not remove the staged object")
	}
}

// Without a bucket the error has to say what to configure, not just that the
// file is too big.
func TestResolve_LargeFileWithoutUploader(t *testing.T) {
	_, err := Resolve(context.Background(), writeFile(t, "big.wav", InlineLimitBytes+1), nil)
	if !errors.Is(err, ErrTooLargeForInline) {
		t.Fatalf("err = %v, want ErrTooLargeForInline", err)
	}
	for _, want := range []string{"staging", "bucket", "MB"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message does not mention %q: %v", want, err)
		}
	}
}

// A gs:// input belongs to the caller. It must be passed through and never
// deleted — removing someone else's object is outside this tool's business.
func TestResolve_GCSURIPassesThroughUntouched(t *testing.T) {
	up := &fakeUploader{}
	src, err := Resolve(context.Background(), "gs://someone/else.flac", up)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src.URI != "gs://someone/else.flac" || src.MIMEType != "audio/flac" {
		t.Errorf("unexpected source: %+v", src)
	}
	if err := src.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if up.removed || up.uploaded != "" {
		t.Error("a gs:// input must not be uploaded or deleted")
	}
}

func TestResolve_Errors(t *testing.T) {
	if _, err := Resolve(context.Background(), "/nonexistent/a.wav", nil); err == nil {
		t.Error("a missing file should be an error")
	}
	if _, err := Resolve(context.Background(), t.TempDir(), nil); err == nil {
		t.Error("a directory should be an error")
	}
	if _, err := Resolve(context.Background(), writeFile(t, "a.txt", 10), nil); err == nil {
		t.Error("an unsupported extension should be an error")
	}

	up := &fakeUploader{err: errors.New("bucket exploded")}
	_, err := Resolve(context.Background(), writeFile(t, "big.wav", InlineLimitBytes+1), up)
	if err == nil || !strings.Contains(err.Error(), "bucket exploded") {
		t.Errorf("upload failure was not surfaced: %v", err)
	}
}

func TestMIMETypeFor(t *testing.T) {
	for in, want := range map[string]string{
		"a.wav": "audio/wav", "a.MP3": "audio/mpeg", "a.m4a": "audio/mp4",
		"a.flac": "audio/flac", "a.opus": "audio/opus", "gs://b/a.ogg": "audio/ogg",
	} {
		got, err := MIMETypeFor(in)
		if err != nil {
			t.Errorf("MIMETypeFor(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("MIMETypeFor(%q) = %q, want %q", in, got, want)
		}
	}

	// The error lists what is accepted, so the user does not have to guess.
	_, err := MIMETypeFor("a.mkv")
	if err == nil || !strings.Contains(err.Error(), ".wav") {
		t.Errorf("the unsupported-format error should list the supported set: %v", err)
	}
}

func TestIsGCSURI(t *testing.T) {
	if !IsGCSURI("gs://b/o") {
		t.Error("gs:// should be recognized")
	}
	for _, s := range []string{"/tmp/a.wav", "https://example.com/a.wav", "gs:/b/o", ""} {
		if IsGCSURI(s) {
			t.Errorf("%q should not be a GCS URI", s)
		}
	}
}

func TestSupportedExtensions_IsSorted(t *testing.T) {
	exts := SupportedExtensions()
	if len(exts) == 0 {
		t.Fatal("no supported extensions")
	}
	for i := 1; i < len(exts); i++ {
		if exts[i] < exts[i-1] {
			t.Fatalf("extensions are not sorted: %v", exts)
		}
	}
}

func TestObjectName_IsUniqueAndKeepsTheBasename(t *testing.T) {
	a, err := objectName("p", "/tmp/meeting.wav")
	if err != nil {
		t.Fatal(err)
	}
	b, err := objectName("p", "/tmp/meeting.wav")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two stagings of the same file produced the same object name; one run would delete the other's audio")
	}
	for _, name := range []string{a, b} {
		if !strings.HasPrefix(name, "p/") || !strings.HasSuffix(name, "-meeting.wav") {
			t.Errorf("unexpected object name %q", name)
		}
	}
}
