package handler

import (
	"github.com/gofiber/fiber/v2"

	"github.com/cis/cis-backend/internal/pkg/response"
	"github.com/cis/cis-backend/internal/service"
)

// OverviewHandler serves the Overview page.
type OverviewHandler struct {
	overview *service.OverviewService
}

// NewOverviewHandler constructs an OverviewHandler.
func NewOverviewHandler(overview *service.OverviewService) *OverviewHandler {
	return &OverviewHandler{overview: overview}
}

// Page handles GET /api/v1/overview: the sentiment gauge, topic treemap, and
// policy leaderboard in one call.
//
// ?limit overrides the size of the policy leaderboard for one request.
// Omitting it uses the configured overview.top_policy_limit, which is where
// the leaderboard's actual size is settled — the query parameter is an
// override, not the setting.
func (h *OverviewHandler) Page(c *fiber.Ctx) error {
	page, err := h.overview.Page(c.UserContext(), c.QueryInt("limit", 0))
	if err != nil {
		return err
	}
	return response.OK(c, "overview", page)
}

// Topic handles GET /api/v1/overview/topics/:id, the treemap's click-through
// modal.
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
