package staging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"cloud.google.com/go/storage"
)

// GCSUploader stages audio into a Cloud Storage bucket for the transcription
// service to read.
type GCSUploader struct {
	client *storage.Client
	bucket string
	prefix string
	// Keep leaves the staged object behind instead of deleting it. The audio
	// is the user's own, so keeping it is a debugging convenience rather than a
	// safety problem — but the default is to clean up, because a staging bucket
	// that silently accumulates recordings is a privacy liability.
	Keep bool
}

// NewGCSUploader connects to Cloud Storage. bucket is the name alone, with no
// gs:// prefix and no path.
func NewGCSUploader(ctx context.Context, bucket string, keep bool) (*GCSUploader, error) {
	bucket = strings.TrimPrefix(bucket, "gs://")
	bucket = strings.Trim(bucket, "/")
	if bucket == "" {
		return nil, fmt.Errorf("staging bucket name is empty")
	}
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to Cloud Storage: %w", err)
	}
	return &GCSUploader{client: client, bucket: bucket, prefix: "gem-scribe-staging", Keep: keep}, nil
}

// Close releases the storage client.
func (u *GCSUploader) Close() error {
	if u.client == nil {
		return nil
	}
	return u.client.Close()
}

// Upload copies localPath into the bucket and returns its gs:// URI along with
// a function that deletes it again.
func (u *GCSUploader) Upload(ctx context.Context, localPath string) (string, func() error, error) {
	name, err := objectName(u.prefix, localPath)
	if err != nil {
		return "", nil, err
	}

	f, err := os.Open(localPath)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = f.Close() }()

	object := u.client.Bucket(u.bucket).Object(name)
	w := object.NewWriter(ctx)
	if _, err := io.Copy(w, f); err != nil {
		// Close the writer to release the resumable upload before reporting.
		_ = w.Close()
		return "", nil, fmt.Errorf("upload to gs://%s/%s: %w", u.bucket, name, err)
	}
	if err := w.Close(); err != nil {
		return "", nil, fmt.Errorf("finish upload to gs://%s/%s: %w", u.bucket, name, err)
	}

	uri := fmt.Sprintf("gs://%s/%s", u.bucket, name)
	remove := func() error {
		if u.Keep {
			return nil
		}
		// A fresh context: cleanup has to run even when the transcription was
		// cancelled, which is exactly when the caller's context is already done.
		if err := object.Delete(context.WithoutCancel(ctx)); err != nil {
			return fmt.Errorf("remove staged object %s: %w", uri, err)
		}
		return nil
	}
	return uri, remove, nil
}

// objectName builds a collision-free name that still shows what was staged.
// The random component matters: two concurrent runs of the same file must not
// have one delete the other's object mid-transcription.
func objectName(prefix, localPath string) (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate staged object name: %w", err)
	}
	base := filepath.Base(localPath)
	return path.Join(prefix, hex.EncodeToString(buf)+"-"+base), nil
}
