// Package storage abstracts where uploaded policy documents live (PRD US40).
//
// Supabase Storage is the production driver, keeping the container stateless.
// The local driver exists so the API can be developed without Supabase
// credentials.
package storage

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/config"
)

// Object describes a stored file.
type Object struct {
	Path     string
	MimeType string
	Size     int64
}

// Storage is the file-store contract used by the policy service.
type Storage interface {
	// Upload streams a file to the store and returns its canonical path.
	Upload(ctx context.Context, path string, r io.Reader, size int64, mimeType string) (*Object, error)
	// SignedURL returns a time-limited download URL, or ok=false when the
	// driver cannot produce one and the caller must stream instead.
	SignedURL(ctx context.Context, path string) (url string, ok bool, err error)
	// Download streams a stored file back.
	Download(ctx context.Context, path string) (io.ReadCloser, error)
	// Delete removes a stored file.
	Delete(ctx context.Context, path string) error
	// Driver names the backing implementation, for health output.
	Driver() string
}

// New builds the configured storage driver.
func New(cfg config.StorageConfig) (Storage, error) {
	switch cfg.Driver {
	case "supabase":
		return NewSupabase(cfg)
	case "local":
		return NewLocal(cfg)
	default:
		return nil, fmt.Errorf("unsupported storage driver %q", cfg.Driver)
	}
}

// Allowed upload formats. US40 restricts policy documents to PDF and Word and
// requires other formats to be rejected with an inline error.
var allowedExtensions = map[string]string{
	".pdf":  "application/pdf",
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
}

var allowedMimeTypes = map[string]struct{}{
	"application/pdf":    {},
	"application/msword": {},
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": {},
	// Some clients send this generic type for .doc uploads.
	"application/octet-stream": {},
}

// ValidateDocument checks an upload's extension and declared content type
// against the US40 allowlist, returning the MIME type to store.
//
// The extension is authoritative because browsers are inconsistent about the
// Content-Type they attach to .doc/.docx; the declared type is only used to
// reject an obvious mismatch.
func ValidateDocument(filename, declaredMime string) (string, error) {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	canonical, ok := allowedExtensions[ext]
	if !ok {
		return "", fmt.Errorf("unsupported file format %q: only PDF and Word (.pdf, .doc, .docx) documents are accepted", ext)
	}

	declared := strings.ToLower(strings.TrimSpace(strings.Split(declaredMime, ";")[0]))
	if declared != "" {
		if _, allowed := allowedMimeTypes[declared]; !allowed {
			return "", fmt.Errorf("unsupported content type %q: only PDF and Word documents are accepted", declared)
		}
	}
	return canonical, nil
}

// BuildObjectPath produces a collision-free storage key that preserves the
// original filename for downloads.
func BuildObjectPath(policyID uuid.UUID, filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	base := sanitizeFilename(strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)))
	if base == "" {
		base = "policy"
	}
	return fmt.Sprintf("policies/%s/%s%s", policyID.String(), base, ext)
}

// sanitizeFilename strips path separators and characters that object stores
// handle poorly.
func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ', r == '.':
			b.WriteRune('-')
		}
	}
	trimmed := strings.Trim(b.String(), "-_")
	if len(trimmed) > 120 {
		trimmed = trimmed[:120]
	}
	return trimmed
}
