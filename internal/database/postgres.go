// Package database owns the GORM connection to Supabase Postgres.
//
// IMPORTANT: this database is shared with a separately-developed AI service.
// See migrate.go for the table-ownership rules that keep the two from
// colliding.
package database

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cis/cis-backend/internal/config"
)

// Connect opens the pooled GORM connection and verifies it with a ping.
func Connect(cfg config.DBConfig) (*gorm.DB, error) {
	gormCfg := &gorm.Config{
		Logger:                                   newLogger(cfg.LogLevel),
		NowFunc:                                  func() time.Time { return time.Now().UTC() },
		DisableForeignKeyConstraintWhenMigrating: true,
		SkipDefaultTransaction:                   true,
	}

	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: cfg.DSN(),
		// Supabase's transaction-mode pooler (port 6543) rejects the extended
		// protocol's prepared statements, so callers using it must set
		// DB_PREFER_SIMPLE_PROTOCOL=true.
		PreferSimpleProtocol: cfg.PreferSimpleProtocol,
	}), gormCfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres connection: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("access underlying sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return db, nil
}

// Ping verifies the connection is still usable, backing GET /health/ready.
func Ping(ctx context.Context, db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// Close releases the connection pool.
func Close(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func newLogger(level string) gormlogger.Interface {
	var logLevel gormlogger.LogLevel
	switch level {
	case "silent":
		logLevel = gormlogger.Silent
	case "error":
		logLevel = gormlogger.Error
	case "info":
		logLevel = gormlogger.Info
	default:
		logLevel = gormlogger.Warn
	}

	return gormlogger.New(
		log.New(os.Stdout, "[gorm] ", log.LstdFlags),
		gormlogger.Config{
			SlowThreshold:             500 * time.Millisecond,
			LogLevel:                  logLevel,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)
}
