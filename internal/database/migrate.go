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
	&models.CISClaimScoreSnapshot{},
	&models.CISSetting{},
}

// requiredAITables are read by the backend but owned by the AI service. Their
// absence is reported as a warning at boot rather than a fatal error, so the
// API can still start (and F2/F4 still work) against a database where the AI
// pipeline has not run yet.
var requiredAITables = []string{
	"claims", "topics", "policies", "claim_policies", "content_items",
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
