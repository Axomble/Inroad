package mailbox

import (
	"context"
	"errors"
	"fmt"
	netmail "net/mail" // aliased: platform/mail below owns the unqualified name here
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/events"
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
// and the crypto.Keyring for at-rest secret encryption under a per-workspace
// DEK -- dependency inversion all the way down.
type Service struct {
	store   Store
	tester  mail.ConnectionTester
	keyring *crypto.Keyring
	// oauth holds the app's Google OAuth client config (zero value = Gmail
	// OAuth disabled); exchanger performs the authorization-code exchange (a
	// seam so tests fake it without hitting Google). Both drive the Gmail
	// connect flow in oauth.go.
	oauth     mail.GoogleOAuth
	exchanger TokenExchanger
	// msOAuth / msExchanger are the Microsoft 365 counterparts of oauth /
	// exchanger (zero value msOAuth = M365 OAuth disabled). They drive the
	// M365 connect flow in oauth.go.
	msOAuth     mail.MicrosoftOAuth
	msExchanger TokenExchanger
	// events announces status transitions to a workspace's open tabs. NIL IS
	// VALID and means realtime is disabled — events.Emit treats it as a no-op.
	events events.Publisher
}

// ServiceOption configures an optional collaborator. An option rather than an
// eighth positional parameter: this constructor already takes seven, and every
// existing caller and test would otherwise have to pass a zero value for
// something it does not use.
type ServiceOption func(*Service)

// WithEvents wires realtime announcements. Unwired (or nil) the service is
// silent and clients learn about a status change on their next refetch — the
// pre-socket behaviour.
func WithEvents(p events.Publisher) ServiceOption { return func(s *Service) { s.events = p } }

func NewService(store Store, tester mail.ConnectionTester, keyring *crypto.Keyring, oauth mail.GoogleOAuth, exchanger TokenExchanger, msOAuth mail.MicrosoftOAuth, msExchanger TokenExchanger, opts ...ServiceOption) *Service {
	s := &Service{store: store, tester: tester, keyring: keyring, oauth: oauth, exchanger: exchanger, msOAuth: msOAuth, msExchanger: msExchanger}
	for _, opt := range opts {
		opt(s)
	}
	return s
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
	// AllowPlaintext explicitly opts the SMTP connection test AND every subsequent
	// send out of TLS (rare cleartext-only internal relay). It is persisted on the
	// mailbox so connect and send apply the SAME policy. Omitted/false keeps TLS
	// enforced (security Invariant 6).
	AllowPlaintext bool
}

func (in ConnectInput) validate() error {
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

// canonicalEmail parses a submitted mailbox address and returns it in the form
// that must be STORED.
//
// Returning the parsed address rather than the caller's string is the whole
// point. net/mail.ParseAddress SUCCEEDS on "a@ example.com" and reports the
// address as "a@example.com", so a parse whose result is discarded validates the
// input and still writes the whitespace to mailboxes.email. That row has already
// caused a live defect: the ESP sweep projected the untrimmed domain while the
// write-back trimmed it, the two keys disagreed, and a legitimate domain was
// pinned to the wrong ESP.
//
// The address is also rejected when the canonical form still contains
// whitespace. ParseAddress unquotes a quoted local part, so `"a b"@example.com`
// canonicalizes to `a b@example.com` -- a string that no longer parses as an
// address at all, which would put this column back in the state the trim/no-trim
// disagreement came from. Unquoted addresses cannot contain whitespace, so
// nothing usable is refused.
func canonicalEmail(raw string) (string, error) {
	addr, err := netmail.ParseAddress(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("%w: email is not a valid address: %w", ErrValidation, err)
	}
	if strings.ContainsFunc(addr.Address, unicode.IsSpace) {
		return "", fmt.Errorf("%w: email contains whitespace", ErrValidation)
	}
	return addr.Address, nil
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
	// Before anything reads in.Email: the username defaults below, the dedupe
	// check, and the persisted row must all see the same canonical address.
	email, err := canonicalEmail(in.Email)
	if err != nil {
		return MailboxSafe{}, err
	}
	in.Email = email

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

	if err := s.tester.TestSMTP(ctx, mail.SMTPConfig{
		Host:           in.SMTPHost,
		Port:           in.SMTPPort,
		Username:       in.SMTPUsername,
		Password:       in.Secret,
		AllowPlaintext: in.AllowPlaintext,
	}); err != nil {
		return MailboxSafe{}, fmt.Errorf("%w: smtp: %w", ErrConnectionTestFailed, err)
	}
	if err := s.tester.TestIMAP(ctx, mail.IMAPConfig{
		Host:     in.IMAPHost,
		Port:     in.IMAPPort,
		Username: in.IMAPUsername,
		Password: in.Secret,
	}); err != nil {
		return MailboxSafe{}, fmt.Errorf("%w: imap: %w", ErrConnectionTestFailed, err)
	}

	sealer, err := s.keyring.SealerFor(ctx, workspaceID)
	if err != nil {
		return MailboxSafe{}, err
	}
	ciphertext, err := sealer.Seal([]byte(in.Secret))
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
		AllowPlaintext:     in.AllowPlaintext,
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
	return s.setStatus(ctx, workspaceID, id, "paused")
}

// Resume re-activates a paused mailbox.
func (s *Service) Resume(ctx context.Context, workspaceID, id uuid.UUID) (MailboxSafe, error) {
	return s.setStatus(ctx, workspaceID, id, "active")
}

// setStatus applies a status transition and announces it. Pause and Resume
// differ only in the target status, and routing both through here means a future
// transition cannot be added with the write but without the announcement.
func (s *Service) setStatus(ctx context.Context, workspaceID, id uuid.UUID, status string) (MailboxSafe, error) {
	box, err := s.store.UpdateStatus(ctx, workspaceID, id, status, "")
	if err != nil {
		return box, err
	}
	s.announceChanged(ctx, workspaceID, id, status)
	return box, nil
}

// announceChanged tells the workspace's open tabs a mailbox's state moved.
//
// Ids and the new status only — never the address, host or credentials. The
// console's mailbox counts are derived from status, so this is what lets them
// update without the 45s pulse poll; anything richer the client refetches
// through the authorized endpoint.
func (s *Service) announceChanged(ctx context.Context, workspaceID, id uuid.UUID, status string) {
	events.Emit(ctx, s.events, workspaceID.String(), events.Event{
		Type:        "mailbox.changed",
		SubjectKind: "mailbox",
		SubjectID:   id.String(),
		ActorID:     events.ActorFrom(ctx),
		OccurredAt:  time.Now().UTC(),
		Data:        map[string]any{"mailbox_id": id.String(), "status": status},
	})
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
	// "deleted" is not a stored status — the row is gone — but the console needs
	// to drop it from its counts, and a client cannot infer a deletion from
	// silence.
	s.announceChanged(ctx, workspaceID, id, "deleted")
	return nil
}
