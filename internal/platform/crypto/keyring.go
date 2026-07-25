package crypto

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// dekBytes is the fixed DEK length (AES-256).
const dekBytes = 32

// dekCacheTTL bounds how long a plaintext DEK lives in the in-memory cache.
// Plaintext DEKs never touch disk; this caps their process-memory exposure. On
// eviction (TTL sweep or access-time expiry) the DEK bytes are zeroized.
const dekCacheTTL = 5 * time.Minute

// ErrDEKNotFound is the sentinel a DEKStore MUST return (wrapped is fine) from
// GetWrappedDEK when no DEK row exists for the workspace, distinguishing a
// first-use miss (Keyring creates a DEK) from a real backend error (surfaced).
var ErrDEKNotFound = errors.New("crypto: workspace DEK not found")

// DEKStore persists wrapped per-workspace DEKs. It is satisfied by the
// sqlc-backed store; the plaintext DEK is never handed to it.
type DEKStore interface {
	// GetWrappedDEK returns the wrapped DEK and the name of the KeyProvider that
	// wrapped it. It MUST return ErrDEKNotFound (possibly wrapped) when no row
	// exists for ws, and any other error for a genuine backend failure.
	GetWrappedDEK(ctx context.Context, ws uuid.UUID) (wrapped []byte, provider string, err error)
	// PutWrappedDEK stores a wrapped DEK fail-if-exists: it MUST return an error
	// (e.g. on a primary-key conflict) rather than overwrite an existing DEK —
	// overwriting would silently invalidate every prior ciphertext.
	PutWrappedDEK(ctx context.Context, ws uuid.UUID, wrapped []byte, provider string) error
}

type dekEntry struct {
	dek []byte
	exp time.Time
}

// Keyring creates, caches, and hands out per-workspace Sealers. The first use of
// a workspace generates a 32-byte DEK, wraps it via the KeyProvider (KEK), and
// persists the wrapped form; thereafter it unwraps the stored DEK. Plaintext
// DEKs live only in the short-TTL in-memory cache.
type Keyring struct {
	provider KeyProvider
	store    DEKStore
	legacy   *Sealer

	mu    sync.Mutex
	cache map[uuid.UUID]dekEntry
}

// NewKeyring builds a Keyring over a KeyProvider (KEK), a DEKStore, and the
// legacy master-key Sealer (may be nil) used to open pre-DEK v1 blobs.
func NewKeyring(provider KeyProvider, store DEKStore, legacy *Sealer) *Keyring {
	return &Keyring{
		provider: provider,
		store:    store,
		legacy:   legacy,
		cache:    make(map[uuid.UUID]dekEntry),
	}
}

// workspaceAAD is the additional authenticated data bound to a workspace. The
// SAME value binds both layers: the wrapped DEK (provider.Wrap/Unwrap) and every
// field ciphertext (the DEK sealer's aad). A blob or DEK from one workspace
// therefore fails to open in another's context.
func workspaceAAD(ws uuid.UUID) []byte {
	return []byte("ws:" + ws.String())
}

// SealerFor returns a Sealer bound to ws's DEK, creating and persisting the DEK
// on first use.
func (k *Keyring) SealerFor(ctx context.Context, ws uuid.UUID) (*Sealer, error) {
	aad := workspaceAAD(ws)

	if dek, ok := k.cacheGet(ws); ok {
		return newDEKSealer(dek, aad, k.legacy), nil
	}

	dek, err := k.loadOrCreate(ctx, ws, aad)
	if err != nil {
		return nil, err
	}
	k.cachePut(ws, dek)
	return newDEKSealer(dek, aad, k.legacy), nil
}

// loadOrCreate returns the plaintext DEK for ws, generating and persisting it on
// first use. aad binds the wrapped DEK to the workspace at the KEK layer.
func (k *Keyring) loadOrCreate(ctx context.Context, ws uuid.UUID, aad []byte) ([]byte, error) {
	wrapped, _ /* provider name: not needed on read, only unwrapped */, err := k.store.GetWrappedDEK(ctx, ws)
	switch {
	case err == nil:
		return k.unwrap(ctx, wrapped, aad)
	case !errors.Is(err, ErrDEKNotFound):
		// Genuine backend error — surface it, do NOT waste crypto creating a DEK
		// that a later Put would reject.
		return nil, err
	}

	// First use for this workspace — generate a DEK. crypto/rand only (never
	// math/rand).
	dek := make([]byte, dekBytes)
	if _, err := rand.Read(dek); err != nil {
		return nil, err
	}
	wrapped, err = k.provider.Wrap(ctx, dek, aad)
	if err != nil {
		return nil, err
	}
	if err := k.store.PutWrappedDEK(ctx, ws, wrapped, k.provider.Name()); err != nil {
		// Fail-if-exists conflict (or write error): another caller/process may
		// have won the race. Re-read and unwrap the winner; if that also fails,
		// wrap both errors so a genuine double-failure is debuggable.
		winner, _, gerr := k.store.GetWrappedDEK(ctx, ws)
		if gerr != nil {
			return nil, fmt.Errorf("put dek (and re-get failed: %w): %w", gerr, err)
		}
		return k.unwrap(ctx, winner, aad)
	}
	return dek, nil
}

// unwrap unwraps a stored DEK under the workspace aad and validates its length.
func (k *Keyring) unwrap(ctx context.Context, wrapped, aad []byte) ([]byte, error) {
	dek, err := k.provider.Unwrap(ctx, wrapped, aad)
	if err != nil {
		return nil, err
	}
	if len(dek) != dekBytes {
		return nil, fmt.Errorf("crypto: unwrapped DEK is %d bytes, want %d", len(dek), dekBytes)
	}
	return dek, nil
}

func (k *Keyring) cacheGet(ws uuid.UUID) ([]byte, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	e, ok := k.cache[ws]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.exp) {
		zeroize(e.dek)
		delete(k.cache, ws)
		return nil, false
	}
	return e.dek, true
}

func (k *Keyring) cachePut(ws uuid.UUID, dek []byte) {
	k.mu.Lock()
	defer k.mu.Unlock()
	// Sweep expired entries first: bounds the map to currently-live workspaces
	// and wipes plaintext DEKs at TTL rather than only on the next lookup.
	now := time.Now()
	for w, e := range k.cache {
		if now.After(e.exp) {
			zeroize(e.dek)
			delete(k.cache, w)
		}
	}
	k.cache[ws] = dekEntry{dek: dek, exp: now.Add(dekCacheTTL)}
}

// zeroize wipes a plaintext DEK's bytes before the entry is dropped.
func zeroize(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
