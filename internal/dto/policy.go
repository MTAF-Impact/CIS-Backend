package dto

import "time"

// PolicyCard is the F2 policy card (US37).
type PolicyCard struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// MonthYear is the pre-formatted "January 2026" label the card shows.
	MonthYear     string    `json:"month_year"`
	RolledOutDate time.Time `json:"rolled_out_date"`
	CreatedAt     time.Time `json:"created_at"`
	Status        string    `json:"status"` // rolled_out | not_rolled_out

	// Document metadata backing the card's download icon.
	FileName     string `json:"file_name"`
	FileMimeType string `json:"file_mime_type"`
	FileSize     int64  `json:"file_size_bytes"`
	DownloadURL  string `json:"download_url"`

	// AI matchmaking state driving the "Processing" badge (US42).
	ProcessingStatus string  `json:"processing_status"`
	ProcessingError  *string `json:"processing_error,omitempty"`
	IsProcessing     bool    `json:"is_processing"`

	LinkedClaimCount    int64      `json:"linked_claim_count"`
	LastClaimActivityAt *time.Time `json:"last_claim_activity_at"`
	AIPolicyID          *string    `json:"ai_policy_id"`
}

// PolicyDetail is the F2 policy detail page (US39).
//
// The two claim lists reuse the exact ClaimCard shape from F1, so the frontend
// can render them with the identical component per the PRD's design guideline.
type PolicyDetail struct {
	PolicyCard
	Description *string `json:"description"`

	ExistingClaims    []ClaimCard `json:"existing_claims"`
	NonExistingClaims []ClaimCard `json:"non_existing_claims"`
}

// CreatePolicyRequest is the multipart form behind the "Add Public Policy"
// modal (US40). The file itself arrives as the `file` part.
type CreatePolicyRequest struct {
	Name string `form:"name" validate:"required,min=2,max=500"`
	// RolledOutDate is a YYYY-MM-DD date from the modal's date picker.
	RolledOutDate string  `form:"rolled_out_date" validate:"required,datetime=2006-01-02"`
	Description   *string `form:"description" validate:"omitempty,max=5000"`
}

// UpdatePolicyRequest is the body of PATCH /api/v1/policies/:id.
type UpdatePolicyRequest struct {
	Name          *string `json:"name" validate:"omitempty,min=2,max=500"`
	RolledOutDate *string `json:"rolled_out_date" validate:"omitempty,datetime=2006-01-02"`
	Description   *string `json:"description" validate:"omitempty,max=5000"`
	// Status is the rollout status (US41). Editing the date alone never moves
	// it: the transition is a human decision, so it has to be stated.
	Status *string `json:"status" validate:"omitempty,oneof=rolled_out not_rolled_out"`
}

// UpdatePolicyStatusRequest is the body of PUT /api/v1/policies/:id/status.
type UpdatePolicyStatusRequest struct {
	Status string `json:"status" validate:"required,oneof=rolled_out not_rolled_out"`
}

// PolicyProcessingStatus is the lightweight payload the F2 card polls while the
// matchmaking job runs (US42).
type PolicyProcessingStatus struct {
	PolicyID         string     `json:"policy_id"`
	ProcessingStatus string     `json:"processing_status"`
	IsProcessing     bool       `json:"is_processing"`
	Attempts         int        `json:"attempts"`
	ProcessedAt      *time.Time `json:"processed_at"`
	ProcessingError  *string    `json:"processing_error,omitempty"`
	AIPolicyID       *string    `json:"ai_policy_id"`
	LinkedClaimCount int64      `json:"linked_claim_count"`
}

// PolicyYearsResponse backs the US34 year filter chips.
type PolicyYearsResponse struct {
	Years []int `json:"years"`
}

// MatchmakingResultRequest is the body the AI service posts back to
// POST /api/v1/internal/policies/:id/matchmaking-result once US42's pipeline
// finishes.
//
// The AI service writes the claims, topics, and link rows into its own tables
// itself; this callback only tells the backend which policy id it used and
// whether the job succeeded, so the "Processing" badge can clear.
type MatchmakingResultRequest struct {
	AIPolicyID          *string `json:"ai_policy_id" validate:"omitempty,uuid"`
	Status              string  `json:"status" validate:"required,oneof=completed failed processing"`
	MatchedClaimCount   int     `json:"matched_claim_count" validate:"omitempty,gte=0"`
	GeneratedClaimCount int     `json:"generated_claim_count" validate:"omitempty,gte=0"`
	Error               *string `json:"error" validate:"omitempty,max=2000"`
}

// DownloadResponse describes where a policy document can be fetched (US37).
type DownloadResponse struct {
	FileName    string     `json:"file_name"`
	MimeType    string     `json:"mime_type"`
	SizeBytes   int64      `json:"size_bytes"`
	URL         string     `json:"url"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	IsSignedURL bool       `json:"is_signed_url"`
}
