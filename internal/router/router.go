// Package router wires every HTTP route.
//
// This file is the single place to see the whole API surface; it mirrors
// docs/api/ one-for-one.
package router

import (
	"github.com/gofiber/fiber/v2"

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
func Register(app *fiber.App, h Handlers, auth *service.AuthService) {
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

	// --- Internal: AI service callbacks ---
	//
	// DELIBERATELY UNAUTHENTICATED. There is no shared secret between the two
	// services: the AI service reaches the backend over a private network, and
	// adding a key that both sides default to empty bought a checkbox rather
	// than a guarantee.
	//
	// The consequence is a deployment constraint, not a code one: /api/v1/internal/*
	// must never be routed from the public internet. Anyone who can reach it can
	// post a matchmaking result for any policy id — which sets ai_policy_id, the
	// join key the whole policy-to-claim correlation hangs off. Block the prefix
	// at the ingress, or bind these routes to an internal listener.
	//
	// To reintroduce auth: a guard on this one group is all it takes. See
	// docs/api/internal.md.
	internal := v1.Group("/internal")
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
	// Flow 4: the one claim mutation that has to travel through the AI service,
	// because it writes AI-owned score columns.
	claims.Put("/:id/harm/confirm", h.Claim.ConfirmHarm)

	// F2 — Public Policy Bank.
	policies := v1.Group("/policies", authed)
	policies.Get("/years", h.Policy.Years)
	policies.Get("/", h.Policy.List)
	policies.Post("/", h.Policy.Create)
	policies.Get("/:id", h.Policy.Detail)
	policies.Get("/:id/file", h.Policy.Download)
	policies.Put("/:id/file", h.Policy.ReplaceFile)
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
	// Proxies onto AI-owned capabilities. The frontend can only reach this
	// backend, so without these the AI service's ingestion, clustering and
	// rescoring endpoints are unreachable from the product.
	admin.Post("/generate-sample-content", h.Admin.GenerateSampleContent)
	admin.Post("/cluster-now", h.Admin.ClusterNow)
	admin.Post("/rescore", h.Admin.Rescore)
	admin.Post("/reconcile", h.Admin.Reconcile)
}
