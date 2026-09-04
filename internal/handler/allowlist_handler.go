package handler

import (
	"github.com/gofiber/fiber/v2"

	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/middleware"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/pkg/response"
	"github.com/cis/cis-backend/internal/service"
)

// AllowlistHandler serves the declared-coordination allowlist and the
// common-phrase exclusion list.
type AllowlistHandler struct {
	allowlist *service.AllowlistService
}

// NewAllowlistHandler constructs an AllowlistHandler.
func NewAllowlistHandler(allowlist *service.AllowlistService) *AllowlistHandler {
	return &AllowlistHandler{allowlist: allowlist}
}

// List handles GET /api/v1/admin/allowlist.
func (h *AllowlistHandler) List(c *fiber.Ctx) error {
	rows, total, page, err := h.allowlist.List(c.UserContext(), service.ListAllowlistQuery{
		Search:   c.Query("q"),
		Platform: c.Query("platform"),
		Category: c.Query("category"),
		// Removed entries stay reachable: "who withdrew this NGO's protection,
		// when, and why" is the question the soft delete exists to answer.
		IncludeRemoved: c.QueryBool("include_removed", false),
		Page:           c.QueryInt("page", dto.DefaultPage),
		Limit:          c.QueryInt("limit", dto.DefaultLimit),
	})
	if err != nil {
		return err
	}
	return response.List(c, "declared-coordination allowlist", rows, response.NewMeta(page.Page, page.Limit, total))
}

// Categories handles GET /api/v1/admin/allowlist/categories.
//
// The list is meant to be seeded during onboarding with the city's known
// civil-society partners, before the first detection run rather than after
// the first false positive. These counts are how an operator can see at a
// glance whether that ever happened.
func (h *AllowlistHandler) Categories(c *fiber.Ctx) error {
	counts, err := h.allowlist.Categories(c.UserContext())
	if err != nil {
		return err
	}
	return response.OK(c, "allowlist entries per category", counts)
}

// Create handles POST /api/v1/admin/allowlist.
func (h *AllowlistHandler) Create(c *fiber.Ctx) error {
	var req dto.CreateAllowlistEntryRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	res, err := h.allowlist.Create(c.UserContext(), req, middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.Created(c, "account added to the declared-coordination allowlist", res)
}

// Update handles PATCH /api/v1/admin/allowlist/:id.
func (h *AllowlistHandler) Update(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	var req dto.UpdateAllowlistEntryRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	entry, err := h.allowlist.Update(c.UserContext(), id, req, middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.OK(c, "allowlist entry updated", entry)
}

// Remove handles DELETE /api/v1/admin/allowlist/:id.
//
// A DELETE with a body, which is unusual — but a reason is required and the
// removal must be logged, since withdrawing an organisation's protection is
// the change most worth being able to attribute later.
func (h *AllowlistHandler) Remove(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	var req dto.RemoveAllowlistEntryRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("a JSON body with a reason is required to remove an allowlist entry").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	entry, err := h.allowlist.Remove(c.UserContext(), id, req, middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.OK(c, "allowlist entry removed", entry)
}

// AllowlistNetwork handles POST /api/v1/networks/:id/allowlist.
func (h *AllowlistHandler) AllowlistNetwork(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}

	var req dto.AddAllowlistRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	res, err := h.allowlist.AllowlistNetwork(c.UserContext(), id, req, middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.Created(c, "network marked as legitimate coordination", res)
}

// AllowlistAccount handles POST /api/v1/networks/:id/accounts/:accountId/allowlist.
func (h *AllowlistHandler) AllowlistAccount(c *fiber.Ctx) error {
	accountID, err := parsePathUUID(c, "accountId")
	if err != nil {
		return err
	}

	var req dto.AddAllowlistRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	res, err := h.allowlist.AllowlistAccount(c.UserContext(), accountID, req, middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.Created(c, "account marked as legitimate coordination", res)
}

// ListPhrases handles GET /api/v1/admin/common-phrases.
func (h *AllowlistHandler) ListPhrases(c *fiber.Ctx) error {
	rows, total, page, err := h.allowlist.ListPhrases(
		c.UserContext(), c.Query("q"), c.QueryInt("page", dto.DefaultPage), c.QueryInt("limit", dto.DefaultLimit))
	if err != nil {
		return err
	}
	return response.List(c, "common-phrase allowlist", rows, response.NewMeta(page.Page, page.Limit, total))
}

// CreatePhrase handles POST /api/v1/admin/common-phrases.
func (h *AllowlistHandler) CreatePhrase(c *fiber.Ctx) error {
	var req dto.CreateCommonPhraseRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	phrase, err := h.allowlist.AddPhrase(c.UserContext(), req, middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.Created(c, "phrase added to the duplication exclusion list", phrase)
}

// DeletePhrase handles DELETE /api/v1/admin/common-phrases/:id.
func (h *AllowlistHandler) DeletePhrase(c *fiber.Ctx) error {
	id, err := parsePathUUID(c, "id")
	if err != nil {
		return err
	}
	if err := h.allowlist.DeletePhrase(c.UserContext(), id); err != nil {
		return err
	}
	return response.NoContent(c)
}

// Exclusions handles GET /api/v1/internal/detection/exclusions.
//
// Read by the AI pipeline before candidate selection. This is the one place the read direction between the two services reverses:
// everything else the AI service writes and this backend reads.
//
// Served whole rather than paged. Applying half an exclusion list is worse than
// applying none — it produces a detection that looks complete and is not.
func (h *AllowlistHandler) Exclusions(c *fiber.Ctx) error {
	out, err := h.allowlist.Exclusions(c.UserContext())
	if err != nil {
		return err
	}
	return response.OK(c, "detector exclusion lists", out)
}
