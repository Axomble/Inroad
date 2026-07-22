package mailbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

// Sentinel errors the handler layer maps to HTTP status codes.
var (
	// ErrDuplicateMailbox is returned when a mailbox for the given email
	// already exists in the workspace.
	ErrDuplicateMailbox = errors.New("mailbox already connected for this email")
	// ErrNotFound is returned when a mailbox does not exist in the workspace.
	ErrNotFound = errors.New("mailbox not found")
	// ErrValidation is returned when the connect input is missing required fields.
	ErrValidation = errors.New("invalid mailbox input")
	// ErrConnectionTestFailed wraps a failure from the SMTP or IMAP connection test.
	ErrConnectionTestFailed = errors.New("mailbox connection test failed")
)

// Service implements mailbox connection use cases. It depends on the Store
// interface (not a concrete sqlc type), the mail.ConnectionTester interface,
// and the crypto.Sealer for at-rest secret encryption -- dependency
// inversion all the way down.
type Service struct {
	store  Store
	tester mail.ConnectionTester
	sealer *crypto.Sealer
	// oauth holds the app's Google OAuth client config (zero value = Gmail
	// OAuth disabled); exchanger performs the authorization-code exchange (a
	// seam so tests fake it without hitting Google). Both drive the Gmail
	// connect flow in oauth.go.
	oauth     mail.GoogleOAuth
	exchanger TokenExchanger
}

func NewService(store Store, tester mail.ConnectionTester, sealer *crypto.Sealer, oauth mail.GoogleOAuth, exchanger TokenExchanger) *Service {
	return &Service{store: store, tester: tester, sealer: sealer, oauth: oauth, exchanger: exchanger}
}

// ConnectInput carries the fields needed to connect a new SMTP/IMAP mailbox.
type ConnectInput struct {
	Email        string
	DisplayName  string
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	IMAPHost     string
	IMAPPort     int
	IMAPUsername string
	Secret       string
	UseTLS       bool
}

func (in ConnectInput) validate() error {
	if in.Email == "" {
		return fmt.Errorf("%w: email is required", ErrValidation)
	}
	if in.SMTPHost == "" {
		return fmt.Errorf("%w: smtp_host is required", ErrValidation)
	}
	if in.SMTPPort == 0 {
		return fmt.Errorf("%w: smtp_port is required", ErrValidation)
	}
	if in.IMAPHost == "" {
		return fmt.Errorf("%w: imap_host is required", ErrValidation)
	}
	if in.IMAPPort == 0 {
		return fmt.Errorf("%w: imap_port is required", ErrValidation)
	}
	if in.Secret == "" {
		return fmt.Errorf("%w: secret is required", ErrValidation)
	}
	return nil
}

// Default policy applied to every newly connected mailbox (PRD 9.1.3 warm-up ramp).
const (
	defaultDailyCap           = int32(50)
	defaultMinIntervalSeconds = int32(120)
	defaultRampStartCap       = int32(5)
	defaultRampDays           = int32(30)
)

// ConnectSMTP validates input, dedupes on email, verifies the credentials
// against real SMTP/IMAP servers, seals the secret, and persists the
// mailbox. Nothing is persisted if the connection test fails.
func (s *Service) ConnectSMTP(ctx context.Context, workspaceID uuid.UUID, in ConnectInput) (MailboxSafe, error) {
	if in.SMTPUsername == "" {
		in.SMTPUsername = in.Email
	}
	if in.IMAPUsername == "" {
		in.IMAPUsername = in.Email
	}
	if err := in.validate(); err != nil {
		return MailboxSafe{}, err
	}

	count, err := s.store.CountByEmail(ctx, workspaceID, in.Email)
	if err != nil {
		return MailboxSafe{}, err
	}
	if count > 0 {
		return MailboxSafe{}, ErrDuplicateMailbox
	}

	if err := s.tester.TestSMTP(mail.SMTPConfig{
		Host:     in.SMTPHost,
		Port:     in.SMTPPort,
		Username: in.SMTPUsername,
		Password: in.Secret,
		UseTLS:   in.UseTLS,
	}); err != nil {
		return MailboxSafe{}, fmt.Errorf("%w: smtp: %v", ErrConnectionTestFailed, err)
	}
	if err := s.tester.TestIMAP(mail.IMAPConfig{
		Host:     in.IMAPHost,
		Port:     in.IMAPPort,
		Username: in.IMAPUsername,
		Password: in.Secret,
	}); err != nil {
		return MailboxSafe{}, fmt.Errorf("%w: imap: %v", ErrConnectionTestFailed, err)
	}

	ciphertext, err := s.sealer.Seal([]byte(in.Secret))
	if err != nil {
		return MailboxSafe{}, err
	}

	return s.store.Create(ctx, gen.CreateMailboxParams{
		WorkspaceID:        workspaceID,
		Provider:           "smtp",
		Email:              in.Email,
		DisplayName:        in.DisplayName,
		SmtpHost:           in.SMTPHost,
		SmtpPort:           int32(in.SMTPPort),
		SmtpUsername:       in.SMTPUsername,
		ImapHost:           in.IMAPHost,
		ImapPort:           int32(in.IMAPPort),
		ImapUsername:       in.IMAPUsername,
		SecretCiphertext:   ciphertext,
		UseTls:             in.UseTLS,
		DailyCap:           defaultDailyCap,
		MinIntervalSeconds: defaultMinIntervalSeconds,
		RampEnabled:        true,
		RampStartCap:       defaultRampStartCap,
		RampDays:           defaultRampDays,
	})
}

// List returns every mailbox connected in the workspace.
func (s *Service) List(ctx context.Context, workspaceID uuid.UUID) ([]MailboxSafe, error) {
	return s.store.List(ctx, workspaceID)
}

// Get returns a single mailbox, scoped to the workspace.
func (s *Service) Get(ctx context.Context, workspaceID, id uuid.UUID) (MailboxSafe, error) {
	return s.store.Get(ctx, workspaceID, id)
}

// Pause stops a mailbox from sending or polling without deleting it.
func (s *Service) Pause(ctx context.Context, workspaceID, id uuid.UUID) (MailboxSafe, error) {
	return s.store.UpdateStatus(ctx, workspaceID, id, "paused", "")
}

// Resume re-activates a paused mailbox.
func (s *Service) Resume(ctx context.Context, workspaceID, id uuid.UUID) (MailboxSafe, error) {
	return s.store.UpdateStatus(ctx, workspaceID, id, "active", "")
}

// Delete removes a mailbox from the workspace. Returns ErrNotFound if no row
// matched (belongs to another workspace or does not exist).
func (s *Service) Delete(ctx context.Context, workspaceID, id uuid.UUID) error {
	rows, err := s.store.Delete(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}
