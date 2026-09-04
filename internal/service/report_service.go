package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/config"
	"github.com/cis/cis-backend/internal/detector"
	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/report"
	"github.com/cis/cis-backend/internal/repository"
	"github.com/cis/cis-backend/internal/storage"
)

// ReportService generates the coordinated-network evidence artefacts: the PDF
// report, the machine-readable evidence bundle, and the export audit log they
// both write to.
//
// # The gate
//
// Every path through this service passes through assertExportable first,
// which permits generation only for networks at Medium or High confidence
// whose review status is Under Review, Confirmed, or Action Taken. It is
// enforced here, in the service layer, and not in the UI — human review before
// escalation is a governance requirement, and a rule enforced only in a form
// is a rule that a second client does not have.
//
// # Where the files live
//
// store is bound to the coordinated-network bucket (SUPABASE_REPORT_BUCKET,
// default "coordinated-network-pdf"), separate from the bucket holding
// uploaded policy documents. Downloads are handed to the browser as signed URLs
// pointing at that bucket rather than proxied through this API: the artefacts
// are large, the container is stateless, and a navigation cannot carry a bearer
// token — which is exactly the failure a proxied download produces.
type ReportService struct {
	networks  *repository.NetworkRepository
	reports   *repository.ReportRepository
	netSvc    *NetworkService
	settings  *SettingService
	store     storage.Storage
	appConfig config.AppConfig
}

// NewReportService constructs a ReportService. store must be the
// coordinated-network artefact bucket, not the policy-document one.
func NewReportService(
	networks *repository.NetworkRepository,
	reports *repository.ReportRepository,
	netSvc *NetworkService,
	settings *SettingService,
	store storage.Storage,
	appConfig config.AppConfig,
) *ReportService {
	return &ReportService{
		networks:  networks,
		reports:   reports,
		netSvc:    netSvc,
		settings:  settings,
		store:     store,
		appConfig: appConfig,
	}
}

// Generate produces a PDF report for a network.
//
// The ordering inside this function is not arbitrary:
//
//  1. Gate first. Nothing is assembled for a network that may not be exported.
//  2. The audit row is created BEFORE rendering, because the report prints its
//     id inside the document as the chain-of-custody entry. "Log the export
//     after it succeeds" would produce a report with an empty slot there.
//  3. The file is uploaded, then the report row is written. A row pointing at
//     an object that failed to upload would offer a download that 404s.
func (s *ReportService) Generate(
	ctx context.Context, networkID uuid.UUID, req dto.GenerateReportRequest, user *AuthenticatedUser,
) (*dto.ReportView, error) {
	detail, err := s.assertExportable(ctx, networkID)
	if err != nil {
		return nil, err
	}

	if !models.IsValidReportType(req.ReportType) {
		return nil, apperr.Unprocessable("report_type must be platform_referral or internal_briefing")
	}

	sections := resolveSections(req)
	redact := req.RedactAnalystNames != nil && *req.RedactAnalystNames

	reportID := uuid.New()
	auditID := uuid.New()
	generatedAt := time.Now().UTC()

	settingsJSON := models.MustJSONB(map[string]any{
		"report_type":          req.ReportType,
		"sections":             sections,
		"redact_analyst_names": redact,
	})

	// Step 2: the audit entry, before rendering. See the function comment.
	auditEntry := &models.CISExportAuditLog{
		ID:         auditID,
		ObjectType: models.AuditObjectReport,
		ObjectID:   reportID,
		NetworkID:  networkID,
		RunID:      parseUUIDPtr(detail.Run.RunID),
		ExportType: models.ExportTypeReport,
		Settings:   settingsJSON,
		UserID:     user.IDPtr(),
		CreatedAt:  generatedAt,
	}
	if err := s.reports.CreateAuditEntry(ctx, auditEntry); err != nil {
		return nil, apperr.Internal("could not record the export in the audit log").Wrap(err)
	}

	data, err := s.gatherReportData(ctx, networkID, detail, reportID, auditID, generatedAt, req.ReportType, sections, redact, user)
	if err != nil {
		return nil, err
	}

	pdfBytes, err := report.Render(*data)
	if err != nil {
		return nil, apperr.Internal("could not render the report").Wrap(err)
	}

	filename := storage.ReportFileName(networkID, generatedAt)
	path := storage.BuildReportPath(networkID, reportID, filename)
	if _, err := s.store.Upload(ctx, path, bytes.NewReader(pdfBytes), int64(len(pdfBytes)), "application/pdf"); err != nil {
		return nil, apperr.Internal("could not store the generated report").Wrap(err)
	}

	row := &models.CISNetworkReport{
		ID:             reportID,
		NetworkID:      networkID,
		RunID:          uuid.MustParse(detail.Run.RunID),
		ReportType:     req.ReportType,
		Sections:       models.MustJSONB(sections),
		RedactionFlags: models.MustJSONB(map[string]bool{"analyst_names": redact}),
		FileName:       filename,
		FilePath:       path,
		FileSHA256:     storage.SHA256Hex(pdfBytes),
		FileSize:       int64(len(pdfBytes)),
		AuditID:        &auditID,
		GeneratedBy:    user.IDPtr(),
		GeneratedAt:    generatedAt,
	}
	if data.SnapshotID != "" {
		row.SnapshotID = parseUUIDPtr(data.SnapshotID)
		hash := data.SnapshotSHA256
		row.SnapshotSHA256 = &hash
	}
	if err := s.reports.CreateReport(ctx, row); err != nil {
		return nil, apperr.Internal("could not record the generated report").Wrap(err)
	}

	view := toReportView(*row)
	s.attachSignedURL(ctx, &view, row.FilePath)
	return &view, nil
}

// attachSignedURL fills the view's direct-download link, best effort.
//
// A signing failure is logged and dropped rather than failing the request: the
// artefact is generated, recorded and downloadable through the API endpoint, so
// refusing the whole response over a missing convenience link would discard
// work that succeeded. The client falls back to DownloadURL.
func (s *ReportService) attachSignedURL(ctx context.Context, view *dto.ReportView, path string) {
	signed, expiresAt, ok, err := s.store.SignedURL(ctx, path)
	if err != nil {
		log.Printf("[report] could not sign %s, returning the API download path only: %v", view.ID, err)
		return
	}
	if !ok {
		return
	}
	view.FileURL = signed
	view.ExpiresAt = &expiresAt
}

// resolveSections applies the report's section toggles, with its one non-negotiable.
//
// The account annex is MANDATORY in a platform referral and cannot be switched
// off: "a referral without the account list is not actionable". An internal
// briefing may omit it, because its reader has the platform in front of them.
func resolveSections(req dto.GenerateReportRequest) dto.ReportSections {
	boolOr := func(p *bool, fallback bool) bool {
		if p == nil {
			return fallback
		}
		return *p
	}

	sections := dto.ReportSections{
		Graph:           boolOr(req.IncludeGraph, true),
		ContentClusters: boolOr(req.IncludeContentClusters, true),
		AccountAnnex:    boolOr(req.IncludeAccountAnnex, true),
		Methodology:     boolOr(req.IncludeMethodology, true),
	}
	if req.ReportType == models.ReportTypePlatformReferral {
		sections.AccountAnnex = true
	}
	return sections
}

// assertExportable is the export gate, written as an allowlist.
//
// Two conditions, both server-side:
//
//   - the confidence band is Medium or High, and
//   - the review status is one of Under Review, Confirmed, Action Taken.
//
// Written as models.IsReportableNetworkStatus rather than as
// `status != unreviewed`, which is the shape that invites the failure this
// exists to prevent: exporting a network the team already examined and
// concluded was organic — the single largest harm this system can cause, and
// this predicate is where it either happens or does not.
func (s *ReportService) assertExportable(ctx context.Context, networkID uuid.UUID) (*dto.NetworkDetail, error) {
	detail, err := s.netSvc.Detail(ctx, networkID)
	if err != nil {
		return nil, err
	}

	if !models.IsReportableNetworkStatus(detail.ReviewStatus) {
		if detail.ReviewStatus == models.NetworkStatusDismissedFP {
			return nil, apperr.Forbidden(
				"this network was assessed and dismissed as a false positive. Exporting it would submit a " +
					"referral about accounts the team has already concluded were not coordinating")
		}
		return nil, apperr.Forbidden(
			"a network cannot be exported while its review status is %q. A machine has not yet been checked "+
				"by a person, and an unreviewed export is an unreviewed accusation. "+
				"Allowed statuses: %s",
			detail.ReviewStatus, strings.Join(models.ReportableNetworkStatuses, ", "))
	}

	if detail.ConfidenceBand == models.ConfidenceLow {
		return nil, apperr.Forbidden(
			"reports may only be generated for networks at Medium or High confidence; this one is Low")
	}

	return detail, nil
}

// gatherReportData assembles everything the renderer needs.
//
// The renderer performs no queries of its own, which is what makes the
// byte-identical requirement testable: the ten sections are a pure function of
// this struct.
func (s *ReportService) gatherReportData(
	ctx context.Context,
	networkID uuid.UUID,
	detail *dto.NetworkDetail,
	reportID, auditID uuid.UUID,
	generatedAt time.Time,
	reportType string,
	sections dto.ReportSections,
	redact bool,
	user *AuthenticatedUser,
) (*report.Data, error) {
	timeline, err := s.netSvc.Timeline(ctx, networkID)
	if err != nil {
		return nil, err
	}

	data := &report.Data{
		ReportID:           reportID.String(),
		Type:               reportType,
		Sections:           sections,
		RedactAnalystNames: redact,
		GeneratedAt:        generatedAt,
		GeneratedBy:        user.Label(),
		CityLocation:       s.settings.CityTimezone(ctx),
		Organisation:       s.appConfig.Name,
		Network:            *detail,
		Timeline:           *timeline,
		RunID:              detail.Run.RunID,
		AuditID:            auditID.String(),
	}

	if sections.ContentClusters {
		content, err := s.netSvc.Content(ctx, networkID)
		if err != nil {
			return nil, err
		}
		data.Content = *content
	}
	if sections.AccountAnnex {
		accounts, _, _, err := s.netSvc.Accounts(ctx, networkID, AccountsQuery{
			Role:   models.MembershipMember,
			SortBy: repository.AccountSortCentrality,
			Limit:  dto.MaxLimit,
		})
		if err != nil {
			return nil, err
		}
		data.Accounts = accounts
	}
	if sections.Graph {
		graph, err := s.netSvc.Graph(ctx, networkID)
		if err != nil {
			return nil, err
		}
		data.Graph = *graph
	}
	if reportType == models.ReportTypeInternalBriefing {
		log, err := s.netSvc.ReviewLog(ctx, networkID, 50)
		if err != nil {
			return nil, err
		}
		data.ReviewLog = log
	}

	// Chain of custody. A missing snapshot is not fatal — the report still
	// documents what was detected — but it is reported as absent rather than
	// silently rendered as a blank field, because the whole value of this
	// section is that a reader can tell whether the link exists.
	if snap, err := s.networks.FindSnapshot(ctx, networkID); err == nil {
		data.SnapshotID = snap.ID.String()
		data.SnapshotSHA256 = snap.SnapshotSHA256
	} else if !errors.Is(err, repository.ErrNotFound) && !errors.Is(err, repository.ErrPipelineUnavailable) {
		return nil, apperr.Internal("could not load the evidence snapshot").Wrap(err)
	}

	// The methodology appendix prints the parameters that produced THIS run,
	// read from the run rather than from current settings. A report printing
	// today's configuration for last quarter's detection would be reproducible
	// only by coincidence.
	if runID, err := uuid.Parse(detail.Run.RunID); err == nil {
		if run, err := s.networks.FindRun(ctx, runID); err == nil && len(run.ParametersJSON) > 0 {
			var params map[string]any
			if err := json.Unmarshal(run.ParametersJSON, &params); err == nil {
				data.RunParameters = params
			}
		}
	}

	return data, nil
}

// ListReports returns a network's generated reports.
func (s *ReportService) ListReports(ctx context.Context, networkID uuid.UUID) ([]dto.ReportView, error) {
	rows, err := s.reports.ListReports(ctx, networkID)
	if err != nil {
		return nil, apperr.Internal("could not load generated reports").Wrap(err)
	}
	out := make([]dto.ReportView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toReportView(r))
	}
	return out, nil
}

// Download resolves a stored, versioned, re-downloadable artefact for delivery.
//
// It returns metadata and, when the store can sign, a URL the browser fetches
// directly from object storage — the reader is nil in that case and the file
// never transits this server. Only a driver that cannot sign (local disk, in
// development) produces a reader for the caller to proxy.
func (s *ReportService) Download(
	ctx context.Context, reportID uuid.UUID,
) (*dto.ReportDownload, io.ReadCloser, error) {
	row, err := s.findReport(ctx, reportID)
	if err != nil {
		return nil, nil, err
	}

	meta := &dto.ReportDownload{
		ReportID:  row.ID.String(),
		FileName:  row.FileName,
		MimeType:  reportMimeType(row.FileName),
		SizeBytes: row.FileSize,
		SHA256:    row.FileSHA256,
	}

	signed, expiresAt, ok, err := s.store.SignedURL(ctx, row.FilePath)
	if err != nil {
		return nil, nil, apperr.Internal("could not prepare the download").Wrap(err)
	}
	if ok {
		meta.URL = signed
		meta.IsSignedURL = true
		meta.ExpiresAt = &expiresAt
		return meta, nil, nil
	}

	reader, err := s.store.Download(ctx, row.FilePath)
	if err != nil {
		return nil, nil, apperr.Internal("could not read the stored report").Wrap(err)
	}
	return meta, reader, nil
}

// findReport loads one artefact row, translating a missing one into a 404.
func (s *ReportService) findReport(ctx context.Context, reportID uuid.UUID) (*models.CISNetworkReport, error) {
	row, err := s.reports.FindReport(ctx, reportID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("report not found")
		}
		return nil, apperr.Internal("could not load the report").Wrap(err)
	}
	return row, nil
}

// readReport streams an artefact's bytes back into this process.
//
// Distinct from Download, which prefers to hand the client a signed URL. The
// evidence bundle needs the PDF itself to place inside the ZIP, so it takes
// this path regardless of what the driver can sign.
func (s *ReportService) readReport(
	ctx context.Context, reportID uuid.UUID,
) (*models.CISNetworkReport, []byte, error) {
	row, err := s.findReport(ctx, reportID)
	if err != nil {
		return nil, nil, err
	}

	reader, err := s.store.Download(ctx, row.FilePath)
	if err != nil {
		return nil, nil, apperr.Internal("could not read the stored report").Wrap(err)
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, nil, apperr.Internal("could not read the stored report").Wrap(err)
	}
	return row, data, nil
}

// reportMimeType maps a generated artefact's filename to its content type. The
// two produced by this service are the PDF report and the evidence-bundle ZIP.
func reportMimeType(filename string) string {
	if strings.HasSuffix(strings.ToLower(filename), ".zip") {
		return "application/zip"
	}
	return "application/pdf"
}

// EvidenceBundle produces a machine-readable ZIP.
//
// Contents: the PDF, network.json (the full snapshot including per-edge
// signal decomposition and run parameters), accounts.csv, posts.csv (with
// per-post SHA-256), and MANIFEST.txt listing every file with its hash and
// the bundle's generation timestamp.
//
// The manifest is the point: its hashes establish that the bundle was not
// modified after generation, which is what lets it function as evidence
// rather than assertion. A recipient can verify it with sha256sum and nothing
// else.
func (s *ReportService) EvidenceBundle(
	ctx context.Context, networkID uuid.UUID, req dto.GenerateReportRequest, user *AuthenticatedUser,
) (*dto.ReportView, error) {
	// Same gate as the PDF. A bundle is a superset of a report, so it can never
	// be the looser path — that would make the gate decorative.
	detail, err := s.assertExportable(ctx, networkID)
	if err != nil {
		return nil, err
	}

	if req.ReportType == "" {
		req.ReportType = models.ReportTypePlatformReferral
	}
	if !models.IsValidReportType(req.ReportType) {
		return nil, apperr.Unprocessable("report_type must be platform_referral or internal_briefing")
	}

	// The bundle carries a report, so one is generated as part of it rather
	// than referencing whichever report happened to exist. A bundle whose PDF
	// and whose JSON described different detections would be worse than useless.
	pdfView, err := s.Generate(ctx, networkID, req, user)
	if err != nil {
		return nil, err
	}
	pdfRow, pdfBytes, err := s.readReport(ctx, uuid.MustParse(pdfView.ID))
	if err != nil {
		return nil, err
	}

	bundleID := uuid.New()
	generatedAt := time.Now().UTC()

	networkJSON, err := s.buildNetworkJSON(ctx, networkID, detail)
	if err != nil {
		return nil, err
	}
	accountsCSV, err := s.buildAccountsCSV(ctx, networkID)
	if err != nil {
		return nil, err
	}
	postsCSV, err := s.buildPostsCSV(ctx, networkID)
	if err != nil {
		return nil, err
	}

	files := []bundleFile{
		{Name: pdfRow.FileName, Data: pdfBytes},
		{Name: "network.json", Data: networkJSON},
		{Name: "accounts.csv", Data: accountsCSV},
		{Name: "posts.csv", Data: postsCSV},
	}

	auditID := uuid.New()
	manifest := buildManifest(networkID, detail.Run.RunID, bundleID, auditID, generatedAt, files)
	files = append(files, bundleFile{Name: "MANIFEST.txt", Data: manifest})

	zipBytes, err := buildZip(files, generatedAt)
	if err != nil {
		return nil, apperr.Internal("could not assemble the evidence bundle").Wrap(err)
	}

	filename := storage.BundleFileName(networkID, generatedAt)
	path := storage.BuildBundlePath(networkID, bundleID, filename)
	if _, err := s.store.Upload(ctx, path, bytes.NewReader(zipBytes), int64(len(zipBytes)), "application/zip"); err != nil {
		return nil, apperr.Internal("could not store the evidence bundle").Wrap(err)
	}

	entry := &models.CISExportAuditLog{
		ID:         auditID,
		ObjectType: models.AuditObjectReport,
		ObjectID:   bundleID,
		NetworkID:  networkID,
		RunID:      parseUUIDPtr(detail.Run.RunID),
		ExportType: models.ExportTypeEvidenceBundle,
		Settings: models.MustJSONB(map[string]any{
			"report_id": pdfView.ID,
			"files":     []string{pdfRow.FileName, "network.json", "accounts.csv", "posts.csv", "MANIFEST.txt"},
		}),
		UserID:    user.IDPtr(),
		CreatedAt: generatedAt,
	}
	if err := s.reports.CreateAuditEntry(ctx, entry); err != nil {
		return nil, apperr.Internal("could not record the bundle export").Wrap(err)
	}

	bundleRow := models.CISNetworkReport{
		ID:             bundleID,
		NetworkID:      networkID,
		RunID:          pdfRow.RunID,
		SnapshotID:     pdfRow.SnapshotID,
		SnapshotSHA256: pdfRow.SnapshotSHA256,
		ReportType:     req.ReportType,
		Sections:       pdfRow.Sections,
		RedactionFlags: pdfRow.RedactionFlags,
		FileName:       filename,
		FilePath:       path,
		FileSHA256:     storage.SHA256Hex(zipBytes),
		FileSize:       int64(len(zipBytes)),
		AuditID:        &auditID,
		GeneratedBy:    user.IDPtr(),
		GeneratedAt:    generatedAt,
	}
	if err := s.reports.CreateReport(ctx, &bundleRow); err != nil {
		return nil, apperr.Internal("could not record the evidence bundle").Wrap(err)
	}

	view := toReportView(bundleRow)
	s.attachSignedURL(ctx, &view, bundleRow.FilePath)
	return &view, nil
}

type bundleFile struct {
	Name string
	Data []byte
}

// buildZip writes the archive with fixed per-entry metadata.
//
// Every entry gets the same modification time and deflate is used consistently,
// so two bundles built from the same evidence differ only where their content
// differs. Without that, the ZIP container itself would vary between runs and
// the determinism argument would stop at the PDF.
func buildZip(files []bundleFile, at time.Time) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for _, f := range files {
		header := &zip.FileHeader{Name: f.Name, Method: zip.Deflate}
		header.Modified = at
		w, err := zw.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.Data); err != nil {
			return nil, err
		}
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// buildManifest writes MANIFEST.txt: every file with its hash, plus the
// bundle's generation timestamp.
func buildManifest(networkID uuid.UUID, runID string, bundleID, auditID uuid.UUID, at time.Time, files []bundleFile) []byte {
	var b strings.Builder
	b.WriteString("CIS Coordinated-Network Evidence Bundle\n")
	b.WriteString("=======================================\n\n")
	fmt.Fprintf(&b, "Bundle ID:        %s\n", bundleID)
	fmt.Fprintf(&b, "Network ID:       %s\n", networkID)
	fmt.Fprintf(&b, "Detection run ID: %s\n", runID)
	fmt.Fprintf(&b, "Audit entry ID:   %s\n", auditID)
	fmt.Fprintf(&b, "Generated at:     %s\n\n", at.Format(time.RFC3339))

	b.WriteString("Files (SHA-256):\n")
	for _, f := range files {
		fmt.Fprintf(&b, "  %s  %s\n", storage.SHA256Hex(f.Data), f.Name)
	}

	b.WriteString("\nVerify with:  sha256sum -c <(awk '/^  /{print $1\"  \"$2}' MANIFEST.txt)\n")
	b.WriteString("\nThese digests establish that the bundle was not modified after generation.\n")
	b.WriteString("\n")
	b.WriteString(wrapText(detector.Disclaimer, 96))
	b.WriteString("\n")
	return []byte(b.String())
}

// buildNetworkJSON produces network.json: the full snapshot, including the
// per-edge signal decomposition and the run parameters.
func (s *ReportService) buildNetworkJSON(ctx context.Context, networkID uuid.UUID, detail *dto.NetworkDetail) ([]byte, error) {
	graph, err := s.netSvc.Graph(ctx, networkID)
	if err != nil {
		return nil, err
	}
	timeline, err := s.netSvc.Timeline(ctx, networkID)
	if err != nil {
		return nil, err
	}
	content, err := s.netSvc.Content(ctx, networkID)
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"schema":      "cis.coordinated-network.v1",
		"exported_at": time.Now().UTC(),
		"disclaimer":  detector.Disclaimer,
		"network":     detail,
		"graph":       graph,
		"timeline":    timeline,
		"content":     content,
	}

	if runID, err := uuid.Parse(detail.Run.RunID); err == nil {
		if run, err := s.networks.FindRun(ctx, runID); err == nil {
			runPayload := map[string]any{
				"run_id":              run.ID,
				"trigger_source":      run.TriggerSource,
				"window_start":        run.WindowStart,
				"window_end":          run.WindowEnd,
				"random_seed":         run.RandomSeed,
				"library_version":     run.LibraryVersion,
				"signals_unavailable": []string(run.SignalsUnavailable),
				"truncated":           run.Truncated,
				"candidates_count":    run.CandidatesCount,
			}
			if len(run.ParametersJSON) > 0 {
				runPayload["parameters"] = json.RawMessage(run.ParametersJSON)
			}
			if len(run.ModelVersions) > 0 {
				runPayload["model_versions"] = json.RawMessage(run.ModelVersions)
			}
			payload["detection_run"] = runPayload
		}
	}

	// Indented so a recipient can read it without a formatter. An evidence
	// bundle is opened by a person before it is opened by a parser.
	return json.MarshalIndent(payload, "", "  ")
}

func (s *ReportService) buildAccountsCSV(ctx context.Context, networkID uuid.UUID) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := s.netSvc.AccountsCSV(ctx, networkID, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// buildPostsCSV produces posts.csv, with a per-post SHA-256.
//
// The digest is what lets a recipient show that a post they are reading is
// byte-for-byte what was captured, months after the original was deleted.
func (s *ReportService) buildPostsCSV(ctx context.Context, networkID uuid.UUID) ([]byte, error) {
	posts, err := s.networks.ListEvidencePosts(ctx, networkID, nil)
	if err != nil {
		return nil, translatePipelineErr(err, "could not load evidence posts")
	}

	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	if err := cw.Write([]string{
		"network_id", "post_platform_id", "platform", "handle",
		"posted_at", "captured_at", "content_sha256",
		"duplicate_group_id", "is_canonical", "still_publicly_available", "text",
	}); err != nil {
		return nil, apperr.Internal("could not write posts.csv").Wrap(err)
	}

	for _, p := range posts {
		group := ""
		if p.DuplicateGroupID != nil {
			group = p.DuplicateGroupID.String()
		}
		record := []string{
			networkID.String(),
			p.PostPlatformID,
			p.Platform,
			p.Handle,
			p.PostedAt.UTC().Format(time.RFC3339),
			p.CapturedAt.UTC().Format(time.RFC3339),
			p.ContentSHA256,
			group,
			strconv.FormatBool(p.IsCanonical),
			strconv.FormatBool(p.StillPublic),
			p.CapturedText,
		}
		if err := cw.Write(record); err != nil {
			return nil, apperr.Internal("could not write a posts.csv row").Wrap(err)
		}
	}

	cw.Flush()
	if err := cw.Error(); err != nil {
		return nil, apperr.Internal("could not finish posts.csv").Wrap(err)
	}
	return buf.Bytes(), nil
}

// RecordCSVExport writes the audit entry for an account-list CSV export.
//
// This is the export most likely to be treated as casual — a spreadsheet, not
// a referral — but it leaves the platform carrying every member handle, so it
// is logged like the others.
func (s *ReportService) RecordCSVExport(ctx context.Context, networkID uuid.UUID, user *AuthenticatedUser) error {
	row, err := s.networks.FindNetworkByID(ctx, networkID)
	if err != nil {
		return translatePipelineErr(err, "could not load the network")
	}

	entry := &models.CISExportAuditLog{
		ObjectType: models.AuditObjectNetwork,
		ObjectID:   networkID,
		NetworkID:  networkID,
		RunID:      &row.RunID,
		ExportType: models.ExportTypeAccountsCSV,
		Settings:   models.MustJSONB(map[string]any{"columns": "account annex"}),
		UserID:     user.IDPtr(),
	}
	if err := s.reports.CreateAuditEntry(ctx, entry); err != nil {
		return apperr.Internal("could not record the CSV export").Wrap(err)
	}
	return nil
}

// AuditLog returns export audit entries.
//
// Readable by any authenticated user — this backend has no role model at all,
// a deliberate, recorded deviation. The audit property comes from
// attribution: every entry records who exported what
// and when, which is what makes the log answer its question. A role check would
// have been a second, weaker layer over that.
func (s *ReportService) AuditLog(
	ctx context.Context, f repository.AuditFilter, page, limit int,
) ([]dto.AuditLogEntry, int64, dto.PageParams, error) {
	window := dto.NormalizePage(page, limit)
	f.Limit = window.Limit
	f.Offset = window.Offset()

	rows, total, err := s.reports.ListAuditLog(ctx, f)
	if err != nil {
		return nil, 0, window, apperr.Internal("could not load the export audit log").Wrap(err)
	}

	out := make([]dto.AuditLogEntry, 0, len(rows))
	for _, r := range rows {
		entry := dto.AuditLogEntry{
			ID:         r.ID.String(),
			ObjectType: r.ObjectType,
			ObjectID:   r.ObjectID.String(),
			NetworkID:  r.NetworkID.String(),
			ExportType: r.ExportType,
			UserName:   r.UserName,
			CreatedAt:  r.CreatedAt,
		}
		if r.RunID != nil {
			id := r.RunID.String()
			entry.RunID = &id
		}
		if r.UserID != nil {
			id := r.UserID.String()
			entry.UserID = &id
		}
		if len(r.Settings) > 0 {
			entry.Settings = json.RawMessage(r.Settings)
		}
		out = append(out, entry)
	}
	return out, total, window, nil
}

func toReportView(r models.CISNetworkReport) dto.ReportView {
	view := dto.ReportView{
		ID:          r.ID.String(),
		NetworkID:   r.NetworkID.String(),
		RunID:       r.RunID.String(),
		ReportType:  r.ReportType,
		FileName:    r.FileName,
		FileSHA256:  r.FileSHA256,
		FileSize:    r.FileSize,
		GeneratedAt: r.GeneratedAt,
		DownloadURL: "/api/v1/reports/" + r.ID.String() + "/file",
	}
	if len(r.Sections) > 0 {
		_ = json.Unmarshal(r.Sections, &view.Sections)
	}
	if len(r.RedactionFlags) > 0 {
		var flags map[string]bool
		if err := json.Unmarshal(r.RedactionFlags, &flags); err == nil {
			view.RedactAnalysts = flags["analyst_names"]
		}
	}
	if r.SnapshotID != nil {
		id := r.SnapshotID.String()
		view.SnapshotID = &id
	}
	view.SnapshotSHA256 = r.SnapshotSHA256
	if r.AuditID != nil {
		id := r.AuditID.String()
		view.AuditID = &id
	}
	if r.GeneratedBy != nil {
		id := r.GeneratedBy.String()
		view.GeneratedBy = &id
	}
	return view
}

// AuthenticatedUser carries the acting caller's identity into the export
// artefacts.
//
// Every coordinated-network write records who did it — updated_by, added_by, generated_by,
// user_id — and that attribution is doing the work a role check would otherwise
// do. This backend has no roles by design, so the log is the accountability
// mechanism rather than a supplement to one.
type AuthenticatedUser struct {
	ID    *uuid.UUID
	Name  string
	Email string
}

// IDPtr returns the caller's id, or nil for an unattributed action.
func (u *AuthenticatedUser) IDPtr() *uuid.UUID {
	if u == nil {
		return nil
	}
	return u.ID
}

// Label returns the name to print on a report cover.
func (u *AuthenticatedUser) Label() string {
	if u == nil {
		return ""
	}
	switch {
	case u.Name != "":
		return u.Name
	case u.Email != "":
		return u.Email
	case u.ID != nil:
		return u.ID.String()
	default:
		return ""
	}
}

func parseUUIDPtr(s string) *uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		return nil
	}
	return &id
}

// wrapText hard-wraps a paragraph for the plain-text manifest.
func wrapText(s string, width int) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}

	var b strings.Builder
	line := words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) > width {
			b.WriteString(line)
			b.WriteByte('\n')
			line = w
			continue
		}
		line += " " + w
	}
	b.WriteString(line)
	b.WriteByte('\n')
	return b.String()
}

// sortedKeys is used where a map has to be walked deterministically.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
