package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cis/cis-backend/internal/models"
)

// ReportRepository manages generated network reports and the export audit log.
//
// Both tables are backend-owned. Nothing here reads a detector table, which
// means the audit trail stays queryable even on a deployment where the pipeline
// has not been provisioned — a property that matters, because the audit log's
// whole job is to answer questions after the fact.
type ReportRepository struct {
	db *gorm.DB
}

// NewReportRepository constructs a ReportRepository.
func NewReportRepository(db *gorm.DB) *ReportRepository {
	return &ReportRepository{db: db}
}

// ListReports returns a network's generated reports, newest first.
//
// Versioning is implicit — multiple rows per network, never an overwrite — so
// an earlier report stays downloadable exactly as it was submitted. A referral
// already sent to a platform cannot be allowed to change underneath the
// recipient.
func (r *ReportRepository) ListReports(ctx context.Context, networkID uuid.UUID) ([]models.CISNetworkReport, error) {
	var rows []models.CISNetworkReport
	err := r.db.WithContext(ctx).
		Where("network_id = ?", networkID).
		Order("generated_at DESC, id DESC").
		Find(&rows).Error
	return rows, err
}

// FindReport loads one generated report.
func (r *ReportRepository) FindReport(ctx context.Context, id uuid.UUID) (*models.CISNetworkReport, error) {
	var row models.CISNetworkReport
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// CreateAuditEntry writes an export audit row and returns it.
//
// # Why this is called BEFORE the file is rendered
//
// The report's chain-of-custody section prints the export audit entry ID —
// the id is inside the document the export produces. So the row has to exist,
// with its id allocated, before rendering starts.
// Ordinary "log the export after it succeeds" wiring produces a report with an
// empty chain-of-custody slot, which is precisely the field that distinguishes
// the document from an assertion.
//
// The cost is that a failed render leaves an audit row for an export that never
// completed. That is the right direction to fail: an over-recorded audit log is
// a nuisance, an under-recorded one defeats its purpose.
func (r *ReportRepository) CreateAuditEntry(ctx context.Context, entry *models.CISExportAuditLog) error {
	if entry.ID == uuid.Nil {
		entry.ID = uuid.New()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	return r.db.WithContext(ctx).Create(entry).Error
}

// CreateReport persists a generated report record.
func (r *ReportRepository) CreateReport(ctx context.Context, report *models.CISNetworkReport) error {
	if report.ID == uuid.Nil {
		report.ID = uuid.New()
	}
	if report.GeneratedAt.IsZero() {
		report.GeneratedAt = time.Now().UTC()
	}
	return r.db.WithContext(ctx).Create(report).Error
}

// AuditFilter narrows the export audit log.
type AuditFilter struct {
	UserID     *uuid.UUID
	NetworkID  *uuid.UUID
	RunID      *uuid.UUID
	ExportType string
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int
}

// AuditRow is one audit entry with the acting user's name resolved.
type AuditRow struct {
	models.CISExportAuditLog `gorm:"embedded"`
	UserName                 *string `gorm:"column:user_name"`
	UserEmail                *string `gorm:"column:user_email"`
}

// ListAuditLog returns export audit entries, newest first.
//
// Filterable by user, network and date. NetworkID and RunID are first-class
// columns rather than a generic object_type/object_id pair, because without
// them "which exported reports contain a now-allowlisted account?" has no
// query.
func (r *ReportRepository) ListAuditLog(ctx context.Context, f AuditFilter) ([]AuditRow, int64, error) {
	q := r.db.WithContext(ctx).Table("cis_export_audit_log AS l")

	if f.UserID != nil {
		q = q.Where("l.user_id = ?", *f.UserID)
	}
	if f.NetworkID != nil {
		q = q.Where("l.network_id = ?", *f.NetworkID)
	}
	if f.RunID != nil {
		q = q.Where("l.run_id = ?", *f.RunID)
	}
	if f.ExportType != "" {
		q = q.Where("l.export_type = ?", f.ExportType)
	}
	if f.From != nil {
		q = q.Where("l.created_at >= ?", *f.From)
	}
	if f.To != nil {
		q = q.Where("l.created_at <= ?", *f.To)
	}

	var total int64
	if err := q.Session(&gorm.Session{}).Select("COUNT(l.id)").Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []AuditRow
	err := q.Session(&gorm.Session{}).
		Joins("LEFT JOIN cis_users u ON u.id = l.user_id").
		Select("l.*, u.name AS user_name, u.email AS user_email").
		Order("l.created_at DESC, l.id DESC").
		Limit(f.Limit).
		Offset(f.Offset).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ExportsForNetworks returns audit entries touching any of the given networks.
//
// Allowlisting an account suppresses and relabels its historical networks,
// but a PDF citing accounts since allowlisted is already in someone's inbox
// and cannot be recalled. Naming the exports at least makes the exposure
// answerable.
func (r *ReportRepository) ExportsForNetworks(ctx context.Context, networkIDs []uuid.UUID) ([]AuditRow, error) {
	if len(networkIDs) == 0 {
		return nil, nil
	}
	var rows []AuditRow
	err := r.db.WithContext(ctx).
		Table("cis_export_audit_log AS l").
		Joins("LEFT JOIN cis_users u ON u.id = l.user_id").
		Select("l.*, u.name AS user_name, u.email AS user_email").
		Where("l.network_id IN ?", networkIDs).
		Order("l.created_at DESC").
		Scan(&rows).Error
	return rows, err
}
