package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
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
