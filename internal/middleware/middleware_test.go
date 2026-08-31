package middleware

import (
	"bytes"
	"log"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/cis/cis-backend/internal/pkg/apperr"
)

// newLoggedApp builds an app with the real RequestID + AccessLog + Recover
// chain and redirects the standard logger into buf so the emitted lines can be
// asserted on.
func newLoggedApp(t *testing.T) (*fiber.App, *bytes.Buffer) {
	t.Helper()

	buf := &bytes.Buffer{}
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler(true)})
	app.Use(RequestID())
	app.Use(AccessLog())
	app.Use(Recover())
	return app, buf
}

func do(t *testing.T, app *fiber.App, method, path string) {
	t.Helper()
	res, err := app.Test(httptest.NewRequest(method, path, nil), 5000)
	if err != nil {
		t.Fatalf("dispatch %s %s: %v", method, path, err)
	}
	_ = res.Body.Close()
}

func TestAccessLogOneLinePerRequest(t *testing.T) {
	app, buf := newLoggedApp(t)
	app.Get("/api/v1/ping", func(c *fiber.Ctx) error { return c.SendString("pong") })

	do(t, app, fiber.MethodGet, "/api/v1/ping")

	lines := nonEmptyLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want 1:\n%s", len(lines), buf.String())
	}
	line := lines[0]
	for _, want := range []string{"[http]", "GET", "/api/v1/ping", "200"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q missing %q", line, want)
		}
	}
	if strings.Contains(line, "error=") {
		t.Errorf("healthy request logged an error field: %q", line)
	}
}

func TestAccessLogRecordsDomainErrorCode(t *testing.T) {
	app, buf := newLoggedApp(t)
	app.Get("/api/v1/boom", func(c *fiber.Ctx) error {
		return apperr.NotFound("nope")
	})

	do(t, app, fiber.MethodGet, "/api/v1/boom")

	line := buf.String()
	if !strings.Contains(line, "404") || !strings.Contains(line, "error=NOT_FOUND") {
		t.Errorf("expected 404 / error=NOT_FOUND, got: %q", line)
	}
}

func TestAccessLogNamesTheRoutePattern(t *testing.T) {
	app, buf := newLoggedApp(t)
	app.Get("/api/v1/claims/:id", func(c *fiber.Ctx) error { return c.SendString("ok") })

	do(t, app, fiber.MethodGet, "/api/v1/claims/abc-123")

	line := buf.String()
	if !strings.Contains(line, "/api/v1/claims/abc-123") {
		t.Errorf("expected the concrete path, got: %q", line)
	}
	if !strings.Contains(line, "route=/api/v1/claims/:id") {
		t.Errorf("expected route=/api/v1/claims/:id, got: %q", line)
	}
}

func TestAccessLogOmitsRedundantRoutePattern(t *testing.T) {
	app, buf := newLoggedApp(t)
	app.Get("/api/v1/ping", func(c *fiber.Ctx) error { return c.SendString("pong") })

	do(t, app, fiber.MethodGet, "/api/v1/ping")

	if strings.Contains(buf.String(), "route=") {
		t.Errorf("static route should not repeat itself as route=, got: %q", buf.String())
	}
}

func TestAccessLogReportsRecoveredPanic(t *testing.T) {
	app, buf := newLoggedApp(t)
	app.Get("/api/v1/panic", func(c *fiber.Ctx) error {
		panic("kaboom")
	})

	do(t, app, fiber.MethodGet, "/api/v1/panic")

	out := buf.String()
	if !strings.Contains(out, "[http]") || !strings.Contains(out, "500") {
		t.Errorf("recovered panic produced no 500 access line: %q", out)
	}
}

func TestAccessLogSkipsHealthyProbes(t *testing.T) {
	app, buf := newLoggedApp(t)
	app.Get("/health", func(c *fiber.Ctx) error { return c.SendString("ok") })

	do(t, app, fiber.MethodGet, "/health")

	if got := strings.TrimSpace(buf.String()); got != "" {
		t.Errorf("healthy probe should not be logged, got: %q", got)
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
