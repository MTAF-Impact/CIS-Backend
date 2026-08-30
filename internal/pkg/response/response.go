// Package response defines the single JSON envelope every endpoint returns.
//
// Keeping one shape means the frontend can write one unwrap helper and one
// error handler, regardless of which feature it is talking to.
package response

import (
	"math"

	"github.com/gofiber/fiber/v2"

	"github.com/cis/cis-backend/internal/pkg/apperr"
)

// Envelope is the response body shared by every route.
type Envelope struct {
	Success bool       `json:"success"`
	Message string     `json:"message,omitempty"`
	Data    any        `json:"data,omitempty"`
	Error   *ErrorBody `json:"error,omitempty"`
	Meta    *Meta      `json:"meta,omitempty"`
}

// ErrorBody carries the machine-readable failure description.
type ErrorBody struct {
	Code    apperr.Code `json:"code"`
	Details any         `json:"details,omitempty"`
}

// Meta carries pagination for list endpoints.
type Meta struct {
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

// NewMeta builds pagination metadata from a page/limit/total triple.
func NewMeta(page, limit int, total int64) *Meta {
	totalPages := 0
	if limit > 0 {
		totalPages = int(math.Ceil(float64(total) / float64(limit)))
	}
	return &Meta{Page: page, Limit: limit, Total: total, TotalPages: totalPages}
}

// OK writes a 200 with a data payload.
func OK(c *fiber.Ctx, message string, data any) error {
	return c.Status(fiber.StatusOK).JSON(Envelope{Success: true, Message: message, Data: data})
}

// Created writes a 201 with a data payload.
func Created(c *fiber.Ctx, message string, data any) error {
	return c.Status(fiber.StatusCreated).JSON(Envelope{Success: true, Message: message, Data: data})
}

// List writes a 200 with a data payload and pagination metadata.
func List(c *fiber.Ctx, message string, data any, meta *Meta) error {
	return c.Status(fiber.StatusOK).JSON(Envelope{Success: true, Message: message, Data: data, Meta: meta})
}

// NoContent writes a 204 with no body.
func NoContent(c *fiber.Ctx) error {
	return c.SendStatus(fiber.StatusNoContent)
}

// Fail writes an error envelope derived from an *apperr.Error.
func Fail(c *fiber.Ctx, e *apperr.Error) error {
	return c.Status(e.Status).JSON(Envelope{
		Success: false,
		Message: e.Message,
		Error:   &ErrorBody{Code: e.Code, Details: e.Details},
	})
}
