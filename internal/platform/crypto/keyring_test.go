package crypto

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeDEKStore is an in-memory DEKStore with fail-if-exists Put semantics, plus
// hooks to simulate races (forced Get misses) and write failures.
type fakeDEKStore struct {
	mu           sync.Mutex
	rows         map[uuid.UUID]fakeDEKRow
	puts         int
	forceGetMiss int   // pretend the row is absent for the next N Gets
	putErr       error // if set, Put returns this without inserting
	getErr       error // if set, Get returns this (a real backend error) before any lookup
}

type fakeDEKRow struct {
	wrapped  []byte
	provider string
}

func newFakeDEKStore() *fakeDEKStore {
	return &fakeDEKStore{rows: make(map[uuid.UUID]fakeDEKRow)}
}

func (f *fakeDEKStore) GetWrappedDEK(_ context.Context, ws uuid.UUID) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, "", f.getErr
	}
	if f.forceGetMiss > 0 {
		f.forceGetMiss--
		return nil, "", fmt.Errorf("simulated race: %w", ErrDEKNotFound)
	}
	r, ok := f.rows[ws]
	if !ok {
		return nil, "", fmt.Errorf("no row: %w", ErrDEKNotFound)
	}
	return append([]byte(nil), r.wrapped...), r.provider, nil
}

func (f *fakeDEKStore) PutWrappedDEK(_ context.Context, ws uuid.UUID, wrapped []byte, provider string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	if f.putErr != nil {
		return f.putErr
	}
	if _, ok := f.rows[ws]; ok {
		return errors.New("dek exists")
	}
	f.rows[ws] = fakeDEKRow{wrapped: append([]byte(nil), wrapped...), provider: provider}
	return nil
}

func (f *fakeDEKStore) rowCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func newTestKeyring(t *testing.T, store DEKStore, legacy *Sealer) *Keyring {
	t.Helper()
	provider, err := NewLocalKeyProvider(key32())
	if err != nil {
		t.Fatalf("NewLocalKeyProvider: %v", err)
	}
	return NewKeyring(provider, store, legacy)
}

func TestKeyringSealerForCreatesThenReuses(t *testing.T) {
	ctx := context.Background()
	store := newFakeDEKStore()
	kr := newTestKeyring(t, store, nil)
	ws := uuid.New()

	s1, err := kr.SealerFor(ctx, ws)
	if err != nil {
		t.Fatalf("SealerFor #1: %v", err)
	}
	if store.puts != 1 {
		t.Fatalf("first SealerFor: want 1 Put, got %d", store.puts)
	}
	if store.rowCount() != 1 {
		t.Fatalf("want 1 persisted DEK, got %d", store.rowCount())
	}

	// Second call for the same workspace: cache hit, no extra Put.
	s2, err := kr.SealerFor(ctx, ws)
	if err != nil {
		t.Fatalf("SealerFor #2: %v", err)
	}
	if store.puts != 1 {
		t.Fatalf("second SealerFor should not Put again, got %d", store.puts)
	}

	// The two sealers share the same DEK+AAD and interoperate.
	tok, err := s1.Seal([]byte("secret"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := s2.Open(tok)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(got) != "secret" {
		t.Fatalf("round-trip mismatch: %q", got)
	}

	// A fresh Keyring over the same store reuses the persisted DEK (Get path,
	// no new Put).
	kr2 := newTestKeyring(t, store, nil)
	s3, err := kr2.SealerFor(ctx, ws)
	if err != nil {
		t.Fatalf("SealerFor via fresh keyring: %v", err)
	}
	if store.puts != 1 {
		t.Fatalf("reuse via Get should not Put, got %d", store.puts)
	}
	got2, err := s3.Open(tok)
	if err != nil {
		t.Fatalf("Open via reused DEK: %v", err)
	}
	if string(got2) != "secret" {
		t.Fatalf("reuse mismatch: %q", got2)
	}
}

func TestKeyringSealerForRace(t *testing.T) {
	ctx := context.Background()
	store := newFakeDEKStore()
	kr := newTestKeyring(t, store, nil)
	ws := uuid.New()

	const n = 8
	sealers := make([]*Sealer, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			sealers[i], errs[i] = kr.SealerFor(ctx, ws)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("SealerFor goroutine %d: %v", i, err)
		}
	}
	// Exactly one DEK wins the race.
	if store.rowCount() != 1 {
		t.Fatalf("want exactly 1 persisted DEK, got %d", store.rowCount())
	}
	// Every returned sealer resolved to the same winning DEK: seal with one,
	// open with all the others.
	tok, err := sealers[0].Seal([]byte("shared"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	for i := 1; i < n; i++ {
		got, err := sealers[i].Open(tok)
		if err != nil {
			t.Fatalf("sealer %d could not open sealer 0's blob: %v", i, err)
		}
		if string(got) != "shared" {
			t.Fatalf("sealer %d mismatch: %q", i, got)
		}
	}
}

func TestKeyringSealerForAADMismatch(t *testing.T) {
	ctx := context.Background()
	store := newFakeDEKStore()
	kr := newTestKeyring(t, store, nil)
	wsA, wsB := uuid.New(), uuid.New()

	sa, err := kr.SealerFor(ctx, wsA)
	if err != nil {
		t.Fatalf("SealerFor A: %v", err)
	}
	sb, err := kr.SealerFor(ctx, wsB)
	if err != nil {
		t.Fatalf("SealerFor B: %v", err)
	}

	tok, err := sa.Seal([]byte("workspace-A-secret"))
	if err != nil {
		t.Fatalf("Seal A: %v", err)
	}
	// Different DEK AND different AAD => B cannot open A's blob.
	if _, err := sb.Open(tok); err == nil {
		t.Fatal("expected cross-workspace Open to fail (AAD + DEK mismatch)")
	}
}

func TestKeyringRejectsWrongProviderWrappedDEK(t *testing.T) {
	ctx := context.Background()
	store := newFakeDEKStore()
	ws := uuid.New()

	// Persist a DEK wrapped by provider A (master key key32).
	krA := newTestKeyring(t, store, nil)
	if _, err := krA.SealerFor(ctx, ws); err != nil {
		t.Fatalf("SealerFor A: %v", err)
	}

	// A keyring backed by a different master key cannot unwrap A's DEK.
	providerB, err := NewLocalKeyProvider(bytesRepeat(0x22, 32))
	if err != nil {
		t.Fatalf("NewLocalKeyProvider B: %v", err)
	}
	krB := NewKeyring(providerB, store, nil)
	if _, err := krB.SealerFor(ctx, ws); err == nil {
		t.Fatal("expected Unwrap under wrong provider key to fail")
	}
}

func TestKeyringPutRacePathRecoversWinner(t *testing.T) {
	ctx := context.Background()
	store := newFakeDEKStore()
	ws := uuid.New()
	aad := workspaceAAD(ws)

	// Simulate a winner that already persisted a DEK for this workspace, but
	// arrange for the keyring's first Get to miss (as if it read before the
	// winner's commit) so it takes the create->Put-conflict->re-Get path.
	provider, err := NewLocalKeyProvider(key32())
	if err != nil {
		t.Fatalf("NewLocalKeyProvider: %v", err)
	}
	winnerDEK := bytesRepeat(0x33, 32)
	winnerWrapped, err := provider.Wrap(ctx, winnerDEK, aad)
	if err != nil {
		t.Fatalf("Wrap winner: %v", err)
	}
	store.rows[ws] = fakeDEKRow{wrapped: winnerWrapped, provider: provider.Name()}
	store.forceGetMiss = 1 // first Get misses -> create path -> Put conflict -> re-Get

	kr := NewKeyring(provider, store, nil)
	s, err := kr.SealerFor(ctx, ws)
	if err != nil {
		t.Fatalf("SealerFor race path: %v", err)
	}

	// The returned sealer must be bound to the WINNER's DEK, not the loser's.
	winnerSealer := newDEKSealer(winnerDEK, aad, nil)
	tok, err := winnerSealer.Seal([]byte("winner-only"))
	if err != nil {
		t.Fatalf("Seal winner: %v", err)
	}
	got, err := s.Open(tok)
	if err != nil {
		t.Fatalf("keyring sealer failed to open winner blob: %v", err)
	}
	if string(got) != "winner-only" {
		t.Fatalf("race path resolved wrong DEK: %q", got)
	}
}

func TestKeyringPutFailsAndReGetFails(t *testing.T) {
	ctx := context.Background()
	store := newFakeDEKStore()
	store.putErr = errors.New("db write failed")
	store.forceGetMiss = 2 // both the initial Get and the re-Get miss

	kr := newTestKeyring(t, store, nil)
	if _, err := kr.SealerFor(ctx, uuid.New()); err == nil {
		t.Fatal("expected SealerFor to surface the Put error when re-Get also fails")
	}
}

func TestKeyringCacheTTLExpiryIsSweptAndZeroized(t *testing.T) {
	ctx := context.Background()
	store := newFakeDEKStore()
	kr := newTestKeyring(t, store, nil)
	ws := uuid.New()

	// Seed a STALE cache entry (exp in the past) holding a bogus DEK that does
	// not match anything the store will hand back.
	stale := bytesRepeat(0x77, 32)
	kr.cache[ws] = dekEntry{dek: stale, exp: time.Now().Add(-time.Minute)}

	// SealerFor must NOT serve the stale entry; it goes to the store (a Put
	// happens because the workspace has no persisted DEK yet).
	s, err := kr.SealerFor(ctx, ws)
	if err != nil {
		t.Fatalf("SealerFor: %v", err)
	}
	if store.puts != 1 {
		t.Fatalf("stale entry should have been ignored (store consulted), puts=%d", store.puts)
	}

	// The stale DEK's backing array was zeroized on eviction.
	if !bytes.Equal(stale, make([]byte, 32)) {
		t.Fatalf("expected evicted DEK to be zeroized, got %x", stale)
	}

	// The live sealer uses the freshly created DEK, not the stale one.
	tok, err := s.Seal([]byte("fresh"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if got, err := s.Open(tok); err != nil || string(got) != "fresh" {
		t.Fatalf("round-trip: got %q err %v", got, err)
	}
}

func TestKeyringSealerForSurfacesRealStoreError(t *testing.T) {
	ctx := context.Background()
	store := newFakeDEKStore()
	sentinel := errors.New("connection refused")
	store.getErr = sentinel // a real backend error, NOT ErrDEKNotFound

	kr := newTestKeyring(t, store, nil)
	_, err := kr.SealerFor(ctx, uuid.New())
	if err == nil {
		t.Fatal("expected a real store error to surface")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("want the backend error, got %v", err)
	}
	if store.puts != 0 {
		t.Fatalf("must not attempt create on a real Get error, puts=%d", store.puts)
	}
}

func TestKeyringSealerLegacyThenV2(t *testing.T) {
	ctx := context.Background()
	master := key32()
	legacy, err := NewSealer(master)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	// A value written under the old master-key sealer (v1, no version prefix).
	v1tok, err := legacy.Seal([]byte("legacy-secret"))
	if err != nil {
		t.Fatalf("legacy Seal: %v", err)
	}

	store := newFakeDEKStore()
	kr := newTestKeyring(t, store, legacy)
	ws := uuid.New()
	s, err := kr.SealerFor(ctx, ws)
	if err != nil {
		t.Fatalf("SealerFor: %v", err)
	}

	// The workspace sealer opens the legacy v1 blob via its legacy fallback.
	got, err := s.Open(v1tok)
	if err != nil {
		t.Fatalf("Open legacy via DEK sealer: %v", err)
	}
	if string(got) != "legacy-secret" {
		t.Fatalf("legacy open mismatch: %q", got)
	}

	// A fresh Seal by the workspace sealer is v2 (0x02 prefix), i.e. the value
	// migrates forward on the next write.
	v2tok, err := s.Seal([]byte("legacy-secret"))
	if err != nil {
		t.Fatalf("v2 Seal: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(v2tok)
	if err != nil {
		t.Fatalf("decode v2: %v", err)
	}
	if len(raw) == 0 || raw[0] != 0x02 {
		t.Fatalf("expected v2 0x02 prefix, got %x", raw)
	}
}
