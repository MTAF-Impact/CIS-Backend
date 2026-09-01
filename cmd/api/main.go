// Command api is the CIS backend HTTP server.
//
// Startup order: load config -> connect to Postgres -> migrate the
// backend-owned cis_* tables -> build the dependency graph -> start cron ->
// serve, with a graceful shutdown on SIGINT/SIGTERM.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/cis/cis-backend/internal/aiclient"
	"github.com/cis/cis-backend/internal/config"
	"github.com/cis/cis-backend/internal/database"
	"github.com/cis/cis-backend/internal/handler"
	"github.com/cis/cis-backend/internal/middleware"
	"github.com/cis/cis-backend/internal/repository"
	"github.com/cis/cis-backend/internal/router"
	"github.com/cis/cis-backend/internal/scheduler"
	"github.com/cis/cis-backend/internal/service"
	"github.com/cis/cis-backend/internal/storage"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}
	log.Printf("[boot] %s starting in %s mode", cfg.App.Name, cfg.App.Env)

	db, err := database.Connect(cfg.DB)
	if err != nil {
		log.Fatalf("database error: %v", err)
	}
	defer func() {
		if err := database.Close(db); err != nil {
			log.Printf("[shutdown] closing database: %v", err)
		}
	}()
	log.Println("[boot] connected to Postgres")

	if err := database.Migrate(db, cfg); err != nil {
		log.Fatalf("migration error: %v", err)
	}

	store, err := storage.New(cfg.Storage)
	if err != nil {
		log.Fatalf("storage error: %v", err)
	}
	log.Printf("[boot] storage driver: %s", store.Driver())

	ai := aiclient.New(cfg.AI)
	if ai.Enabled() {
		log.Printf("[boot] AI service configured at %s", cfg.AI.BaseURL)
	} else {
		log.Println("[boot] AI_SERVICE_URL is unset: policy matchmaking and the F4 claim generator are disabled")
	}

	// --- dependency graph: repositories -> services -> handlers ---
	userRepo := repository.NewUserRepository(db)
	claimRepo := repository.NewClaimRepository(db)
	alertRepo := repository.NewAlertRepository(db)
	policyRepo := repository.NewPolicyRepository(db)
	snapshotRepo := repository.NewSnapshotRepository(db)
	settingRepo := repository.NewSettingRepository(db)
	// F6 — Overview. Probes for the PRD v1.5 columns at construction, so the
	// page degrades on a database the AI service has not caught up with rather
	// than failing.
	overviewRepo := repository.NewOverviewRepository(db)
	// F5 — Coordinated-Network Detector. networkRepo reads the AI-owned
	// pipeline tables and the cis_* overlays; it is also F1's and F2's one F5
	// dependency, for the US61 indicator on claim cards.
	networkRepo := repository.NewNetworkRepository(db)
	allowlistRepo := repository.NewAllowlistRepository(db)
	reportRepo := repository.NewReportRepository(db)

	authSvc := service.NewAuthService(userRepo, cfg.Auth)
	settingSvc := service.NewSettingService(settingRepo)
	claimSvc := service.NewClaimService(claimRepo, alertRepo, policyRepo, snapshotRepo, networkRepo, settingSvc, ai)
	policySvc := service.NewPolicyService(policyRepo, claimRepo, alertRepo, networkRepo, store, ai, cfg.App)
	alertSvc := service.NewAlertService(alertRepo, claimRepo, snapshotRepo, settingSvc)
	adminSvc := service.NewAdminService(ai, settingSvc, policyRepo, claimRepo)
	overviewSvc := service.NewOverviewService(overviewRepo, policyRepo, settingSvc)

	// The F5 graph. allowlistSvc comes before detectionSvc because the pipeline
	// is handed the exclusion lists at dispatch, and reportSvc wraps networkSvc
	// because a report is a rendering of the same detail payload the API
	// serves — the document and the screen must not be able to disagree.
	networkSvc := service.NewNetworkService(networkRepo, claimRepo, policyRepo, settingSvc)
	allowlistSvc := service.NewAllowlistService(allowlistRepo, networkRepo, reportRepo)
	reportSvc := service.NewReportService(networkRepo, reportRepo, networkSvc, settingSvc, store, cfg.App)
	detectionSvc := service.NewDetectionService(networkRepo, claimRepo, settingSvc, allowlistSvc, ai)

	handlers := router.Handlers{
		Health:  handler.NewHealthHandler(db, cfg, store, ai),
		Auth:    handler.NewAuthHandler(authSvc),
		Claim:   handler.NewClaimHandler(claimSvc),
		Topic:   handler.NewTopicHandler(claimSvc),
		Policy:  handler.NewPolicyHandler(policySvc),
		Alert:   handler.NewAlertHandler(alertSvc),
		Setting: handler.NewSettingHandler(settingSvc),
		Admin:   handler.NewAdminHandler(adminSvc, alertSvc),

		Overview: handler.NewOverviewHandler(overviewSvc),

		Network:         handler.NewNetworkHandler(networkSvc, reportSvc),
		Allowlist:       handler.NewAllowlistHandler(allowlistSvc),
		Detection:       handler.NewDetectionHandler(detectionSvc, reportSvc),
		DetectorSetting: handler.NewDetectorSettingsHandler(settingSvc, allowlistSvc),
	}

	app := fiber.New(fiber.Config{
		AppName:      cfg.App.Name,
		ErrorHandler: middleware.ErrorHandler(cfg.App.IsProduction()),
		// US40 allows policy documents of any size; config resolves 0 to an
		// effectively unlimited cap.
		BodyLimit:    cfg.App.BodyLimitBytes,
		ReadTimeout:  cfg.App.ReadTimeout,
		WriteTimeout: cfg.App.WriteTimeout,
		// StreamRequestBody is deliberately left off: policy uploads are parsed
		// with MultipartForm, which needs the buffered body. fasthttp already
		// spools large multipart payloads to temp files.
		DisableStartupMessage: true,
	})

	app.Use(middleware.RequestID())
	// AccessLog wraps Recover so a recovered panic is still reported as a
	// request line, not just a stack trace on stderr.
	app.Use(middleware.AccessLog())
	app.Use(middleware.Recover())
	app.Use(middleware.CORS(cfg.App))

	router.Register(app, handlers, authSvc)

	cron := scheduler.New(cfg.Cron, policySvc, alertSvc, adminSvc, detectionSvc)
	if err := cron.Start(); err != nil {
		log.Fatalf("scheduler error: %v", err)
	}
	defer cron.Stop()

	// Serve in the background so the main goroutine can wait for a signal.
	serverErr := make(chan error, 1)
	go func() {
		addr := ":" + cfg.App.Port
		log.Printf("[boot] listening on http://localhost%s", addr)
		if err := app.Listen(addr); err != nil {
			serverErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Fatalf("server error: %v", err)
	case sig := <-quit:
		log.Printf("[shutdown] received %s, draining connections", sig)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(ctx); err != nil {
		log.Printf("[shutdown] forced close: %v", err)
	}
	log.Println("[shutdown] stopped cleanly")
}
