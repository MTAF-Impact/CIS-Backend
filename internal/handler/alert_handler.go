package handler

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/middleware"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/pkg/response"
	"github.com/cis/cis-backend/internal/service"
)

// AlertHandler serves F3, the Alert page.
type AlertHandler struct {
	alerts *service.AlertService
}

// NewAlertHandler constructs an AlertHandler.
func NewAlertHandler(alerts *service.AlertService) *AlertHandler {
	return &AlertHandler{alerts: alerts}
}

// List handles GET /api/v1/alerts, the [C3] watchlist table.
func (h *AlertHandler) List(c *fiber.Ctx) error {
	rows, total, page, err := h.alerts.List(
		c.UserContext(), middleware.UserIDFromContext(c), c.Query("q"),
		c.QueryInt("page", dto.DefaultPage), c.QueryInt("limit", dto.DefaultLimit),
	)
	if err != nil {
		return err
	}
	return response.List(c, "alert watchlist", rows, response.NewMeta(page.Page, page.Limit, total))
}

// Notifications handles GET /api/v1/alerts/notifications, the US71 sidebar
// counter badge.
func (h *AlertHandler) Notifications(c *fiber.Ctx) error {
	res, err := h.alerts.Notifications(c.UserContext(), middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.OK(c, "alert notifications", res)
}

// Acknowledge handles POST /api/v1/alerts/notifications/acknowledge.
//
// US71 clears the counter and the row highlights when the user opens F3, so the
// frontend calls this on entering the page — after it has rendered the rows it
// was given, since acknowledging is what makes the next render unhighlighted.
func (h *AlertHandler) Acknowledge(c *fiber.Ctx) error {
	res, err := h.alerts.Acknowledge(c.UserContext(), middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.OK(c, "threshold crossings acknowledged", res)
}

// Add handles POST /api/v1/alerts, called after the user confirms the bell
// dialog on an F1 card (US14).
func (h *AlertHandler) Add(c *fiber.Ctx) error {
	var req dto.AddAlertRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	claimID, err := uuid.Parse(req.ClaimID)
	if err != nil {
		return apperr.BadRequest("claim_id must be a valid UUID")
	}

	res, err := h.alerts.Add(c.UserContext(), claimID, middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.Created(c, "claim added to the alert watchlist", res)
}

// Remove handles DELETE /api/v1/alerts/:claimId (US14).
func (h *AlertHandler) Remove(c *fiber.Ctx) error {
	claimID, err := parsePathUUID(c, "claimId")
	if err != nil {
		return err
	}

	res, err := h.alerts.Remove(c.UserContext(), claimID)
	if err != nil {
		return err
	}
	return response.OK(c, "claim removed from the alert watchlist", res)
}

// SetChartVisibility handles PATCH /api/v1/alerts/:claimId/chart (US28).
func (h *AlertHandler) SetChartVisibility(c *fiber.Ctx) error {
	claimID, err := parsePathUUID(c, "claimId")
	if err != nil {
		return err
	}

	var req dto.SetChartVisibilityRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	res, err := h.alerts.SetChartVisible(c.UserContext(), claimID, *req.Visible)
	if err != nil {
		return err
	}
	return response.OK(c, "chart visibility updated", res)
}

// Chart handles GET /api/v1/alerts/chart, the [C1] line chart and [C2] key.
func (h *AlertHandler) Chart(c *fiber.Ctx) error {
	from, err := parseOptionalTime(c.Query("from"))
	if err != nil {
		return apperr.BadRequest("from must be an RFC3339 timestamp or YYYY-MM-DD date")
	}
	to, err := parseOptionalTime(c.Query("to"))
	if err != nil {
		return apperr.BadRequest("to must be an RFC3339 timestamp or YYYY-MM-DD date")
	}

	chart, err := h.alerts.Chart(c.UserContext(), c.Query("granularity"), from, to)
	if err != nil {
		return err
	}
	return response.OK(c, "alert chart", chart)
}
