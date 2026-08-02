package domainauth

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
)

// sweepCore is a fake Core: the two-method seam the handler depends on, so this
// file never has to restate the whole coreapi.Client surface.
type sweepCore struct {
	stale     []coreapi.SendingDomainRef
	listErr   error
	recordErr error

	staleAfter time.Duration
	recorded   []coreapi.SendingDomainAuth
}

func (c *sweepCore) ListStaleSendingDomains(_ context.Context, staleAfter time.Duration) ([]coreapi.SendingDomainRef, error) {
	c.staleAfter = staleAfter
	return c.stale, c.listErr
}

func (c *sweepCore) RecordSendingDomainAuth(_ context.Context, in coreapi.SendingDomainAuth) error {
	if c.recordErr != nil {
		return c.recordErr
	}
	c.recorded = append(c.recorded, in)
	return nil
}

type fakeResolver struct {
	txt   map[string][]string
	errs  map[string]error
	calls []string
}

func (f *fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	f.calls = append(f.calls, name)
	if err, ok := f.errs[name]; ok {
		return nil, err
	}
	if txt, ok := f.txt[name]; ok {
		return txt, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

// run drives the handler with the default staleness window.
func run(t *testing.T, core *sweepCore, res *fakeResolver) error {
	t.Helper()
	return SweepHandler(core, res, DefaultStaleAfter)(context.Background(), nil)
}

func TestSweepRecordsCompletedChecksPerWorkspace(t *testing.T) {
	core := &sweepCore{stale: []coreapi.SendingDomainRef{
		{WorkspaceID: "ws-1", Domain: "passing.test"},
		{WorkspaceID: "ws-2", Domain: "failing.test"},
	}}
	res := &fakeResolver{txt: map[string][]string{
		"passing.test":                    {"v=spf1 -all"},
		"_dmarc.passing.test":             {"v=DMARC1; p=quarantine"},
		"google._domainkey.passing.test":  {"v=DKIM1; p=MIGf"},
		"failing.test":                    {"v=spf1 -all"},
		"selector1._domainkey.passing.te": nil,
	}}

	if err := run(t, core, res); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if core.staleAfter != DefaultStaleAfter {
		t.Fatalf("staleAfter = %v, want %v", core.staleAfter, DefaultStaleAfter)
	}
	if len(core.recorded) != 2 {
		t.Fatalf("recorded %d results, want 2: %+v", len(core.recorded), core.recorded)
	}
	first := core.recorded[0]
	if first.WorkspaceID != "ws-1" || first.State != "passing" || first.DMARCPolicy != "quarantine" {
		t.Errorf("first = %+v, want ws-1 passing quarantine", first)
	}
	if !first.DKIMFound || first.DKIMSelector != "google" {
		t.Errorf("dkim = %+v, want the matched selector carried for display", first)
	}
	// DMARC missing -> failing, and the workspace is the one the scan reported.
	second := core.recorded[1]
	if second.WorkspaceID != "ws-2" || second.State != "failing" || !second.SPFFound {
		t.Errorf("second = %+v, want ws-2 failing with spf found", second)
	}
}

// The heart of invariant 1 at the sweep level: a resolver blip is reported to
// nobody, so it can neither overwrite a good verdict nor stamp checked_at (which
// would hide the domain from the next sweep for a full staleness window).
func TestSweepNeverReportsAnUnknownResult(t *testing.T) {
	core := &sweepCore{stale: []coreapi.SendingDomainRef{
		{WorkspaceID: "ws-1", Domain: "flaky.test"},
		{WorkspaceID: "ws-1", Domain: "fine.test"},
	}}
	res := &fakeResolver{
		txt: map[string][]string{
			"fine.test":        {"v=spf1 -all"},
			"_dmarc.fine.test": {"v=DMARC1; p=reject"},
			// flaky.test's DMARC resolves, but its SPF lookup times out.
			"_dmarc.flaky.test": {"v=DMARC1; p=reject"},
		},
		errs: map[string]error{"flaky.test": &net.DNSError{Err: "i/o timeout", IsTimeout: true}},
	}

	if err := run(t, core, res); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(core.recorded) != 1 || core.recorded[0].Domain != "fine.test" {
		t.Fatalf("recorded = %+v, want only fine.test", core.recorded)
	}
}

// One domain's write failure must not abandon the rest of the pass.
func TestSweepContinuesPastARecordFailure(t *testing.T) {
	core := &sweepCore{
		stale:     []coreapi.SendingDomainRef{{WorkspaceID: "ws-1", Domain: "a.test"}, {WorkspaceID: "ws-1", Domain: "b.test"}},
		recordErr: errors.New("db down"),
	}
	res := &fakeResolver{}
	if err := run(t, core, res); err != nil {
		t.Fatalf("sweep returned %v, want nil (the next tick retries)", err)
	}
	// Both domains were still looked up despite the first write failing.
	var looked int
	for _, name := range res.calls {
		if name == "a.test" || name == "b.test" {
			looked++
		}
	}
	if looked != 2 {
		t.Fatalf("looked up %d apex names, want 2 (calls=%v)", looked, res.calls)
	}
}

// A failure to even list the stale domains IS returned, so asynq retries the
// tick rather than logging a silent no-op pass.
func TestSweepPropagatesTheListFailure(t *testing.T) {
	boom := errors.New("db down")
	if err := run(t, &sweepCore{listErr: boom}, &fakeResolver{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestSweepStopsWhenTheContextIsCancelled(t *testing.T) {
	core := &sweepCore{stale: []coreapi.SendingDomainRef{
		{WorkspaceID: "ws-1", Domain: "a.test"},
		{WorkspaceID: "ws-1", Domain: "b.test"},
	}}
	res := &fakeResolver{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SweepHandler(core, res, DefaultStaleAfter)(ctx, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.calls) != 0 {
		t.Fatalf("kept resolving after shutdown: %v", res.calls)
	}
}
