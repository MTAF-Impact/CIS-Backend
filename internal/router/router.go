// Package router wires every HTTP route.
//
// This file is the single place to see the whole API surface; it mirrors
// docs/api/ one-for-one.
package router

import (
	"github.com/gofiber/fiber/v2"

	"github.com/cis/cis-backend/internal/config"
	"github.com/cis/cis-backend/internal/handler"
	"github.com/cis/cis-backend/internal/middleware"
	"github.com/cis/cis-backend/internal/service"
)

// Handlers bundles every handler the router needs.
type Handlers struct {
	Health  *handler.HealthHandler
	Auth    *handler.AuthHandler
	Claim   *handler.ClaimHandler
	Topic   *handler.TopicHandler
	Policy  *handler.PolicyHandler
	Alert   *handler.AlertHandler
	Setting *handler.SettingHandler
	Admin   *handler.AdminHandler
}

// Register mounts all routes on the app.
func Register(app *fiber.App, cfg *config.Config, h Handlers, auth *service.AuthService) {
	// --- Public: probes ---
	app.Get("/health", h.Health.Live)
	app.Get("/health/ready", h.Health.Ready)

	v1 := app.Group("/api/v1")

	// Applied explicitly per group rather than as a blanket Use on /api/v1, so
	// which routes are protected does not depend on registration order.
	authed := middleware.RequireAuth(auth)

	// --- Public: login flow ---
	authGroup := v1.Group("/auth")
	authGroup.Post("/register", h.Auth.Register)
	authGroup.Post("/login", h.Auth.Login)
	authGroup.Post("/refresh", h.Auth.Refresh)
	authGroup.Get("/me", authed, h.Auth.Me)
	authGroup.Post("/logout", authed, h.Auth.Logout)

	// --- Internal: AI service callbacks, guarded by X-Internal-Key ---
	internal := v1.Group("/internal", middleware.RequireInternalKey(cfg.Internal))
	internal.Post("/policies/:id/matchmaking-result", h.Policy.MatchmakingResult)

	// Topics — the filter chips shared by S1 and S2 (US6, US15).
	topics := v1.Group("/topics", authed)
	topics.Get("/", h.Topic.List)
	topics.Get("/:id", h.Topic.Detail)

	// F1 — Claim Repository Bank.
	claims := v1.Group("/claims", authed)
	// Registered before "/:id" so the literal path is not captured as an id.
	claims.Get("/repository", h.Claim.Repository)
	claims.Get("/", h.Claim.List)
	claims.Get("/:id", h.Claim.Detail)
	claims.Get("/:id/statements", h.Claim.Statements)
	claims.Get("/:id/top-accounts", h.Claim.TopAccounts)
	claims.Get("/:id/policies", h.Claim.Policies)
	claims.Get("/:id/score-history", h.Claim.ScoreHistory)
	claims.Put("/:id/status", h.Claim.UpdateStatus)

	// F2 — Public Policy Bank.
	policies := v1.Group("/policies", authed)
	policies.Get("/years", h.Policy.Years)
	policies.Get("/", h.Policy.List)
	policies.Post("/", h.Policy.Create)
	policies.Get("/:id", h.Policy.Detail)
	policies.Get("/:id/file", h.Policy.Download)
	policies.Get("/:id/processing", h.Policy.ProcessingStatus)
	policies.Post("/:id/rematch", h.Policy.Rematch)
	policies.Patch("/:id", h.Policy.Update)
	policies.Delete("/:id", h.Policy.Delete)

	// F3 — Alert Page.
	alerts := v1.Group("/alerts", authed)
	alerts.Get("/chart", h.Alert.Chart)
	alerts.Get("/", h.Alert.List)
	alerts.Post("/", h.Alert.Add)
	alerts.Delete("/:claimId", h.Alert.Remove)
	alerts.Patch("/:claimId/chart", h.Alert.SetChartVisibility)

	// F4 — Admin Setting Page.
	settings := v1.Group("/settings", authed)
	settings.Get("/", h.Setting.List)
	settings.Get("/alert-threshold", h.Setting.GetAlertThreshold)
	settings.Put("/alert-threshold", h.Setting.UpdateAlertThreshold)

	admin := v1.Group("/admin", authed)
	admin.Post("/generate-generic-claim", h.Admin.GenerateGenericClaim)
	admin.Post("/snapshot-scores", h.Admin.SnapshotScores)
}
