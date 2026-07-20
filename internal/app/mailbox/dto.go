package mailbox

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// MailboxSafe is the domain-safe view of a mailbox row: every field the
// caller might need, minus SecretCiphertext. Store returns and Service
// returns this shape so the encrypted secret can't accidentally escape via
// a stray httpx.JSON(w, ..., m) at the HTTP boundary.
type MailboxSafe struct {
	ID                 uuid.UUID
	WorkspaceID        uuid.UUID
	Provider           string
	Email              string
	DisplayName        string
	SmtpHost           string
	SmtpPort           int32
	SmtpUsername       string
	ImapHost           string
	ImapPort           int32
	ImapUsername       string
	UseTls             bool
	DailyCap           int32
	MinIntervalSeconds int32
	RampEnabled       bool
	RampStartCap       int32
	RampDays           int32
	Status             string
	LastError          string
	CreatedAt          pgtype.Timestamptz
}

// safeFromGen strips SecretCiphertext from a persistence row. Used by every
// Store method that returns a mailbox — the secret only ever leaves the
// store during ConnectSMTP's internal sealer flow, never via the domain
// API surface.
func safeFromGen(m gen.Mailbox) MailboxSafe {
	return MailboxSafe{
		ID:                 m.ID,
		WorkspaceID:        m.WorkspaceID,
		Provider:           m.Provider,
		Email:              m.Email,
		DisplayName:        m.DisplayName,
		SmtpHost:           m.SmtpHost,
		SmtpPort:           m.SmtpPort,
		SmtpUsername:       m.SmtpUsername,
		ImapHost:           m.ImapHost,
		ImapPort:           m.ImapPort,
		ImapUsername:       m.ImapUsername,
		UseTls:             m.UseTls,
		DailyCap:           m.DailyCap,
		MinIntervalSeconds: m.MinIntervalSeconds,
		RampEnabled:        m.RampEnabled,
		RampStartCap:       m.RampStartCap,
		RampDays:           m.RampDays,
		Status:             m.Status,
		LastError:          m.LastError,
		CreatedAt:          m.CreatedAt,
	}
}
