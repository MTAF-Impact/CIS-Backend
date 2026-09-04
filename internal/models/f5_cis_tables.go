package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Coordinated-Network Detector: the tables recording human decisions.
//
// Everything the *pipeline* produces lives in f5_ai_tables.go and is read-only.
// Everything a *person* does lives here, is `cis_` prefixed, and is managed by
// AutoMigrate. The split follows one question — is this produced by the
// pipeline, or by a human? — and keeps the startup ownership guard in
// database/migrate.go intact.
//
// Three of these tables have no counterpart in the AI-owned schema at all
// (cis_network_reviews, cis_detector_settings, cis_setting_history); two more
// carry a column the AI-owned schema does not declare. Each is annotated
// where it appears.

// Network review statuses.
//
// DELIBERATELY NOT the four-value claim status set in ValidReviewStatuses. A
// network assessment is an evidentiary judgment about a set of real accounts,
// so "we assessed this and concluded it was organic" (Dismissed — False
// Positive) has to be recorded distinctly from "we stopped tracking it".
// Collapsing them would destroy the signal that trains the allowlist and
// would leave the same false positive re-triageable forever.
//
// Reusing IsValidReviewStatus here would silently accept `active`/`inactive`
// and reject `under_review`, so networks get their own validator.
const (
	NetworkStatusUnreviewed  = "unreviewed"
	NetworkStatusUnderReview = "under_review"
	NetworkStatusConfirmed   = "confirmed"
	NetworkStatusDismissedFP = "dismissed_false_positive"
	NetworkStatusActionTaken = "action_taken"
)

// ValidNetworkReviewStatuses lists every accepted network review status.
var ValidNetworkReviewStatuses = []string{
	NetworkStatusUnreviewed,
	NetworkStatusUnderReview,
	NetworkStatusConfirmed,
	NetworkStatusDismissedFP,
	NetworkStatusActionTaken,
}

// IsValidNetworkReviewStatus reports whether s is an accepted network status.
func IsValidNetworkReviewStatus(s string) bool {
	for _, v := range ValidNetworkReviewStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// ReportableNetworkStatuses is the export gate: reports may only be generated
// for networks at Medium or High confidence whose review status is Under
// Review, Confirmed, or Action Taken. Written as an ALLOWLIST.
//
// The shape matters. The obvious denylist — `status != unreviewed` — permits
// exporting a network the team has already examined and concluded was organic,
// which is a government submitting a platform referral about residents it
// itself determined were not coordinating — the single largest harm the
// platform can cause. An allowlist also fails closed if a status is added
// later.
var ReportableNetworkStatuses = []string{
	NetworkStatusUnderReview,
	NetworkStatusConfirmed,
	NetworkStatusActionTaken,
}

// IsReportableNetworkStatus reports whether a network in this review status may
// be exported at all.
func IsReportableNetworkStatus(s string) bool {
	for _, v := range ReportableNetworkStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// NetworkStatusReasonMinLength is the mandatory free-text reason floor.
// Unlike claim review notes, which are optional, a network status change
// without a stated reason is not recordable.
const NetworkStatusReasonMinLength = 20

// Declared-coordination allowlist categories.
//
// Self-exclusion — the city's own communications estate, which posts the same
// message at the same time by design and would otherwise reliably self-flag —
// is modelled as a category here rather than as a second list.
// It is the same operation (exclude these accounts from every candidate set)
// with a different justification, and splitting it would give the pipeline two
// lists to read instead of one.
const (
	AllowlistCategoryNGO           = "ngo"
	AllowlistCategoryNewsroom      = "newsroom"
	AllowlistCategoryCampaignGroup = "campaign_group"
	AllowlistCategoryGovernment    = "government"
	AllowlistCategoryUnion         = "union"
	AllowlistCategoryOther         = "other"
	// AllowlistCategorySelfExclusion is the city's own comms estate.
	AllowlistCategorySelfExclusion = "self_exclusion"
)

// ValidAllowlistCategories lists every accepted allowlist category.
var ValidAllowlistCategories = []string{
	AllowlistCategoryNGO,
	AllowlistCategoryNewsroom,
	AllowlistCategoryCampaignGroup,
	AllowlistCategoryGovernment,
	AllowlistCategoryUnion,
	AllowlistCategoryOther,
	AllowlistCategorySelfExclusion,
}

// IsValidAllowlistCategory reports whether s is an accepted category.
func IsValidAllowlistCategory(s string) bool {
	for _, v := range ValidAllowlistCategories {
		if v == s {
			return true
		}
	}
	return false
}

// Report types.
const (
	// ReportTypePlatformReferral carries behavioural sections and the account
	// annex, with no internal commentary. The default.
	ReportTypePlatformReferral = "platform_referral"
	// ReportTypeInternalBriefing adds analyst notes, review history, and the
	// linked claim/policy context.
	ReportTypeInternalBriefing = "internal_briefing"
)

// IsValidReportType reports whether s is an accepted report type.
func IsValidReportType(s string) bool {
	return s == ReportTypePlatformReferral || s == ReportTypeInternalBriefing
}

// Export types recorded in the audit log.
const (
	ExportTypeReport         = "report"
	ExportTypeEvidenceBundle = "evidence_bundle"
	ExportTypeAccountsCSV    = "accounts_csv"
)

// Audit-log object types.
const (
	AuditObjectNetwork = "network"
	AuditObjectReport  = "report"
)

// Coordinated-Network Detector settings keys stored in cis_settings alongside
// the other feature areas'.
const (
	// SettingCityTimezone is the IANA zone used for the city-local half of
	// every report footer timestamp.
	//
	// Every page needs both UTC and city-local time, but nothing else in the
	// system knows which city: cis_settings has no locale key and the
	// scheduler is pinned to UTC. Without this the report cannot render its
	// own footer.
	SettingCityTimezone = "city_timezone"
)

// DefaultCityTimezone is the Jakarta prototype context.
const DefaultCityTimezone = "Asia/Jakarta"

// CISNetworkReview is the current review status for one network.
//
// An OVERLAY, exactly like CISClaimReview and for the same reason: the
// AI-owned schema puts review_status as a column on coordinated_network, but
// the backend must never write an AI-owned table, and a pipeline re-run that
// rewrote that row would silently erase an analyst's judgement. Reads resolve
// a network's status as COALESCE(review.status, 'unreviewed').
//
// This holds only the CURRENT status. The history is CISNetworkReviewLog, which
// is append-only — a different shape from cis_claim_reviews, which keeps no
// history at all.
type CISNetworkReview struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	NetworkID uuid.UUID `gorm:"column:network_id;type:uuid;not null;uniqueIndex:idx_cis_network_reviews_network"`
	Status    string    `gorm:"column:status;type:varchar(32);not null;index:idx_cis_network_reviews_status"`

	// Reason is the most recent status change's justification. The full
	// sequence lives in the log; this is denormalised so the detail page does
	// not have to read the log to render its header.
	Reason     string     `gorm:"column:reason;type:text;not null"`
	ReviewedBy *uuid.UUID `gorm:"column:reviewed_by;type:uuid"`
	ReviewedAt time.Time  `gorm:"column:reviewed_at;not null"`
	CreatedAt  time.Time  `gorm:"column:created_at;not null"`
	UpdatedAt  time.Time  `gorm:"column:updated_at;not null"`
}

// TableName pins the backend-owned table name.
func (CISNetworkReview) TableName() string { return "cis_network_reviews" }

// BeforeCreate assigns a UUID.
func (r *CISNetworkReview) BeforeCreate(*gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// CISNetworkReviewLog is the append-only history of network status changes.
//
// Append-only is the point. cis_claim_reviews holds one row per claim and is
// overwritten on each change; copying that pattern here would be a bug, because
// dismissal rates and dismissal signal profiles need to be reviewable in
// aggregate, which requires every decision to be retained.
type CISNetworkReviewLog struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	NetworkID uuid.UUID `gorm:"column:network_id;type:uuid;not null;index:idx_cis_network_review_log_network"`

	FromStatus string `gorm:"column:from_status;type:varchar(32);not null"`
	ToStatus   string `gorm:"column:to_status;type:varchar(32);not null;index:idx_cis_network_review_log_to_status"`
	// Reason is mandatory and at least NetworkStatusReasonMinLength characters.
	// Enforced in the service layer, which is where the message can be useful.
	Reason string `gorm:"column:reason;type:text;not null"`

	// SignalProfile is a COPY of the network's scores at the moment of the
	// decision: SY, DU, CO, PR, AU, SignalBreadth, CoordinationScore,
	// confidence band, and the signals unavailable that run.
	//
	// Every false-positive dismissal needs to be logged with its reason and its
	// full signal profile, so the profile is captured here rather than resolved
	// by joining to coordinated_network at read time: a later detection run can
	// recompute those scores, or recurrence can move the network, and an
	// aggregate analysis of dismissals is meaningless if the profile drifts
	// after the dismissal. So it is snapshotted at write time — the same
	// reasoning that governs detection_run.parameters_json.
	//
	// It must ship with the first version of this table: adding the column
	// later leaves every dismissal recorded before that point permanently
	// unanalysable, and those are exactly the rows the precision target depends
	// on.
	SignalProfile JSONB `gorm:"column:signal_profile_json"`

	UserID    *uuid.UUID `gorm:"column:user_id;type:uuid"`
	CreatedAt time.Time  `gorm:"column:created_at;not null;index:idx_cis_network_review_log_created_at"`
}

// TableName pins the backend-owned table name.
func (CISNetworkReviewLog) TableName() string { return "cis_network_review_log" }

// BeforeCreate assigns a UUID.
func (l *CISNetworkReviewLog) BeforeCreate(*gorm.DB) error {
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	return nil
}

// CISCoordinationAllowlist is the declared-coordination allowlist.
//
// NGOs, newsrooms, unions and grassroots campaigns coordinate openly and by
// design. A climate campaign posting a shared message at a shared time is
// doing exactly what campaigns do. Without this control the detector
// systematically flags civil society, which for a government-operated tool is
// the platform's most serious failure mode.
//
// The backend writes this table and the AI pipeline READS it before candidate
// selection — the one place the read direction between the two services
// reverses.
//
// Removal is a SOFT delete. A hard delete would erase the record of who
// protected an organisation and why, and every removal must carry a reason
// that stays logged.
type CISCoordinationAllowlist struct {
	ID       uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	Platform string    `gorm:"column:platform;type:varchar(64);not null;uniqueIndex:idx_cis_allowlist_identity,priority:1"`
	// PlatformAccountID is the durable identity. Handles get renamed; the
	// platform-issued id does not, so protection keyed on the handle alone
	// would lapse the moment an NGO rebranded.
	PlatformAccountID string `gorm:"column:platform_account_id;type:varchar(255);not null;uniqueIndex:idx_cis_allowlist_identity,priority:2"`
	Handle            string `gorm:"column:handle;type:varchar(255);not null;index:idx_cis_allowlist_handle"`

	Category string `gorm:"column:category;type:varchar(32);not null;index:idx_cis_allowlist_category"`
	Reason   string `gorm:"column:reason;type:text;not null"`

	AddedBy *uuid.UUID `gorm:"column:added_by;type:uuid"`
	AddedAt time.Time  `gorm:"column:added_at;not null"`

	// RemovalReason is a separate column from Reason, which holds the
	// *addition* reason. A removal needs its own stated reason too, and
	// overwriting the addition reason with it would destroy the record of why
	// the entry existed.
	RemovalReason *string    `gorm:"column:removal_reason;type:text"`
	RemovedBy     *uuid.UUID `gorm:"column:removed_by;type:uuid"`
	RemovedAt     *time.Time `gorm:"column:removed_at;index:idx_cis_allowlist_removed_at"`

	CreatedAt time.Time `gorm:"column:created_at;not null"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null"`
}

// TableName pins the backend-owned table name.
func (CISCoordinationAllowlist) TableName() string { return "cis_coordination_allowlist" }

// BeforeCreate assigns a UUID.
func (a *CISCoordinationAllowlist) BeforeCreate(*gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return nil
}

// IsActive reports whether the entry currently protects its account.
func (a CISCoordinationAllowlist) IsActive() bool { return a.RemovedAt == nil }

// CISCommonPhrase is the common-phrase allowlist read by the w_text signal.
//
// A separate list from the account allowlist: that one holds accounts, this
// holds TEXT — official slogans, hashtags, standard policy names, quoted
// press-release lines. Without it, residents quoting the same government
// announcement register as content duplication, which is the textbook false
// positive the whole feature is built to avoid.
//
// Backend-owned and pipeline-read, same direction as the account allowlist.
type CISCommonPhrase struct {
	ID uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	// Phrase is stored as typed. NormalizedPhrase is what the pipeline matches
	// on, using the same normalisation the pipeline applies (lowercased, URLs
	// and mentions replaced, emoji and repeated punctuation stripped,
	// whitespace collapsed).
	Phrase           string `gorm:"column:phrase;type:text;not null"`
	NormalizedPhrase string `gorm:"column:normalized_phrase;type:text;not null;uniqueIndex:idx_cis_common_phrases_normalized"`

	// Category is free-form guidance for the humans maintaining the list
	// (slogan, hashtag, policy_name, press_release).
	Category string  `gorm:"column:category;type:varchar(32);not null"`
	Notes    *string `gorm:"column:notes;type:text"`

	AddedBy   *uuid.UUID `gorm:"column:added_by;type:uuid"`
	CreatedAt time.Time  `gorm:"column:created_at;not null"`
	UpdatedAt time.Time  `gorm:"column:updated_at;not null"`
}

// TableName pins the backend-owned table name.
func (CISCommonPhrase) TableName() string { return "cis_common_phrases" }

// BeforeCreate assigns a UUID and derives the normalised form.
func (p *CISCommonPhrase) BeforeCreate(*gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	if p.NormalizedPhrase == "" {
		p.NormalizedPhrase = NormalizePhrase(p.Phrase)
	}
	return nil
}

// NormalizePhrase lowercases and collapses whitespace, the portion of the
// pipeline's normalisation that is well defined without its tokenizer. The
// pipeline applies the rest; matching on this form is a superset, which is
// the safe direction for an exclusion list.
func NormalizePhrase(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// CISNetworkReport is one generated PDF report.
//
// Versioning is implicit: multiple rows per network, never an overwrite, so an
// earlier report stays downloadable exactly as it was submitted. RunID records
// which detection the report was built from, which is what lets a report
// generated months later state the configuration that produced it.
type CISNetworkReport struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	NetworkID uuid.UUID `gorm:"column:network_id;type:uuid;not null;index:idx_cis_network_reports_network"`
	RunID     uuid.UUID `gorm:"column:run_id;type:uuid;not null"`

	// SnapshotID and SnapshotSHA256 are copied in at generation time for the
	// chain-of-custody section. Copied, not joined: the point of a chain of
	// custody is that it records what was true when the document was issued.
	SnapshotID     *uuid.UUID `gorm:"column:snapshot_id;type:uuid"`
	SnapshotSHA256 *string    `gorm:"column:snapshot_sha256;type:varchar(64)"`

	ReportType string `gorm:"column:report_type;type:varchar(32);not null"`
	// Sections records which optional sections were included, so a regeneration
	// with the same settings can be byte-identical.
	Sections       JSONB `gorm:"column:sections_json"`
	RedactionFlags JSONB `gorm:"column:redaction_flags"`

	FileName   string `gorm:"column:file_name;type:varchar(500);not null"`
	FilePath   string `gorm:"column:file_path;type:varchar(1000);not null"`
	FileSHA256 string `gorm:"column:file_sha256;type:varchar(64);not null"`
	FileSize   int64  `gorm:"column:file_size_bytes;not null"`

	// AuditID is the export audit entry this report was written into. It is
	// allocated BEFORE rendering, because the PDF prints it inside itself:
	// ordinary "log the export after it succeeds" wiring produces a report
	// with an empty chain-of-custody slot.
	AuditID *uuid.UUID `gorm:"column:audit_id;type:uuid"`

	GeneratedBy *uuid.UUID `gorm:"column:generated_by;type:uuid"`
	GeneratedAt time.Time  `gorm:"column:generated_at;not null;index:idx_cis_network_reports_generated_at"`
}

// TableName pins the backend-owned table name.
func (CISNetworkReport) TableName() string { return "cis_network_reports" }

// BeforeCreate assigns a UUID.
func (r *CISNetworkReport) BeforeCreate(*gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// CISExportAuditLog records every export.
//
// An export needs to be traceable by the report/bundle id, the NETWORK id, and
// the DETECTION RUN id together, not just a single generic object reference, so
// NetworkID and RunID are first-class columns here. Without them "which
// exported reports contain a now-allowlisted account?" is not answerable, and
// that is precisely the question a retroactive allowlist change raises.
type CISExportAuditLog struct {
	ID uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`

	ObjectType string    `gorm:"column:object_type;type:varchar(32);not null"`
	ObjectID   uuid.UUID `gorm:"column:object_id;type:uuid;not null"`

	NetworkID uuid.UUID  `gorm:"column:network_id;type:uuid;not null;index:idx_cis_export_audit_network"`
	RunID     *uuid.UUID `gorm:"column:run_id;type:uuid"`

	ExportType string `gorm:"column:export_type;type:varchar(32);not null;index:idx_cis_export_audit_type"`
	// Settings records the sections included and the redaction flags in force,
	// so a later reader can know what the recipient actually saw.
	Settings JSONB `gorm:"column:settings_json"`

	UserID    *uuid.UUID `gorm:"column:user_id;type:uuid;index:idx_cis_export_audit_user"`
	CreatedAt time.Time  `gorm:"column:created_at;not null;index:idx_cis_export_audit_created_at"`
}

// TableName pins the backend-owned table name.
func (CISExportAuditLog) TableName() string { return "cis_export_audit_log" }

// BeforeCreate assigns a UUID.
func (l *CISExportAuditLog) BeforeCreate(*gorm.DB) error {
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	return nil
}

// CISSettingHistory is the versioned change log for every Admin Settings
// value.
//
// Every change must be versioned with the user and timestamp who made it, and
// cis_settings stores only the current value plus updated_by/updated_at — there
// is no history anywhere else in the schema.
//
// This covers the whole Admin Settings surface, not only the detector
// parameters: the alert threshold gets the same treatment for free.
type CISSettingHistory struct {
	ID  uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	Key string    `gorm:"column:key;type:varchar(128);not null;index:idx_cis_setting_history_key"`

	FromValue *string `gorm:"column:from_value;type:text"`
	ToValue   string  `gorm:"column:to_value;type:text;not null"`

	ChangedBy *uuid.UUID `gorm:"column:changed_by;type:uuid"`
	CreatedAt time.Time  `gorm:"column:created_at;not null;index:idx_cis_setting_history_created_at"`
}

// TableName pins the backend-owned table name.
func (CISSettingHistory) TableName() string { return "cis_setting_history" }

// BeforeCreate assigns a UUID.
func (h *CISSettingHistory) BeforeCreate(*gorm.DB) error {
	if h.ID == uuid.Nil {
		h.ID = uuid.New()
	}
	return nil
}
