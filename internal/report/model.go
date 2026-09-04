// Package report renders the Coordinated-Network evidence report.
//
// # What this package is for
//
// Platforms act on behaviour, not on complaints about content, so the report
// is a behavioural evidence package, not a fact-check. Nothing rendered here
// asserts that a claim is false or that an account is inauthentic. It
// documents timing, duplication and provenance, and it says so on its cover.
//
// # Determinism
//
// Regenerating a report from the same detection run and the same section
// settings must produce a byte-identical document apart from the generation
// timestamp — non-deterministic reports cannot be relied on as evidence.
//
// Four things are done to hold that:
//
//  1. Core PDF fonts only (Helvetica). No font file is embedded, so there is no
//     font-subsetting step to vary between runs.
//  2. Catalog sorting is enabled, which fixes the order of internal resource
//     dictionaries.
//  3. The creation and modification dates are set explicitly to the generation
//     timestamp — the one value allowed to differ between regenerations.
//  4. Nothing is laid out from a source that could reorder. Graph coordinates
//     come from the stored ForceAtlas2 snapshot rather than being recomputed,
//     which is exactly why those coordinates are persisted at all.
package report

import (
	"time"

	"github.com/cis/cis-backend/internal/dto"
)

// Data is everything the renderer needs, gathered by the service layer.
//
// The renderer performs no queries. That keeps the report's ten sections a
// pure function of this struct, which is what makes byte-identical regeneration
// testable rather than merely intended.
type Data struct {
	ReportID string
	// Type is platform_referral or internal_briefing. It decides whether the
	// analyst-facing sections are rendered at all: a platform referral carries
	// behavioural sections and the account annex and no internal commentary.
	Type     string
	Sections dto.ReportSections
	// RedactAnalystNames removes the generating user's name from the cover and
	// the review history.
	RedactAnalystNames bool

	GeneratedAt time.Time
	GeneratedBy string
	// CityLocation is the zone for the city-local half of every page footer.
	// There is no default that would be correct, which is why it is a
	// configured setting rather than a constant.
	CityLocation *time.Location
	Organisation string

	Network  dto.NetworkDetail
	Timeline dto.BurstTimeline
	Content  dto.RepresentativeContent
	Accounts []dto.AccountAnnexRow
	Graph    dto.NetworkGraph

	// ReviewLog appears only in an internal briefing.
	ReviewLog []dto.NetworkReviewLogEntry

	// Chain of custody. All four fields are required for the section to be
	// meaningful; the audit id in particular is allocated before rendering
	// starts, because it is printed inside the document the export produces.
	SnapshotID     string
	SnapshotSHA256 string
	RunID          string
	AuditID        string

	// RunParameters is the configuration in force when the detection executed,
	// read from the run rather than from current settings, so the methodology
	// appendix stays reproducible — a report that printed today's parameters
	// for last quarter's run would be reproducible only by accident.
	RunParameters map[string]any
}

// ExecutiveSummaryWordLimit is the executive summary's word cap.
//
// The summary is generated from a fixed template with numeric slots, never from
// free-form model output, "so that the same detection always produces the same
// summary and the summary can never assert more than the data supports". There
// is no LLM anywhere in this package for that reason.
const ExecutiveSummaryWordLimit = 200
