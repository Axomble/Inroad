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
		AllowPlaintext:     arg.AllowPlaintext,
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

func (t *fakeTester) TestSMTP(_ context.Context, cfg mail.SMTPConfig) error { return t.smtpErr }
func (t *fakeTester) TestIMAP(_ context.Context, cfg mail.IMAPConfig) error { return t.imapErr }

// inMemDEKStore is a minimal in-memory crypto.DEKStore for unit tests: it
// returns ErrDEKNotFound on a miss and fails a Put for an already-present
// workspace, matching the fail-if-exists contract the sqlc-backed store
// enforces via the workspace_deks primary key.
type inMemDEKStore struct {
	mu   sync.Mutex
	rows map[uuid.UUID][]byte
}

func newInMemDEKStore() *inMemDEKStore { return &inMemDEKStore{rows: make(map[uuid.UUID][]byte)} }

func (s *inMemDEKStore) GetWrappedDEK(_ context.Context, ws uuid.UUID) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.rows[ws]
	if !ok {
		return nil, "", crypto.ErrDEKNotFound
	}
	return w, "local", nil
}

func (s *inMemDEKStore) PutWrappedDEK(_ context.Context, ws uuid.UUID, wrapped []byte, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rows[ws]; ok {
		return errors.New("dek exists")
	}
	s.rows[ws] = wrapped
	return nil
}

// newTestKeyring builds a Keyring over the in-memory DEKStore, a local
// KeyProvider, and a legacy master-key Sealer — all under one fixed test key so
// seals round-trip and legacy v1 blobs still open.
func newTestKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	key := bytes.Repeat([]byte{1}, 32)
	kp, err := crypto.NewLocalKeyProvider(key)
	if err != nil {
		t.Fatalf("NewLocalKeyProvider() error = %v", err)
	}
	legacy, err := crypto.NewSealer(key)
	if err != nil {
		t.Fatalf("NewSealer() error = %v", err)
	}
	return crypto.NewKeyring(kp, newInMemDEKStore(), legacy)
}

func validConnectInput() ConnectInput {
	return ConnectInput{
		Email:    "sender@example.com",
		SMTPHost: "smtp.example.com",
		SMTPPort: 587,
		IMAPHost: "imap.example.com",
		IMAPPort: 993,
		Secret:   "super-secret-password",
	}
}

func TestConnectSMTP_SuccessPersistsSealedSecret(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, &fakeTester{}, newTestKeyring(t), mail.GoogleOAuth{}, nil, mail.MicrosoftOAuth{}, nil)
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

func TestConnectSMTP_PersistsAllowPlaintext(t *testing.T) {
	// A self-hoster who connect-tests a cleartext relay with allow_plaintext=true
	// must have that policy PERSISTED, so every later send applies the same rule
	// the test validated (MAJOR 2). Default input keeps it false (TLS enforced).
	cases := []struct {
		name  string
		allow bool
	}{
		{"plaintext opt-out persisted", true},
		{"default enforces tls", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			svc := NewService(store, &fakeTester{}, newTestKeyring(t), mail.GoogleOAuth{}, nil, mail.MicrosoftOAuth{}, nil)
			in := validConnectInput()
			in.AllowPlaintext = tc.allow
			m, err := svc.ConnectSMTP(context.Background(), uuid.New(), in)
			if err != nil {
				t.Fatalf("ConnectSMTP() error = %v", err)
			}
			store.mu.Lock()
			got := store.lastCreate.AllowPlaintext
			store.mu.Unlock()
			if got != tc.allow {
				t.Fatalf("persisted allow_plaintext = %v, want %v", got, tc.allow)
			}
			if m.AllowPlaintext != tc.allow {
				t.Fatalf("returned mailbox allow_plaintext = %v, want %v", m.AllowPlaintext, tc.allow)
			}
		})
	}
}

func TestConnectSMTP_ConnectionTestFailureDoesNotPersist(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, &fakeTester{smtpErr: errors.New("dial tcp: connection refused")}, newTestKeyring(t), mail.GoogleOAuth{}, nil, mail.MicrosoftOAuth{}, nil)
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
	svc := NewService(store, &fakeTester{}, newTestKeyring(t), mail.GoogleOAuth{}, nil, mail.MicrosoftOAuth{}, nil)
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

// The subtlety this pins: net/mail.ParseAddress SUCCEEDS on "a@ example.com" and
// reports the address as "a@example.com". Parsing without using the result would
// therefore accept the input and still store the raw string, whitespace and all —
// which is the bug, not a fix for it. A whitespace domain in this column has
// already caused a live defect: the ESP sweep projected the untrimmed domain, the
// write-back trimmed it, the two keys disagreed, and a legitimate domain was
// pinned to the wrong ESP.
func TestConnectSMTP_StoresTheCanonicalAddressNotTheRawInput(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"whitespace inside the domain is dropped", "a@ example.com", "a@example.com"},
		{"surrounding whitespace is dropped", "  sender@example.com\t", "sender@example.com"},
		{"a display name is reduced to the address", "Alex <alex@example.com>", "alex@example.com"},
		{"an already-canonical address is unchanged", "sender@example.com", "sender@example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			svc := NewService(store, &fakeTester{}, newTestKeyring(t), mail.GoogleOAuth{}, nil, mail.MicrosoftOAuth{}, nil)
			in := validConnectInput()
			in.Email = tc.raw
			m, err := svc.ConnectSMTP(context.Background(), uuid.New(), in)
			if err != nil {
				t.Fatalf("ConnectSMTP() error = %v", err)
			}
			store.mu.Lock()
			persisted := store.lastCreate.Email
			store.mu.Unlock()
			if persisted != tc.want {
				t.Errorf("persisted email = %q, want %q", persisted, tc.want)
			}
			if m.Email != tc.want {
				t.Errorf("returned email = %q, want %q", m.Email, tc.want)
			}
		})
	}
}

func TestConnectSMTP_RejectsAnUnparseableEmail(t *testing.T) {
	cases := []struct{ name, raw string }{
		{"empty", ""},
		{"whitespace only", "   "},
		{"no at sign", "not-an-email"},
		{"no domain", "a@"},
		{"no local part", "@example.com"},
		{"two addresses", "a@example.com, b@example.com"},
		{"whitespace inside the local part", `"a b"@example.com`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			svc := NewService(store, &fakeTester{}, newTestKeyring(t), mail.GoogleOAuth{}, nil, mail.MicrosoftOAuth{}, nil)
			workspaceID := uuid.New()
			in := validConnectInput()
			in.Email = tc.raw

			_, err := svc.ConnectSMTP(context.Background(), workspaceID, in)
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("ConnectSMTP() error = %v, want ErrValidation", err)
			}
			all, err := store.List(context.Background(), workspaceID)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if len(all) != 0 {
				t.Fatalf("len(all) = %d, want 0 (a rejected address must persist nothing)", len(all))
			}
		})
	}
}

// The usernames default to the mailbox address and are the credentials SMTP AUTH
// and IMAP LOGIN actually present, so they must be defaulted from the CANONICAL
// address — defaulting first and canonicalizing after would log in as
// "a@ example.com" while the row says "a@example.com".
func TestConnectSMTP_DefaultsUsernamesToTheCanonicalAddress(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, &fakeTester{}, newTestKeyring(t), mail.GoogleOAuth{}, nil, mail.MicrosoftOAuth{}, nil)
	in := validConnectInput()
	in.Email = "a@ example.com"

	if _, err := svc.ConnectSMTP(context.Background(), uuid.New(), in); err != nil {
		t.Fatalf("ConnectSMTP() error = %v", err)
	}

	store.mu.Lock()
	got := store.lastCreate
	store.mu.Unlock()
	if got.SmtpUsername != "a@example.com" {
		t.Errorf("smtp_username = %q, want the canonical address", got.SmtpUsername)
	}
	if got.ImapUsername != "a@example.com" {
		t.Errorf("imap_username = %q, want the canonical address", got.ImapUsername)
	}
}

// Dedupe must see the canonical form, or two spellings of one address both get in
// and the workspace sends twice from the same mailbox.
func TestConnectSMTP_DedupesOnTheCanonicalAddress(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, &fakeTester{}, newTestKeyring(t), mail.GoogleOAuth{}, nil, mail.MicrosoftOAuth{}, nil)
	workspaceID := uuid.New()

	in := validConnectInput()
	in.Email = "a@example.com"
	if _, err := svc.ConnectSMTP(context.Background(), workspaceID, in); err != nil {
		t.Fatalf("first ConnectSMTP() error = %v", err)
	}

	in.Email = "a@ example.com"
	if _, err := svc.ConnectSMTP(context.Background(), workspaceID, in); !errors.Is(err, ErrDuplicateMailbox) {
		t.Fatalf("second ConnectSMTP() error = %v, want ErrDuplicateMailbox", err)
	}
}

func TestPauseThenGetShowsPausedStatus(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, &fakeTester{}, newTestKeyring(t), mail.GoogleOAuth{}, nil, mail.MicrosoftOAuth{}, nil)
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
