package handler

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/middleware"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/pkg/response"
	"github.com/cis/cis-backend/internal/service"
	"github.com/cis/cis-backend/internal/storage"
)

// PolicyHandler serves the Public Policy Bank.
type PolicyHandler struct {
	policies *service.PolicyService
}

// NewPolicyHandler constructs a PolicyHandler.
func NewPolicyHandler(policies *service.PolicyService) *PolicyHandler {
	return &PolicyHandler{policies: policies}
}

// List handles GET /api/v1/policies.
func (h *PolicyHandler) List(c *fiber.Ctx) error {
	years, err := parseYearList(c.Query("years"))
	if err != nil {
		return apperr.BadRequest("years must be a comma-separated list of 4-digit years")
	}

	status := strings.ToLower(strings.TrimSpace(c.Query("status")))
	if status != "" && status != models.PolicyStatusRolledOut && status != models.PolicyStatusNotRolledOut {
		return apperr.BadRequest("status must be one of: rolled_out, not_rolled_out")
	}

	cards, total, page, err := h.policies.List(c.UserContext(), service.ListPoliciesQuery{
		Years:  years,
		Search: c.Query("q"),
		Status: status,
		Page:   c.QueryInt("page", dto.DefaultPage),
		Limit:  c.QueryInt("limit", dto.DefaultLimit),
	})
	if err != nil {
		return err
	}
	return response.List(c, "public policies", cards, response.NewMeta(page.Page, page.Limit, total))
}

// Years handles GET /api/v1/policies/years.
func (h *PolicyHandler) Years(c *fiber.Ctx) error {
	years, err := h.policies.Years(c.UserContext())
	if err != nil {
		return err
	}
	return response.OK(c, "available policy years", years)
}

// Detail handles GET /api/v1/policies/:id.
func (h *PolicyHandler) Detail(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	detail, err := h.policies.Detail(c.UserContext(), id)
	if err != nil {
		return err
	}
	return response.OK(c, "policy detail", detail)
}

// Create handles POST /api/v1/policies, the "Add Public Policy" modal.
//
// The body is multipart/form-data: `file`, `name`, `rolled_out_date`.
func (h *PolicyHandler) Create(c *fiber.Ctx) error {
	var req dto.CreatePolicyRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request must be multipart/form-data with name and rolled_out_date fields").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	rolledOutDate, err := time.Parse("2006-01-02", req.RolledOutDate)
	if err != nil {
		return apperr.Unprocessable("rolled_out_date must be a YYYY-MM-DD date")
	}

	header, err := c.FormFile("file")
	if err != nil {
		return apperr.BadRequest("a policy document must be uploaded in the 'file' field")
	}

	// Only PDF and Word are accepted, and other formats must be rejected with
	// an inline error the modal can display.
	mimeType, err := storage.ValidateDocument(header.Filename, header.Header.Get("Content-Type"))
	if err != nil {
		return apperr.Unprocessable("%s", err.Error())
	}

	file, err := header.Open()
	if err != nil {
		return apperr.BadRequest("could not read the uploaded file").Wrap(err)
	}
	defer file.Close()

	card, err := h.policies.Create(c.UserContext(), service.CreatePolicyInput{
		Name:          strings.TrimSpace(req.Name),
		Description:   req.Description,
		RolledOutDate: rolledOutDate,
		FileName:      header.Filename,
		MimeType:      mimeType,
		FileSize:      header.Size,
		File:          file,
		CreatedBy:     middleware.UserIDFromContext(c),
	})
	if err != nil {
		return err
	}
	return response.Created(c, "public policy created", card)
}

// Update handles PATCH /api/v1/policies/:id.
//
// Changing `rolled_out_date` no longer moves the rollout status: since the
// nightly flip was removed, the status is a human judgement and has to be
// stated, either here as `status` or through PUT /policies/:id/status.
func (h *PolicyHandler) Update(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	var req dto.UpdatePolicyRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	card, err := h.policies.Update(c.UserContext(), id, req)
	if err != nil {
		return err
	}
	return response.OK(c, "public policy updated", card)
}

// UpdateStatus handles PUT /api/v1/policies/:id/status.
//
// The rollout status is set by whoever knows whether the policy actually
// launched. It used to be derived nightly from the rolled-out date; a date is a
// plan, and plans slip, so a delayed policy was reported as live and flipped
// back every night after somebody corrected it.
func (h *PolicyHandler) UpdateStatus(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	var req dto.UpdatePolicyStatusRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	card, err := h.policies.SetStatus(c.UserContext(), id, req.Status)
	if err != nil {
		return err
	}
	return response.OK(c, "policy rollout status updated", card)
}

// ReplaceFile handles PUT /api/v1/policies/:id/file, swapping the policy's
// document without losing its id, ai_policy_id, or existing claim
// correlations the way DELETE + re-create would.
func (h *PolicyHandler) ReplaceFile(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	header, err := c.FormFile("file")
	if err != nil {
		return apperr.BadRequest("a policy document must be uploaded in the 'file' field")
	}

	mimeType, err := storage.ValidateDocument(header.Filename, header.Header.Get("Content-Type"))
	if err != nil {
		return apperr.Unprocessable("%s", err.Error())
	}

	file, err := header.Open()
	if err != nil {
		return apperr.BadRequest("could not read the uploaded file").Wrap(err)
	}
	defer file.Close()

	card, err := h.policies.ReplaceFile(c.UserContext(), id, service.ReplaceFileInput{
		FileName: header.Filename,
		MimeType: mimeType,
		FileSize: header.Size,
		File:     file,
	})
	if err != nil {
		return err
	}
	return response.OK(c, "policy document replaced", card)
}

// Delete handles DELETE /api/v1/policies/:id.
func (h *PolicyHandler) Delete(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}
	if err := h.policies.Delete(c.UserContext(), id); err != nil {
		return err
	}
	return response.OK(c, "public policy deleted", nil)
}

// Download handles GET /api/v1/policies/:id/file.
//
// By default it redirects to a time-limited signed URL so the file never
// transits this server. Pass ?mode=json to receive the URL as data instead, or
// ?mode=stream to force proxying the bytes.
func (h *PolicyHandler) Download(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	mode := strings.ToLower(strings.TrimSpace(c.Query("mode")))
	meta, body, err := h.policies.Download(c.UserContext(), id)
	if err != nil {
		return err
	}

	// The driver produced a signed URL, so the file never transits this server.
	if body == nil {
		if mode == "json" {
			return response.OK(c, "policy document", meta)
		}
		return c.Redirect(meta.URL, fiber.StatusTemporaryRedirect)
	}

	// The driver cannot sign (local disk), so the bytes are proxied instead.
	if mode == "json" {
		// Nothing to stream for a metadata-only request; the caller should come
		// back to this same endpoint to fetch the file.
		_ = body.Close()
		meta.URL = c.Path()
		meta.IsSignedURL = false
		return response.OK(c, "policy document", meta)
	}

	c.Set(fiber.HeaderContentType, meta.MimeType)
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+sanitizeHeaderValue(meta.FileName)+`"`)

	// body is deliberately NOT closed here. SendStream hands the reader to
	// fasthttp, which writes it only after this handler returns and closes it
	// itself; closing it now would truncate the response to zero bytes.
	//
	// The length is passed to SendStream rather than set as a Content-Length
	// header, because SendStream without a size switches to chunked encoding
	// and the two together produce a response clients reject.
	if meta.SizeBytes > 0 && meta.SizeBytes <= math.MaxInt32 {
		return c.SendStream(body, int(meta.SizeBytes))
	}
	return c.SendStream(body)
}

// ProcessingStatus handles GET /api/v1/policies/:id/processing, polled by the
// policy card while the "Processing" badge is shown.
func (h *PolicyHandler) ProcessingStatus(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	status, err := h.policies.ProcessingStatus(c.UserContext(), id)
	if err != nil {
		return err
	}
	return response.OK(c, "matchmaking status", status)
}

// Rematch handles POST /api/v1/policies/:id/rematch.
func (h *PolicyHandler) Rematch(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	status, err := h.policies.Rematch(c.UserContext(), id)
	if err != nil {
		return err
	}
	return response.OK(c, "matchmaking re-queued", status)
}

// MatchmakingResult handles
// POST /api/v1/internal/policies/:id/matchmaking-result, the AI service's
// callback.
//
// Unauthenticated: this route has no guard, by design. The caller is trusted
// because it can reach the route at all, which makes the network boundary
// load-bearing — see router.go. Treat the body as it already is treated:
// validated field by field, and applied only to the policy named in the path.
func (h *PolicyHandler) MatchmakingResult(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	var req dto.MatchmakingResultRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	status, err := h.policies.ApplyMatchmakingResult(c.UserContext(), id, req)
	if err != nil {
		return err
	}
	return response.OK(c, "matchmaking result recorded", status)
}

// parseYearList parses the multi-select year filter.
func parseYearList(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || strings.EqualFold(part, "all") {
			continue
		}
		year, err := strconv.Atoi(part)
		if err != nil || year < 1900 || year > 9999 {
			return nil, apperr.BadRequest("invalid year %q", part)
		}
		out = append(out, year)
	}
	return out, nil
}

// sanitizeHeaderValue strips characters that would let a filename break out of
// the Content-Disposition header.
func sanitizeHeaderValue(v string) string {
	replacer := strings.NewReplacer("\"", "", "\r", "", "\n", "", ";", "")
	return replacer.Replace(v)
}
