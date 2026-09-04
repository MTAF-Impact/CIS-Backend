package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/repository"
)

// AllowlistService owns the declared-coordination allowlist and the
// common-phrase exclusion list.
//
// # Why this is the most important service in the detector
//
// NGOs, newsrooms, unions and grassroots campaigns coordinate openly and by
// design. A climate campaign posting a shared message at a shared time is
// doing exactly what campaigns do. Without this control the detector
// systematically flags civil society — which, for a tool operated by a
// government, is the platform's most serious failure mode.
//
// It is also why the list should be seeded during onboarding with the city's
// known civil-society partners, before the first detection run rather than
// after the first false positive. Building it late means the first thing the
// tool does in production is accuse an NGO.
type AllowlistService struct {
	allowlist *repository.AllowlistRepository
	networks  *repository.NetworkRepository
	reports   *repository.ReportRepository
}

// NewAllowlistService constructs an AllowlistService.
func NewAllowlistService(
	allowlist *repository.AllowlistRepository,
	networks *repository.NetworkRepository,
	reports *repository.ReportRepository,
) *AllowlistService {
	return &AllowlistService{allowlist: allowlist, networks: networks, reports: reports}
}

// ListQuery is the normalized input for the allowlist management screen.
type ListAllowlistQuery struct {
	Search         string
	Platform       string
	Category       string
	IncludeRemoved bool
	Page           int
	Limit          int
}

// List returns a page of allowlist entries.
func (s *AllowlistService) List(
	ctx context.Context, q ListAllowlistQuery,
) ([]dto.AllowlistEntry, int64, dto.PageParams, error) {
	page := dto.NormalizePage(q.Page, q.Limit)

	if q.Category != "" && !models.IsValidAllowlistCategory(q.Category) {
		return nil, 0, page, apperr.BadRequest(
			"category must be one of: %s", strings.Join(models.ValidAllowlistCategories, ", "))
	}

	rows, total, err := s.allowlist.List(ctx, repository.AllowlistFilter{
		Search:         q.Search,
		Platform:       q.Platform,
		Category:       q.Category,
		IncludeRemoved: q.IncludeRemoved,
		Limit:          page.Limit,
		Offset:         page.Offset(),
	})
	if err != nil {
		return nil, 0, page, apperr.Internal("could not load the allowlist").Wrap(err)
	}

	out := make([]dto.AllowlistEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAllowlistEntry(r))
	}
	return out, total, page, nil
}

// Categories returns the active count per category, so the management screen
// can show at a glance whether the list was ever seeded.
func (s *AllowlistService) Categories(ctx context.Context) (map[string]int64, error) {
	counts, err := s.allowlist.CountByCategory(ctx)
	if err != nil {
		return nil, apperr.Internal("could not count allowlist entries").Wrap(err)
	}
	return counts, nil
}

// SelfExclusionCount is how many accounts are excluded as the city's own
// comms estate.
func (s *AllowlistService) SelfExclusionCount(ctx context.Context) int64 {
	counts, err := s.allowlist.CountByCategory(ctx)
	if err != nil {
		return 0
	}
	return counts[models.AllowlistCategorySelfExclusion]
}

// Create adds one account manually from the admin settings screen.
func (s *AllowlistService) Create(
	ctx context.Context, req dto.CreateAllowlistEntryRequest, addedBy *uuid.UUID,
) (*dto.AllowlistActionResult, error) {
	if !models.IsValidAllowlistCategory(req.Category) {
		return nil, apperr.Unprocessable(
			"category must be one of: %s", strings.Join(models.ValidAllowlistCategories, ", "))
	}

	entry := repository.AllowlistEntryInput{
		Platform:          strings.TrimSpace(req.Platform),
		PlatformAccountID: strings.TrimSpace(req.PlatformAccountID),
		Handle:            strings.TrimSpace(req.Handle),
		Category:          req.Category,
		Reason:            strings.TrimSpace(req.Reason),
		AddedBy:           addedBy,
	}

	added, err := s.allowlist.Add(ctx, []repository.AllowlistEntryInput{entry})
	if err != nil {
		return nil, apperr.Internal("could not add the allowlist entry").Wrap(err)
	}

	return s.summarise(ctx, added, []repository.PlatformAccountKey{{
		Platform:          entry.Platform,
		PlatformAccountID: entry.PlatformAccountID,
		Handle:            entry.Handle,
	}})
}

// AllowlistAccount marks a single account as legitimate coordination.
func (s *AllowlistService) AllowlistAccount(
	ctx context.Context, accountID uuid.UUID, req dto.AddAllowlistRequest, addedBy *uuid.UUID,
) (*dto.AllowlistActionResult, error) {
	key, err := s.networks.FindAccountKey(ctx, accountID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("account not found")
		}
		return nil, translatePipelineErr(err, "could not resolve the account")
	}
	return s.allowlistKeys(ctx, []repository.PlatformAccountKey{*key}, req, addedBy)
}

// AllowlistNetwork marks every member of a network as legitimate coordination.
//
// This is the action taken when an analyst recognises that what the detector
// found is a real campaign the city knows about. It protects the whole set at
// once, because protecting them one at a time would leave the network partially
// flagged in the meantime.
func (s *AllowlistService) AllowlistNetwork(
	ctx context.Context, networkID uuid.UUID, req dto.AddAllowlistRequest, addedBy *uuid.UUID,
) (*dto.AllowlistActionResult, error) {
	keys, err := s.networks.ListMemberKeys(ctx, networkID)
	if err != nil {
		return nil, translatePipelineErr(err, "could not load the network's members")
	}
	if len(keys) == 0 {
		return nil, apperr.NotFound("that network has no member accounts to allowlist")
	}
	return s.allowlistKeys(ctx, keys, req, addedBy)
}

func (s *AllowlistService) allowlistKeys(
	ctx context.Context, keys []repository.PlatformAccountKey, req dto.AddAllowlistRequest, addedBy *uuid.UUID,
) (*dto.AllowlistActionResult, error) {
	if !models.IsValidAllowlistCategory(req.Category) {
		return nil, apperr.Unprocessable(
			"category must be one of: %s", strings.Join(models.ValidAllowlistCategories, ", "))
	}

	entries := make([]repository.AllowlistEntryInput, 0, len(keys))
	for _, k := range keys {
		entries = append(entries, repository.AllowlistEntryInput{
			Platform:          k.Platform,
			PlatformAccountID: k.PlatformAccountID,
			Handle:            k.Handle,
			Category:          req.Category,
			Reason:            strings.TrimSpace(req.Reason),
			AddedBy:           addedBy,
		})
	}

	added, err := s.allowlist.Add(ctx, entries)
	if err != nil {
		return nil, apperr.Internal("could not update the allowlist").Wrap(err)
	}
	return s.summarise(ctx, added, keys)
}

// summarise reports what an allowlist addition actually reached.
//
// # The part that cannot be undone
//
// Allowlisted accounts are excluded from all future candidate sets and their
// historical networks are suppressed and relabelled. Suppression and
// relabelling are the pipeline's to apply — those columns are on AI-owned
// tables — so what this backend can do is name the blast radius: which networks
// contained these accounts, and which of those a report was already generated
// from.
//
// That last set is the uncomfortable one. A PDF citing accounts since
// allowlisted is already in someone's inbox and cannot be recalled. Surfacing it
// is the most the system can do, and saying nothing would leave the team
// unaware that a referral they sent names an organisation they have since
// declared legitimate.
func (s *AllowlistService) summarise(
	ctx context.Context, added int, keys []repository.PlatformAccountKey,
) (*dto.AllowlistActionResult, error) {
	result := &dto.AllowlistActionResult{AccountsAdded: added, Handles: make([]string, 0, len(keys))}
	for _, k := range keys {
		result.Handles = append(result.Handles, k.Handle)
	}

	affected := map[uuid.UUID]struct{}{}
	for _, k := range keys {
		ids, err := s.networks.NetworkIDsForAccount(ctx, k.Platform, k.PlatformAccountID)
		if err != nil {
			// The allowlist entry is already written and is the part that
			// matters; failing the whole call because the impact summary could
			// not be computed would leave the caller unsure whether the
			// protection took effect.
			if errors.Is(err, repository.ErrPipelineUnavailable) {
				result.Note = "The detection tables are not provisioned, so historical impact could not be assessed. " +
					"The allowlist entry is stored and will apply to every future run."
				return result, nil
			}
			return nil, apperr.Internal("could not assess the allowlist change's impact").Wrap(err)
		}
		for _, id := range ids {
			affected[id] = struct{}{}
		}
	}

	ids := make([]uuid.UUID, 0, len(affected))
	for id := range affected {
		ids = append(ids, id)
	}
	result.NetworksAffected = len(ids)

	reported, err := s.networks.ReportedNetworkIDs(ctx, ids)
	if err != nil {
		return nil, apperr.Internal("could not check for existing exports").Wrap(err)
	}
	for _, id := range reported {
		result.ExportedReportsAffected = append(result.ExportedReportsAffected, id.String())
	}

	if len(reported) > 0 {
		result.Note = fmt.Sprintf(
			"%d of the affected networks have already had a report generated from them. "+
				"Those documents name accounts now declared legitimate coordination and cannot be recalled — "+
				"review them in the export audit log and notify any recipient.", len(reported))
	}
	return result, nil
}

// Update edits an active entry.
func (s *AllowlistService) Update(
	ctx context.Context, id uuid.UUID, req dto.UpdateAllowlistEntryRequest, updatedBy *uuid.UUID,
) (*dto.AllowlistEntry, error) {
	category := ""
	if req.Category != nil {
		if !models.IsValidAllowlistCategory(*req.Category) {
			return nil, apperr.Unprocessable(
				"category must be one of: %s", strings.Join(models.ValidAllowlistCategories, ", "))
		}
		category = *req.Category
	}
	reason := ""
	if req.Reason != nil {
		reason = strings.TrimSpace(*req.Reason)
	}
	if category == "" && reason == "" {
		return nil, apperr.BadRequest("provide a category or a reason to update")
	}

	if err := s.allowlist.Update(ctx, id, category, reason, updatedBy); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("no active allowlist entry with that id")
		}
		return nil, apperr.Internal("could not update the allowlist entry").Wrap(err)
	}

	row, err := s.allowlist.FindByID(ctx, id)
	if err != nil {
		return nil, apperr.Internal("could not reload the allowlist entry").Wrap(err)
	}
	entry := toAllowlistEntry(*row)
	return &entry, nil
}

// Remove withdraws an account's protection.
//
// Soft delete with a mandatory reason. Removing an organisation's protection is
// the action that lets the detector flag it again, so it is the change most
// worth being able to attribute later.
func (s *AllowlistService) Remove(
	ctx context.Context, id uuid.UUID, req dto.RemoveAllowlistEntryRequest, removedBy *uuid.UUID,
) (*dto.AllowlistEntry, error) {
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return nil, apperr.Unprocessable("a reason is required to remove an allowlist entry")
	}

	if err := s.allowlist.Remove(ctx, id, reason, removedBy); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("no active allowlist entry with that id")
		}
		return nil, apperr.Internal("could not remove the allowlist entry").Wrap(err)
	}

	row, err := s.allowlist.FindByID(ctx, id)
	if err != nil {
		return nil, apperr.Internal("could not reload the allowlist entry").Wrap(err)
	}
	entry := toAllowlistEntry(*row)
	return &entry, nil
}

func toAllowlistEntry(r models.CISCoordinationAllowlist) dto.AllowlistEntry {
	entry := dto.AllowlistEntry{
		ID:                r.ID.String(),
		Platform:          r.Platform,
		PlatformAccountID: r.PlatformAccountID,
		Handle:            r.Handle,
		Category:          r.Category,
		Reason:            r.Reason,
		AddedAt:           r.AddedAt,
		RemovedAt:         r.RemovedAt,
		RemovalReason:     r.RemovalReason,
		Active:            r.IsActive(),
	}
	if r.AddedBy != nil {
		id := r.AddedBy.String()
		entry.AddedBy = &id
	}
	if r.RemovedBy != nil {
		id := r.RemovedBy.String()
		entry.RemovedBy = &id
	}
	return entry
}

// --- Common-phrase allowlist ---

// ListPhrases returns a page of the text exclusion list.
func (s *AllowlistService) ListPhrases(
	ctx context.Context, search string, page, limit int,
) ([]dto.CommonPhrase, int64, dto.PageParams, error) {
	window := dto.NormalizePage(page, limit)

	rows, total, err := s.allowlist.ListPhrases(ctx, search, window.Limit, window.Offset())
	if err != nil {
		return nil, 0, window, apperr.Internal("could not load the common-phrase list").Wrap(err)
	}

	out := make([]dto.CommonPhrase, 0, len(rows))
	for _, r := range rows {
		out = append(out, dto.CommonPhrase{
			ID:        r.ID.String(),
			Phrase:    r.Phrase,
			Category:  r.Category,
			Notes:     r.Notes,
			CreatedAt: r.CreatedAt,
		})
	}
	return out, total, window, nil
}

// AddPhrase adds text the duplication signal must ignore.
//
// Without this list, residents quoting the same government announcement
// register as content duplication — the textbook false positive the whole
// feature exists to avoid.
func (s *AllowlistService) AddPhrase(
	ctx context.Context, req dto.CreateCommonPhraseRequest, addedBy *uuid.UUID,
) (*dto.CommonPhrase, error) {
	phrase := strings.TrimSpace(req.Phrase)
	if models.NormalizePhrase(phrase) == "" {
		return nil, apperr.Unprocessable("phrase cannot be empty once normalised")
	}

	row, err := s.allowlist.AddPhrase(ctx, phrase, req.Category, req.Notes, addedBy)
	if err != nil {
		return nil, apperr.Internal("could not add the common phrase").Wrap(err)
	}
	return &dto.CommonPhrase{
		ID:        row.ID.String(),
		Phrase:    row.Phrase,
		Category:  row.Category,
		Notes:     row.Notes,
		CreatedAt: row.CreatedAt,
	}, nil
}

// DeletePhrase removes a common phrase.
func (s *AllowlistService) DeletePhrase(ctx context.Context, id uuid.UUID) error {
	if err := s.allowlist.DeletePhrase(ctx, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperr.NotFound("common phrase not found")
		}
		return apperr.Internal("could not delete the common phrase").Wrap(err)
	}
	return nil
}

// ExclusionsForPipeline is the payload the detection pipeline reads before
// candidate selection: every protected account identity and every excluded
// phrase.
//
// Exposed whole rather than paged because the pipeline needs the complete list
// to apply it, and applying half an exclusion list is worse than applying none —
// it produces a detection that looks complete and is not.
type ExclusionsForPipeline struct {
	Accounts []repository.PlatformAccountKey `json:"accounts"`
	Phrases  []string                        `json:"phrases"`
}

// Exclusions returns the lists the pipeline consumes.
func (s *AllowlistService) Exclusions(ctx context.Context) (*ExclusionsForPipeline, error) {
	accounts, err := s.allowlist.ActiveKeys(ctx)
	if err != nil {
		return nil, apperr.Internal("could not load allowlisted accounts").Wrap(err)
	}
	phrases, err := s.allowlist.AllActivePhrases(ctx)
	if err != nil {
		return nil, apperr.Internal("could not load common phrases").Wrap(err)
	}
	if accounts == nil {
		accounts = []repository.PlatformAccountKey{}
	}
	if phrases == nil {
		phrases = []string{}
	}
	return &ExclusionsForPipeline{Accounts: accounts, Phrases: phrases}, nil
}
