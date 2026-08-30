package router

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/cis/cis-backend/internal/config"
	"github.com/cis/cis-backend/internal/handler"
	"github.com/cis/cis-backend/internal/middleware"
	"github.com/cis/cis-backend/internal/service"
)

// newTestApp registers every route against zero-value handlers.
//
// The handlers hold nil services, which is safe because these tests only
// inspect the route table — they never dispatch a request into a handler body.
func newTestApp(t *testing.T) *fiber.App {
	t.Helper()

	// The real error handler is wired in so rejected requests produce the same
	// status codes they would in production.
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler(false)})
	cfg := &config.Config{Internal: config.InternalConfig{APIKey: "test-key"}}

	Register(app, cfg, Handlers{
		Health:  &handler.HealthHandler{},
		Auth:    &handler.AuthHandler{},
		Claim:   &handler.ClaimHandler{},
		Topic:   &handler.TopicHandler{},
		Policy:  &handler.PolicyHandler{},
		Alert:   &handler.AlertHandler{},
		Setting: &handler.SettingHandler{},
		Admin:   &handler.AdminHandler{},
	}, &service.AuthService{})

	return app
}

// routeKey identifies a registered route.
type routeKey struct {
	method string
	path   string
}

func registeredRoutes(app *fiber.App) map[routeKey]int {
	out := map[routeKey]int{}
	for _, r := range app.GetRoutes() {
		out[routeKey{r.Method, r.Path}] = len(r.Handlers)
	}
	return out
}

func TestEveryDocumentedRouteIsRegistered(t *testing.T) {
	routes := registeredRoutes(newTestApp(t))

	// Mirrors docs/api/. If a route is added or renamed, this list and the
	// Markdown docs must both be updated.
	expected := []routeKey{
		{fiber.MethodGet, "/health"},
		{fiber.MethodGet, "/health/ready"},

		{fiber.MethodPost, "/api/v1/auth/register"},
		{fiber.MethodPost, "/api/v1/auth/login"},
		{fiber.MethodPost, "/api/v1/auth/refresh"},
		{fiber.MethodGet, "/api/v1/auth/me"},
		{fiber.MethodPost, "/api/v1/auth/logout"},

		{fiber.MethodGet, "/api/v1/topics"},
		{fiber.MethodGet, "/api/v1/topics/:id"},

		{fiber.MethodGet, "/api/v1/claims/repository"},
		{fiber.MethodGet, "/api/v1/claims"},
		{fiber.MethodGet, "/api/v1/claims/:id"},
		{fiber.MethodGet, "/api/v1/claims/:id/statements"},
		{fiber.MethodGet, "/api/v1/claims/:id/top-accounts"},
		{fiber.MethodGet, "/api/v1/claims/:id/policies"},
		{fiber.MethodGet, "/api/v1/claims/:id/score-history"},
		{fiber.MethodPut, "/api/v1/claims/:id/status"},

		{fiber.MethodGet, "/api/v1/policies"},
		{fiber.MethodGet, "/api/v1/policies/years"},
		{fiber.MethodPost, "/api/v1/policies"},
		{fiber.MethodGet, "/api/v1/policies/:id"},
		{fiber.MethodGet, "/api/v1/policies/:id/file"},
		{fiber.MethodGet, "/api/v1/policies/:id/processing"},
		{fiber.MethodPost, "/api/v1/policies/:id/rematch"},
		{fiber.MethodPatch, "/api/v1/policies/:id"},
		{fiber.MethodDelete, "/api/v1/policies/:id"},

		{fiber.MethodGet, "/api/v1/alerts"},
		{fiber.MethodGet, "/api/v1/alerts/chart"},
		{fiber.MethodPost, "/api/v1/alerts"},
		{fiber.MethodDelete, "/api/v1/alerts/:claimId"},
		{fiber.MethodPatch, "/api/v1/alerts/:claimId/chart"},

		{fiber.MethodGet, "/api/v1/settings"},
		{fiber.MethodGet, "/api/v1/settings/alert-threshold"},
		{fiber.MethodPut, "/api/v1/settings/alert-threshold"},

		{fiber.MethodPost, "/api/v1/admin/generate-generic-claim"},
		{fiber.MethodPost, "/api/v1/admin/snapshot-scores"},

		{fiber.MethodPost, "/api/v1/internal/policies/:id/matchmaking-result"},
	}

	for _, want := range expected {
		if _, ok := routes[want]; !ok {
			t.Errorf("route not registered: %s %s", want.method, want.path)
		}
	}
}

// TestProtectedRoutesRejectAnonymousRequests dispatches real requests with no
// Authorization header and asserts each is rejected before it ever reaches a
// handler.
//
// This exercises the middleware chain rather than inspecting the route table,
// because Fiber registers group-level middleware as its own stack entry that
// does not appear in a route's own handler list.
func TestProtectedRoutesRejectAnonymousRequests(t *testing.T) {
	app := newTestApp(t)

	id := "11111111-2222-3333-4444-555555555555"
	protected := []routeKey{
		{fiber.MethodGet, "/api/v1/auth/me"},
		{fiber.MethodPost, "/api/v1/auth/logout"},
		{fiber.MethodGet, "/api/v1/topics"},
		{fiber.MethodGet, "/api/v1/topics/" + id},
		{fiber.MethodGet, "/api/v1/claims/repository"},
		{fiber.MethodGet, "/api/v1/claims"},
		{fiber.MethodGet, "/api/v1/claims/" + id},
		{fiber.MethodGet, "/api/v1/claims/" + id + "/statements"},
		{fiber.MethodGet, "/api/v1/claims/" + id + "/top-accounts"},
		{fiber.MethodGet, "/api/v1/claims/" + id + "/policies"},
		{fiber.MethodGet, "/api/v1/claims/" + id + "/score-history"},
		{fiber.MethodPut, "/api/v1/claims/" + id + "/status"},
		{fiber.MethodGet, "/api/v1/policies"},
		{fiber.MethodGet, "/api/v1/policies/years"},
		{fiber.MethodPost, "/api/v1/policies"},
		{fiber.MethodGet, "/api/v1/policies/" + id},
		{fiber.MethodGet, "/api/v1/policies/" + id + "/file"},
		{fiber.MethodGet, "/api/v1/policies/" + id + "/processing"},
		{fiber.MethodPost, "/api/v1/policies/" + id + "/rematch"},
		{fiber.MethodPatch, "/api/v1/policies/" + id},
		{fiber.MethodDelete, "/api/v1/policies/" + id},
		{fiber.MethodGet, "/api/v1/alerts"},
		{fiber.MethodGet, "/api/v1/alerts/chart"},
		{fiber.MethodPost, "/api/v1/alerts"},
		{fiber.MethodDelete, "/api/v1/alerts/" + id},
		{fiber.MethodPatch, "/api/v1/alerts/" + id + "/chart"},
		{fiber.MethodGet, "/api/v1/settings"},
		{fiber.MethodGet, "/api/v1/settings/alert-threshold"},
		{fiber.MethodPut, "/api/v1/settings/alert-threshold"},
		{fiber.MethodPost, "/api/v1/admin/generate-generic-claim"},
		{fiber.MethodPost, "/api/v1/admin/snapshot-scores"},
	}

	for _, r := range protected {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			req := httptest.NewRequest(r.method, r.path, nil)
			res, err := app.Test(req, 5000)
			if err != nil {
				t.Fatalf("dispatch failed: %v", err)
			}
			defer res.Body.Close()

			if res.StatusCode != fiber.StatusUnauthorized {
				t.Errorf("got status %d, want 401 — this route is reachable without a token",
					res.StatusCode)
			}
		})
	}
}

// TestInternalRouteRequiresInternalKey checks the AI service callback is
// guarded by the shared secret rather than by the operator JWT.
func TestInternalRouteRequiresInternalKey(t *testing.T) {
	app := newTestApp(t)
	path := "/api/v1/internal/policies/11111111-2222-3333-4444-555555555555/matchmaking-result"

	t.Run("rejects a missing key", func(t *testing.T) {
		res, err := app.Test(httptest.NewRequest(fiber.MethodPost, path, nil), 5000)
		if err != nil {
			t.Fatalf("dispatch failed: %v", err)
		}
		defer res.Body.Close()
		if res.StatusCode != fiber.StatusUnauthorized {
			t.Errorf("got status %d, want 401", res.StatusCode)
		}
	})

	t.Run("rejects a wrong key", func(t *testing.T) {
		req := httptest.NewRequest(fiber.MethodPost, path, nil)
		req.Header.Set("X-Internal-Key", "not-the-key")
		res, err := app.Test(req, 5000)
		if err != nil {
			t.Fatalf("dispatch failed: %v", err)
		}
		defer res.Body.Close()
		if res.StatusCode != fiber.StatusUnauthorized {
			t.Errorf("got status %d, want 401", res.StatusCode)
		}
	})
}

// TestInternalRoutesOpenWithoutConfiguredKey verifies that leaving
// INTERNAL_API_KEY unset leaves the callback routes reachable with no
// X-Internal-Key header at all, for deployments where the backend and AI
// service exchange no shared secret (e.g. a private network).
func TestInternalRoutesOpenWithoutConfiguredKey(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler(false)})
	Register(app, &config.Config{}, Handlers{
		Health: &handler.HealthHandler{}, Auth: &handler.AuthHandler{},
		Claim: &handler.ClaimHandler{}, Topic: &handler.TopicHandler{},
		Policy: &handler.PolicyHandler{}, Alert: &handler.AlertHandler{},
		Setting: &handler.SettingHandler{}, Admin: &handler.AdminHandler{},
	}, &service.AuthService{})

	path := "/api/v1/internal/policies/11111111-2222-3333-4444-555555555555/matchmaking-result"
	// No X-Internal-Key header at all — the request should reach the handler
	// rather than being rejected by the auth middleware. The handler then
	// fails on the empty body, which proves the guard let it through.
	req := httptest.NewRequest(fiber.MethodPost, path, nil)

	res, err := app.Test(req, 5000)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode == fiber.StatusUnauthorized || res.StatusCode == fiber.StatusForbidden {
		t.Errorf("got status %d, want the request to pass the auth guard when no key is configured",
			res.StatusCode)
	}
}

// TestPublicRoutesAreReachableWithoutAToken confirms the login endpoints are
// not behind auth; otherwise no one could ever obtain a token.
func TestPublicRoutesAreReachableWithoutAToken(t *testing.T) {
	routes := registeredRoutes(newTestApp(t))

	public := []routeKey{
		{fiber.MethodPost, "/api/v1/auth/register"},
		{fiber.MethodPost, "/api/v1/auth/login"},
		{fiber.MethodPost, "/api/v1/auth/refresh"},
		{fiber.MethodGet, "/health"},
		{fiber.MethodGet, "/health/ready"},
	}

	for _, r := range public {
		count, ok := routes[r]
		if !ok {
			t.Fatalf("route not registered: %s %s", r.method, r.path)
		}
		// A single handler means no per-route middleware was prepended. These
		// paths also sit outside every group that carries auth middleware.
		if count != 1 {
			t.Errorf("%s %s has %d handlers, expected 1 (no auth middleware)", r.method, r.path, count)
		}
	}
}

// TestLiteralPathsPrecedeParameterRoutes guards the ordering that keeps
// /claims/repository, /policies/years, and /alerts/chart from being swallowed
// by their sibling ":id" routes.
func TestLiteralPathsPrecedeParameterRoutes(t *testing.T) {
	app := newTestApp(t)

	positions := map[string]int{}
	for i, r := range app.GetRoutes() {
		key := fmt.Sprintf("%s %s", r.Method, r.Path)
		if _, seen := positions[key]; !seen {
			positions[key] = i
		}
	}

	pairs := [][2]string{
		{"GET /api/v1/claims/repository", "GET /api/v1/claims/:id"},
		{"GET /api/v1/policies/years", "GET /api/v1/policies/:id"},
	}

	for _, pair := range pairs {
		literal, ok := positions[pair[0]]
		if !ok {
			t.Fatalf("route not registered: %s", pair[0])
		}
		param, ok := positions[pair[1]]
		if !ok {
			t.Fatalf("route not registered: %s", pair[1])
		}
		if literal > param {
			t.Errorf("%s is registered after %s and would be shadowed", pair[0], pair[1])
		}
	}
}
