// Package middleware holds the Fiber middleware chain: error translation,
// logging, recovery, CORS, and the operator JWT guard.
//
// There is deliberately no guard on /api/v1/internal/*. See router.go.
package middleware

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"
	"github.com/gofiber/fiber/v2/utils"
	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/config"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/pkg/response"
	"github.com/cis/cis-backend/internal/service"
)

// Context keys for values the middleware stores on the request.
const (
	CtxUserID    = "auth_user_id"
	CtxUserEmail = "auth_user_email"
	CtxUserName  = "auth_user_name"

	// ctxRequestID is the locals key RequestID stores the correlation id under.
	// Every log line quotes it so a request can be followed across the access
	// line and any error lines it produced.
	ctxRequestID = "requestid"
)

// ErrorHandler converts any error returned by a handler into the standard
// response envelope. Domain errors keep their status and code; everything else
// becomes a 500 with the detail hidden from the client but logged server-side.
func ErrorHandler(isProduction bool) fiber.ErrorHandler {
	return func(c *fiber.Ctx, err error) error {
		if appErr, ok := apperr.As(err); ok {
			if appErr.Status >= fiber.StatusInternalServerError {
				log.Printf("[error] %s %s %s: %v", requestID(c), c.Method(), c.Path(), appErr)
			}
			return response.Fail(c, appErr)
		}

		// Fiber's own errors (404 from the router, 413, malformed body...).
		var fiberErr *fiber.Error
		if errors.As(err, &fiberErr) {
			code := apperr.CodeInternal
			switch fiberErr.Code {
			case fiber.StatusNotFound:
				code = apperr.CodeNotFound
			case fiber.StatusMethodNotAllowed, fiber.StatusBadRequest:
				code = apperr.CodeBadRequest
			case fiber.StatusRequestEntityTooLarge:
				code = apperr.CodeTooLarge
			}
			return response.Fail(c, &apperr.Error{
				Status:  fiberErr.Code,
				Code:    code,
				Message: fiberErr.Message,
			})
		}

		log.Printf("[error] unhandled %s %s %s: %v", requestID(c), c.Method(), c.Path(), err)
		message := "an unexpected error occurred"
		if !isProduction {
			message = err.Error()
		}
		return response.Fail(c, apperr.Internal("%s", message))
	}
}

// Recover converts panics into 500 responses instead of killing the process.
func Recover() fiber.Handler {
	return recover.New(recover.Config{EnableStackTrace: true})
}

// RequestID attaches a correlation id to every request and response.
func RequestID() fiber.Handler {
	return requestid.New(requestid.Config{
		ContextKey: ctxRequestID,
		// UUIDv4 rather than the default counter-backed generator, whose
		// sequential ids would leak the server's request volume into our logs.
		Generator: utils.UUIDv4,
	})
}

// AccessLog writes exactly one line per request:
//
//	[http] <request-id> <method> <path> <status> <latency> [user=<id>] [error=<code>]
//
// The user and error fields appear only when they apply, so a run of healthy
// reads stays quiet and the lines that matter stand out. It carries no
// timestamp of its own — the standard logger prefix already stamps every line
// in UTC.
//
// This must sit outside Recover in the chain so a recovered panic still yields
// a line.
func AccessLog() fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()

		// Resolve the chain error through the app error handler now, before the
		// status is read, so the code logged is the one the client received.
		// This mirrors what Fiber's own logger middleware does.
		chainErr := c.Next()
		if chainErr != nil {
			if err := c.App().ErrorHandler(c, chainErr); err != nil {
				_ = c.SendStatus(fiber.StatusInternalServerError)
			}
		}

		status := c.Response().StatusCode()

		// Liveness and readiness probes are polled continuously by the platform;
		// a successful one is noise. A failing one still gets a line.
		if status < fiber.StatusBadRequest && strings.HasPrefix(c.Path(), "/health") {
			return nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "[http] %s %s %s %d %s",
			requestID(c), c.Method(), c.Path(), status,
			time.Since(start).Round(100*time.Microsecond))
		// The matched route pattern names the endpoint (and so the handler),
		// which the concrete path can't when it carries ids. Omitted when it
		// adds nothing: static routes, or a request that matched no route.
		if pattern := c.Route().Path; pattern != c.Path() && pattern != "/" {
			fmt.Fprintf(&b, " route=%s", pattern)
		}
		if uid := UserIDFromContext(c); uid != nil {
			fmt.Fprintf(&b, " user=%s", uid)
		}
		if appErr, ok := apperr.As(chainErr); ok {
			fmt.Fprintf(&b, " error=%s", appErr.Code)
		} else if chainErr != nil {
			fmt.Fprintf(&b, " error=%q", chainErr.Error())
		}
		log.Print(b.String())

		return nil
	}
}

// requestID returns the correlation id attached by RequestID, or "-" when the
// middleware has not run yet.
func requestID(c *fiber.Ctx) string {
	if v, ok := c.Locals(ctxRequestID).(string); ok && v != "" {
		return v
	}
	return "-"
}

// CORS restricts browser access to the configured origins.
func CORS(cfg config.AppConfig) fiber.Handler {
	origins := strings.Join(cfg.AllowedOrigins, ",")
	return cors.New(cors.Config{
		AllowOrigins: origins,
		AllowMethods: strings.Join([]string{
			fiber.MethodGet, fiber.MethodPost, fiber.MethodPut,
			fiber.MethodPatch, fiber.MethodDelete, fiber.MethodOptions,
		}, ","),
		AllowHeaders: "Origin,Content-Type,Accept,Authorization,X-Request-ID",
		// Credentials cannot be combined with a wildcard origin; enable them
		// only when an explicit allowlist is configured.
		AllowCredentials: origins != "*",
		MaxAge:           3600,
	})
}

// RequireAuth validates the Bearer access token and stores the caller's
// identity on the request context.
func RequireAuth(auth *service.AuthService) fiber.Handler {
	return func(c *fiber.Ctx) error {
		header := c.Get(fiber.HeaderAuthorization)
		if header == "" {
			return apperr.Unauthorized("authorization header is required")
		}

		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
			return apperr.Unauthorized("authorization header must be in the form 'Bearer <token>'")
		}

		claims, err := auth.ParseAccessToken(strings.TrimSpace(parts[1]))
		if err != nil {
			return err
		}

		userID, err := uuid.Parse(claims.UserID)
		if err != nil {
			return apperr.Unauthorized("access token carries an invalid subject")
		}

		c.Locals(CtxUserID, userID)
		c.Locals(CtxUserEmail, claims.Email)
		c.Locals(CtxUserName, claims.Name)
		return c.Next()
	}
}

// UserIDFromContext returns the authenticated caller's id, if any.
func UserIDFromContext(c *fiber.Ctx) *uuid.UUID {
	if v, ok := c.Locals(CtxUserID).(uuid.UUID); ok {
		return &v
	}
	return nil
}

// MustUserID returns the authenticated caller's id, erroring when absent. Only
// valid on routes behind RequireAuth.
func MustUserID(c *fiber.Ctx) (uuid.UUID, error) {
	if id := UserIDFromContext(c); id != nil {
		return *id, nil
	}
	return uuid.Nil, apperr.Unauthorized("authentication required")
}
