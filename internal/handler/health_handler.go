package handler

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/cis/cis-backend/internal/aiclient"
	"github.com/cis/cis-backend/internal/config"
	"github.com/cis/cis-backend/internal/database"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/pkg/response"
	"github.com/cis/cis-backend/internal/storage"
)

// HealthHandler serves liveness and readiness probes.
type HealthHandler struct {
	db      *gorm.DB
	cfg     *config.Config
	store   storage.Storage
	ai      *aiclient.Client
	started time.Time
}

// NewHealthHandler constructs a HealthHandler.
func NewHealthHandler(db *gorm.DB, cfg *config.Config, store storage.Storage, ai *aiclient.Client) *HealthHandler {
	return &HealthHandler{db: db, cfg: cfg, store: store, ai: ai, started: time.Now().UTC()}
}

// Live handles GET /health: the process is up. It deliberately performs no
// dependency checks so a database blip never restarts a healthy container.
func (h *HealthHandler) Live(c *fiber.Ctx) error {
	return response.OK(c, "ok", fiber.Map{
		"status":         "healthy",
		"service":        h.cfg.App.Name,
		"environment":    h.cfg.App.Env,
		"uptime_seconds": int(time.Since(h.started).Seconds()),
	})
}

// Ready handles GET /health/ready: every dependency required to serve traffic
// is reachable.
func (h *HealthHandler) Ready(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.UserContext(), 5*time.Second)
	defer cancel()

	dbStatus := "up"
	if err := database.Ping(ctx, h.db); err != nil {
		dbStatus = "down: " + err.Error()
	}

	payload := fiber.Map{
		"database":        dbStatus,
		"storage_driver":  h.store.Driver(),
		"ai_service":      map[string]any{"configured": h.ai.Enabled()},
		"internal_routes_authenticated": h.cfg.Internal.APIKey != "",
	}

	if dbStatus != "up" {
		return apperr.Unavailable("database is not reachable").WithDetails(payload)
	}
	return response.OK(c, "ready", payload)
}
