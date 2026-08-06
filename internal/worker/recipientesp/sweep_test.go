package recipientesp

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
)

// sweepCore is a fake Core: the three-method seam the handler depends on, so
// this file never restates the whole coreapi.Client surface.
type sweepCore struct {
	stale    []coreapi.RecipientDomainRef
	listErr  error
	writeErr error
	purgeErr error

	staleAfter time.Duration
	retention  time.Duration
	purges     int
	recorded   []coreapi.RecipientDomainESP
}

func (c *sweepCore) ListStaleRecipientDomains(_ context.Context, staleAfter time.Duration) ([]coreapi.RecipientDomainRef, error) {
	c.staleAfter = staleAfter
	return c.stale, c.listErr
}

func (c *sweepCore) RecordRecipientDomainESP(_ context.Context, in coreapi.RecipientDomainESP) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	c.recorded = append(c.recorded, in)
	return nil
}

func (c *sweepCore) PurgeExpiredRecipientDomains(_ context.Context, retention time.Duration) (int64, error) {
	c.retention = retention
	c.purges++
	return 3, c.purgeErr
}

type fakeResolver struct {
	mx    map[string][]*net.MX
	errs  map[string]error
	calls []string
}

func (f *fakeResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	f.calls = append(f.calls, name)
	if err, ok := f.errs[name]; ok {
		return nil, err
	}
	return f.mx[name], nil
}

func run(t *testing.T, core *sweepCore, res *fakeResolver) error {
	t.Helper()
	return SweepHandler(core, res, DefaultStaleAfter, DefaultRetention)(context.Background(), nil)
}

func TestSweepClassifiesEachStaleDomainPerWorkspace(t *testing.T) {
	core := &sweepCore{stale: []coreapi.RecipientDomainRef{
		{WorkspaceID: "ws-1", Domain: "acme.test"},
		{WorkspaceID: "ws-2", Domain: "contoso.test"},
		{WorkspaceID: "ws-2", Domain: "relayed.test"},
	}}
	res := &fakeResolver{mx: map[string][]*net.MX{
		"acme.test":      {{Host: "aspmx.l.google.com", Pref: 10}},
		"contoso.test":   {{Host: "contoso.mail.protection.outlook.com", Pref: 10}},
		"relayed.test":   {{Host: "mx.mailgun.org", Pref: 10}},
		"unrelated.test": nil,
	}}

	if err := run(t, core, res); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if core.staleAfter != DefaultStaleAfter || core.retention != DefaultRetention {
		t.Fatalf("windows = (%v, %v), want (%v, %v)",
			core.staleAfter, core.retention, DefaultStaleAfter, DefaultRetention)
	}
	if len(core.recorded) != 3 {
		t.Fatalf("recorded %d results, want 3: %+v", len(core.recorded), core.recorded)
	}
	want := []coreapi.RecipientDomainESP{
		{WorkspaceID: "ws-1", Domain: "acme.test", ESP: "google", MXHost: "aspmx.l.google.com"},
		{WorkspaceID: "ws-2", Domain: "contoso.test", ESP: "microsoft", MXHost: "contoso.mail.protection.outlook.com"},
		{WorkspaceID: "ws-2", Domain: "relayed.test", ESP: "other", MXHost: "mx.mailgun.org"},
	}
	for i, w := range want {
		if core.recorded[i] != w {
			t.Errorf("recorded[%d] = %+v, want %+v", i, core.recorded[i], w)
		}
	}
}

// The rule the whole cache design rests on: a resolver blip is reported to
// nobody, so it can neither overwrite a good answer nor stamp checked_at (which
// would hide the domain from the next sweep for a full staleness window). An
// NXDOMAIN is the opposite — a completed negative — and must be persisted so it
// is not re-resolved every tick.
func TestSweepReportsCompletedNegativesButNeverAFailedLookup(t *testing.T) {
	core := &sweepCore{stale: []coreapi.RecipientDomainRef{
		{WorkspaceID: "ws-1", Domain: "timeout.test"},
		{WorkspaceID: "ws-1", Domain: "broken.test"},
		{WorkspaceID: "ws-1", Domain: "gone.test"},
	}}
	res := &fakeResolver{errs: map[string]error{
		"timeout.test": &net.DNSError{Err: "i/o timeout", IsTimeout: true},
		"broken.test":  errors.New("resolver exploded"),
		"gone.test":    &net.DNSError{Err: "no such host", IsNotFound: true},
	}}

	if err := run(t, core, res); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(core.recorded) != 1 {
		t.Fatalf("recorded %+v, want only the completed negative", core.recorded)
	}
	if core.recorded[0].Domain != "gone.test" || core.recorded[0].ESP != "other" {
		t.Errorf("recorded = %+v, want gone.test classified other", core.recorded[0])
	}
}

// One domain's write failure must not abort the pass: the rest of the deployment's
// domains are independent, and an unreported one simply stays stale.
func TestSweepContinuesPastAWriteFailure(t *testing.T) {
	core := &sweepCore{
		stale: []coreapi.RecipientDomainRef{
			{WorkspaceID: "ws-1", Domain: "a.test"},
			{WorkspaceID: "ws-1", Domain: "b.test"},
		},
		writeErr: errors.New("db down"),
	}
	res := &fakeResolver{mx: map[string][]*net.MX{
		"a.test": {{Host: "aspmx.l.google.com"}},
		"b.test": {{Host: "aspmx.l.google.com"}},
	}}

	if err := run(t, core, res); err != nil {
		t.Fatalf("a per-domain write failure must not fail the tick: %v", err)
	}
	if len(res.calls) != 2 {
		t.Errorf("resolver calls = %v, want both domains attempted", res.calls)
	}
}

// Eviction is the half of the retention policy that keeps a table sized by the
// contact list from growing without bound, so unlike everything else in the tick
// its failure is fatal — asynq then retries rather than logging it away.
func TestSweepEvictsFirstAndFailsTheTickWhenEvictionFails(t *testing.T) {
	core := &sweepCore{
		stale:    []coreapi.RecipientDomainRef{{WorkspaceID: "ws-1", Domain: "a.test"}},
		purgeErr: errors.New("delete failed"),
	}
	res := &fakeResolver{mx: map[string][]*net.MX{"a.test": {{Host: "aspmx.l.google.com"}}}}

	if err := run(t, core, res); err == nil {
		t.Fatal("a failed eviction must fail the tick")
	}
	if core.purges != 1 {
		t.Errorf("purges = %d, want 1", core.purges)
	}
	if len(res.calls) != 0 {
		t.Errorf("resolver called %v after a failed eviction; eviction runs first", res.calls)
	}
}

// A listing failure is fatal for the same reason asynq retries exist: nothing was
// looked at, so retrying is the only way the tick means anything.
func TestSweepFailsWhenTheStaleListCannotBeRead(t *testing.T) {
	core := &sweepCore{listErr: errors.New("db down")}
	if err := run(t, core, &fakeResolver{}); err == nil {
		t.Fatal("a failed listing must fail the tick")
	}
}

// A cancelled tick (worker shutdown, asynq deadline) stops cleanly rather than
// erroring: the domains not reached were never reported, so they are still stale
// and the next tick takes them.
func TestSweepStopsCleanlyWhenCancelled(t *testing.T) {
	core := &sweepCore{stale: []coreapi.RecipientDomainRef{
		{WorkspaceID: "ws-1", Domain: "a.test"},
		{WorkspaceID: "ws-1", Domain: "b.test"},
	}}
	res := &fakeResolver{mx: map[string][]*net.MX{"a.test": {{Host: "aspmx.l.google.com"}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := SweepHandler(core, res, DefaultStaleAfter, DefaultRetention)(ctx, nil); err != nil {
		t.Fatalf("a cancelled tick is not a failure: %v", err)
	}
	if len(res.calls) != 0 {
		t.Errorf("resolver called %v after cancellation", res.calls)
	}
}

// Retention must outlast staleness by a clear margin, or a domain that is still
// being mailed would be evicted between refreshes and re-resolved from scratch
// every window — the cache would churn instead of caching.
func TestRetentionOutlastsStaleness(t *testing.T) {
	if DefaultRetention <= 2*DefaultStaleAfter {
		t.Errorf("retention %v must exceed two staleness windows of %v", DefaultRetention, DefaultStaleAfter)
	}
}
