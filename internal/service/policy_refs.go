package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/repository"
)

// resolvePolicyRefs turns a set of AI policy ids into the PolicyRef shape the
// API returns.
//
// A policy can exist in two forms and both have to render: one registered
// through F2 (cis_policies, carrying a document, a rollout status and a
// rolled-out date) and one the AI service created directly during matchmaking,
// which has none of those. The `source` field is what tells them apart, and the
// F2 record wins when both exist because it is the one with the operator's own
// metadata on it.
//
// Shared by F1's claim detail (US12), F2's own lookups, and F5's network detail
// (US49), where the linked policies are resolved transitively through the
// network's linked claims. Three callers, one shape — a network's policy list
// must look exactly like a claim's, since it is the same object.
func resolvePolicyRefs(
	ctx context.Context, policies *repository.PolicyRepository, aiPolicyIDs []uuid.UUID,
) ([]dto.PolicyRef, error) {
	refs := make([]dto.PolicyRef, 0, len(aiPolicyIDs))
	if len(aiPolicyIDs) == 0 {
		return refs, nil
	}

	cisPolicies, err := policies.FindByAIPolicyIDs(ctx, aiPolicyIDs)
	if err != nil {
		return nil, apperr.Internal("could not load policy records").Wrap(err)
	}
	byAIID := make(map[uuid.UUID]models.CISPolicy, len(cisPolicies))
	for _, p := range cisPolicies {
		if p.AIPolicyID != nil {
			byAIID[*p.AIPolicyID] = p
		}
	}

	aiPolicies, err := policies.FindAIPoliciesByIDs(ctx, aiPolicyIDs)
	if err != nil {
		return nil, apperr.Internal("could not load AI policy records").Wrap(err)
	}
	aiByID := make(map[uuid.UUID]models.AIPolicy, len(aiPolicies))
	for _, p := range aiPolicies {
		aiByID[p.ID] = p
	}

	for _, aiID := range aiPolicyIDs {
		aiIDStr := aiID.String()
		if p, ok := byAIID[aiID]; ok {
			status := p.Status
			rolledOut := p.RolledOutDate
			refs = append(refs, dto.PolicyRef{
				ID:            p.ID.String(),
				Name:          p.Name,
				Source:        "cis",
				AIPolicyID:    &aiIDStr,
				Status:        &status,
				RolledOutDate: &rolledOut,
				HasDocument:   p.FilePath != "",
			})
			continue
		}
		// A policy the AI service created directly, with no F2 record behind it.
		if p, ok := aiByID[aiID]; ok {
			refs = append(refs, dto.PolicyRef{
				ID:          p.ID.String(),
				Name:        p.Title,
				Source:      "ai",
				AIPolicyID:  &aiIDStr,
				HasDocument: false,
			})
		}
	}
	return refs, nil
}
