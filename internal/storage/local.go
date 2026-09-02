package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cis/cis-backend/internal/config"
)

// Local stores files on the filesystem, one directory per bucket.
//
// Intended for development only: files written here are lost when a container
// restarts unless the directory is a persisted volume, and the API cannot scale
// beyond a single instance. Production should use the Supabase driver.
type Local struct {
	dir    string
	bucket string
}

// NewLocal constructs the local-disk driver for one bucket, creating its
// directory under STORAGE_LOCAL_DIR.
func NewLocal(cfg config.StorageConfig, bucket string) (*Local, error) {
	dir := cfg.LocalDir
	if dir == "" {
		dir = "./uploads"
	}
	if bucket == "" {
		return nil, fmt.Errorf("a storage bucket name is required")
	}

	abs, err := filepath.Abs(filepath.Join(dir, bucket))
	if err != nil {
		return nil, fmt.Errorf("resolve storage directory: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create storage directory: %w", err)
	}
	return &Local{dir: abs, bucket: bucket}, nil
}

// Driver names the implementation.
func (l *Local) Driver() string { return "local" }

// Bucket names the directory this instance writes to.
func (l *Local) Bucket() string { return l.bucket }

// resolve maps a storage path to an absolute filesystem path, refusing any
// path that would escape the storage root.
func (l *Local) resolve(path string) (string, error) {
	clean := filepath.Clean("/" + strings.ReplaceAll(path, "\\", "/"))
	full := filepath.Join(l.dir, filepath.FromSlash(clean))

	rel, err := filepath.Rel(l.dir, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("invalid storage path %q", path)
	}
	return full, nil
}

// Upload writes the file to disk.
func (l *Local) Upload(ctx context.Context, path string, r io.Reader, _ int64, mimeType string) (*Object, error) {
	full, err := l.resolve(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, fmt.Errorf("create storage subdirectory: %w", err)
	}

	file, err := os.Create(full)
	if err != nil {
		return nil, fmt.Errorf("create file: %w", err)
	}
	defer file.Close()

	written, err := io.Copy(file, r)
	if err != nil {
		// Do not leave a truncated file behind for the download endpoint to
		// serve.
		_ = os.Remove(full)
		return nil, fmt.Errorf("write file: %w", err)
	}
	return &Object{Path: path, MimeType: mimeType, Size: written}, nil
}

// SignedURL reports that this driver cannot sign, so callers stream instead.
func (l *Local) SignedURL(context.Context, string) (string, time.Time, bool, error) {
	return "", time.Time{}, false, nil
}

// Download opens a stored file.
func (l *Local) Download(ctx context.Context, path string) (io.ReadCloser, error) {
	full, err := l.resolve(path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(full)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	return file, nil
}

// Delete removes a stored file, treating an already-missing file as success.
func (l *Local) Delete(ctx context.Context, path string) error {
	full, err := l.resolve(path)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete file: %w", err)
	}
	return nil
}
