package emailotp

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/notify"
)

// otpRow is the in-memory analogue of an email_otp_codes row.
type otpRow struct {
	id          uuid.UUID
	userID      uuid.UUID
	codeHash    string
	attempts    int32
	maxAttempts int32
	consumed    bool
	expiresAt   time.Time
}

// fakeStore is an in-memory Store for unit tests — no DB. It reproduces the atomic
// semantics the real queries guarantee (claim-under-cap, single-use consume,
// one-active-code) so the service's fail-closed logic is exercised faithfully.
type fakeStore struct {
	mu         sync.Mutex
	users      map[string]uuid.UUID // email -> user id
	rows       map[uuid.UUID]*otpRow
	failLookup error // when set, GetUserIDByEmail returns it (a non-NoRows DB error)
}

func newFakeStore() *fakeStore {
	return &fakeStore{users: map[string]uuid.UUID{}, rows: map[uuid.UUID]*otpRow{}}
}

func (f *fakeStore) addUser(email string) uuid.UUID {
	id := uuid.New()
	f.users[email] = id
	return id
}

func (f *fakeStore) GetUserIDByEmail(_ context.Context, email string) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failLookup != nil {
		return uuid.Nil, f.failLookup
	}
	id, ok := f.users[email]
	if !ok {
		return uuid.Nil, pgx.ErrNoRows
	}
	return id, nil
}

func (f *fakeStore) ReplaceActiveCode(_ context.Context, userID uuid.UUID, codeHash string, maxAttempts int32, expiresAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, r := range f.rows {
		if r.userID == userID && !r.consumed {
			delete(f.rows, id)
		}
	}
	id := uuid.New()
	f.rows[id] = &otpRow{id: id, userID: userID, codeHash: codeHash, maxAttempts: maxAttempts, expiresAt: expiresAt}
	return nil
}

func (f *fakeStore) GetActiveCode(_ context.Context, userID uuid.UUID) (gen.GetActiveEmailOTPRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.userID == userID && !r.consumed {
			return gen.GetActiveEmailOTPRow{
				ID:          r.id,
				CodeHash:    r.codeHash,
				Attempts:    r.attempts,
				MaxAttempts: r.maxAttempts,
				ExpiresAt:   pgxTimestamp(r.expiresAt),
			}, nil
		}
	}
	return gen.GetActiveEmailOTPRow{}, pgx.ErrNoRows
}

func (f *fakeStore) ClaimCodeAttempt(_ context.Context, id uuid.UUID) (int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok || r.consumed || r.attempts >= r.maxAttempts {
		return 0, pgx.ErrNoRows // dead: mirrors the UPDATE ... WHERE attempts < max
	}
	r.attempts++
	return r.attempts, nil
}

func (f *fakeStore) ConsumeCode(_ context.Context, id uuid.UUID) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok || r.consumed {
		return 0, nil
	}
	r.consumed = true
	return 1, nil
}

// activeCodeCount reports how many live codes a user has (for the one-active-code
// assertion).
func (f *fakeStore) activeCodeCount(userID uuid.UUID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.rows {
		if r.userID == userID && !r.consumed {
			n++
		}
	}
	return n
}

// captureSender records every message it is asked to send (so a test can read the
// code out of the delivered email — the raw code is never stored).
type captureSender struct {
	mu   sync.Mutex
	sent []notify.Message
	err  error
}

func (c *captureSender) Send(_ context.Context, m notify.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.sent = append(c.sent, m)
	return nil
}

func (c *captureSender) last() (notify.Message, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) == 0 {
		return notify.Message{}, false
	}
	return c.sent[len(c.sent)-1], true
}

// newTestService builds a Service whose dispatch runs INLINE (deterministic) with
// a frozen clock, over the given store and sender.
func newTestService(store Store, sender notify.Sender, now time.Time) *Service {
	s := NewService(store, sender)
	s.dispatch = func(f func()) { f() }
	s.now = func() time.Time { return now }
	return s
}
