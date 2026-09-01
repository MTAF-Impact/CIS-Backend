package database

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/cis/cis-backend/internal/config"
	"github.com/cis/cis-backend/internal/models"
)

// ownedModels is the complete, exhaustive list of tables this backend may
// create or alter.
//
// This database is shared with a separately-developed AI service. Adding any
// AI-owned model (models.AIClaim, models.AIContentItem, ...) to this slice
// would be destructive: those tables carry pgvector `embedding` columns that
// the Go structs deliberately do not model, so AutoMigrate would happily
// recreate them without the embeddings and break the AI pipeline.
//
// If you add a new table, it must be prefixed `cis_` and listed here.
var ownedModels = []any{
	&models.CISUser{},
	&models.CISRefreshToken{},
	&models.CISPolicy{},
	&models.CISClaimReview{},
	&models.CISClaimAlert{},
	&models.CISAlertAcknowledgement{},
	&models.CISClaimHarmEdit{},
	&models.CISClaimScoreSnapshot{},
	&models.CISSetting{},

	// F5 — Coordinated-Network Detector. Every one of these records a human
	// decision or a backend-generated artefact; the detector's own output is
	// AI-owned and appears in optionalAITables below, never here.
	&models.CISNetworkReview{},
	&models.CISNetworkReviewLog{},
	&models.CISCoordinationAllowlist{},
	&models.CISCommonPhrase{},
	&models.CISNetworkReport{},
	&models.CISExportAuditLog{},
	&models.CISDetectorSettings{},
	&models.CISSettingHistory{},
}

// requiredAITables are read by the backend but owned by the AI service. Their
// absence is reported as a warning at boot rather than a fatal error, so the
// API can still start (and F2/F4 still work) against a database where the AI
// pipeline has not run yet.
var requiredAITables = []string{
	"claims", "topics", "policies", "claim_policies", "content_items",
}

// optionalAITables are the F5 detection pipeline's output tables (PRD 10.10).
// They are AI-owned and read-only here, exactly like requiredAITables, but
// their absence is a different situation: F1-F4 work perfectly without them,
// and they only exist once the detection pipeline has been built and has run.
//
// So a missing one is reported at a lower volume than a missing claims table,
// and the F5 endpoints answer 503 rather than leaking a Postgres
// "relation does not exist" — see repository.ErrPipelineUnavailable.
var optionalAITables = []string{
	"detection_run", "coordinated_network", "network_account", "account",
	"network_edge", "network_evidence_post", "network_burst_bin",
	"network_claim_link", "offtopic_cluster", "evidence_snapshot",
}

// optionalF6Tables and optionalF6Columns are the v1.5 additions the AI service
// has to provision before F6 and the segmented Debunk Activity can be served
// (PRD v1.5, US12, US67). They degrade rather than fail: a missing debunk
// segment table falls back to the single cached draft, and missing sentiment
// data makes O1 report "Insufficient Data", which is the state PRD 6.6.3
// already requires for thin coverage. See docs/sql/02_f6_reference_schema.sql.
var optionalF6Tables = []string{"claim_debunk_segments"}

var optionalF6Columns = map[string][]string{
	"content_items": {"sentiment", "city"},
}

// Migrate creates and updates the backend-owned cis_* tables, then seeds
// defaults. It never touches an AI-owned table.
func Migrate(db *gorm.DB, cfg *config.Config) error {
	if !cfg.DB.AutoMigrate {
		log.Println("[migrate] DB_AUTO_MIGRATE=false, skipping schema migration")
	} else {
		if err := assertOwnedTablesOnly(); err != nil {
			return err
		}
		if err := db.AutoMigrate(ownedModels...); err != nil {
			return fmt.Errorf("automigrate cis_* tables: %w", err)
		}
		log.Printf("[migrate] migrated %d backend-owned tables (cis_*); AI-owned tables untouched", len(ownedModels))
	}

	warnMissingAITables(db)

	if err := seedSettings(db); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}
	if cfg.DB.AutoMigrate {
		if err := seedDetectorSettings(db); err != nil {
			return fmt.Errorf("seed detector settings: %w", err)
		}
	}
	if err := seedUser(db, cfg.Auth); err != nil {
		return fmt.Errorf("seed user: %w", err)
	}
	return nil
}

// assertOwnedTablesOnly is a compile-time-ish guard that fails loudly if a
// non-cis_ table ever finds its way into ownedModels.
func assertOwnedTablesOnly() error {
	for _, m := range ownedModels {
		namer, ok := m.(interface{ TableName() string })
		if !ok {
			return fmt.Errorf("model %T does not declare TableName()", m)
		}
		if name := namer.TableName(); !strings.HasPrefix(name, "cis_") {
			return fmt.Errorf(
				"refusing to migrate table %q: this database is shared with the AI service "+
					"and AutoMigrate may only manage cis_* tables", name)
		}
	}
	return nil
}

func warnMissingAITables(db *gorm.DB) {
	var missing []string
	for _, table := range requiredAITables {
		if !db.Migrator().HasTable(table) {
			missing = append(missing, table)
		}
	}
	if len(missing) > 0 {
		log.Printf(
			"[migrate] WARNING: AI-owned tables not found: %s. "+
				"Claim and topic endpoints will return empty results until the AI service provisions them. "+
				"For local development see docs/sql/00_ai_reference_schema.sql",
			strings.Join(missing, ", "))
	}

	var missingF5 []string
	for _, table := range optionalAITables {
		if !db.Migrator().HasTable(table) {
			missingF5 = append(missingF5, table)
		}
	}
	if len(missingF5) > 0 {
		log.Printf(
			"[migrate] F5 detection tables not present (%d of %d missing). "+
				"The Coordinated-Network Detector endpoints will answer 503 until the AI service "+
				"provisions them; F1-F4 are unaffected. See docs/sql/01_f5_reference_schema.sql",
			len(missingF5), len(optionalAITables))
	}

	var missingF6 []string
	for _, table := range optionalF6Tables {
		if !db.Migrator().HasTable(table) {
			missingF6 = append(missingF6, table)
		}
	}
	for table, columns := range optionalF6Columns {
		if !db.Migrator().HasTable(table) {
			continue
		}
		for _, column := range columns {
			if !db.Migrator().HasColumn(table, column) {
				missingF6 = append(missingF6, table+"."+column)
			}
		}
	}
	if len(missingF6) > 0 {
		log.Printf(
			"[migrate] PRD v1.5 AI-owned additions not present: %s. "+
				"Segmented Debunk Activity falls back to the single cached draft and the F6 "+
				"Climate Sentiment Index reports insufficient_data until they exist; every other "+
				"F6 section works without them. See docs/sql/02_f6_reference_schema.sql",
			strings.Join(missingF6, ", "))
	}
}

// seedSettings inserts the F4 defaults only when absent, so an operator's saved
// threshold survives every restart.
func seedSettings(db *gorm.DB) error {
	defaults := []models.CISSetting{
		{
			Key:         models.SettingAlertThreshold,
			Value:       strconv.FormatFloat(models.DefaultAlertThreshold, 'f', -1, 64),
			ValueType:   "number",
			Description: "Global FinalClaimScore threshold (0-100) deciding Over/Under Threshold on the Alert page (PRD US32).",
		},
		{
			Key:         models.SettingClaimsLastFetchedAt,
			Value:       time.Now().UTC().Format(time.RFC3339),
			ValueType:   "timestamp",
			Description: "Timestamp shown as 'last fetched' on the Existing Claim section (PRD US9/US33).",
		},
		{
			Key:       models.SettingMonitoredCity,
			Value:     models.DefaultMonitoredCity,
			ValueType: "string",
			Description: "The single Indonesian city this instance monitors (PRD v1.5, US65). " +
				"Scopes every city-level metric on the F6 Overview page.",
		},
		{
			Key:       models.SettingCityTimezone,
			Value:     models.DefaultCityTimezone,
			ValueType: "string",
			Description: "IANA timezone for the city-local half of every F5 report footer timestamp (PRD 10.8). " +
				"The PRD requires 'UTC and city-local time' on every page but never names the city.",
		},
	}

	for i := range defaults {
		s := defaults[i]
		// DoNothing on conflict keeps existing operator-configured values.
		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoNothing: true,
		}).Create(&s).Error; err != nil {
			return err
		}
	}
	return nil
}

// seedUser creates the initial operator account from SEED_USER_* env vars so a
// fresh deployment has something to log in with. It is a no-op when the vars
// are unset or the email already exists.
func seedUser(db *gorm.DB, cfg config.AuthConfig) error {
	if cfg.SeedUserEmail == "" || cfg.SeedUserPassword == "" {
		return nil
	}

	email := strings.ToLower(strings.TrimSpace(cfg.SeedUserEmail))

	var count int64
	if err := db.Model(&models.CISUser{}).Where("email = ?", email).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.SeedUserPassword), cfg.BcryptCost)
	if err != nil {
		return fmt.Errorf("hash seed password: %w", err)
	}

	user := models.CISUser{
		Email:        email,
		PasswordHash: string(hash),
		Name:         cfg.SeedUserName,
	}
	if err := db.Create(&user).Error; err != nil {
		return err
	}

	log.Printf("[migrate] seeded initial user %s", email)
	return nil
}

// seedDetectorSettings inserts PRD 10.11's default parameter set as the single
// cis_detector_settings row, only when absent.
//
// DoNothing on conflict is load-bearing: an operator's tuned thresholds must
// survive every restart, and silently resetting a governed parameter to its
// default would be indistinguishable from an admin changing it, except that
// nothing would appear in cis_setting_history to say who did.
func seedDetectorSettings(db *gorm.DB) error {
	defaults := models.DefaultDetectorSettings()
	now := time.Now().UTC()
	defaults.CreatedAt = now
	defaults.UpdatedAt = now

	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoNothing: true,
	}).Create(&defaults).Error
}
