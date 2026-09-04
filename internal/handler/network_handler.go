package handler

import (
	"math"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/middleware"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/pkg/response"
	"github.com/cis/cis-backend/internal/repository"
	"github.com/cis/cis-backend/internal/service"
)

// NetworkHandler serves the Coordinated-Network Detector endpoints.
type NetworkHandler struct {
	networks *service.NetworkService
	reports  *service.ReportService
}

// NewNetworkHandler constructs a NetworkHandler.
func NewNetworkHandler(networks *service.NetworkService, reports *service.ReportService) *NetworkHandler {
	return &NetworkHandler{networks: networks, reports: reports}
}

// List handles GET /api/v1/networks, the coordinated-network list page.
func (h *NetworkHandler) List(c *fiber.Ctx) error {
	claimIDs, err := parseUUIDList(c.Query("claim_ids"))
	if err != nil {
		return apperr.BadRequest("claim_ids must be a comma-separated list of UUIDs")
	}
	topicIDs, err := parseUUIDList(c.Query("topic_ids"))
	if err != nil {
		return apperr.BadRequest("topic_ids must be a comma-separated list of UUIDs")
	}
	policyIDs, err := parseUUIDList(c.Query("policy_ids"))
	if err != nil {
		return apperr.BadRequest("policy_ids must be a comma-separated list of UUIDs")
	}

	from, err := parseOptionalTime(c.Query("detected_from"))
	if err != nil {
		return apperr.BadRequest("detected_from must be an RFC3339 timestamp or YYYY-MM-DD date")
	}
	to, err := parseOptionalTime(c.Query("detected_to"))
	if err != nil {
		return apperr.BadRequest("detected_to must be an RFC3339 timestamp or YYYY-MM-DD date")
	}

	res, total, page, err := h.networks.List(c.UserContext(), service.ListNetworksQuery{
		Status:          c.Query("status"),
		ConfidenceBands: splitCSV(c.Query("confidence")),
		// "Show low-confidence networks" toggle, default off. Low networks are
		// de-emphasised and labelled when revealed; they are never reachable
		// from the claims list at all.
		ShowLowConfidence: c.QueryBool("show_low_confidence", false),
		ClaimIDs:          claimIDs,
		TopicIDs:          topicIDs,
		PolicyIDs:         policyIDs,
		Search:            c.Query("q"),
		DetectedFrom:      from,
		DetectedTo:        to,
		SortBy:            c.Query("sort"),
		Page:              c.QueryInt("page", dto.DefaultPage),
		Limit:             c.QueryInt("limit", dto.DefaultLimit),
	})
	if err != nil {
		return err
	}
	return response.List(c, "coordinated networks", res, response.NewMeta(page.Page, page.Limit, total))
}

// Detail handles GET /api/v1/networks/:id.
func (h *NetworkHandler) Detail(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	detail, err := h.networks.Detail(c.UserContext(), id)
	if err != nil {
		return err
	}
	return response.OK(c, "coordinated network detail", detail)
}

// UpdateStatus handles PUT /api/v1/networks/:id/status.
func (h *NetworkHandler) UpdateStatus(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	var req dto.UpdateNetworkStatusRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	res, err := h.networks.UpdateStatus(c.UserContext(), id, req, middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.OK(c, "network review status updated", res)
}

// ReviewLog handles GET /api/v1/networks/:id/review-log.
func (h *NetworkHandler) ReviewLog(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	entries, err := h.networks.ReviewLog(c.UserContext(), id, c.QueryInt("limit", 100))
	if err != nil {
		return err
	}
	return response.OK(c, "network review log", entries)
}

// Graph handles GET /api/v1/networks/:id/graph.
func (h *NetworkHandler) Graph(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	graph, err := h.networks.Graph(c.UserContext(), id)
	if err != nil {
		return err
	}
	return response.OK(c, "network graph", graph)
}

// Timeline handles GET /api/v1/networks/:id/timeline.
func (h *NetworkHandler) Timeline(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	timeline, err := h.networks.Timeline(c.UserContext(), id)
	if err != nil {
		return err
	}
	return response.OK(c, "network burst timeline", timeline)
}

// Content handles GET /api/v1/networks/:id/content.
func (h *NetworkHandler) Content(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	content, err := h.networks.Content(c.UserContext(), id)
	if err != nil {
		return err
	}
	return response.OK(c, "representative content", content)
}

// Accounts handles GET /api/v1/networks/:id/accounts.
func (h *NetworkHandler) Accounts(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	rows, total, page, err := h.networks.Accounts(c.UserContext(), id, service.AccountsQuery{
		Role:   c.Query("role"),
		Search: c.Query("q"),
		SortBy: c.Query("sort"),
		Page:   c.QueryInt("page", dto.DefaultPage),
		Limit:  c.QueryInt("limit", dto.DefaultLimit),
	})
	if err != nil {
		return err
	}
	return response.List(c, "network account annex", rows, response.NewMeta(page.Page, page.Limit, total))
}

// AccountDrawer handles GET /api/v1/networks/:id/accounts/:accountId.
//
// The endpoint behind "No account may appear in a network without a viewable
// reason": it returns that account's posts and the specific edges, with their
// per-signal weights, that connected it.
func (h *NetworkHandler) AccountDrawer(c *fiber.Ctx) error {
	networkID, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}
	accountID, err := parsePathUUID(c, "accountId")
	if err != nil {
		return err
	}

	drawer, err := h.networks.AccountDrawer(c.UserContext(), networkID, accountID)
	if err != nil {
		return err
	}
	return response.OK(c, "account detail", drawer)
}

// AccountsCSV handles GET /api/v1/networks/:id/accounts.csv.
//
// The download is written straight to the response and the export is recorded
// in the audit log first. Recording first is the same ordering choice the report
// generator makes: an over-recorded audit log is a nuisance, an under-recorded
// one defeats its purpose.
func (h *NetworkHandler) AccountsCSV(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	user := currentUser(c)
	if err := h.reports.RecordCSVExport(c.UserContext(), id, user); err != nil {
		return err
	}

	var buf strings.Builder
	filename, err := h.networks.AccountsCSV(c.UserContext(), id, &buf)
	if err != nil {
		return err
	}

	c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+filename+`"`)
	return c.SendString(buf.String())
}

// GenerateReport handles POST /api/v1/networks/:id/reports.
func (h *NetworkHandler) GenerateReport(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	req := dto.GenerateReportRequest{ReportType: "platform_referral"}
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&req); err != nil {
			return apperr.BadRequest("request body must be valid JSON").Wrap(err)
		}
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	view, err := h.reports.Generate(c.UserContext(), id, req, currentUser(c))
	if err != nil {
		return err
	}
	return response.Created(c, "report generated", view)
}

// ListReports handles GET /api/v1/networks/:id/reports.
func (h *NetworkHandler) ListReports(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	views, err := h.reports.ListReports(c.UserContext(), id)
	if err != nil {
		return err
	}
	return response.OK(c, "generated reports", views)
}

// EvidenceBundle handles POST /api/v1/networks/:id/evidence-bundle.
func (h *NetworkHandler) EvidenceBundle(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	req := dto.GenerateReportRequest{ReportType: "platform_referral"}
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&req); err != nil {
			return apperr.BadRequest("request body must be valid JSON").Wrap(err)
		}
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	view, err := h.reports.EvidenceBundle(c.UserContext(), id, req, currentUser(c))
	if err != nil {
		return err
	}
	return response.Created(c, "evidence bundle generated", view)
}

// DownloadReport handles GET /api/v1/reports/:reportId/file.
//
// The artefact lives in Supabase Storage, so by default this redirects to a
// time-limited signed URL and the bytes never transit this server.
//
// Pass `?mode=json` to receive that URL as data instead. A browser cannot put
// an Authorization header on a navigation, so a client that opens this path in
// a tab is refused — the working sequence is an authenticated JSON request
// here, followed by a navigation to the returned `url`. See
// local_docs/FE_Revision_for_coordinatex_network_pdf.md.
func (h *NetworkHandler) DownloadReport(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "reportId")
	if err != nil {
		return err
	}

	mode := strings.ToLower(strings.TrimSpace(c.Query("mode")))
	meta, body, err := h.reports.Download(c.UserContext(), id)
	if err != nil {
		return err
	}

	// The digest travels with every form of the response so a recipient can
	// verify the file against what cis_network_reports recorded without a
	// second request.
	c.Set("X-Content-SHA256", meta.SHA256)

	// The store signed the object, so there is nothing here to stream.
	if body == nil {
		if mode == "json" {
			return response.OK(c, "report download", meta)
		}
		return c.Redirect(meta.URL, fiber.StatusTemporaryRedirect)
	}

	// The driver cannot sign (local disk in development), so the bytes are
	// proxied instead.
	if mode == "json" {
		_ = body.Close()
		meta.URL = c.Path()
		meta.IsSignedURL = false
		return response.OK(c, "report download", meta)
	}

	c.Set(fiber.HeaderContentType, meta.MimeType)
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+sanitizeHeaderValue(meta.FileName)+`"`)

	// body is deliberately NOT closed here: SendStream hands the reader to
	// fasthttp, which writes it after this handler returns and closes it
	// itself. Closing it now would truncate the response to zero bytes.
	if meta.SizeBytes > 0 && meta.SizeBytes <= math.MaxInt32 {
		return c.SendStream(body, int(meta.SizeBytes))
	}
	return c.SendStream(body)
}

// currentUser packages the caller's identity for the export artefacts.
func currentUser(c *fiber.Ctx) *service.AuthenticatedUser {
	user := &service.AuthenticatedUser{ID: middleware.UserIDFromContext(c)}
	if v, ok := c.Locals(middleware.CtxUserName).(string); ok {
		user.Name = v
	}
	if v, ok := c.Locals(middleware.CtxUserEmail).(string); ok {
		user.Email = v
	}
	return user
}

// splitCSV splits a comma-separated query parameter into trimmed values.
func splitCSV(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// DetectionHandler serves the detection-run and recalibration endpoints.
type DetectionHandler struct {
	detection *service.DetectionService
	reports   *service.ReportService
}

// NewDetectionHandler constructs a DetectionHandler.
func NewDetectionHandler(detection *service.DetectionService, reports *service.ReportService) *DetectionHandler {
	return &DetectionHandler{detection: detection, reports: reports}
}

// Trigger handles POST /api/v1/admin/detection-runs.
//
// Rejects Synthetic claims with a 400: a predicted claim has no real posts to
// cluster.
func (h *DetectionHandler) Trigger(c *fiber.Ctx) error {
	var req dto.TriggerDetectionRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	ids := make([]uuid.UUID, 0, len(req.ClaimIDs))
	for _, raw := range req.ClaimIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			return apperr.BadRequest("claim_ids must all be valid UUIDs")
		}
		ids = append(ids, id)
	}

	res, err := h.detection.Trigger(c.UserContext(), ids, "on_demand")
	if err != nil {
		return err
	}
	return response.Created(c, "detection run requested", res)
}

// Run handles GET /api/v1/detection-runs/:id.
func (h *DetectionHandler) Run(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	view, err := h.detection.Run(c.UserContext(), id)
	if err != nil {
		return err
	}
	return response.OK(c, "detection run", view)
}

// ListRuns handles GET /api/v1/detection-runs.
//
// Exists because truncation and unavailable signal families are run-level facts
// that cap confidence for every network in a run: "why is everything Medium
// this week?" is a question about runs.
func (h *DetectionHandler) ListRuns(c *fiber.Ctx) error {
	from, err := parseOptionalTime(c.Query("from"))
	if err != nil {
		return apperr.BadRequest("from must be an RFC3339 timestamp or YYYY-MM-DD date")
	}
	to, err := parseOptionalTime(c.Query("to"))
	if err != nil {
		return apperr.BadRequest("to must be an RFC3339 timestamp or YYYY-MM-DD date")
	}

	views, total, page, err := h.detection.ListRuns(c.UserContext(), repository.RunFilter{
		Status:        c.Query("status"),
		TriggerSource: c.Query("trigger"),
		OnlyTruncated: c.QueryBool("truncated", false),
		From:          from,
		To:            to,
	}, c.QueryInt("page", dto.DefaultPage), c.QueryInt("limit", dto.DefaultLimit))
	if err != nil {
		return err
	}
	return response.List(c, "detection runs", views, response.NewMeta(page.Page, page.Limit, total))
}

// OfftopicClusters handles GET /api/v1/admin/offtopic-clusters.
func (h *DetectionHandler) OfftopicClusters(c *fiber.Ctx) error {
	filter := repository.OfftopicFilter{FailedTest: c.Query("failed_test")}

	if raw := strings.TrimSpace(c.Query("run_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return apperr.BadRequest("run_id must be a valid UUID")
		}
		filter.RunID = &id
	}
	if raw := strings.TrimSpace(c.Query("claim_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return apperr.BadRequest("claim_id must be a valid UUID")
		}
		filter.ClaimID = &id
	}

	from, err := parseOptionalTime(c.Query("from"))
	if err != nil {
		return apperr.BadRequest("from must be an RFC3339 timestamp or YYYY-MM-DD date")
	}
	to, err := parseOptionalTime(c.Query("to"))
	if err != nil {
		return apperr.BadRequest("to must be an RFC3339 timestamp or YYYY-MM-DD date")
	}
	filter.From, filter.To = from, to

	views, total, page, err := h.detection.OfftopicClusters(
		c.UserContext(), filter, c.QueryInt("page", dto.DefaultPage), c.QueryInt("limit", dto.DefaultLimit))
	if err != nil {
		return err
	}
	return response.List(c, "off-topic coordinated clusters", views, response.NewMeta(page.Page, page.Limit, total))
}

// OfftopicRates handles GET /api/v1/admin/offtopic-clusters/rates.
func (h *DetectionHandler) OfftopicRates(c *fiber.Ctx) error {
	rates, err := h.detection.OfftopicRates(c.UserContext(), c.QueryInt("limit", 30))
	if err != nil {
		return err
	}
	return response.OK(c, "off-topic rates per run", rates)
}

// Dismissals handles GET /api/v1/admin/dismissals.
func (h *DetectionHandler) Dismissals(c *fiber.Ctx) error {
	from, err := parseOptionalTime(c.Query("from"))
	if err != nil {
		return apperr.BadRequest("from must be an RFC3339 timestamp or YYYY-MM-DD date")
	}
	to, err := parseOptionalTime(c.Query("to"))
	if err != nil {
		return apperr.BadRequest("to must be an RFC3339 timestamp or YYYY-MM-DD date")
	}

	views, total, page, err := h.detection.Dismissals(
		c.UserContext(), from, to, c.QueryInt("page", dto.DefaultPage), c.QueryInt("limit", dto.DefaultLimit))
	if err != nil {
		return err
	}
	return response.List(c, "false-positive dismissals", views, response.NewMeta(page.Page, page.Limit, total))
}

// DismissalSummary handles GET /api/v1/admin/dismissals/summary.
//
// The aggregate that decides whether beta_k or the thresholds need
// recalibrating, plus the precision figure tracked against its target.
func (h *DetectionHandler) DismissalSummary(c *fiber.Ctx) error {
	summary, err := h.detection.DismissalSummary(c.UserContext(), c.QueryInt("window_days", 90))
	if err != nil {
		return err
	}
	return response.OK(c, "dismissal summary", summary)
}

// AuditLog handles GET /api/v1/admin/export-audit.
func (h *DetectionHandler) AuditLog(c *fiber.Ctx) error {
	filter := repository.AuditFilter{ExportType: c.Query("export_type")}

	for _, spec := range []struct {
		key string
		dst **uuid.UUID
	}{
		{"user_id", &filter.UserID},
		{"network_id", &filter.NetworkID},
		{"run_id", &filter.RunID},
	} {
		raw := strings.TrimSpace(c.Query(spec.key))
		if raw == "" {
			continue
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			return apperr.BadRequest("%s must be a valid UUID", spec.key)
		}
		*spec.dst = &id
	}

	from, err := parseOptionalTime(c.Query("from"))
	if err != nil {
		return apperr.BadRequest("from must be an RFC3339 timestamp or YYYY-MM-DD date")
	}
	to, err := parseOptionalTime(c.Query("to"))
	if err != nil {
		return apperr.BadRequest("to must be an RFC3339 timestamp or YYYY-MM-DD date")
	}
	filter.From, filter.To = from, to

	entries, total, page, err := h.reports.AuditLog(
		c.UserContext(), filter, c.QueryInt("page", dto.DefaultPage), c.QueryInt("limit", dto.DefaultLimit))
	if err != nil {
		return err
	}
	return response.List(c, "export audit log", entries, response.NewMeta(page.Page, page.Limit, total))
}
