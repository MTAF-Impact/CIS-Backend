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
		"database":       dbStatus,
		"storage_driver": h.store.Driver(),
		"ai_service":     h.aiStatus(ctx),
	}

	if dbStatus != "up" {
		return apperr.Unavailable("database is not reachable").WithDetails(payload)
	}
	return response.OK(c, "ready", payload)
}

// aiStatus reports both whether the AI service is configured and whether it
// actually answers — a URL being set says nothing about anything listening on
// it, and the two failures need different fixes.
//
// Deliberately non-fatal. The backend serves all read endpoints in full
// against a dead AI service (every claim read is a plain database query), so
// an unreachable AI service must never fail readiness and take the pods out
// of rotation. Only the write-through flows degrade, and they say so themselves.
func (h *HealthHandler) aiStatus(ctx context.Context) map[string]any {
	status := map[string]any{"configured": h.ai.Enabled()}
	if !h.ai.Enabled() {
		return status
	}

	if err := h.ai.Health(ctx); err != nil {
		status["reachable"] = false
		status["error"] = err.Error()
		return status
	}
	status["reachable"] = true
	return status
}
