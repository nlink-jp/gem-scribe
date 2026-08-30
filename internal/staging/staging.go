// Package staging turns a user-supplied input — a local file or a gs:// URI —
// into audio the transcription API can read.
//
// Small files are sent inline, which is what lets gem-scribe work before anyone
// has configured a Cloud Storage bucket. Large ones are staged into a bucket
// and removed afterwards, because holding tens of megabytes in a request body
// is slow and the request limit is not something to lean on.
package staging

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InlineLimitBytes is the size above which a local file is staged through GCS
// rather than sent inline.
//
// It is far below what the endpoint actually accepts: inline requests of 87 MB
// of audio (117 MB once base64-encoded) were measured to succeed. That headroom
// is undocumented, so it is not a contract — the threshold sits at a size where
// inlining is still fast and well inside anything Google publishes.
const InlineLimitBytes = 20 << 20

// ErrTooLargeForInline reports a file that needs a bucket which is not configured.
var ErrTooLargeForInline = errors.New("file is larger than the inline limit and no staging bucket is configured")

// Source is prepared audio: either bytes to inline or a URI the service reads.
type Source struct {
	Data     []byte
	URI      string
	MIMEType string
	// Cleanup removes anything this package created. It is never nil.
	Cleanup func() error
}

// Uploader stages a local file into object storage. It is an interface so the
// resolution logic can be tested without a bucket.
type Uploader interface {
	Upload(ctx context.Context, localPath string) (uri string, remove func() error, err error)
}

// Resolve prepares the audio for one transcription.
//
// A gs:// input is passed through untouched: the caller already put it
// somewhere the service can read, and deleting someone else's object would be
// well outside this tool's business.
func Resolve(ctx context.Context, input string, uploader Uploader) (*Source, error) {
	noCleanup := func() error { return nil }

	if IsGCSURI(input) {
		mime, err := MIMETypeFor(input)
		if err != nil {
			return nil, err
		}
		return &Source{URI: input, MIMEType: mime, Cleanup: noCleanup}, nil
	}

	info, err := os.Stat(input)
	if err != nil {
		return nil, fmt.Errorf("read audio file: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory, not an audio file", input)
	}

	mime, err := MIMETypeFor(input)
	if err != nil {
		return nil, err
	}

	if info.Size() > InlineLimitBytes {
		if uploader == nil {
			return nil, fmt.Errorf("%w (%s is %s): set [staging].bucket or GEMSCRIBE_STAGING_BUCKET",
				ErrTooLargeForInline, filepath.Base(input), humanSize(info.Size()))
		}
		uri, remove, err := uploader.Upload(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("stage %s: %w", filepath.Base(input), err)
		}
		return &Source{URI: uri, MIMEType: mime, Cleanup: remove}, nil
	}

	data, err := os.ReadFile(input)
	if err != nil {
		return nil, fmt.Errorf("read audio file: %w", err)
	}
	return &Source{Data: data, MIMEType: mime, Cleanup: noCleanup}, nil
}

// IsGCSURI reports whether input names an object in Cloud Storage.
func IsGCSURI(s string) bool { return strings.HasPrefix(s, "gs://") }

// audioTypes maps the extensions the transcription model accepts to their MIME
// types. The list is the model's documented set, not everything ffmpeg knows:
// an unsupported container is better refused here, with the list in hand, than
// as an opaque error from the API.
var audioTypes = map[string]string{
	".wav":  "audio/wav",
	".mp3":  "audio/mpeg",
	".m4a":  "audio/mp4",
	".mp4":  "audio/mp4",
	".aac":  "audio/aac",
	".flac": "audio/flac",
	".ogg":  "audio/ogg",
	".opus": "audio/opus",
	".oga":  "audio/ogg",
	".aiff": "audio/aiff",
	".aif":  "audio/aiff",
	".webm": "audio/webm",
}

// MIMETypeFor derives the MIME type from the file extension.
func MIMETypeFor(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if mime, ok := audioTypes[ext]; ok {
		return mime, nil
	}
	return "", fmt.Errorf("unsupported audio format %q (supported: %s)", ext, strings.Join(SupportedExtensions(), ", "))
}

// SupportedExtensions lists the accepted extensions, sorted, for help text and
// error messages.
func SupportedExtensions() []string {
	exts := make([]string, 0, len(audioTypes))
	for ext := range audioTypes {
		exts = append(exts, ext)
	}
	sortStrings(exts)
	return exts
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func humanSize(n int64) string {
	const unit = 1 << 20
	if n < unit {
		return fmt.Sprintf("%d bytes", n)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/unit)
}
