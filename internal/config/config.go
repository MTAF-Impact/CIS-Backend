// Package config loads all runtime configuration from environment variables.
//
// Nothing in this package may contain a real credential. Defaults are only ever
// provided for non-secret values; secrets are required and cause a startup error
// when missing, so a misconfigured deployment fails loudly instead of silently
// falling back to something insecure.
package config

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config is the fully resolved application configuration.
type Config struct {
	App     AppConfig
	DB      DBConfig
	Auth    AuthConfig
	Storage StorageConfig
	AI      AIConfig
	Cron    CronConfig
}

// UnlimitedBodyLimit is the request-body cap applied when
// APP_BODY_LIMIT_BYTES is 0.
//
// US40 asks for "no file-size limit" on policy uploads. A literal absence of a
// limit is not expressible: fasthttp treats any value <= 0 as "use the 4 MB
// default", which would silently reject real policy PDFs. So "unlimited" is
// implemented as the largest value that fits in an int on every platform,
// ~2 GiB, which is far beyond any plausible policy document.
const UnlimitedBodyLimit = math.MaxInt32

// AppConfig holds process-level settings.
type AppConfig struct {
	Env            string // development | staging | production
	Port           string
	Name           string
	AllowedOrigins []string
	BodyLimitBytes int // resolved value in bytes; see UnlimitedBodyLimit
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
}

// IsProduction reports whether the app is running with production semantics.
func (a AppConfig) IsProduction() bool { return a.Env == "production" }

// DBConfig holds Supabase/Postgres connection settings.
type DBConfig struct {
	// URL, when set, takes precedence over the discrete fields below. Supabase
	// exposes this as the "Connection string" in the dashboard.
	URL      string
	Host     string
	Port     string
	User     string
	Password string
	Name     string
	SSLMode  string

	// PreferSimpleProtocol must be true when connecting through Supabase's
	// transaction-mode pooler (port 6543), which cannot handle the prepared
	// statements pgx uses by default.
	PreferSimpleProtocol bool

	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	LogLevel        string // silent | error | warn | info

	// AutoMigrate controls whether GORM creates/updates the cis_* tables on
	// boot. It never touches AI-owned tables regardless of this setting.
	AutoMigrate bool
}

// DSN builds the Postgres connection string.
func (d DBConfig) DSN() string {
	if d.URL != "" {
		return d.URL
	}
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}

// AuthConfig holds JWT and seeding settings for the login flow.
type AuthConfig struct {
	JWTSecret         string
	AccessTokenTTL    time.Duration
	RefreshTokenTTL   time.Duration
	Issuer            string
	BcryptCost        int
	AllowRegistration bool
	SeedUserEmail     string
	SeedUserPassword  string
	SeedUserName      string
}

// StorageConfig selects and configures the policy-document file store.
type StorageConfig struct {
	Driver string // supabase | local

	SupabaseURL        string
	SupabaseServiceKey string
	SupabaseBucket     string
	SignedURLTTL       time.Duration

	LocalDir string
}

// AIConfig describes how to reach the separately-developed AI service.
type AIConfig struct {
	// BaseURL is optional. When empty the backend degrades gracefully: policy
	// matchmaking is left pending and the F4 generator returns 503.
	BaseURL string
	APIKey  string

	// Timeout bounds the fast calls: Flow 1's matchmaking hand-off (the AI
	// service acks in milliseconds and works in the background) and the
	// readiness probe.
	Timeout time.Duration
	// LongTimeout bounds the calls that do real work inside the request:
	// Flow 3's claim generation, synthetic ingestion, clustering, and
	// rescoring. The AI service documents Flow 3 alone at 30-60s of sequential
	// LLM calls, so reusing Timeout here would fail a normal successful run
	// while the AI service kept going and committed the claim anyway.
	LongTimeout time.Duration

	// MatchmakingStaleAfter is how long a policy may sit in
	// processing_status="processing" before the retry sweep assumes the Flow 2
	// callback was lost and re-queues it. The AI service never retries its
	// callback, so this is the only place a dropped result is recovered.
	MatchmakingStaleAfter time.Duration

	// The AI service's route table is deliberately NOT here. Those paths are
	// part of the contract between the two services rather than properties of
	// this deployment, and they live as constants in
	// internal/aiclient/endpoints.go alongside the request and response types
	// they belong to.

	// CallbackBaseURL is this backend's externally reachable base URL, sent to
	// the AI service as `callback_url` on Flow 1. Optional: when empty the
	// field is omitted and the AI service falls back to its own BACKEND_URL.
	CallbackBaseURL string
}

// Enabled reports whether outbound AI calls are configured.
func (a AIConfig) Enabled() bool { return a.BaseURL != "" }

// CronConfig controls the background jobs.
type CronConfig struct {
	Enabled           bool
	PolicyRolloutSpec string // US41: flip Not Rolled Out -> Rolled Out
	ScoreSnapshotSpec string // F3 chart history, preceded by an AI rescore
	// MatchmakingRetrySpec re-queues matchmaking jobs that failed or whose
	// Flow 2 callback never arrived. It runs on its own, much more frequent
	// schedule than the daily rollout job: a policy stranded on "Processing"
	// shows a spinning badge and empty claim lists until it is re-queued, so
	// waiting until the next day to notice is not acceptable.
	MatchmakingRetrySpec string
	// DetectionSpec is how often the F5 detection tick fires. It is NOT the
	// detection cadence: PRD 10.5.8 makes that a detector setting an admin
	// edits at runtime (1-24 h), and a cron spec is fixed when the scheduler
	// starts. So the tick runs at the finest cadence the setting allows —
	// hourly — and DetectionService.RunScheduled decides whether the tick is
	// due. The velocity trigger rides the same tick, since a growth spike is
	// worth noticing on the same granularity.
	DetectionSpec string
	// SnapshotRetentionSpec drives the PRD 10.9.1 rule 7 purge of expired
	// evidence snapshots. Daily and off-peak: it is a deletion sweep with a
	// 24-month default horizon, so nothing about it is urgent, and it hands
	// work to the AI service that should not compete with a detection run.
	SnapshotRetentionSpec string
}

// Load reads the environment (optionally seeded from a .env file) into a Config.
func Load() (*Config, error) {
	// A missing .env is fine: real deployments inject variables directly.
	_ = godotenv.Load()

	cfg := &Config{
		App: AppConfig{
			Env:            getEnv("APP_ENV", "development"),
			Port:           getEnv("APP_PORT", "8080"),
			Name:           getEnv("APP_NAME", "CIS Backend"),
			AllowedOrigins: getEnvSlice("CORS_ALLOWED_ORIGINS", []string{"*"}),
			BodyLimitBytes: resolveBodyLimit(getEnvInt("APP_BODY_LIMIT_BYTES", 0)),
			ReadTimeout:    getEnvDuration("APP_READ_TIMEOUT", 5*time.Minute),
			WriteTimeout:   getEnvDuration("APP_WRITE_TIMEOUT", 5*time.Minute),
		},
		DB: DBConfig{
			URL:                  getEnv("DATABASE_URL", ""),
			Host:                 getEnv("DB_HOST", ""),
			Port:                 getEnv("DB_PORT", "5432"),
			User:                 getEnv("DB_USER", ""),
			Password:             getEnv("DB_PASSWORD", ""),
			Name:                 getEnv("DB_NAME", "postgres"),
			SSLMode:              getEnv("DB_SSLMODE", "require"),
			PreferSimpleProtocol: getEnvBool("DB_PREFER_SIMPLE_PROTOCOL", false),
			MaxOpenConns:         getEnvInt("DB_MAX_OPEN_CONNS", 20),
			MaxIdleConns:         getEnvInt("DB_MAX_IDLE_CONNS", 5),
			ConnMaxLifetime:      getEnvDuration("DB_CONN_MAX_LIFETIME", 30*time.Minute),
			LogLevel:             getEnv("DB_LOG_LEVEL", "warn"),
			AutoMigrate:          getEnvBool("DB_AUTO_MIGRATE", true),
		},
		Auth: AuthConfig{
			JWTSecret:         getEnv("JWT_SECRET", ""),
			AccessTokenTTL:    getEnvDuration("JWT_ACCESS_TTL", 24*time.Hour),
			RefreshTokenTTL:   getEnvDuration("JWT_REFRESH_TTL", 720*time.Hour),
			Issuer:            getEnv("JWT_ISSUER", "cis-backend"),
			BcryptCost:        getEnvInt("BCRYPT_COST", 12),
			AllowRegistration: getEnvBool("AUTH_ALLOW_REGISTRATION", true),
			SeedUserEmail:     getEnv("SEED_USER_EMAIL", ""),
			SeedUserPassword:  getEnv("SEED_USER_PASSWORD", ""),
			SeedUserName:      getEnv("SEED_USER_NAME", "CIS Admin"),
		},
		Storage: StorageConfig{
			Driver:             getEnv("STORAGE_DRIVER", "supabase"),
			SupabaseURL:        strings.TrimRight(getEnv("SUPABASE_URL", ""), "/"),
			SupabaseServiceKey: getEnv("SUPABASE_SERVICE_ROLE_KEY", ""),
			SupabaseBucket:     getEnv("SUPABASE_STORAGE_BUCKET", "policy-documents"),
			SignedURLTTL:       getEnvDuration("SUPABASE_SIGNED_URL_TTL", time.Hour),
			LocalDir:           getEnv("STORAGE_LOCAL_DIR", "./uploads"),
		},
		AI: AIConfig{
			BaseURL:               strings.TrimRight(getEnv("AI_SERVICE_URL", ""), "/"),
			APIKey:                getEnv("AI_SERVICE_API_KEY", ""),
			Timeout:               getEnvDuration("AI_SERVICE_TIMEOUT", 30*time.Second),
			LongTimeout:           getEnvDuration("AI_SERVICE_LONG_TIMEOUT", 180*time.Second),
			MatchmakingStaleAfter: getEnvDuration("AI_MATCHMAKING_STALE_AFTER", 30*time.Minute),
			CallbackBaseURL:       strings.TrimRight(getEnv("BACKEND_PUBLIC_URL", ""), "/"),
		},
		Cron: CronConfig{
			Enabled:              getEnvBool("CRON_ENABLED", true),
			PolicyRolloutSpec:    getEnv("CRON_POLICY_ROLLOUT_SPEC", "0 1 * * *"),
			ScoreSnapshotSpec:    getEnv("CRON_SCORE_SNAPSHOT_SPEC", "0 * * * *"),
			MatchmakingRetrySpec: getEnv("CRON_MATCHMAKING_RETRY_SPEC", "*/15 * * * *"),
			// Offset from the score snapshot job's :00 so the two do not
			// contend for the AI service on the hour.
			DetectionSpec:         getEnv("CRON_DETECTION_SPEC", "20 * * * *"),
			SnapshotRetentionSpec: getEnv("CRON_SNAPSHOT_RETENTION_SPEC", "40 2 * * *"),
		},
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	var missing []string

	if c.DB.URL == "" && (c.DB.Host == "" || c.DB.User == "" || c.DB.Password == "") {
		missing = append(missing, "DATABASE_URL (or DB_HOST + DB_USER + DB_PASSWORD)")
	}
	if c.Auth.JWTSecret == "" {
		missing = append(missing, "JWT_SECRET")
	}
	if c.Storage.Driver == "supabase" {
		if c.Storage.SupabaseURL == "" {
			missing = append(missing, "SUPABASE_URL")
		}
		if c.Storage.SupabaseServiceKey == "" {
			missing = append(missing, "SUPABASE_SERVICE_ROLE_KEY")
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	if len(c.Auth.JWTSecret) < 32 && c.App.IsProduction() {
		return fmt.Errorf("JWT_SECRET must be at least 32 characters in production")
	}
	if c.Storage.Driver != "supabase" && c.Storage.Driver != "local" {
		return fmt.Errorf("STORAGE_DRIVER must be 'supabase' or 'local', got %q", c.Storage.Driver)
	}
	if c.Auth.BcryptCost < 4 || c.Auth.BcryptCost > 31 {
		return fmt.Errorf("BCRYPT_COST must be between 4 and 31, got %d", c.Auth.BcryptCost)
	}
	return nil
}

// resolveBodyLimit maps 0 (and any non-positive value) onto the effectively
// unlimited cap, since fasthttp would otherwise fall back to its 4 MB default.
func resolveBodyLimit(configured int) int {
	if configured <= 0 {
		return UnlimitedBodyLimit
	}
	return configured
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			return parsed
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if parsed, err := strconv.ParseBool(v); err == nil {
			return parsed
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if parsed, err := time.ParseDuration(v); err == nil {
			return parsed
		}
	}
	return fallback
}

func getEnvSlice(key string, fallback []string) []string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}
