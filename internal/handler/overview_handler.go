package handler

import (
	"github.com/gofiber/fiber/v2"

	"github.com/cis/cis-backend/internal/pkg/response"
	"github.com/cis/cis-backend/internal/service"
)

// OverviewHandler serves F6, the Overview page (PRD v1.5, Section 11).
type OverviewHandler struct {
	overview *service.OverviewService
}

// NewOverviewHandler constructs an OverviewHandler.
func NewOverviewHandler(overview *service.OverviewService) *OverviewHandler {
	return &OverviewHandler{overview: overview}
}

// Page handles GET /api/v1/overview: O1, O2 and O3 in one call (US66-US70).
//
// ?limit overrides the size of the O3 leaderboard for one request. Omitting it
// uses the configured overview.top_policy_limit, which is where US70's "Top 10
// vs top 5" contradiction is actually settled — the query parameter is an
// override, not the setting.
func (h *OverviewHandler) Page(c *fiber.Ctx) error {
	page, err := h.overview.Page(c.UserContext(), c.QueryInt("limit", 0))
	if err != nil {
		return err
	}
	return response.OK(c, "overview", page)
}

// Topic handles GET /api/v1/overview/topics/:id, the O2 treemap's
// click-through modal (US69).
func (h *OverviewHandler) Topic(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	detail, err := h.overview.Topic(c.UserContext(), id)
	if err != nil {
		return err
	}
	return response.OK(c, "topic overview", detail)
}
