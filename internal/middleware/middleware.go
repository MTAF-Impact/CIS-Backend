// Package middleware holds the Fiber middleware chain: error translation,
// logging, recovery, CORS, and the two authentication schemes (operator JWT and
// the internal machine-to-machine API key).
package middleware

import (
	"crypto/subtle"
	"errors"
	"log"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"
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
)

// ErrorHandler converts any error returned by a handler into the standard
// response envelope. Domain errors keep their status and code; everything else
// becomes a 500 with the detail hidden from the client but logged server-side.
func ErrorHandler(isProduction bool) fiber.ErrorHandler {
	return func(c *fiber.Ctx, err error) error {
		if appErr, ok := apperr.As(err); ok {
			if appErr.Status >= fiber.StatusInternalServerError {
				log.Printf("[error] %s %s: %v", c.Method(), c.Path(), appErr)
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

		log.Printf("[error] unhandled %s %s: %v", c.Method(), c.Path(), err)
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
	return requestid.New()
}

// Logger writes one structured line per request.
func Logger() fiber.Handler {
	return logger.New(logger.Config{
		Format:     "${time} ${locals:requestid} ${status} ${latency} ${method} ${path}\n",
		TimeFormat: "2006-01-02T15:04:05Z",
		TimeZone:   "UTC",
	})
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
		AllowHeaders: "Origin,Content-Type,Accept,Authorization,X-Internal-Key,X-Request-ID",
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

// RequireInternalKey guards the routes the AI service calls back on.
//
// When INTERNAL_API_KEY is unset, no shared secret was exchanged and the
// routes are left open — callers are expected to be reachable only from a
// private network in that case. Set INTERNAL_API_KEY on both sides to require
// and validate the X-Internal-Key header instead.
func RequireInternalKey(cfg config.InternalConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.APIKey == "" {
			return c.Next()
		}

		presented := c.Get("X-Internal-Key")
		if presented == "" {
			return apperr.Unauthorized("X-Internal-Key header is required")
		}
		if subtle.ConstantTimeCompare([]byte(presented), []byte(cfg.APIKey)) != 1 {
			return apperr.Unauthorized("invalid internal API key")
		}
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
