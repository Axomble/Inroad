package realtime

import (
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// Default connection caps (spec §7.5): an unbounded socket count is a trivial
// resource-exhaustion vector, since each connection costs a goroutine, a 256-
// entry buffer and a registry slot.
//
// Per USER rather than per browser tab: a handful of tabs plus a phone is
// normal, a hundred is a script. Per WORKSPACE bounds one tenant's total cost so
// it cannot starve the node for every other tenant on it.
const (
	DefaultMaxPerUser      = 8
	DefaultMaxPerWorkspace = 200
)

// ErrTooManyConnections is returned when a cap is reached. The handshake maps it
// to 429 rather than 503: the client should back off and retry, not conclude the
// server is broken.
var ErrTooManyConnections = fmt.Errorf("realtime: too many connections")

// connCounter tracks live socket counts per user and per workspace so the
// handshake can refuse before allocating anything expensive.
//
// It counts CONNECTIONS, not sessions: one user with four tabs holds four. The
// release function returned by acquire is what keeps the count honest, and it is
// idempotent so a double-release (a deferred call plus an error path that
// already released) cannot drive a counter negative and permanently free
// capacity that is still in use.
type connCounter struct {
	mu                   sync.Mutex
	perUser              map[uuid.UUID]int
	perWorkspace         map[uuid.UUID]int
	maxPerUser           int
	maxPerWorkspaceLimit int
}

func newConnCounter(maxPerUser, maxPerWorkspace int) *connCounter {
	if maxPerUser <= 0 {
		maxPerUser = DefaultMaxPerUser
	}
	if maxPerWorkspace <= 0 {
		maxPerWorkspace = DefaultMaxPerWorkspace
	}
	return &connCounter{
		perUser:              map[uuid.UUID]int{},
		perWorkspace:         map[uuid.UUID]int{},
		maxPerUser:           maxPerUser,
		maxPerWorkspaceLimit: maxPerWorkspace,
	}
}

// acquire reserves one slot for (workspaceID, userID), returning a release func.
//
// Both caps are checked BEFORE either counter is incremented, so a request that
// is refused leaves no trace — an earlier version that incremented then rolled
// back would let a burst of refused attempts transiently exceed the workspace
// cap and evict legitimate connections.
func (c *connCounter) acquire(workspaceID, userID uuid.UUID) (release func(), err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.perUser[userID] >= c.maxPerUser {
		return nil, fmt.Errorf("%w: user at %d", ErrTooManyConnections, c.maxPerUser)
	}
	if c.perWorkspace[workspaceID] >= c.maxPerWorkspaceLimit {
		return nil, fmt.Errorf("%w: workspace at %d", ErrTooManyConnections, c.maxPerWorkspaceLimit)
	}

	c.perUser[userID]++
	c.perWorkspace[workspaceID]++

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			// Delete at zero rather than leaving a 0 entry: these maps are keyed by
			// user and workspace id, so a long-lived process that never deletes grows
			// one entry per user who ever connected.
			if c.perUser[userID] <= 1 {
				delete(c.perUser, userID)
			} else {
				c.perUser[userID]--
			}
			if c.perWorkspace[workspaceID] <= 1 {
				delete(c.perWorkspace, workspaceID)
			} else {
				c.perWorkspace[workspaceID]--
			}
		})
	}, nil
}

// counts reports the live totals for one (workspace, user) pair. Test-only
// visibility into the maps, so a test can assert a release actually released
// rather than inferring it from a subsequent acquire succeeding.
func (c *connCounter) counts(workspaceID, userID uuid.UUID) (user, workspace int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.perUser[userID], c.perWorkspace[workspaceID]
}
