// Package dto holds the request and response shapes exchanged over HTTP.
//
// Keeping these separate from models means the AI service's column layout is
// never leaked directly to clients, and the API stays stable if that layout
// changes.
package dto

import (
	"errors"
	"strings"

	"github.com/go-playground/validator/v10"

	"github.com/cis/cis-backend/internal/pkg/apperr"
)

// Pagination defaults and bounds shared by every list endpoint.
const (
	DefaultPage  = 1
	DefaultLimit = 20
	MaxLimit     = 200
)

var validate = validator.New(validator.WithRequiredStructEnabled())

// Validate runs struct validation and converts failures into a
// VALIDATION_FAILED apperr carrying per-field details.
func Validate(payload any) error {
	if err := validate.Struct(payload); err != nil {
		var invalid *validator.InvalidValidationError
		if errors.As(err, &invalid) {
			return apperr.Internal("invalid validation target").Wrap(err)
		}

		details := map[string]string{}
		var fieldErrs validator.ValidationErrors
		if errors.As(err, &fieldErrs) {
			for _, fe := range fieldErrs {
				details[toSnake(fe.Field())] = describe(fe)
			}
		}
		return apperr.Validation("request validation failed").WithDetails(details)
	}
	return nil
}

func describe(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "this field is required"
	case "email":
		return "must be a valid email address"
	case "min":
		return "must be at least " + fe.Param() + " in length or value"
	case "max":
		return "must be at most " + fe.Param() + " in length or value"
	case "gte":
		return "must be greater than or equal to " + fe.Param()
	case "lte":
		return "must be less than or equal to " + fe.Param()
	case "oneof":
		return "must be one of: " + strings.ReplaceAll(fe.Param(), " ", ", ")
	case "uuid", "uuid4":
		return "must be a valid UUID"
	case "datetime":
		return "must match the format " + fe.Param()
	default:
		return "failed the '" + fe.Tag() + "' rule"
	}
}

// toSnake converts an exported Go field name to snake_case so error details
// match the JSON keys clients actually sent.
func toSnake(field string) string {
	var b strings.Builder
	for i, r := range field {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// PageParams is the normalized pagination window for a list query.
type PageParams struct {
	Page  int
	Limit int
}

// Offset returns the SQL OFFSET for the window.
func (p PageParams) Offset() int { return (p.Page - 1) * p.Limit }

// NormalizePage clamps caller-supplied paging values into supported bounds.
func NormalizePage(page, limit int) PageParams {
	if page < 1 {
		page = DefaultPage
	}
	if limit < 1 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	return PageParams{Page: page, Limit: limit}
}
