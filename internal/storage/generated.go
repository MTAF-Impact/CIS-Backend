package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Paths and hashing for files this backend GENERATES, as opposed to files a
// user uploads.
//
// Policy documents arrive from a browser, so they run the format allowlist in
// ValidateDocument. Reports and evidence bundles are produced by the server
// itself, so there is nothing to validate — but they acquire two obligations
// an upload does not have:
//
//   - A SHA-256 recorded alongside them, so the bundle can be shown not to
//     have been modified after generation — necessary for a report to function
//     as evidence rather than assertion.
//   - Versioning by path. A report already submitted to a platform must never be
//     silently replaced, so every generation writes a NEW object and the old one
//     stays downloadable exactly as it was sent.

// ReportFileName builds the report filename, verbatim:
//
//	CIS_CoordinatedNetworkReport_{networkID}_{YYYYMMDD-HHMM}.pdf
//
// The timestamp is in UTC. It is part of the name rather than only of the
// content so that two reports for the same network, sitting in the same
// download folder, are distinguishable without opening them.
func ReportFileName(networkID uuid.UUID, generatedAt time.Time) string {
	return fmt.Sprintf("CIS_CoordinatedNetworkReport_%s_%s.pdf",
		networkID.String(), generatedAt.UTC().Format("20060102-1504"))
}

// BundleFileName builds the evidence package's filename.
func BundleFileName(networkID uuid.UUID, generatedAt time.Time) string {
	return fmt.Sprintf("CIS_EvidenceBundle_%s_%s.zip",
		networkID.String(), generatedAt.UTC().Format("20060102-1504"))
}

// BuildReportPath produces the storage key for a generated report.
//
// Keyed by report id rather than by network id: multiple reports exist per
// network by design, stored, versioned and re-downloadable, so a
// network-keyed path would make each generation overwrite the last.
func BuildReportPath(networkID, reportID uuid.UUID, filename string) string {
	return fmt.Sprintf("networks/%s/reports/%s/%s", networkID.String(), reportID.String(), filename)
}

// BuildBundlePath produces the storage key for a generated evidence bundle.
func BuildBundlePath(networkID, bundleID uuid.UUID, filename string) string {
	return fmt.Sprintf("networks/%s/bundles/%s/%s", networkID.String(), bundleID.String(), filename)
}

// SHA256Hex returns the lowercase hex digest of a byte slice.
//
// Used for the per-file hashes in the bundle's MANIFEST.txt and for
// cis_network_reports.file_sha256. Lowercase hex is chosen because that is what
// sha256sum emits, so a recipient can verify a bundle with a tool they already
// have rather than one we would otherwise have to supply.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
