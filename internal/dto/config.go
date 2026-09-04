package dto

import (
	"time"

	"github.com/cis/cis-backend/internal/models"
)

// The Admin Settings dynamic-parameter payloads (models.ConfigParams).
//
// The catalog carries the registry's own metadata alongside each current
// value, so the frontend renders its form from the server's description of the
// parameters rather than from a second copy of the specification. That is what
// keeps a bound in the form and the bound the server enforces from drifting
// apart.

// ConfigTier labels one of the two audiences the parameters split into.
type ConfigTier struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// ConfigParamView is one parameter's definition and its current value.
type ConfigParamView struct {
	models.ConfigParam
	// Value is the effective value: the stored one, or the documented default
	// when no row exists.
	Value string `json:"value"`
	// IsSet reports that the effective value differs from the documented
	// default, so the settings form can offer a reset only where there is
	// something to reset.
	// Deliberately not "a row exists": the seed writes a row for every
	// parameter, so row existence would be true everywhere and mean nothing.
	IsSet bool `json:"is_set"`
	// Writable is false for a derived value and for one that has a dedicated
	// endpoint of its own (see ManagedBy).
	Writable bool `json:"writable"`
}

// ConfigSectionView is one fieldset of the settings form.
type ConfigSectionView struct {
	Key         string            `json:"key"`
	Tier        string            `json:"tier"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Parameters  []ConfigParamView `json:"parameters"`
}

// ConfigCatalog is the whole dynamic-parameter surface in one payload.
type ConfigCatalog struct {
	Tiers       []ConfigTier        `json:"tiers"`
	Sections    []ConfigSectionView `json:"sections"`
	GeneratedAt time.Time           `json:"generated_at"`
}

// UpdateConfigParamsRequest is the body of PUT /api/v1/settings/parameters.
//
// A flat key/value map rather than a typed struct: the registry already
// declares every key's type and bounds, and a struct would be a second list of
// the same parameters that has to be edited whenever one is added. Values
// arrive as strings for the same reason — the declared type decides how each is
// parsed, so the transport does not need to guess.
type UpdateConfigParamsRequest struct {
	Parameters map[string]string `json:"parameters" validate:"required,min=1,dive,keys,required,endkeys"`
}
