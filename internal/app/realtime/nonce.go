package realtime

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// noncePrefix namespaces burnt ticket nonces in Redis. Distinct from the hub's
// own `ws.ch:` keys so a flush of one never touches the other.
const noncePrefix = "ws.nonce:"

// TicketBurner records that a connect ticket's nonce has been spent, so the same
// ticket cannot open a second socket.
//
// An interface at the seam rather than a concrete Redis type: the handshake's
// tests need to drive the "already spent" and "the store is down" branches
// without a server, and those are the two branches that actually matter.
type TicketBurner interface {
	// Burn returns true if this nonce had not been seen before — i.e. the caller
	// now owns the ticket. It returns false if the nonce was already spent, and a
	// non-nil error if it could not be determined, which callers MUST treat as a
	// refusal rather than a pass.
	Burn(ctx context.Context, nonce string, ttl time.Duration) (bool, error)
}

// RedisTicketBurner burns nonces with SETNX plus the ticket's own TTL, so the
// keyspace self-cleans: an unspent nonce expires exactly when the ticket it
// belongs to does, and a spent one is only useful to remember for that long.
type RedisTicketBurner struct {
	rdb *redis.Client
}

func NewRedisTicketBurner(rdb *redis.Client) *RedisTicketBurner {
	return &RedisTicketBurner{rdb: rdb}
}

// Burn is a single SETNX. Atomicity is the whole point: two sockets racing on
// the same ticket both call this, and exactly one can be told it won — which is
// what makes "single-use" true under concurrency rather than only in a
// sequential test.
func (b *RedisTicketBurner) Burn(ctx context.Context, nonce string, ttl time.Duration) (bool, error) {
	// A non-positive TTL would mean SETNX with no expiry, leaking the key
	// forever. It also means the ticket is already dead, so refuse rather than
	// write: Parse rejects an expired ticket before we get here, and a caller
	// that skipped that check must not be quietly rescued.
	if ttl <= 0 {
		return false, fmt.Errorf("realtime: refusing to burn a nonce with a non-positive ttl")
	}
	ok, err := b.rdb.SetNX(ctx, noncePrefix+nonce, 1, ttl).Result()
	if err != nil {
		// Fail closed. A Redis outage must not turn every ticket into a reusable
		// credential — the same reasoning platform/ratelimit uses.
		return false, fmt.Errorf("realtime: burn nonce: %w", err)
	}
	return ok, nil
}
