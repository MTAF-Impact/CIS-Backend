package handler

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/middleware"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/pkg/response"
	"github.com/cis/cis-backend/internal/repository"
	"github.com/cis/cis-backend/internal/service"
)

// ClaimHandler serves F1, the Claim Repository Bank.
type ClaimHandler struct {
	claims *service.ClaimService
}

// NewClaimHandler constructs a ClaimHandler.
func NewClaimHandler(claims *service.ClaimService) *ClaimHandler {
	return &ClaimHandler{claims: claims}
}

// Repository handles GET /api/v1/claims/repository, returning the whole F1 page
// in one call.
func (h *ClaimHandler) Repository(c *fiber.Ctx) error {
	status, err := parseStatusFilter(c.Query("status"))
	if err != nil {
		return err
	}
	topicIDs, err := parseUUIDList(c.Query("topic_ids"))
	if err != nil {
		return apperr.BadRequest("topic_ids must be a comma-separated list of UUIDs")
	}

	res, err := h.claims.Repository(c.UserContext(), status, topicIDs, c.Query("q"))
	if err != nil {
		return err
	}
	return response.OK(c, "claim repository", res)
}

// List handles GET /api/v1/claims, the "See all" list.
func (h *ClaimHandler) List(c *fiber.Ctx) error {
	claimType, err := parseClaimType(c.Query("type"))
	if err != nil {
		return err
	}
	status, err := parseStatusFilter(c.Query("status"))
	if err != nil {
		return err
	}
	topicIDs, err := parseUUIDList(c.Query("topic_ids"))
	if err != nil {
		return apperr.BadRequest("topic_ids must be a comma-separated list of UUIDs")
	}

	sortBy := strings.TrimSpace(c.Query("sort"))
	if sortBy != "" && sortBy != repository.SortByScore && sortBy != repository.SortByCreatedAt {
		return apperr.BadRequest("sort must be one of: score, created_at")
	}

	cards, total, page, err := h.claims.List(c.UserContext(), service.ListClaimsQuery{
		ClaimType: claimType,
		Status:    status,
		TopicIDs:  topicIDs,
		Search:    c.Query("q"),
		SortBy:    sortBy,
		Page:      c.QueryInt("page", dto.DefaultPage),
		Limit:     c.QueryInt("limit", dto.DefaultLimit),
	})
	if err != nil {
		return err
	}
	return response.List(c, "claims", cards, response.NewMeta(page.Page, page.Limit, total))
}

// Detail handles GET /api/v1/claims/:id.
func (h *ClaimHandler) Detail(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	detail, err := h.claims.Detail(c.UserContext(), id)
	if err != nil {
		return err
	}
	return response.OK(c, "claim detail", detail)
}

// Statements handles GET /api/v1/claims/:id/statements.
func (h *ClaimHandler) Statements(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	items, total, page, err := h.claims.Statements(
		c.UserContext(), id, c.Query("stance"),
		c.QueryInt("page", dto.DefaultPage), c.QueryInt("limit", dto.DefaultLimit),
	)
	if err != nil {
		return err
	}
	return response.List(c, "claim statements", items, response.NewMeta(page.Page, page.Limit, total))
}

// TopAccounts handles GET /api/v1/claims/:id/top-accounts.
func (h *ClaimHandler) TopAccounts(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	accounts, err := h.claims.TopAccounts(c.UserContext(), id, c.QueryInt("limit", service.TopAccountLimit))
	if err != nil {
		return err
	}
	return response.OK(c, "top accounts", accounts)
}

// Policies handles GET /api/v1/claims/:id/policies.
func (h *ClaimHandler) Policies(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	policies, err := h.claims.Policies(c.UserContext(), id)
	if err != nil {
		return err
	}
	return response.OK(c, "correlated policies", policies)
}

// UpdateStatus handles PUT /api/v1/claims/:id/status.
func (h *ClaimHandler) UpdateStatus(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	var req dto.UpdateClaimStatusRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	res, err := h.claims.UpdateStatus(c.UserContext(), id, req, middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.OK(c, "claim status updated", res)
}

// ScoreHistory handles GET /api/v1/claims/:id/score-history.
func (h *ClaimHandler) ScoreHistory(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	from, err := parseOptionalTime(c.Query("from"))
	if err != nil {
		return apperr.BadRequest("from must be an RFC3339 timestamp or YYYY-MM-DD date")
	}
	to, err := parseOptionalTime(c.Query("to"))
	if err != nil {
		return apperr.BadRequest("to must be an RFC3339 timestamp or YYYY-MM-DD date")
	}

	history, err := h.claims.ScoreHistory(c.UserContext(), id, c.Query("granularity"), from, to)
	if err != nil {
		return err
	}
	return response.OK(c, "claim score history", history)
}

// TopicHandler serves the topic filter chips shared by S1 and S2.
type TopicHandler struct {
	claims *service.ClaimService
}

// NewTopicHandler constructs a TopicHandler.
func NewTopicHandler(claims *service.ClaimService) *TopicHandler {
	return &TopicHandler{claims: claims}
}

// List handles GET /api/v1/topics.
func (h *TopicHandler) List(c *fiber.Ctx) error {
	topics, err := h.claims.Topics(c.UserContext())
	if err != nil {
		return err
	}
	return response.OK(c, "topics", topics)
}

// Detail handles GET /api/v1/topics/:id.
func (h *TopicHandler) Detail(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	topic, err := h.claims.Topic(c.UserContext(), id)
	if err != nil {
		return err
	}
	return response.OK(c, "topic", topic)
}

// --- shared parsing helpers ---

// parsePathUUID reads and validates a UUID route parameter.
func parsePathUUID(c *fiber.Ctx, name string) (uuid.UUID, error) {
	raw := c.Params(name)
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, apperr.BadRequest("%s must be a valid UUID", name)
	}
	return id, nil
}

// parseUUIDList parses a comma-separated list of UUIDs, used by the
// multi-select topic filter (US6, US15).
func parseUUIDList(raw string) ([]uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	out := make([]uuid.UUID, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || strings.EqualFold(part, "all") {
			continue
		}
		id, err := uuid.Parse(part)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// parseStatusFilter validates the US1 status tab value. Empty and "all" both
// mean no filtering.
func parseStatusFilter(raw string) (string, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "", "all", "all_status":
		return "", nil
	}
	if !models.IsValidReviewStatus(raw) {
		return "", apperr.BadRequest("status must be one of: all, unreviewed, active, inactive, action_taken")
	}
	return raw, nil
}

// parseClaimType maps the API's type filter onto a canonical claim type.
func parseClaimType(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "all":
		return "", nil
	case models.ClaimTypeExisting, "generic":
		return models.ClaimTypeExisting, nil
	case models.ClaimTypeNonExisting, "non-existing", "synthetic":
		return models.ClaimTypeNonExisting, nil
	default:
		return "", apperr.BadRequest("type must be one of: existing, non_existing, all")
	}
}

// parseOptionalTime accepts an RFC3339 timestamp or a bare YYYY-MM-DD date.
func parseOptionalTime(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		utc := t.UTC()
		return &utc, nil
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, err
	}
	utc := t.UTC()
	return &utc, nil
}
