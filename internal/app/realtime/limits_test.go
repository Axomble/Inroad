package realtime

import (
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
)

var (
	wsA   = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	wsB   = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	userA = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	userB = uuid.MustParse("44444444-4444-4444-4444-444444444444")
)

func TestConnCounter_AllowsUpToThePerUserCapThenRefuses(t *testing.T) {
	c := newConnCounter(2, 100)

	for i := range 2 {
		if _, err := c.acquire(wsA, userA); err != nil {
			t.Fatalf("acquire %d: %v", i+1, err)
		}
	}
	_, err := c.acquire(wsA, userA)
	if !errors.Is(err, ErrTooManyConnections) {
		t.Errorf("third acquire err = %v, want ErrTooManyConnections", err)
	}
}

// A release must actually free capacity — asserted on the counters directly
// rather than inferred from a later acquire succeeding, so a release that
// decremented the WRONG key would still fail this.
func TestConnCounter_ReleaseFreesCapacity(t *testing.T) {
	c := newConnCounter(1, 100)

	release, err := c.acquire(wsA, userA)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if user, workspace := c.counts(wsA, userA); user != 1 || workspace != 1 {
		t.Fatalf("after acquire: user=%d workspace=%d, want 1 and 1", user, workspace)
	}

	release()

	if user, workspace := c.counts(wsA, userA); user != 0 || workspace != 0 {
		t.Errorf("after release: user=%d workspace=%d, want 0 and 0", user, workspace)
	}
	if _, err := c.acquire(wsA, userA); err != nil {
		t.Errorf("acquire after release: %v", err)
	}
}

// Release is idempotent. A handler with a deferred release plus an error path
// that already released would otherwise drive the counter negative and hand out
// capacity that is still in use — the bug that makes a cap silently stop capping.
func TestConnCounter_DoubleReleaseDoesNotFreeCapacityTwice(t *testing.T) {
	c := newConnCounter(2, 100)

	release, err := c.acquire(wsA, userA)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := c.acquire(wsA, userA); err != nil {
		t.Fatalf("second acquire: %v", err)
	}

	release()
	release() // the double release under test

	if user, _ := c.counts(wsA, userA); user != 1 {
		t.Errorf("user count = %d after a double release, want 1 (one connection is still live)", user)
	}
}

// The per-workspace cap bounds one tenant's total cost so it cannot starve the
// node for every other tenant on it — independently of the per-user cap.
func TestConnCounter_WorkspaceCapAppliesAcrossDifferentUsers(t *testing.T) {
	c := newConnCounter(100, 2)

	if _, err := c.acquire(wsA, userA); err != nil {
		t.Fatalf("acquire userA: %v", err)
	}
	if _, err := c.acquire(wsA, userB); err != nil {
		t.Fatalf("acquire userB: %v", err)
	}

	// A third user in the same workspace is over the workspace cap even though no
	// user is near the per-user cap.
	thirdUser := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	if _, err := c.acquire(wsA, thirdUser); !errors.Is(err, ErrTooManyConnections) {
		t.Errorf("err = %v, want ErrTooManyConnections", err)
	}
}

// One workspace hitting its cap must not affect another — the tenant-isolation
// property of the limiter itself.
func TestConnCounter_OneWorkspaceAtItsCapDoesNotBlockAnother(t *testing.T) {
	c := newConnCounter(100, 1)

	if _, err := c.acquire(wsA, userA); err != nil {
		t.Fatalf("acquire wsA: %v", err)
	}
	if _, err := c.acquire(wsA, userB); !errors.Is(err, ErrTooManyConnections) {
		t.Fatalf("wsA should be at its cap, got err = %v", err)
	}
	if _, err := c.acquire(wsB, userA); err != nil {
		t.Errorf("wsB was blocked by wsA's cap: %v", err)
	}
}

// A refused acquire must leave no trace. An implementation that incremented then
// rolled back would let a burst of refusals transiently exceed the cap.
func TestConnCounter_ARefusedAcquireDoesNotIncrementAnything(t *testing.T) {
	c := newConnCounter(1, 100)

	if _, err := c.acquire(wsA, userA); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	for range 5 {
		if _, err := c.acquire(wsA, userA); !errors.Is(err, ErrTooManyConnections) {
			t.Fatalf("expected refusal, got %v", err)
		}
	}

	if user, workspace := c.counts(wsA, userA); user != 1 || workspace != 1 {
		t.Errorf("after 5 refusals: user=%d workspace=%d, want 1 and 1", user, workspace)
	}
}

func TestConnCounter_ZeroCapsTakeTheDefaults(t *testing.T) {
	c := newConnCounter(0, 0)

	if c.maxPerUser != DefaultMaxPerUser {
		t.Errorf("maxPerUser = %d, want %d", c.maxPerUser, DefaultMaxPerUser)
	}
	if c.maxPerWorkspaceLimit != DefaultMaxPerWorkspace {
		t.Errorf("maxPerWorkspaceLimit = %d, want %d", c.maxPerWorkspaceLimit, DefaultMaxPerWorkspace)
	}
}

// The counter is reached from every connection goroutine on the node, so the
// -race detector over concurrent acquire/release is the point of this test.
func TestConnCounter_IsSafeUnderConcurrentUse(t *testing.T) {
	c := newConnCounter(50, 500)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			release, err := c.acquire(wsA, userA)
			if err != nil {
				return // at the cap; not a failure
			}
			release()
		}(i)
	}
	wg.Wait()

	if user, workspace := c.counts(wsA, userA); user != 0 || workspace != 0 {
		t.Errorf("after all releases: user=%d workspace=%d, want 0 and 0", user, workspace)
	}
}
