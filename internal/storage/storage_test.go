package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/config"
)

func TestValidateDocument(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		mime     string
		wantMime string
		wantErr  bool
	}{
		{"pdf", "congestion-charge.pdf", "application/pdf", "application/pdf", false},
		{"docx", "policy.docx",
			"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			"application/vnd.openxmlformats-officedocument.wordprocessingml.document", false},
		{"doc", "policy.doc", "application/msword", "application/msword", false},
		{"uppercase extension", "POLICY.PDF", "application/pdf", "application/pdf", false},
		// Browsers frequently send octet-stream for Word uploads, so the
		// extension has to win.
		{"generic content type", "policy.docx", "application/octet-stream",
			"application/vnd.openxmlformats-officedocument.wordprocessingml.document", false},
		{"missing content type", "policy.pdf", "", "application/pdf", false},
		// US40: everything else is rejected with an inline error.
		{"rejects images", "policy.png", "image/png", "", true},
		{"rejects text", "policy.txt", "text/plain", "", true},
		{"rejects no extension", "policy", "application/pdf", "", true},
		{"rejects mismatched type", "policy.pdf", "image/png", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateDocument(tc.filename, tc.mime)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q/%q, got mime %q", tc.filename, tc.mime, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.filename, err)
			}
			if got != tc.wantMime {
				t.Errorf("ValidateDocument(%q) = %q, want %q", tc.filename, got, tc.wantMime)
			}
		})
	}
}

func TestBuildObjectPath(t *testing.T) {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")

	cases := []struct {
		filename string
		want     string
	}{
		{"policy.pdf", "policies/11111111-2222-3333-4444-555555555555/policy.pdf"},
		{"Congestion Charge 2026.pdf", "policies/11111111-2222-3333-4444-555555555555/Congestion-Charge-2026.pdf"},
		// Path traversal in the filename must not escape the policy folder.
		{"../../etc/passwd.pdf", "policies/11111111-2222-3333-4444-555555555555/passwd.pdf"},
		{"../../../evil.docx", "policies/11111111-2222-3333-4444-555555555555/evil.docx"},
	}

	for _, tc := range cases {
		got := BuildObjectPath(id, tc.filename)
		if got != tc.want {
			t.Errorf("BuildObjectPath(%q) = %q, want %q", tc.filename, got, tc.want)
		}
		if strings.Contains(got, "..") {
			t.Errorf("BuildObjectPath(%q) produced a traversal path: %q", tc.filename, got)
		}
	}
}

// TestNewForBucketRequiresName guards the wiring: a driver with no bucket would
// write to a path the object store resolves however it likes, so it is refused
// at construction rather than at the first upload.
func TestNewForBucketRequiresName(t *testing.T) {
	cfg := config.StorageConfig{
		Driver:   "local",
		LocalDir: filepath.Join(t.TempDir(), "uploads"),
	}
	for _, bucket := range []string{"", "   "} {
		if _, err := NewForBucket(cfg, bucket); err == nil {
			t.Errorf("NewForBucket(%q) succeeded, want an error", bucket)
		}
	}
}

// TestLocalBucketsAreIsolated checks that two buckets writing the same path do
// not collide on the local driver, the way they do not collide in Supabase.
//
// This is what lets a report and a policy document share a key shape without
// one overwriting the other in development but not in production — a class of
// bug that only appears after deploy.
func TestLocalBucketsAreIsolated(t *testing.T) {
	cfg := config.StorageConfig{Driver: "local", LocalDir: t.TempDir()}

	docs, err := NewForBucket(cfg, "policy-documents")
	if err != nil {
		t.Fatalf("policy bucket: %v", err)
	}
	reports, err := NewForBucket(cfg, "coordinated-network-pdf")
	if err != nil {
		t.Fatalf("report bucket: %v", err)
	}

	const path = "networks/shared/file.pdf"
	ctx := context.Background()
	if _, err := docs.Upload(ctx, path, strings.NewReader("document"), 8, "application/pdf"); err != nil {
		t.Fatalf("upload to policy bucket: %v", err)
	}
	if _, err := reports.Upload(ctx, path, strings.NewReader("report"), 6, "application/pdf"); err != nil {
		t.Fatalf("upload to report bucket: %v", err)
	}

	for _, tc := range []struct {
		name  string
		store Storage
		want  string
	}{
		{"policy-documents", docs, "document"},
		{"coordinated-network-pdf", reports, "report"},
	} {
		body, err := tc.store.Download(ctx, path)
		if err != nil {
			t.Fatalf("download from %s: %v", tc.name, err)
		}
		got, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil {
			t.Fatalf("read from %s: %v", tc.name, err)
		}
		if string(got) != tc.want {
			t.Errorf("%s holds %q, want %q — the buckets share a directory", tc.name, got, tc.want)
		}
		if tc.store.Bucket() != tc.name {
			t.Errorf("Bucket() = %q, want %q", tc.store.Bucket(), tc.name)
		}
	}
}

// TestLocalResolveContainsTraversal checks that a traversal attempt is
// neutralized into a path inside the storage root, rather than escaping it.
//
// resolve roots the input at "/" before cleaning, so leading ".." segments are
// discarded instead of climbing out of the directory.
func TestLocalResolveContainsTraversal(t *testing.T) {
	root := filepath.Join(os.TempDir(), "cis-storage-test")
	l := &Local{dir: root}

	inputs := []string{
		"../../etc/passwd",
		"../../../../../../windows/system32/config/sam",
		"policies/../../../escape.pdf",
		"..\\..\\windows\\win.ini",
		"/absolute/path.pdf",
	}

	for _, input := range inputs {
		got, err := l.resolve(input)
		if err != nil {
			// Rejecting outright is also acceptable containment.
			continue
		}
		rel, relErr := filepath.Rel(root, got)
		if relErr != nil {
			t.Errorf("resolve(%q) = %q, which is not relative to the storage root", input, got)
			continue
		}
		if strings.HasPrefix(rel, "..") {
			t.Errorf("resolve(%q) escaped the storage root: %q", input, got)
		}
	}
}
