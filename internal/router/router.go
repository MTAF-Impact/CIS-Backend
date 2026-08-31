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

	// F5 — Coordinated-Network Detector.
	Network         *handler.NetworkHandler
	Allowlist       *handler.AllowlistHandler
	Detection       *handler.DetectionHandler
	DetectorSetting *handler.DetectorSettingsHandler
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
	// The detector's exclusion lists, read by the pipeline before candidate
	// selection (PRD 10.5.1, 10.5.2.2). The one place the read direction
	// between the two services reverses: the backend owns the declared-
	// coordination allowlist and the common-phrase list, and the AI service
	// consumes them.
	internal.Get("/detection/exclusions", h.Allowlist.Exclusions)

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
	// F5 detector configuration (US62). Registered before "/:key"-shaped routes
	// would be, and with the literal sub-paths first, so "ranges" and "history"
	// are never captured as a parameter.
	settings.Get("/detector/ranges", h.DetectorSetting.Ranges)
	settings.Get("/detector/history", h.DetectorSetting.History)
	settings.Get("/detector", h.DetectorSetting.Get)
	settings.Put("/detector", h.DetectorSetting.Update)
	settings.Get("/history", h.DetectorSetting.AllHistory)
	// PRD 10.8 requires every report page footer to carry the generation time in
	// UTC and city-local time, and nothing else in the system knows which city.
	settings.Get("/city-timezone", h.DetectorSetting.CityTimezone)
	settings.Put("/city-timezone", h.DetectorSetting.SetCityTimezone)

	// F5 — Coordinated-Network Detector.
	//
	// Every route is behind the same `authed` guard as F1-F4. There is no role
	// system anywhere in this backend, including here: the PRD defines no user
	// or role model, and "As an admin" in US62/US63/US64 is story voice used
	// since v1.3. The safety property these endpoints rely on is attribution,
	// not access control — every change records who made it and why, which is
	// what the review log, the allowlist's added_by/removed_by, and the export
	// audit log exist for. See docs/ARCHITECTURE.md and PRD-v1.4.md 3.3.
	networks := v1.Group("/networks", authed)
	networks.Get("/", h.Network.List)
	networks.Get("/:id", h.Network.Detail)
	networks.Put("/:id/status", h.Network.UpdateStatus)
	networks.Get("/:id/review-log", h.Network.ReviewLog)
	networks.Get("/:id/graph", h.Network.Graph)
	networks.Get("/:id/timeline", h.Network.Timeline)
	networks.Get("/:id/content", h.Network.Content)
	// Registered before "/:id/accounts/:accountId" so the literal ".csv" suffix
	// is not captured as an account id.
	networks.Get("/:id/accounts.csv", h.Network.AccountsCSV)
	networks.Get("/:id/accounts", h.Network.Accounts)
	networks.Get("/:id/accounts/:accountId", h.Network.AccountDrawer)
	networks.Post("/:id/accounts/:accountId/allowlist", h.Allowlist.AllowlistAccount)
	networks.Post("/:id/allowlist", h.Allowlist.AllowlistNetwork)
	networks.Get("/:id/reports", h.Network.ListReports)
	networks.Post("/:id/reports", h.Network.GenerateReport)
	networks.Post("/:id/evidence-bundle", h.Network.EvidenceBundle)

	// Generated artefacts are addressed by report id rather than nested under a
	// network, because a report outlives the page it was generated from: an
	// audit entry links to it directly, and so does a colleague's bookmark.
	reports := v1.Group("/reports", authed)
	reports.Get("/:reportId/file", h.Network.DownloadReport)

	// Detection runs (PRD 10.5.8). The read side is not under /admin: run
	// truncation and unavailable signal families explain why a network is
	// banded where it is, which is an analyst's question, not an operator's.
	runs := v1.Group("/detection-runs", authed)
	runs.Get("/", h.Detection.ListRuns)
	runs.Get("/:id", h.Detection.Run)

	admin := v1.Group("/admin", authed)
	admin.Post("/generate-generic-claim", h.Admin.GenerateGenericClaim)

	// F5 governance surfaces (US62, US63, US64, PRD 10.9.3).
	admin.Post("/detection-runs", h.Detection.Trigger)
	admin.Get("/offtopic-clusters/rates", h.Detection.OfftopicRates)
	admin.Get("/offtopic-clusters", h.Detection.OfftopicClusters)
	admin.Get("/dismissals/summary", h.Detection.DismissalSummary)
	admin.Get("/dismissals", h.Detection.Dismissals)
	admin.Get("/export-audit", h.Detection.AuditLog)
	admin.Get("/allowlist/categories", h.Allowlist.Categories)
	admin.Get("/allowlist", h.Allowlist.List)
	admin.Post("/allowlist", h.Allowlist.Create)
	admin.Patch("/allowlist/:id", h.Allowlist.Update)
	admin.Delete("/allowlist/:id", h.Allowlist.Remove)
	admin.Get("/common-phrases", h.Allowlist.ListPhrases)
	admin.Post("/common-phrases", h.Allowlist.CreatePhrase)
	admin.Delete("/common-phrases/:id", h.Allowlist.DeletePhrase)
	admin.Post("/snapshot-scores", h.Admin.SnapshotScores)
	// Proxies onto AI-owned capabilities. The frontend can only reach this
	// backend, so without these the AI service's ingestion, clustering and
	// rescoring endpoints are unreachable from the product.
	admin.Post("/generate-sample-content", h.Admin.GenerateSampleContent)
	admin.Post("/cluster-now", h.Admin.ClusterNow)
	admin.Post("/rescore", h.Admin.Rescore)
	admin.Post("/reconcile", h.Admin.Reconcile)
}
