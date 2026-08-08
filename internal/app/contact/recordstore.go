package contact

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// The record-page reads. Every one is workspace-pinned in SQL (see
// queries/contact.sql), so a foreign contact id finds no row rather than
// relying on a Go-side ownership check.

func (s *PgStore) Get(ctx context.Context, ws, contactID uuid.UUID) (Record, error) {
	row, err := s.q.GetContactRecord(ctx, gen.GetContactRecordParams{WorkspaceID: ws, ID: contactID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("get contact: %w", err)
	}
	return Record{
		ID: row.ID, Email: row.Email, FirstName: row.FirstName, LastName: row.LastName,
		JobTitle: row.JobTitle, LinkedInURL: row.LinkedinUrl,
		Company:   recordCompany(row.CompanyID, row.CompanyName, row.CompanyDomain),
		Deals:     []RecordDeal{},
		DealCount: row.DealCount,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

// recordCompany builds the nullable company link. company_id is the authority:
// the LEFT JOIN cannot produce a name without one, and a company row with no
// domain yields an empty string rather than a second nullable level.
func recordCompany(id pgtype.UUID, name, domain *string) *RecordCompany {
	if !id.Valid {
		return nil
	}
	out := RecordCompany{ID: uuid.UUID(id.Bytes)}
	if name != nil {
		out.Name = *name
	}
	if domain != nil {
		out.Domain = *domain
	}
	return &out
}

// Suppression returns nil, nil for a contact with no suppressed address: "not
// suppressed" is the normal case and an absence of rows, not an error.
//
// ErrNoRows is the ONLY error that may become a nil result. Widening this to
// swallow other failures would report an unqueryable contact as emailable — see
// the fail-safe note at the call site in Service.Record.
func (s *PgStore) Suppression(ctx context.Context, ws, contactID uuid.UUID) (*RecordSuppression, error) {
	row, err := s.q.GetContactSuppression(ctx, gen.GetContactSuppressionParams{WorkspaceID: ws, ContactID: contactID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("contact suppression: %w", err)
	}
	return &RecordSuppression{
		Reason: row.Reason, Email: row.Email, IsPrimaryEmail: row.IsPrimary, SuppressedAt: row.CreatedAt.Time,
	}, nil
}

func (s *PgStore) ListDeals(ctx context.Context, ws, contactID uuid.UUID, limit int32) ([]RecordDeal, error) {
	rows, err := s.q.ListContactDeals(ctx, gen.ListContactDealsParams{
		WorkspaceID: ws, PrimaryContactID: pgtype.UUID{Bytes: contactID, Valid: true}, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list contact deals: %w", err)
	}
	out := make([]RecordDeal, len(rows))
	for i, row := range rows {
		out[i] = RecordDeal{
			ID: row.ID, Name: row.Name, PipelineID: row.PipelineID, StageID: row.StageID,
			StageLabel: row.StageLabel, StageColor: row.StageColor,
			StageIsWon: row.StageIsWon, StageIsLost: row.StageIsLost,
			AmountMicros: row.AmountMicros, Currency: row.Currency,
			CloseDate: dateValue(row.CloseDate),
			CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
		}
	}
	return out, nil
}

func (s *PgStore) SendStats(ctx context.Context, ws, contactID uuid.UUID) (SendStats, error) {
	row, err := s.q.ContactSendStats(ctx, gen.ContactSendStatsParams{WorkspaceID: ws, ContactID: contactID})
	if err != nil {
		return SendStats{}, fmt.Errorf("contact send stats: %w", err)
	}
	return SendStats{
		EmailsSent: row.EmailsSent, LastSentAt: timeValue(row.LastSentAt),
		OpensMeasurable: row.OpensMeasurable,
	}, nil
}

func (s *PgStore) TrackingStats(ctx context.Context, ws, contactID uuid.UUID) (TrackingStats, error) {
	row, err := s.q.ContactTrackingStats(ctx, gen.ContactTrackingStatsParams{WorkspaceID: ws, ContactID: contactID})
	if err != nil {
		return TrackingStats{}, fmt.Errorf("contact tracking stats: %w", err)
	}
	return TrackingStats{
		OpensIndicative: row.OpensIndicative, Clicks: row.Clicks, LastEventAt: timeValue(row.LastEventAt),
	}, nil
}

func (s *PgStore) EnrollmentCounts(ctx context.Context, ws, contactID uuid.UUID) (map[string]int64, error) {
	rows, err := s.q.ContactEnrollmentCounts(ctx, gen.ContactEnrollmentCountsParams{WorkspaceID: ws, ContactID: contactID})
	if err != nil {
		return nil, fmt.Errorf("contact enrollment counts: %w", err)
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.StopReason] = row.N
	}
	return out, nil
}

func (s *PgStore) ListCampaigns(ctx context.Context, ws, contactID uuid.UUID, limit int32) ([]CampaignEnrollment, error) {
	rows, err := s.q.ListContactCampaigns(ctx, gen.ListContactCampaignsParams{
		WorkspaceID: ws, ContactID: contactID, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list contact campaigns: %w", err)
	}
	out := make([]CampaignEnrollment, len(rows))
	for i, row := range rows {
		out[i] = CampaignEnrollment{
			CampaignID: row.CampaignID, CampaignName: row.CampaignName,
			TrackingEnabled: row.TrackingEnabled, Status: row.Status,
			CurrentStep: row.CurrentStep, StopReason: row.StopReason,
			EnrolledAt: row.EnrolledAt.Time, LastSentAt: timeValue(row.LastSentAt),
		}
	}
	return out, nil
}

func timeValue(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}

func dateValue(value pgtype.Date) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}
