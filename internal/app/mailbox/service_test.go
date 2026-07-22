package mailbox

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

// storeRow is the fakeStore's internal shape: MailboxSafe plus the sealed
// secret, so the fake can round-trip what the sqlc-backed Store would
// while still hiding SecretCiphertext behind the Store interface.
type storeRow struct {
	safe   MailboxSafe
	secret string
}

// fakeStore is an in-memory Store used to unit test Service without a
// database. It enforces the same workspace scoping a real Postgres-backed
// Store would.
type fakeStore struct {
	mu sync.Mutex
	// lastCreate records the params of the most recent Create call so OAuth
	// tests can assert the sealed token landed in SecretCiphertext without a
	// getWithSecret path (MailboxSafe deliberately omits the ciphertext).
	lastCreate gen.CreateMailboxParams
	rows       map[uuid.UUID]storeRow
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: make(map[uuid.UUID]storeRow)}
}

func (s *fakeStore) Create(ctx context.Context, arg gen.CreateMailboxParams) (MailboxSafe, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCreate = arg
	m := MailboxSafe{
		ID:                 uuid.New(),
		WorkspaceID:        arg.WorkspaceID,
		Provider:           arg.Provider,
		Email:              arg.Email,
		DisplayName:        arg.DisplayName,
		SmtpHost:           arg.SmtpHost,
		SmtpPort:           arg.SmtpPort,
		SmtpUsername:       arg.SmtpUsername,
		ImapHost:           arg.ImapHost,
		ImapPort:           arg.ImapPort,
		ImapUsername:       arg.ImapUsername,
		UseTls:             arg.UseTls,
		DailyCap:           arg.DailyCap,
		MinIntervalSeconds: arg.MinIntervalSeconds,
		RampEnabled:        arg.RampEnabled,
		RampStartCap:       arg.RampStartCap,
		RampDays:           arg.RampDays,
		Status:             "active", // mirrors the DB column default
		CreatedAt:          pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	s.rows[m.ID] = storeRow{safe: m, secret: arg.SecretCiphertext}
	return m, nil
}

func (s *fakeStore) Get(ctx context.Context, workspaceID, id uuid.UUID) (MailboxSafe, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.safe.WorkspaceID != workspaceID {
		return MailboxSafe{}, errors.New("not found")
	}
	return r.safe, nil
}

func (s *fakeStore) List(ctx context.Context, workspaceID uuid.UUID) ([]MailboxSafe, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []MailboxSafe
	for _, r := range s.rows {
		if r.safe.WorkspaceID == workspaceID {
			out = append(out, r.safe)
		}
	}
	return out, nil
}

func (s *fakeStore) CountByEmail(ctx context.Context, workspaceID uuid.UUID, email string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, r := range s.rows {
		if r.safe.WorkspaceID == workspaceID && r.safe.Email == email {
			count++
		}
	}
	return count, nil
}

func (s *fakeStore) UpdateStatus(ctx context.Context, workspaceID, id uuid.UUID, status, lastErr string) (MailboxSafe, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.safe.WorkspaceID != workspaceID {
		return MailboxSafe{}, errors.New("not found")
	}
	r.safe.Status = status
	r.safe.LastError = lastErr
	s.rows[id] = r
	return r.safe, nil
}

func (s *fakeStore) Delete(ctx context.Context, workspaceID, id uuid.UUID) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.safe.WorkspaceID != workspaceID {
		return 0, nil
	}
	delete(s.rows, id)
	return 1, nil
}

// fakeTester is a configurable mail.ConnectionTester used to unit test
// Service without ever dialing a real SMTP/IMAP server.
type fakeTester struct {
	smtpErr error
	imapErr error
}

func (t *fakeTester) TestSMTP(cfg mail.SMTPConfig) error { return t.smtpErr }
func (t *fakeTester) TestIMAP(cfg mail.IMAPConfig) error { return t.imapErr }

func newTestSealer(t *testing.T) *crypto.Sealer {
	t.Helper()
	sealer, err := crypto.NewSealer(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatalf("NewSealer() error = %v", err)
	}
	return sealer
}

func validConnectInput() ConnectInput {
	return ConnectInput{
		Email:    "sender@example.com",
		SMTPHost: "smtp.example.com",
		SMTPPort: 587,
		IMAPHost: "imap.example.com",
		IMAPPort: 993,
		Secret:   "super-secret-password",
		UseTLS:   true,
	}
}

func TestConnectSMTP_SuccessPersistsSealedSecret(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, &fakeTester{}, newTestSealer(t), mail.GoogleOAuth{}, nil)
	workspaceID := uuid.New()

	in := validConnectInput()
	m, err := svc.ConnectSMTP(context.Background(), workspaceID, in)
	if err != nil {
		t.Fatalf("ConnectSMTP() error = %v", err)
	}

	// SecretCiphertext deliberately isn't on MailboxSafe (the public shape) —
	// we verify sealing indirectly by reaching into the fakeStore's private
	// row and confirming the stored secret is non-empty and not the plaintext.
	store.mu.Lock()
	row, ok := store.rows[m.ID]
	store.mu.Unlock()
	if !ok {
		t.Fatal("created mailbox missing from fakeStore")
	}
	if row.secret == "" {
		t.Fatal("SecretCiphertext is empty, expected a sealed value")
	}
	if row.secret == in.Secret {
		t.Fatal("SecretCiphertext equals the plaintext secret, expected it to be sealed")
	}

	all, err := store.List(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(all) = %d, want 1", len(all))
	}
}

func TestConnectSMTP_ConnectionTestFailureDoesNotPersist(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, &fakeTester{smtpErr: errors.New("dial tcp: connection refused")}, newTestSealer(t), mail.GoogleOAuth{}, nil)
	workspaceID := uuid.New()

	_, err := svc.ConnectSMTP(context.Background(), workspaceID, validConnectInput())
	if err == nil {
		t.Fatal("ConnectSMTP() error = nil, want error when SMTP test fails")
	}
	if !errors.Is(err, ErrConnectionTestFailed) {
		t.Fatalf("error = %v, want wrapped ErrConnectionTestFailed", err)
	}

	all, err := store.List(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("len(all) = %d, want 0 (nothing should be persisted)", len(all))
	}
}

func TestConnectSMTP_DuplicateEmailRejected(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, &fakeTester{}, newTestSealer(t), mail.GoogleOAuth{}, nil)
	workspaceID := uuid.New()

	in := validConnectInput()
	if _, err := svc.ConnectSMTP(context.Background(), workspaceID, in); err != nil {
		t.Fatalf("first ConnectSMTP() error = %v", err)
	}

	_, err := svc.ConnectSMTP(context.Background(), workspaceID, in)
	if !errors.Is(err, ErrDuplicateMailbox) {
		t.Fatalf("second ConnectSMTP() error = %v, want ErrDuplicateMailbox", err)
	}
}

func TestPauseThenGetShowsPausedStatus(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, &fakeTester{}, newTestSealer(t), mail.GoogleOAuth{}, nil)
	workspaceID := uuid.New()

	m, err := svc.ConnectSMTP(context.Background(), workspaceID, validConnectInput())
	if err != nil {
		t.Fatalf("ConnectSMTP() error = %v", err)
	}

	if _, err := svc.Pause(context.Background(), workspaceID, m.ID); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}

	got, err := svc.Get(context.Background(), workspaceID, m.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != "paused" {
		t.Fatalf("Status = %q, want %q", got.Status, "paused")
	}
}
